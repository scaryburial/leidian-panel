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
import subprocess
import sys
import urllib.parse
import urllib.request

BASE = os.environ.get("UI3344_URL", "http://127.0.0.1:33441/ui3344").rstrip("/")
USER = os.environ.get("UI3344_USER", "3344")
PASS = os.environ.get("UI3344_PASS", "3344")
XRAY = os.environ.get("UI3344_XRAY", "/usr/local/ui3344/bin/xray-linux-amd64")
SNI = os.environ.get("UI3344_SNI", "www.apple.com")

cj = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cj))


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


def main():
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

    has_cert = os.path.exists(TLS_CERT + "/fullchain.pem") and os.path.exists(TLS_CERT + "/privkey.pem")
    ws_stream = ws("/ui3344ws")
    vmess_ws_stream = ws("/ui3344vm")
    tls_stream = tls_selfsigned() if has_cert else None
    reality_stream = reality(priv, pub, sid) if (priv and pub) else None
    items = [
        inbound("VLESS-速度-8443", 8443, "vless", vless_settings("vless-speed", TCP_NONE), TCP_NONE),
        # ws: flow must stay empty -- Vision cannot ride a WebSocket transport.
        inbound("VLESS-综合-WS-2087", 2087, "vless", vless_settings("vless-ws", ws_stream), ws_stream),
        inbound("VMess-速度-8080", 8080, "vmess", vmess_settings("vmess-speed"), TCP_NONE),
        *([inbound("VMess-安全-TLS-2053", 2053, "vmess", vmess_settings("vmess-tls"), tls_stream)] if has_cert else []),
        inbound("VMess-综合-WS-2052", 2052, "vmess", vmess_settings("vmess-ws"), vmess_ws_stream),
        inbound("SS2022-速度-8388", 8388, "shadowsocks", ss_settings("2022-blake3-aes-128-gcm", 16, "tcp,udp", "ss-speed"), TCP_NONE),
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
    return 0


if __name__ == "__main__":
    sys.exit(main())
