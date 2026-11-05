#!/usr/bin/env python3
"""面板域名功能的 TOTP 保底工具。

面板的「域名」页每次启用/停用/改 Token 都要 TOTP 动态码，密钥只有面板数据库里
一份（settings.domainOtpSecret）。这里提供取码、备份、重置密钥与校时检查，
避免手机丢验证器或服务器时钟漂移后卡死在「动态码校验失败」。

子命令：
  code                打印当前动态码（默认）
  at <偏移>           打印偏移若干个 30 秒窗口的动态码，如 -1 / 0 / 1
  status              打印密钥、域名、证书与时钟状态
  backup [目录]       备份数据库/证书/域名配置（默认 /root/.ui3344-backup/<时间戳>）
  reset-secret <密钥> 直接改数据库重设 TOTP 密钥（面板需重启）
  verify              用当前码向面板做一次幂等校验（需 UI3344_PASS/UI3344_URL）

面板的 /panel/api/domain/* 在动态码错误时仍返回 success=true，只在 msg 里写
「动态码校验失败（TOTP）」，所以 verify 只认 msg，不看 success。
"""
import base64
import hashlib
import hmac
import json
import os
import shutil
import sqlite3
import struct
import subprocess
import sys
import time
import urllib.request

DB = "/etc/ui3344/ui3344.db"
BACKUP_ROOT = "/root/.ui3344-backup"
CERT_DIR = "/root/cert"
_PANEL_OPENER = None


