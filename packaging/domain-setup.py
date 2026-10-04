#!/usr/bin/env python3
"""ui3344 域名功能（Cloudflare 自动化）命令行工具。

与面板「域名」页共用同一套后端接口 /panel/api/domain/*，每次启用/停用/改 Token
都需要 TOTP 动态码。可作为交互菜单使用，也可直接带子命令调用（供 ui3344 菜单）。

Env:
  UI3344_URL   面板基址   (留空则自动探测 33441/HTTP 与 8443/HTTPS)
  UI3344_USER  登录用户名 (默认 3344)
  UI3344_PASS  登录密码   (默认 3344)
"""
import http.cookiejar
import json
import os
import re
import ssl
import sys
import urllib.parse
import urllib.request

USER = os.environ.get("UI3344_USER", "3344")
PASS = os.environ.get("UI3344_PASS", "3344")

# 面板启用域名功能后会被搬到 8443 并换用 Cloudflare Origin CA 证书，而那个证书
# 不在系统信任库里；硬编码 http://127.0.0.1:33441 会直接失效
# （<urlopen error [SSL: CERTIFICATE_VERIFY_FAILED] ...>）。这里自动探测实际地址，
# 并对本地回环关闭证书校验。
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
    return op.open(BASE + path, timeout=20).read().decode()


def post(path, obj, csrf):
    req = urllib.request.Request(BASE + path, data=json.dumps(obj).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("X-CSRF-Token", csrf)
    return json.loads(op.open(req, timeout=60).read().decode())


def post_form(path, data, csrf):
    req = urllib.request.Request(BASE + path, data=urllib.parse.urlencode(data).encode(), method="POST")
    req.add_header("X-CSRF-Token", csrf)
    return op.open(req, timeout=20).read().decode()


def login():
    detect_base()
    m = re.search(r"[A-Za-z0-9_\-]{40,}", get("/csrf-token"))
    if not m:
        print("无法获取 CSRF token", file=sys.stderr)
        sys.exit(2)
    csrf = m.group(0)
    post_form("/login", {"username": USER, "password": PASS, "_csrf": csrf}, csrf)
    return csrf


def show_status(csrf):
    st = json.loads(get("/panel/api/domain/config"))
    obj = st.get("obj") or {}
    print("  状态    : %s" % ("已启用" if obj.get("enabled") else "未启用"))
    print("  子域名  : %s" % (obj.get("fqdn") or "-"))
    print("  服务器IP: %s" % (obj.get("serverIp") or "-"))
    print("  证书    : %s %s" % (obj.get("certMode") or "-", obj.get("certPath") or ""))
    print("  CF代理  : %s" % ("是" if obj.get("proxied") else "否"))
    print("  Token   : %s" % ("已配置" if obj.get("cfConfigured") else "未配置"))


def do(csrf, path, obj, ok_msg):
    r = post(path, obj, csrf)
    if r.get("success"):
        print("✅ %s" % ok_msg)
        steps = (r.get("obj") or {}).get("steps")
        if steps:
            for s in steps:
                print("   - " + s)
        return True
    print("❌ %s" % (r.get("msg") or "失败"))
    return False


def menu(csrf):
    while True:
        print("\n==== 域名功能（Cloudflare 自动化） ====")
        show_status(csrf)
        print("  1) 设置 Cloudflare Token")
        print("  2) 设置 TOTP 密钥")
        print("  3) 查看将要执行的操作（预览）")
        print("  4) 启用域名功能")
        print("  5) 停用域名功能")
        print("  0) 退出")
        c = input("请选择: ").strip()
        if c == "0":
            return
        if c == "1":
            tok = input("Cloudflare API Token: ").strip()
            totp = input("TOTP 动态码: ").strip()
            do(csrf, "/panel/api/domain/token", {"token": tok, "totp": totp}, "Token 已保存")
        elif c == "2":
            sec = input("TOTP 密钥(base32): ").strip()
            do(csrf, "/panel/api/domain/otp", {"secret": sec}, "TOTP 密钥已保存")
        elif c == "3":
            sub = input("子域名(留空随机): ").strip()
            do(csrf, "/panel/api/domain/preview", {"subdomain": sub}, "预览")
        elif c == "4":
            sub = input("子域名(留空随机): ").strip()
            totp = input("TOTP 动态码: ").strip()
            do(csrf, "/panel/api/domain/enable", {"subdomain": sub, "totp": totp}, "已启用")
        elif c == "5":
            totp = input("TOTP 动态码: ").strip()
            do(csrf, "/panel/api/domain/disable", {"totp": totp}, "已停用")


def main():
    csrf = login()
    args = sys.argv[1:]
    if not args:
        menu(csrf)
        return 0
    cmd = args[0]
    if cmd == "status":
        show_status(csrf)
    elif cmd == "preview":
        do(csrf, "/panel/api/domain/preview", {"subdomain": args[1] if len(args) > 1 else ""}, "预览")
    elif cmd == "enable":
        if len(args) < 3:
            print("用法: enable <子域名|-> <动态码>")
            return 1
        sub = "" if args[1] in ("-", "none") else args[1]
        do(csrf, "/panel/api/domain/enable", {"subdomain": sub, "totp": args[2]}, "已启用")
    elif cmd == "disable":
        if len(args) < 2:
            print("用法: disable <动态码>")
            return 1
        do(csrf, "/panel/api/domain/disable", {"totp": args[1]}, "已停用")
    elif cmd == "token":
        if len(args) < 3:
            print("用法: token <token> <动态码>")
            return 1
        do(csrf, "/panel/api/domain/token", {"token": args[1], "totp": args[2]}, "Token 已保存")
    elif cmd == "otp":
        if len(args) < 2:
            print("用法: otp <base32密钥>")
            return 1
        do(csrf, "/panel/api/domain/otp", {"secret": args[1]}, "TOTP 密钥已保存")
    else:
        print("未知命令: %s" % cmd)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
