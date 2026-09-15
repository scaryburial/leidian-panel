import json, base64, os, re, urllib.request, http.cookiejar

BASE = "http://127.0.0.1:33441/ui3344"
cj = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cj))

def get(path):
    return op.open(BASE + path, timeout=15).read().decode()

def post_form(path, data, csrf=None):
    body = urllib.parse.urlencode(data).encode()
    req = urllib.request.Request(BASE + path, data=body, method="POST")
    if csrf: req.add_header("X-CSRF-Token", csrf)
    return op.open(req, timeout=15).read().decode()

def post_json(path, obj, csrf):
    body = json.dumps(obj).encode()
    req = urllib.request.Request(BASE + path, data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("X-CSRF-Token", csrf)
    return op.open(req, timeout=20).read().decode()

# login
raw = get("/csrf-token")
m = re.search(r'[A-Za-z0-9_\-]{40,}', raw)
tok = m.group(0)
post_form("/login", {"username":"3344","password":"3344","_csrf":tok})
print("logged in")

def uuid():
    return open("/proc/sys/kernel/random/uuid").read().strip()
def b64file(n):
    return base64.b64encode(os.urandom(n)).decode()

SNIFF = json.dumps({"enabled":True,"destOverride":["http","tls","quic"],"routeOnly":False,"metadataOnly":False})

def vless_client(email, flow="xtls-rprx-vision"):
    return {"id":uuid(),"flow":flow,"email":email,"limitIp":0,"totalGB":0,"expiryTime":0,"enable":True,"tgId":0,"subId":"","comment":"","reset":0}
def vmess_client(email):
    return {"id":uuid(),"alterId":0,"email":email,"security":"auto","limitIp":0,"totalGB":0,"expiryTime":0,"enable":True,"tgId":0,"subId":"","comment":"","reset":0}

def tcp_none(): return json.dumps({"network":"tcp","security":"none","tcpSettings":{"acceptProxyProtocol":False,"header":{"type":"none"}}})
def ws_none(path): return json.dumps({"network":"ws","security":"none","wsSettings":{"path":path,"host":"","headers":{}}})
def tls_selfsigned(): return json.dumps({"network":"tcp","security":"tls","tlsSettings":{"serverName":"ui3344","minVersion":"1.2","maxVersion":"1.3","cipherSuites":"","rejectUnknownSni":False,"allowInsecure":False,"certificates":[{"certificateFile":"/root/cert/ui3344-selfsigned/fullchain.pem","keyFile":"/root/cert/ui3344-selfsigned/privkey.pem","ocspStapling":0,"oneTimeLoading":False,"usage":"encipherment","buildChain":False}],"alpn":["h2","http/1.1"]}})
def reality(priv, pub, sid): return json.dumps({"network":"tcp","security":"reality","realitySettings":{"show":False,"xver":0,"dest":"www.microsoft.com:443","serverNames":["www.microsoft.com"],"privateKey":priv,"minClient":"","maxClient":"","maxTimediff":0,"shortIds":[sid],"settings":{"publicKey":pub,"fingerprint":"chrome","serverName":"","spiderX":"/"},"seed":""},"tcpSettings":{"acceptProxyProtocol":False,"header":{"type":"none"}}})

RIYO = "/usr/local/ui3344/bin/xray-linux-amd64"
def reality_keys():
    out = os.popen(RIYO + " x25519").read()
    priv = re.search(r'PrivateKey:\s*(\S+)', out).group(1)
    pub = re.search(r'Password \(PublicKey\):\s*(\S+)', out).group(1)
    return priv, pub

priv, pub = reality_keys()
sid = os.urandom(4).hex()

def vless_settings(email, flow="xtls-rprx-vision"):
    return json.dumps({"clients":[vless_client(email, flow)],"decryption":"none","fallbacks":[],"encryption":""})
def vmess_settings(email):
    return json.dumps({"clients":[vmess_client(email)]})
def ss_settings(method, keylen, email):
    pwd = b64file(keylen)
    return json.dumps({"method":method,"password":pwd,"network":"tcp,udp","ivCheck":False,"clients":[{"email":email,"password":pwd,"method":method,"limitIp":0,"totalGB":0,"expiryTime":0,"enable":True}]})

def inbound(remark, port, protocol, settings, stream):
    return {"remark":remark,"enable":True,"port":port,"protocol":protocol,
            "settings":settings,"streamSettings":stream,"sniffing":SNIFF,
            "tag":f"in-{port}-{protocol}","listen":"","up":0,"down":0,"total":0,
            "expiryTime":0,"trafficReset":"never","shareAddrStrategy":"listen"}

items = [
 inbound("VLESS-速度-8443", 8443, "vless", vless_settings("vless-speed"), tcp_none()),
 inbound("VLESS-安全-Reality-443", 443, "vless", vless_settings("vless-reality"), reality(priv, pub, sid)),
 inbound("VLESS-综合-WS-2087", 2087, "vless", vless_settings("vless-ws"), ws_none("/ui3344ws")),
 inbound("VMess-速度-8080", 8080, "vmess", vmess_settings("vmess-speed"), tcp_none()),
 inbound("VMess-安全-TLS-2053", 2053, "vmess", vmess_settings("vmess-tls"), tls_selfsigned()),
 inbound("VMess-综合-WS-2052", 2052, "vmess", vmess_settings("vmess-ws"), ws_none("/ui3344vm")),
 inbound("SS2022-速度-8388", 8388, "shadowsocks", ss_settings("2022-blake3-aes-128-gcm", 16, "ss-speed"), tcp_none()),
 inbound("SS2022-安全-8389", 8389, "shadowsocks", ss_settings("2022-blake3-chacha20-poly1305", 32, "ss-secure"), tcp_none()),
 inbound("SS2022-综合-8390", 8390, "shadowsocks", ss_settings("2022-blake3-aes-256-gcm", 32, "ss-balanced"), tcp_none()),
]

existing = set()
try:
    d = json.loads(get("/panel/api/inbounds/list"))
    for i in (d.get("obj") or []):
        existing.add(i["remark"])
except Exception as e:
    print("list warn", e)

for it in items:
    if it["remark"] in existing:
        print(it["remark"], "-> skip (exists)")
        continue
    try:
        r = post_json("/panel/api/inbounds/add", it, tok)
        print(it["remark"], "->", r[:120])
    except Exception as e:
        print(it["remark"], "ERROR", e)
