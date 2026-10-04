#!/usr/bin/env python3
"""Create the ui3344 preset inbounds: VLESS / VMess / Shadowsocks-2022, each in a
speed-first, security-first and balanced variant (9 inbounds total).

Runs on the panel host after `ui3344` is up. Idempotent: any inbound whose remark
already exists is skipped, so it is safe to re-run after upgrades.

Env overrides:
  UI3344_URL    panel base URL   (default http://127.0.0.1:33441/ui3344)
  UI3344_USER   login username   (default 3344)
  UI3344_PASS   login password   (default 3344)
  UI3344_XRAY   xray binary path (default /usr/local/ui3344/bin/xray-linux-amd64)
  UI3344_SNI    Reality dest     (default www.apple.com)
Exit code is non-zero only when the panel cannot be reached / logged into.
"""
import base64
import http.cookiejar
import json
import os
import random
import re
import ssl
import subprocess
import sys
import urllib.parse
import urllib.request

USER = os.environ.get("UI3344_USER", "3344")
PASS = os.environ.get("UI3344_PASS", "3344")
XRAY = os.environ.get("UI3344_XRAY", "/usr/local/ui3344/bin/xray-linux-amd64")
SNI = os.environ.get("UI3344_SNI", "www.apple.com")

# 面板可能跑在 33441(HTTP，全新安装) 或 8443(HTTPS，启用域名功能后会被搬过去并
# 换成 Cloudflare Origin CA 证书)。硬编码 http://127.0.0.1:33441 会在启用域名后
# 直接失效：端口不对，而且 Origin CA 证书不在系统信任库里 →
#   <urlopen error [SSL: CERTIFICATE_VERIFY_FAILED] ...>
# 于是安装器的「创建预设协议」静默失败。这里自动探测实际地址，并对本地回环
# 关闭证书校验（Origin CA 证书本来也不该进系统信任库）。
BASE = os.environ.get("UI3344_URL", "").rstrip("/")
BASE_CANDIDATES = (
    "http://127.0.0.1:33441/ui3344",
    "https://127.0.0.1:8443/ui3344",
    "http://127.0.0.1:8443/ui3344",
)

_ssl_ctx = ssl.create_default_context()
_ssl_ctx.check_hostname = False
_ssl_ctx.verify_mode = ssl.CERT_NONE

cj = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cj),
                                 urllib.request.HTTPSHandler(context=_ssl_ctx))


def detect_base():
    """确定面板基址：优先 UI3344_URL，否则依次探测候选地址。"""
    global BASE
    if BASE:
        return BASE
    for cand in BASE_CANDIDATES:
        try:
            op.open(cand + "/csrf-token", timeout=5).read()
            BASE = cand
            return BASE
        except Exception:  # noqa: BLE001
            continue
    BASE = BASE_CANDIDATES[0]
    return BASE


def get(path):
    return op.open(BASE + path, timeout=15).read().decode()


def post_form(path, data, csrf=None):
    req = urllib.request.Request(BASE + path, data=urllib.parse.urlencode(data).encode(), method="POST")
    if csrf:
        req.add_header("X-CSRF-Token", csrf)
    return op.open(req, timeout=15).read().decode()


