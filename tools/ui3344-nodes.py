#!/usr/bin/env python3
"""ui3344 节点一键部署：按 Cloudflare 可代理端口建常用协议入站。

Cloudflare 代理只认固定端口，且只能转发 TCP/WebSocket：
  HTTP  端口: 80 8080 8880 2052 2082 2086 2095
  HTTPS 端口: 443 2053 2083 2087 2093 2096 8443

本机已占用：8443 面板、2096 订阅服务，所以 CF 可用端口取
2053 / 2083 / 2087 / 2093 / 443。443 留给 Reality 直连（CF 代理会接管该端口的
入站 HTTP，且 Reality 本就无法经 CF）。

TLS 复用「域名功能」签下的 Cloudflare Origin CA 证书：CF 回源时信任它，
客户端到 CF 用 CF 边缘证书，两端都无需 allowInsecure。

用法：
  UI3344_PASS=<面板口令> ui3344-nodes              # 创建（幂等，已存在则跳过）
  UI3344_PASS=<面板口令> ui3344-nodes --list       # 列出各节点链接
  UI3344_PASS=<面板口令> ui3344-nodes --sub        # 输出一键导入的订阅地址
  UI3344_PASS=<面板口令> ui3344-nodes --delete     # 删除本脚本创建的节点
环境变量：
  UI3344_URL  面板基址（默认 https://127.0.0.1:8443/<webBasePath>）
  UI3344_USER 登录用户名（默认 3344）
"""
import base64
import http.cookiejar
import json
import os
import re
import ssl
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

USER = (os.environ.get("UI3344_USER") or "3344").strip()
PASS = os.environ.get("UI3344_PASS") or ""
BASE = os.environ.get("UI3344_URL", "").rstrip("/")
XRAY = "/usr/local/ui3344/bin/xray-linux-amd64"
DB = "/etc/ui3344/ui3344.db"
CERT_DIR = "/root/cert"
PREFIX = "CF-"

_ctx = ssl.create_default_context()
_ctx.check_hostname = False
_ctx.verify_mode = ssl.CERT_NONE
_op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
                                  urllib.request.HTTPSHandler(context=_ctx))

SNIFF = json.dumps({"enabled": True, "destOverride": ["http", "tls", "quic"],
                    "routeOnly": False, "metadataOnly": False})


def detect_base():
    import sqlite3
    global BASE
    if BASE:
        return BASE
    path = ""
    try:
        con = sqlite3.connect(DB)
        rows = dict(con.execute("select key, value from settings"))
        con.close()
        path = (rows.get("webBasePath") or "").strip("/")
    except Exception:
        pass
    port = 8443
    candidates = []
    if path:
        candidates.append("https://127.0.0.1:%d/%s" % (port, path))
    candidates += ["https://127.0.0.1:8443", "http://127.0.0.1:33441"]
    for cand in candidates:
        try:
            _op.open(cand + "/csrf-token", timeout=5).read()
            BASE = cand.rstrip("/")
            return BASE
        except Exception:
            continue
    BASE = candidates[0]
    return BASE


def get(path):
    return _op.open(BASE + path, timeout=20).read().decode()