def totp(secret, offset=0, step=30, digits=6):
    key = base64.b32decode(secret.upper() + "=" * ((8 - len(secret) % 8) % 8))
    counter = int(time.time() // step) + offset
    digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    o = digest[-1] & 0x0F
    value = struct.unpack(">I", digest[o:o + 4])[0] & 0x7FFFFFFF
    return str(value % (10 ** digits)).zfill(digits)


def read_settings():
    if not os.path.exists(DB):
        return {}
    con = sqlite3.connect(DB)
    try:
        rows = dict(con.execute("select key, value from settings"))
    finally:
        con.close()
    return rows


def cmd_code(offset=0):
    secret = read_settings().get("domainOtpSecret") or os.environ.get("OTP_SECRET", "")
    if not secret:
        print("数据库中未设置 TOTP 密钥（domainOtpSecret 为空）。", file=sys.stderr)
        return 1
    remain = 30 - int(time.time()) % 30
    print("%s   （剩余 %d 秒）" % (totp(secret, offset), remain))
    return 0


def ntp_offset_seconds():
    """用 chronyc/timedatectl 读当前时钟偏差，取不到返回 None。"""
    try:
        out = subprocess.run(["timedatectl", "show-timesync", "--property=NTPMessage"],
                             capture_output=True, text=True, timeout=10).stdout
        if "NTPMessage=" in out and "stratum" in out.lower():
            return True
    except Exception:
        pass
    try:
        out = subprocess.run(["timedatectl"], capture_output=True, text=True, timeout=10).stdout
        return "System clock synchronized: yes" in out
    except Exception:
        return None


def cmd_verify():
    """向面板提交一次「写入当前 Token」的请求，按 msg 判断动态码是否真的被接受。"""
    import urllib.parse
    st = read_settings()
    secret = st.get("domainOtpSecret") or os.environ.get("OTP_SECRET", "")
    token = st.get("cfApiToken") or os.environ.get("CF_TOKEN", "")
    if not secret or not token:
        print("缺少 TOTP 密钥或 CF Token。", file=sys.stderr)
        return 1
    csrf = panel_login()
    if not csrf:
        return 1
    code = totp(secret)
    req = urllib.request.Request(
        panel_base() + "/panel/api/domain/token",
        data=json.dumps({"token": token, "totp": code}).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("X-CSRF-Token", csrf)
    res = json.loads(panel_opener().open(req, timeout=60).read().decode())
    msg = res.get("msg") or ""
    if msg.startswith("已保存"):
        print("动态码 %s 被面板接受（时钟与密钥一致）。" % code)
        return 0
    print("动态码 %s 被拒绝：%s" % (code, msg))
    print("  1) 先看 ui3344-totp status 的时钟同步是否为「正常」")
    print("  2) 手机时间需在 ±60 秒内（面板容差 ±1 个 30 秒周期）")
    print("  3) 仍失败就重建密钥：ui3344-totp reset-secret <新密钥> && systemctl restart ui3344")
    return 1


def panel_base():
    return os.environ.get("UI3344_URL", "https://127.0.0.1:8443").rstrip("/")


def panel_opener():
    global _PANEL_OPENER
    if _PANEL_OPENER is not None:
        return _PANEL_OPENER
    import ssl
    import http.cookiejar
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    _PANEL_OPENER = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
        urllib.request.HTTPSHandler(context=ctx))
    return _PANEL_OPENER


def panel_login():
    import re
    import urllib.parse
    password = os.environ.get("UI3344_PASS", "")
    if not password:
        print("需要 UI3344_PASS 才能校验。", file=sys.stderr)
        return None
    opener = panel_opener()
    html = opener.open(panel_base() + "/csrf-token", timeout=10).read().decode()
    m = re.search(r"[A-Za-z0-9_\-]{40,}", html)
    if not m:
        print("取不到 CSRF token，面板地址或端口不对。", file=sys.stderr)
        return None
    csrf = m.group(0)
    body = urllib.parse.urlencode({"username": os.environ.get("UI3344_USER", "3344"),
                                   "password": password, "_csrf": csrf}).encode()
    req = urllib.request.Request(panel_base() + "/login", data=body, method="POST")
    req.add_header("X-CSRF-Token", csrf)
    resp = opener.open(req, timeout=20).read().decode()
    try:
        ok = json.loads(resp).get("success")
    except Exception:
        ok = None
    if ok is False:
        print("面板登录失败，检查 UI3344_PASS。", file=sys.stderr)
        return None
    return csrf


def cmd_status():
    st = read_settings()
    secret = st.get("domainOtpSecret", "")
    cfg = json.loads(st.get("domainConfig") or "{}")
    print("TOTP 密钥  : %s（长度 %d）" % (secret or "-", len(secret)))
    print("面板地址   : %s" % (cfg.get("fqdn") or "-"))
    print("端口       : %s" % (st.get("webPort") or cfg.get("panelPort") or "-"))
    print("证书       : %s %s" % (cfg.get("certMode") or "-", cfg.get("certPath") or "-"))
    print("CF Token   : %s" % ("已配置" if st.get("cfApiToken") else "未配置"))
    print("时钟同步   : %s" % ("正常" if ntp_offset_seconds() is True else "异常/未知"))
    print("当前动态码 : %s" % (totp(secret) if secret else "-"))
    return 0


def _sqlite_backup(dest):
    src = sqlite3.connect(DB)
    dst = sqlite3.connect(dest)
    try:
        src.backup(dst)          # 走 SQLite 在线备份，WAL 内容不会丢
    finally:
        dst.close()
        src.close()


def cmd_backup(target=None):
    target = target or time.strftime(BACKUP_ROOT + "/%Y%m%d-%H%M%S")
    os.makedirs(target, mode=0o700, exist_ok=True)

    _sqlite_backup(os.path.join(target, "ui3344.db"))
    os.chmod(os.path.join(target, "ui3344.db"), 0o600)

    st = read_settings()
    secret = st.get("domainOtpSecret", "")
    with open(os.path.join(target, "domain.env"), "w") as f:
        f.write("# ui3344 域名功能与 TOTP 凭据\n")
        f.write("OTP_SECRET=%s\n" % secret)
        f.write("FQDN=%s\n" % st.get("subDomain", ""))
        f.write("CF_TOKEN=%s\n" % st.get("cfApiToken", ""))
    os.chmod(os.path.join(target, "domain.env"), 0o600)

    if secret:
        with open(os.path.join(target, "otpauth.txt"), "w") as f:
            f.write("otpauth://totp/ui3344:%s?secret=%s&issuer=ui3344&digits=6&period=30\n"
                    % (st.get("subDomain", "panel"), secret))
        os.chmod(os.path.join(target, "otpauth.txt"), 0o600)

    cfg = json.loads(st.get("domainConfig") or "{}")
    cert = cfg.get("certPath")
    if cert and os.path.isdir(cert):
        shutil.copytree(cert, os.path.join(target, "cert"), dirs_exist_ok=True)

    if os.path.isdir(CERT_DIR):
        shutil.copytree(CERT_DIR, os.path.join(target, "root-cert"), dirs_exist_ok=True)

    print("已备份到 %s" % target)
    for name in sorted(os.listdir(target)):
        print("  " + name)
    return 0


def cmd_reset_secret(secret):
    secret = secret.strip().upper()
    if len(secret) < 16:
        print("密钥过短，至少 16 个 base32 字符。", file=sys.stderr)
        return 1
    con = sqlite3.connect(DB)
    try:
        with con:
            cur = con.execute("update settings set value=? where key='domainOtpSecret'", (secret,))
            if cur.rowcount == 0:
                con.execute("insert into settings(key, value) values('domainOtpSecret', ?)", (secret,))
    finally:
        con.close()
    print("已写入新密钥 %s，重启面板后生效：systemctl restart ui3344" % secret)
    print("新的 otpauth: otpauth://totp/ui3344:panel?secret=%s&issuer=ui3344" % secret)
    return 0


def main():
    args = sys.argv[1:]
    cmd = args[0] if args else "code"
    if cmd == "code":
        return cmd_code()
    if cmd == "at":
        return cmd_code(int(args[1]) if len(args) > 1 else 0)
    if cmd == "status":
        return cmd_status()
    if cmd == "backup":
        return cmd_backup(args[1] if len(args) > 1 else None)
    if cmd == "verify":
        return cmd_verify()
    if cmd == "reset-secret":
        if len(args) < 2:
            print("用法: reset-secret <base32密钥>", file=sys.stderr)
            return 1
        return cmd_reset_secret(args[1])
    print(__doc__)
    return 1


if __name__ == "__main__":
    sys.exit(main())