def post_json(path, obj, csrf):
    req = urllib.request.Request(BASE + path, data=json.dumps(obj).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("X-CSRF-Token", csrf)
    return op.open(req, timeout=25).read().decode()


SNIFF = json.dumps({"enabled": True, "destOverride": ["http", "tls", "quic"], "routeOnly": False, "metadataOnly": False})
TCP_NONE = json.dumps({"network": "tcp", "security": "none", "tcpSettings": {"acceptProxyProtocol": False, "header": {"type": "none"}}})
TLS_CERT = "/root/cert/ui3344-selfsigned"


def ws(path):
    return json.dumps({"network": "ws", "security": "none", "wsSettings": {"path": path, "host": "", "headers": {}}})


TLS_ALPN = ["http/1.1"]


def tls_selfsigned():
    return json.dumps({
        "network": "tcp", "security": "tls",
        "tlsSettings": {
            "serverName": "ui3344", "minVersion": "1.2", "maxVersion": "1.3", "cipherSuites": "",
            "rejectUnknownSni": False, "allowInsecure": False,
            "certificates": [{"certificateFile": TLS_CERT + "/fullchain.pem", "keyFile": TLS_CERT + "/privkey.pem",
                              "ocspStapling": 0, "oneTimeLoading": False, "usage": "encipherment", "buildChain": False}],
            "alpn": list(TLS_ALPN),
        },
    })


def reality(priv, pub, sid):
    return json.dumps({
        "network": "tcp", "security": "reality",
        "realitySettings": {"show": False, "xver": 0, "dest": SNI + ":443", "serverNames": [SNI],
                            "privateKey": priv, "minClient": "", "maxClient": "", "maxTimediff": 0,
                            "shortIds": [sid],
                            "settings": {"publicKey": pub, "fingerprint": "chrome", "serverName": "", "spiderX": "/"},
                            "seed": ""},
        "tcpSettings": {"acceptProxyProtocol": False, "header": {"type": "none"}},
    })


def uuid():
    return open("/proc/sys/kernel/random/uuid").read().strip()


def b64(n):
    return base64.b64encode(os.urandom(n)).decode()


def subid():
    # Mirrors the panel's own 16-char [0-9a-z] subscription id, so every preset
    # client gets its own shareable subscription link on first install.
    return "".join(random.choice("0123456789abcdefghijklmnopqrstuvwxyz") for _ in range(16))


def reality_keys():
    out = subprocess.Popen([XRAY, "x25519"], stdout=subprocess.PIPE).stdout.read().decode()
    priv = re.search(r"PrivateKey:\s*(\S+)", out).group(1)
    pub = re.search(r"Password \(PublicKey\):\s*(\S+)", out).group(1)
    return priv, pub


VISION_FLOW = "xtls-rprx-vision"


def stream_network_security(stream):
    """(network, security) of a serialized streamSettings blob."""
    s = json.loads(stream)
    return s.get("network", ""), s.get("security", "")


def vision_flow_for(stream):
    """XTLS Vision flow a VLESS client may declare on this stream, else "".

    Vision needs the transport TLS/Reality is layered on, so it is valid on raw
    TCP with tls or reality -- and on XHTTP once VLESS-level encryption carries
    it. Every other transport (WebSocket, HTTPUpgrade, gRPC, mKCP, QUIC, plain
    HTTP) has no TLS layer under it and cannot express the flow at all.

    Writing flow=xtls-rprx-vision on such an inbound is fatal, not cosmetic:
    the panel's own link/subscription builders strip the flow for a ws transport
    (vlessFlowAllowed in internal/sub), so the client never sends it, while the
    server account still demands it and refuses the connection with

      account <email> is rejected since the client flow is empty

    Mirrors inboundCanEnableTlsFlow() in internal/web/service/inbound_protocol.go.
    """
    network, security = stream_network_security(stream)
    if network == "tcp" and security in ("tls", "reality"):
        return VISION_FLOW
    return ""


def vless_settings(email, stream):
    client = {"id": uuid(), "flow": vision_flow_for(stream), "email": email, "limitIp": 0, "totalGB": 0,
              "expiryTime": 0, "enable": True, "tgId": 0, "subId": subid(), "comment": "", "reset": 0}
    return json.dumps({"clients": [client], "decryption": "none", "fallbacks": [], "encryption": ""})


def vmess_settings(email):
    client = {"id": uuid(), "alterId": 0, "email": email, "security": "auto", "limitIp": 0, "totalGB": 0,
              "expiryTime": 0, "enable": True, "tgId": 0, "subId": subid(), "comment": "", "reset": 0}
    return json.dumps({"clients": [client]})


def ss_settings(method, keylen, net, email):
    pwd = b64(keylen)
    return json.dumps({"method": method, "password": pwd, "network": net, "ivCheck": False,
                       "clients": [{"email": email, "password": pwd, "method": method, "subId": subid(),
                                    "limitIp": 0, "totalGB": 0, "expiryTime": 0, "enable": True}]})


def inbound(remark, port, protocol, settings, stream):
    return {"remark": remark, "enable": True, "port": port, "protocol": protocol,
            "settings": settings, "streamSettings": stream, "sniffing": SNIFF,
            "tag": "in-%d-%s" % (port, protocol), "listen": "", "up": 0, "down": 0, "total": 0,
            "expiryTime": 0, "trafficReset": "never", "shareAddrStrategy": "listen"}


def repair_prohibited_vless(csrf):
    """把既有的「VLESS + 无 TLS/Reality」入站原地修成 Reality。

    本文件是**按 remark 幂等**的：已经存在的入站会被跳过。所以老版本（v1.7 及
    更早）装出来的速度档不会被新建逻辑修到——而它恰恰是坏的：

        vless://<uuid>@<公网IP>:8444?security=none&type=tcp

    Xray 26.x 的 outbound 校验会直接拒绝加载这种配置：

        failed to build outbound config ... vless without TLS or other encryption
        is prohibited unless the server address is a private IP or domain

    客户端连配置都起不来，节点在订阅里存在但永远用不了。这里在安装/升级时做一次
    原地修复：只把这类入站的 streamSettings 换成 Reality，不动端口、备注与客户端。
    """
    try:
        lst = json.loads(get("/panel/api/inbounds/list")).get("obj") or []
    except Exception as e:  # noqa: BLE001
        print("ui3344 presets: repair 列表读取失败 %s" % e, file=sys.stderr)
        return 0

    fixed = 0
    for ib in lst:
        if (ib.get("protocol") or "").lower() != "vless":
            continue
        raw = ib.get("streamSettings")
        if isinstance(raw, str):
            try:
                stream = json.loads(raw or "{}")
            except Exception:  # noqa: BLE001
                continue
        else:
            stream = raw or {}
        if (stream.get("network") or "").lower() != "tcp":
            continue
        if (stream.get("security") or "none").lower() != "none":
            continue
        try:
            p, q = reality_keys()
        except Exception:  # noqa: BLE001
            print("ui3344 presets: repair 跳过（xray x25519 不可用）", file=sys.stderr)
            return fixed
        ib["streamSettings"] = reality(p, q, os.urandom(4).hex())
        try:
            r = json.loads(post_json("/panel/api/inbounds/update/%d" % ib["id"], ib, csrf))
            if r.get("success"):
                fixed += 1
                print("ui3344 presets: repaired %s (端口 %s) -> Reality"
                      % (ib.get("remark"), ib.get("port")))
            else:
                print("ui3344 presets: repair %s -> %s" % (ib.get("remark"), r.get("msg")),
                      file=sys.stderr)
        except Exception as e:  # noqa: BLE001
            print("ui3344 presets: repair %s ERROR %s" % (ib.get("remark"), e), file=sys.stderr)
    return fixed


def repair_shadowsocks_method_mix(csrf):
    """把 PSK 长度不一致的 Shadowsocks-2022 入站统一成 aes-256-gcm。

    `clients` 表里**一个客户端只有一个 password 字段**，而 SS2022 的 PSK 长度由
    method 决定（aes-128 = 16 字节，aes-256/chacha20 = 32 字节）。只要三条 SS 入站的
    method 长度不一致，把同一个客户端挂到多条入站上就必然有一边不匹配——
    轻则该节点连不上，重则配置非法、xray 以 `proxy/shadowsocks_2022: bad key`
    拒绝启动，**整台服务器所有节点一起下线**。

    这里在安装/升级时做一次统一：method 改为 aes-256-gcm，服务器 PSK 与各客户端 PSK
    重新生成为 32 字节（同一 email 在所有入站里用同一个密钥）。改密钥意味着已导入的
    客户端需要重新拉一次订阅，但总好过节点不可用或整机下线。
    """
    try:
        lst = json.loads(get("/panel/api/inbounds/list")).get("obj") or []
    except Exception as e:  # noqa: BLE001
        print("ui3344 presets: ss 修复列表读取失败 %s" % e, file=sys.stderr)
        return 0

    ss = [ib for ib in lst if (ib.get("protocol") or "").lower() == "shadowsocks"]
    if len(ss) < 2:
        return 0

    want = "2022-blake3-aes-256-gcm"
    lens = set()
    for ib in ss:
        raw = ib.get("streamSettings")
        settings = ib.get("settings")
        if isinstance(settings, str):
            try:
                settings = json.loads(settings)
            except Exception:  # noqa: BLE001
                continue
        m = (settings or {}).get("method") or ""
        lens.add(SS_KEY_BYTES.get(m, 0))
    if len(lens) <= 1:
        return 0  # 长度已经一致，不需要动

    # 同一个 email 用同一个新密钥，这样一个客户端可以通吃所有 SS 入站
    keys = {}
    fixed = 0
    for ib in ss:
        settings = ib.get("settings")
        if isinstance(settings, str):
            try:
                settings = json.loads(settings)
            except Exception:  # noqa: BLE001
                continue
        settings = settings or {}
        settings["method"] = want
        settings["password"] = b64(32)
        for cl in (settings.get("clients") or []):
            email = cl.get("email")
            if email not in keys:
                keys[email] = b64(32)
            cl["password"] = keys[email]
            cl.pop("method", None)
        ib["settings"] = json.dumps(settings, ensure_ascii=False)
        try:
            r = json.loads(post_json("/panel/api/inbounds/update/%d" % ib["id"], ib, csrf))
            if r.get("success"):
                fixed += 1
                print("ui3344 presets: ss 统一 %s (端口 %s) -> %s" % (ib.get("remark"), ib.get("port"), want))
            else:
                print("ui3344 presets: ss 统一 %s -> %s" % (ib.get("remark"), r.get("msg")), file=sys.stderr)
        except Exception as e:  # noqa: BLE001
            print("ui3344 presets: ss 统一 %s ERROR %s" % (ib.get("remark"), e), file=sys.stderr)
    return fixed


SS_KEY_BYTES = {
    "2022-blake3-aes-128-gcm": 16,
    "2022-blake3-aes-256-gcm": 32,
    "2022-blake3-chacha20-poly1305": 32,
}


def main():
    detect_base()
    token = re.search(r"[A-Za-z0-9_\-]{40,}", get("/csrf-token"))
    if not token:
        print("ui3344 presets: could not read csrf token", file=sys.stderr)
        return 2
    token = token.group(0)
    try:
        post_form("/login", {"username": USER, "password": PASS, "_csrf": token})
    except Exception as e:  # noqa: BLE001
        print("ui3344 presets: login failed (%s); skip" % e, file=sys.stderr)
        return 3

    priv = pub = sid = None
    try:
        priv, pub = reality_keys()
        sid = os.urandom(4).hex()
    except Exception:  # noqa: BLE001
        print("ui3344 presets: xray x25519 unavailable; Reality inbound will be skipped", file=sys.stderr)

    # 速度档原本是「VLESS + 裸 TCP + 无加密」，但那已经不可能用了：Xray 26.x 的
    # outbound 校验会直接拒绝加载
    #
    #   Failed to start: infra/conf: vless without TLS or other encryption is
    #   prohibited unless the server address is a private IP or domain
    #
    # 客户端连配置都起不来——不是"不安全"，而是根本用不了。VLESS 自身不含任何
    # 加密，脱离 TLS/Reality 就是明文，所以这里给速度档也套一层 Reality：不需要
    # 证书、握手开销小，且现代客户端都接受。用独立的一对密钥与 shortId，
    # 避免与 443 的安全档共用凭据。
    priv2 = pub2 = sid2 = None
    try:
        priv2, pub2 = reality_keys()
        sid2 = os.urandom(4).hex()
    except Exception:  # noqa: BLE001
        pass

    has_cert = os.path.exists(TLS_CERT + "/fullchain.pem") and os.path.exists(TLS_CERT + "/privkey.pem")
    ws_stream = ws("/ui3344ws")
    vmess_ws_stream = ws("/ui3344vm")
    tls_stream = tls_selfsigned() if has_cert else None
    reality_stream = reality(priv, pub, sid) if (priv and pub) else None
    speed_stream = reality(priv2, pub2, sid2) if (priv2 and pub2) else TCP_NONE
    if speed_stream is TCP_NONE:
        print("ui3344 presets: WARNING 速度档拿不到 Reality 密钥，已退回裸 TCP；"
              "Xray 26.x 客户端会拒绝加载该节点", file=sys.stderr)
    items = [
        inbound("VLESS-速度-8443", 8443, "vless", vless_settings("vless-speed", speed_stream), speed_stream),
        # ws: flow must stay empty -- Vision cannot ride a WebSocket transport.
        inbound("VLESS-综合-WS-2087", 2087, "vless", vless_settings("vless-ws", ws_stream), ws_stream),
        inbound("VMess-速度-8080", 8080, "vmess", vmess_settings("vmess-speed"), TCP_NONE),
        *([inbound("VMess-安全-TLS-2053", 2053, "vmess", vmess_settings("vmess-tls"), tls_stream)] if has_cert else []),
        inbound("VMess-综合-WS-2052", 2052, "vmess", vmess_settings("vmess-ws"), vmess_ws_stream),
        # 速度档也用 aes-256-gcm，不再是 aes-128。
        #
        # SS2022 的 PSK 长度由 method 决定（aes-128 要 16 字节、aes-256 要 32 字节），
        # 而 clients 表里**一个客户端只有一个 password 字段**。只要三条入站的
        # method 长度不一致，同一个客户端就必然有一边挂不上：轻则该节点连不上，
        # 重则生成的配置非法、xray 直接拒绝启动（proxy/shadowsocks_2022: bad key），
        # **整台服务器所有节点一起下线**。
        # 三条统一成 aes-256 后，一个客户端就能同时用于三条入站。aes-256-gcm 的性能
        # 损失很小，换来的是整类故障消失。
        inbound("SS2022-速度-8388", 8388, "shadowsocks", ss_settings("2022-blake3-aes-256-gcm", 32, "tcp,udp", "ss-speed"), TCP_NONE),
        inbound("SS2022-安全-8389", 8389, "shadowsocks", ss_settings("2022-blake3-aes-256-gcm", 32, "tcp,udp", "ss-secure"), TCP_NONE),
        inbound("SS2022-综合-8390", 8390, "shadowsocks", ss_settings("2022-blake3-aes-256-gcm", 32, "tcp", "ss-balanced"), TCP_NONE),
    ]
    if reality_stream:
        items.insert(1, inbound("VLESS-安全-Reality-443", 443, "vless", vless_settings("vless-reality", reality_stream), reality_stream))

    existing = set()
    try:
        d = json.loads(get("/panel/api/inbounds/list"))
        for i in (d.get("obj") or []):
            existing.add(i["remark"])
    except Exception as e:  # noqa: BLE001
        print("ui3344 presets: list warn %s" % e, file=sys.stderr)

    created = skipped = failed = 0
    for it in items:
        if it["remark"] in existing:
            skipped += 1
            continue
        try:
            r = json.loads(post_json("/panel/api/inbounds/add", it, token))
            if r.get("success"):
                created += 1
            else:
                failed += 1
                print("ui3344 presets: %s -> %s" % (it["remark"], r.get("msg")), file=sys.stderr)
        except Exception as e:  # noqa: BLE001
            failed += 1
            print("ui3344 presets: %s ERROR %s" % (it["remark"], e), file=sys.stderr)
    print("ui3344 presets: created=%d skipped=%d failed=%d" % (created, skipped, failed))

    # 既有的坏入站不会被上面的幂等逻辑修到（同 remark 直接跳过），所以单独修一次。
    repaired = repair_prohibited_vless(token)
    if repaired:
        print("ui3344 presets: repaired=%d (VLESS 裸 TCP -> Reality)" % repaired)

    # 老版本可能造出 PSK 长度不一致的三条 SS2022 入站，统一成 aes-256-gcm。
    unified = repair_shadowsocks_method_mix(token)
    if unified:
        print("ui3344 presets: ss 已统一 %d 条入站为 %s" % (unified, "2022-blake3-aes-256-gcm"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