def post(path, obj):
    req = urllib.request.Request(BASE + path, data=json.dumps(obj).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("X-CSRF-Token", CSRF)
    return json.loads(_op.open(req, timeout=60).read().decode())


def post_raw(path, obj):
    req = urllib.request.Request(BASE + path, data=json.dumps(obj).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("X-CSRF-Token", CSRF)
    return _op.open(req, timeout=60).read().decode()


def panel_user():
    """用 users 表里的管理员名，避免写死默认的 3344。"""
    import sqlite3
    try:
        con = sqlite3.connect(DB)
        row = con.execute("select username from users order by id limit 1").fetchone()
        con.close()
        if row and row[0]:
            return row[0]
    except Exception:
        pass
    return USER


def login():
    global CSRF
    user = panel_user()
    if not PASS:
        print("需要 UI3344_PASS（面板登录口令）。", file=sys.stderr)
        sys.exit(4)
    detect_base()
    html = get("/csrf-token")
    m = re.search(r"[A-Za-z0-9_\-]{40,}", html)
    if not m:
        print("取不到 CSRF token，检查面板地址：%s" % BASE, file=sys.stderr)
        sys.exit(2)
    CSRF = m.group(0)
    body = urllib.parse.urlencode({"username": user, "password": PASS, "_csrf": CSRF}).encode()
    req = urllib.request.Request(BASE + "/login", data=body, method="POST")
    req.add_header("X-CSRF-Token", CSRF)
    res = _op.open(req, timeout=20).read().decode()
    try:
        ok = json.loads(res).get("success")
    except Exception:
        ok = None
    if ok is False:
        print("面板登录失败：口令不对或用户名不是 %s。" % user, file=sys.stderr)
        sys.exit(3)


def panel_settings():
    import sqlite3
    con = sqlite3.connect(DB)
    try:
        return dict(con.execute("select key, value from settings"))
    finally:
        con.close()


def node_domain():
    st = panel_settings()
    cfg = json.loads(st.get("domainConfig") or "{}")
    fqdn = cfg.get("fqdn") or st.get("subDomain") or ""
    if not fqdn:
        print("未启用域名功能，无法为 WS+TLS 节点提供有效证书。", file=sys.stderr)
        sys.exit(5)
    return fqdn


def uuid4():
    return open("/proc/sys/kernel/random/uuid").read().strip()


def subid():
    import random
    return "".join(random.choice("0123456789abcdefghijklmnopqrstuvwxyz") for _ in range(16))


def b64(n):
    return base64.b64encode(os.urandom(n)).decode()


def reality_keys():
    out = subprocess.run([XRAY, "x25519"], capture_output=True, text=True).stdout
    priv = re.search(r"PrivateKey:\s*(\S+)", out).group(1)
    pub = re.search(r"Password \(PublicKey\):\s*(\S+)", out).group(1)
    return priv, pub


def ws_stream(path):
    return json.dumps({"network": "ws", "security": "none",
                       "wsSettings": {"path": path, "host": "", "headers": {}}})


def ws_tls_stream(path, domain, cert_dir):
    return json.dumps({
        "network": "ws", "security": "tls",
        "tlsSettings": {
            "serverName": domain, "minVersion": "1.2", "maxVersion": "1.3", "cipherSuites": "",
            "rejectUnknownSni": False, "allowInsecure": False,
            "certificates": [{"certificateFile": cert_dir + "/fullchain.pem",
                              "keyFile": cert_dir + "/privkey.pem",
                              "ocspStapling": 0, "oneTimeLoading": False,
                              "usage": "encipherment", "buildChain": False}],
            "alpn": ["http/1.1"],
        },
        "wsSettings": {"path": path, "host": domain, "headers": {}},
    })


def reality_stream():
    priv, pub = reality_keys()
    short_id = os.urandom(4).hex()
    dest = "www.microsoft.com"
    return json.dumps({
        "network": "tcp", "security": "reality",
        "realitySettings": {
            "show": False, "xver": 0, "dest": dest + ":443", "serverNames": [dest],
            "privateKey": priv, "minClient": "", "maxClient": "", "maxTimediff": 0,
            "shortIds": [short_id],
            "settings": {"publicKey": pub, "fingerprint": "chrome", "serverName": "", "spiderX": "/"},
        },
        "tcpSettings": {"acceptProxyProtocol": False, "header": {"type": "none"}},
    }), pub, short_id, dest


def vless_settings(email, flow=""):
    return json.dumps({"clients": [{"id": uuid4(), "flow": flow, "email": email, "limitIp": 0,
                                    "totalGB": 0, "expiryTime": 0, "enable": True, "tgId": 0,
                                    "subId": subid(), "comment": "", "reset": 0}],
                       "decryption": "none", "fallbacks": [], "encryption": ""})


def vmess_settings(email):
    return json.dumps({"clients": [{"id": uuid4(), "alterId": 0, "email": email, "security": "auto",
                                    "limitIp": 0, "totalGB": 0, "expiryTime": 0, "enable": True,
                                    "tgId": 0, "subId": subid(), "comment": "", "reset": 0}]})


def trojan_settings(email):
    return json.dumps({"clients": [{"password": b64(18), "email": email, "limitIp": 0,
                                    "totalGB": 0, "expiryTime": 0, "enable": True, "tgId": 0,
                                    "subId": subid(), "comment": "", "reset": 0}]})


def ss_settings(method, keylen, net, email):
    pwd = b64(keylen)
    return json.dumps({"method": method, "password": pwd, "network": net, "ivCheck": False,
                       "clients": [{"email": email, "password": pwd, "method": method,
                                    "subId": subid(), "limitIp": 0, "totalGB": 0,
                                    "expiryTime": 0, "enable": True}]})


def inbound(remark, port, protocol, settings, stream):
    return {"remark": remark, "enable": True, "port": port, "protocol": protocol,
            "settings": settings, "streamSettings": stream, "sniffing": SNIFF,
            "tag": "in-%d-%s" % (port, protocol), "listen": "", "up": 0, "down": 0,
            "total": 0, "expiryTime": 0, "trafficReset": "never", "shareAddrStrategy": "listen"}


def build_plan():
    domain = node_domain()
    cert_dir = "%s/%s" % (CERT_DIR, domain)
    if not (os.path.exists(cert_dir + "/fullchain.pem") and os.path.exists(cert_dir + "/privkey.pem")):
        print("找不到域名证书：%s" % cert_dir, file=sys.stderr)
        sys.exit(5)

    plan = []
    # 三个走 Cloudflare 的节点：CF 只能转发 WS，故一律 WS+TLS，路径互不冲突
    plan.append(("CF-VLESS-WS-2087", 2087, "vless",
                 lambda e: vless_settings(e), ws_tls_stream("/cf-vless", domain, cert_dir), "cf"))
    plan.append(("CF-VMess-WS-2083", 2083, "vmess",
                 lambda e: vmess_settings(e), ws_tls_stream("/cf-vmess", domain, cert_dir), "cf"))
    plan.append(("CF-Trojan-WS-2053", 2053, "trojan",
                 lambda e: trojan_settings(e), ws_tls_stream("/cf-trojan", domain, cert_dir), "cf"))
    return plan, domain


def existing_inbounds():
    res = json.loads(get("/panel/api/inbounds/list"))
    return {i["remark"]: i for i in (res.get("obj") or [])}


def _obj(v):
    """面板不同接口返回的 JSON 字段可能是字符串也可能是 dict。"""
    if isinstance(v, (dict, list)):
        return v
    try:
        return json.loads(v or "{}")
    except Exception:
        return {}


def cmd_list():
    items = existing_inbounds()
    domain = ""
    try:
        domain = node_domain()
    except SystemExit:
        pass
    st = panel_settings()
    cfg = json.loads(st.get("domainConfig") or "{}")
    ip = cfg.get("ip") or ""

    found = False
    for remark, ib in sorted(items.items(), key=lambda kv: kv[1]["port"]):
        found = True
        s = _obj(ib["streamSettings"])
        net, sec = s.get("network"), s.get("security")
        print("\n[%s] port=%s proto=%s %s/%s" % (remark, ib["port"], ib["protocol"], net, sec))
        for line in links_for(ib, domain, ip):
            print("  " + line)
    if not found:
        print("当前没有任何入站。")
    return 0


def links_for(ib, domain, ip):
    out = []
    st = _obj(ib["settings"])
    stream = _obj(ib["streamSettings"])
    net = stream.get("network", "tcp")
    sec = stream.get("security", "none")
    path = (stream.get("wsSettings") or {}).get("path", "/")
    port = ib["port"]

    if sec == "reality":
        rs = stream.get("realitySettings") or {}
        pub = (rs.get("settings") or {}).get("publicKey", "")
        sid = (rs.get("shortIds") or [""])[0]
        sni = (rs.get("serverNames") or [""])[0]
        for c in st.get("clients") or []:
            q = ("encryption=none&security=reality&sni=%s&fp=chrome&pbk=%s&sid=%s&type=tcp&headerType=none"
                 % (sni, pub, sid))
            out.append("vless://%s@%s:%d?%s#%s" % (c["id"], ip, port, q,
                                                   urllib.parse.quote(ib["remark"])))
        return out

    host = domain if sec == "tls" else ip
    for c in st.get("clients") or []:
        if ib["protocol"] == "vless":
            q = "encryption=none&security=%s&type=%s&path=%s&host=%s" % (sec, net, urllib.parse.quote(path), host)
            if sec == "tls":
                q += "&sni=%s" % host
            out.append("vless://%s@%s:%d?%s#%s" % (c["id"], host, port, q,
                                                   urllib.parse.quote(ib["remark"])))
        elif ib["protocol"] == "vmess":
            obj = {"v": "2", "ps": ib["remark"], "add": host, "port": str(port), "id": c["id"],
                   "aid": "0", "scy": "auto", "net": net, "type": "none", "host": host,
                   "path": path, "tls": sec, "sni": host if sec == "tls" else ""}
            b = base64.b64encode(json.dumps(obj, ensure_ascii=False).encode()).decode()
            out.append("vmess://" + b)
        elif ib["protocol"] == "trojan":
            q = "security=%s&type=%s&path=%s&host=%s" % (sec, net, urllib.parse.quote(path), host)
            if sec == "tls":
                q += "&sni=%s" % host
            out.append("trojan://%s@%s:%d?%s#%s" % (c["password"], host, port, q,
                                                    urllib.parse.quote(ib["remark"])))
        elif ib["protocol"] == "shadowsocks":
            info = base64.b64encode(("%s:%s" % (st.get("method"), c["password"])).encode()).decode()
            out.append("ss://%s@%s:%d#%s" % (info, ip, port, urllib.parse.quote(ib["remark"])))
    return out


def cmd_create():
    plan, domain = build_plan()
    items = existing_inbounds()

    print("域名：%s" % domain)
    print("证书：%s/%s" % (CERT_DIR, domain))
    print("")

    created = skipped = failed = 0
    for remark, port, proto, settings_fn, stream, _kind in plan:
        if remark in items:
            print("跳过（已存在）：%s" % remark)
            skipped += 1
            continue
        email = remark.lower().replace("-", ".")
        it = inbound(remark, port, proto, settings_fn(email), stream)
        try:
            r = post("/panel/api/inbounds/add", it)
            if r.get("success"):
                print("已创建：%s  port=%d %s" % (remark, port, proto))
                created += 1
            else:
                print("失败：%s -> %s" % (remark, r.get("msg")), file=sys.stderr)
                failed += 1
        except Exception as e:  # noqa: BLE001
            print("失败：%s -> %s" % (remark, e), file=sys.stderr)
            failed += 1

    # Reality 直连保底：CF 出问题时仍可用
    if "CF-Reality-443" not in items:
        try:
            stream, pub, sid, dest = reality_stream()
            it = inbound("CF-Reality-443", 443, "vless",
                         vless_settings("cf.reality", "xtls-rprx-vision"), stream)
            r = post("/panel/api/inbounds/add", it)
            if r.get("success"):
                print("已创建：CF-Reality-443  port=443 vless (reality, 直连保底)")
                created += 1
            else:
                print("失败：CF-Reality-443 -> %s" % r.get("msg"), file=sys.stderr)
                failed += 1
        except Exception as e:  # noqa: BLE001
            print("失败：CF-Reality-443 -> %s" % e, file=sys.stderr)
            failed += 1
    else:
        print("跳过（已存在）：CF-Reality-443")
        skipped += 1

    print("\n结果：新建 %d，跳过 %d，失败 %d" % (created, skipped, failed))
    if created:
        print("\n正在让 xray 重载配置…")
        try:
            post("/panel/api/server/restartXray", {})
        except Exception as e:  # noqa: BLE001
            print("重载请求失败（%s），稍后自动生效或手动 ui3344 restart" % e)
    return 0 if failed == 0 else 1


def cmd_delete():
    items = existing_inbounds()
    targets = [k for k in items if k.startswith(PREFIX)]
    if not targets:
        print("没有本脚本创建的节点。")
        return 0
    for remark in targets:
        ib = items[remark]
        try:
            r = post("/panel/api/inbounds/del/%d" % ib["id"], {})
            print(("已删除：" if r.get("success") else "失败：") + remark)
        except Exception as e:  # noqa: BLE001
            print("失败：%s -> %s" % (remark, e), file=sys.stderr)
    try:
        post("/panel/api/server/restartXray", {})
    except Exception:
        pass
    return 0


def cmd_sub():
    st = panel_settings()
    domain = st.get("subDomain") or ""
    path = (st.get("subPath") or "").strip("/")
    con = __import__("sqlite3").connect(DB)
    try:
        row = con.execute("select sub_id from clients where sub_id!='' limit 1").fetchone()
    finally:
        con.close()
    if not row or not row[0]:
        print("没有可用的订阅 ID。", file=sys.stderr)
        return 1
    sub = row[0]
    base = "https://%s:2096/%s/%s" % (domain, path, sub)
    print("订阅地址（Base64，通用客户端）:")
    print("  " + base)
    print("\n导入提示：")
    print("  · v2rayN / v2rayNG / NekoBox / sing-box / Shadowrocket 直接粘贴上面的地址")
    print("  · Clash 系客户端请用订阅转换，或改用 --list 逐个添加")
    print("  · 订阅内容含 %d 个节点" % _count_nodes())
    return 0


def _count_nodes():
    try:
        return len(existing_inbounds())
    except Exception:
        return 0


def main():
    args = sys.argv[1:]
    login()
    if "--sub" in args:
        return cmd_sub()
    if "--list" in args:
        return cmd_list()
    if "--delete" in args:
        return cmd_delete()
    return cmd_create()


if __name__ == "__main__":
    sys.exit(main())
