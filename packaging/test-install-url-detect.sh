#!/bin/bash
# 测试 packaging/install.sh 的「面板基址探测」与「管理员口令传递」逻辑。
#
# 为什么要有这个脚本：
#   线上真实踩到两个坑，都是装完才发现的——
#     · install.sh 把面板基址写死成 `http://127.0.0.1:${PORT}`。启用域名后面板搬到
#       8443 且是 HTTPS，于是拿明文 HTTP 去敲 TLS 端口，create-inbounds.py 抛
#       `http.client.UnknownProtocol: HTTP/0.0`，预设创建静默失败；
#       「等待面板就绪」的循环还会白等满 30 秒。
#     · 面板管理员口令已从默认 3344 改强，而面板新版把口令存在 users 表里，
#       `ui3344 setting -show` 不打印它。install.sh 又从不传 UI3344_USER/UI3344_PASS，
#       于是任何非默认口令的面板，预设创建都会认证失败。
#
#   install.sh 把这两段逻辑包在 `# >>> panel-url-detect >>>` / `# >>> panel-credentials >>>`
#   标记之间（它们只依赖 $PORT/$WBP/$UI3344_* 这几个变量，没有安装副作用），
#   这里把它们抽出来，用**本地起的假面板**（HTTP + 自签 HTTPS）真跑一遍。
#
# 用法（在 packaging/ 或解包目录里）：
#     bash test-install-url-detect.sh
#   退出码 0 = 全部通过。
#
# 安全护栏：所有操作都在 mktemp -d 出来的临时目录内；不碰线上服务、不碰线上数据库，
# 也不打印任何真实口令（用例里只用占位符 SENTINEL-PASSWORD-DO-NOT-LEAK）。
set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
INSTALL_SH="$HERE/install.sh"
CREATE_PY="$HERE/create-inbounds.py"
DOMAIN_PY="$HERE/domain-setup.py"

for f in "$INSTALL_SH" "$CREATE_PY" "$DOMAIN_PY"; do
  [ -f "$f" ] || { echo "缺少 $f"; exit 1; }
done

TMP="$(mktemp -d)"
chmod 700 "$TMP"
STUB_PID=""
cleanup() {
  [ -n "$STUB_PID" ] && kill "$STUB_PID" 2>/dev/null
  rm -rf "$TMP"
}
trap cleanup EXIT

PASS=0; FAIL=0
ok()  { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
bad() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }
check() { # <说明> <实际> <期望>
  if [ "$2" = "$3" ]; then ok "$1 = [$2]"; else bad "$1：期望 [$3]，实际 [$2]"; fi
}

# 抽出两段标记之间的代码（与 test-install-credentials.sh 同一套路）
FUNCS="$TMP/url-funcs.sh"
sed -n '/^# >>> panel-url-detect >>>$/,/^# <<< panel-url-detect <<<$/p' "$INSTALL_SH" > "$FUNCS"
sed -n '/^# >>> panel-credentials >>>$/,/^# <<< panel-credentials <<<$/p' "$INSTALL_SH" >> "$FUNCS"
if ! grep -q 'detect_panel_url()' "$FUNCS"; then
  echo "!! 无法从 install.sh 抽出 panel-url-detect 段（标记被改动过？）"; exit 1
fi
if ! grep -q 'panel_credential_hint()' "$FUNCS"; then
  echo "!! 无法从 install.sh 抽出 panel-credentials 段（标记被改动过？）"; exit 1
fi

# python 依赖检查（缺 python3/openssl 时只跳过对应用例，不算失败）
HAVE_PY=0; command -v python3 >/dev/null 2>&1 && HAVE_PY=1
HAVE_OPENSSL=0; command -v openssl >/dev/null 2>&1 && HAVE_OPENSSL=1
HAVE_CURL=0; command -v curl >/dev/null 2>&1 && HAVE_CURL=1

SENTINEL='SENTINEL-PASSWORD-DO-NOT-LEAK'

echo "============================================================"
echo " install.sh 面板基址探测 + 管理员口令传递 —— 逻辑测试"
echo " install.sh : $INSTALL_SH"
echo " 临时目录   : $TMP"
echo " python3=$HAVE_PY openssl=$HAVE_OPENSSL curl=$HAVE_CURL"
echo "============================================================"

# ---------------------------------------------------------------- 假面板
HTTP_PORT=""; HTTPS_PORT=""
if [ "$HAVE_PY" = "1" ] && [ "$HAVE_OPENSSL" = "1" ]; then
  cat > "$TMP/stubs.py" <<'PY'
"""本地假面板：一个 HTTP 端口 + 一个自签 HTTPS 端口。
/csrf-token 返回 200（探测用）；POST /login 返回 {"success": false}（模拟口令已改强）。
两个端口都绑 0（内核分配），写进 argv[1] 指定的文件，避免与线上端口冲突。"""
import http.server, ssl, sys, threading

class H(http.server.BaseHTTPRequestHandler):
    def _send(self, code, body):
        self.send_response(code)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.endswith("/csrf-token"):
            self._send(200, b"x" * 48)
        else:
            self._send(404, b"nope")

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(length)
        if self.path.endswith("/login"):
            self._send(200, b'{"success": false, "msg": "Wrong username or password"}')
        else:
            self._send(404, b"nope")

    def log_message(self, *a):
        pass

plain = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
tls = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(sys.argv[2])
tls.socket = ctx.wrap_socket(tls.socket, server_side=True)
with open(sys.argv[1], "w") as fh:
    fh.write("%d %d\n" % (plain.socket.getsockname()[1], tls.socket.getsockname()[1]))
threading.Thread(target=tls.serve_forever, daemon=True).start()
plain.serve_forever()
PY
  openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "$TMP/k.pem" -out "$TMP/c.pem" -days 1 -subj "/CN=localhost" >/dev/null 2>&1
  cat "$TMP/c.pem" "$TMP/k.pem" > "$TMP/cert.pem"
  python3 "$TMP/stubs.py" "$TMP/ports" "$TMP/cert.pem" &
  STUB_PID=$!
  for _ in $(seq 1 50); do
    [ -s "$TMP/ports" ] && break
    sleep 0.2
  done
  if [ -s "$TMP/ports" ]; then
    HTTP_PORT="$(awk '{print $1}' "$TMP/ports")"
    HTTPS_PORT="$(awk '{print $2}' "$TMP/ports")"
    echo "假面板: http://127.0.0.1:$HTTP_PORT/ui3344/  https://127.0.0.1:$HTTPS_PORT/ui3344/"
  else
    bad "假面板没起来（ports 文件为空）"
  fi
else
  echo "（缺 python3 / openssl，跳过需要假面板的用例）"
fi

# ---------------------------------------------------------------- S1 HTTP 端口用 http
echo
echo "S1 面板在 HTTP 端口上：必须先探测到 http://127.0.0.1:\$HTTP_PORT/ui3344/"
if [ -n "$HTTP_PORT" ]; then
  out=$( ( PORT="$HTTP_PORT"; WBP="/ui3344/"; . "$FUNCS"
           detect_panel_url; echo "DETECTRC=$? URL=$PANEL_URL" ) 2>&1 )
  check "S1 探测返回码" "$(printf '%s' "$out" | sed -n 's/.*DETECTRC=\([0-9]*\).*/\1/p')" "0"
  check "S1 PANEL_URL" "$(printf '%s' "$out" | sed -n 's/.*URL=\(.*\)$/\1/p')" "http://127.0.0.1:$HTTP_PORT/ui3344/"
  if printf '%s' "$out" | grep -q '^https://'; then bad "S1 不该选 https"; else ok "S1 选的是 http（明文端口）"; fi
else
  echo "  [SKIP] 假面板未启动"
fi

# ---------------------------------------------------------------- S2 TLS 端口用 https
echo
echo "S2 只在 TLS 端口上（启用域名后线上就是这个状态）：必须自动选 https"
if [ -n "$HTTPS_PORT" ]; then
  # 把 PORT 指到 TLS 端口：http 探测必然失败（明文打 TLS），只有 https 能通。
  out=$( ( PORT="$HTTPS_PORT"; WBP="/ui3344/"; . "$FUNCS"
           detect_panel_url; echo "DETECTRC=$? URL=$PANEL_URL" ) 2>&1 )
  check "S2 探测返回码" "$(printf '%s' "$out" | sed -n 's/.*DETECTRC=\([0-9]*\).*/\1/p')" "0"
  check "S2 PANEL_URL" "$(printf '%s' "$out" | sed -n 's/.*URL=\(.*\)$/\1/p')" "https://127.0.0.1:$HTTPS_PORT/ui3344/"
  # 这一条正是线上那次失败的回归：写死 http 的旧实现会拿到 HTTP/0.0
  if printf '%s' "$out" | grep -q "^http://127.0.0.1:$HTTPS_PORT"; then
    bad "S2 对 TLS 端口选了 http（就是线上 UnknownProtocol: HTTP/0.0 的成因）"
  else
    ok "S2 对 TLS 端口没有选 http"
  fi
else
  echo "  [SKIP] 假面板未启动"
fi

# ---------------------------------------------------------------- S3 都没监听
echo
echo "S3 两个 scheme 都不通：不设 UI3344_URL，并且不能长时间白等"
if [ -n "$HTTP_PORT" ]; then
  DEAD_PORT=1   # 1 号端口不会有服务在听
  start=$(date +%s)
  out=$( ( PORT="$DEAD_PORT"; WBP="/ui3344/"; . "$FUNCS"
           detect_panel_url; echo "DETECTRC=$? URL=[$PANEL_URL]"
           wait_panel_url 2; echo "WAITRC=$? WAITURL=[$PANEL_URL]" ) 2>&1 )
  elapsed=$(( $(date +%s) - start ))
  check "S3 detect 返回码" "$(printf '%s' "$out" | sed -n 's/.*DETECTRC=\([0-9]*\).*/\1/p')" "1"
  check "S3 PANEL_URL 为空" "$(printf '%s' "$out" | sed -n 's/.*URL=\[\(.*\)\]$/\1/p')" ""
  check "S3 wait_panel_url 返回码" "$(printf '%s' "$out" | sed -n 's/.*WAITRC=\([0-9]*\).*/\1/p')" "1"
  if [ "$elapsed" -le 20 ]; then ok "S3 总耗时 ${elapsed}s（预算内）"; else bad "S3 耗时 ${elapsed}s，疑似白等"; fi
else
  echo "  [SKIP] 假面板未启动"
fi

# ---------------------------------------------------------------- S4 两个都在 → http 优先
echo
echo "S4 安全入口没写尾斜杠时补成 /ui3344/（探测路径才拼得对）"
if [ -n "$HTTP_PORT" ]; then
  out=$( ( PORT="$HTTP_PORT"; WBP="/ui3344"; . "$FUNCS"
           detect_panel_url; echo "URL=$PANEL_URL" ) 2>&1 )
  check "S4 PANEL_URL" "$(printf '%s' "$out" | sed -n 's/.*URL=\(.*\)$/\1/p')" "http://127.0.0.1:$HTTP_PORT/ui3344/"
else
  echo "  [SKIP] 假面板未启动"
fi

# ---------------------------------------------------------------- S5 缺 curl 不卡死
echo
echo "S5 目标机没有 curl：探测立刻失败（不卡死），且不设 UI3344_URL"
NOCURL="$TMP/nocurl"; mkdir -p "$NOCURL"
# 只暴露一个不含 curl 的 PATH；bash 内建命令与外层 date 仍可用。
s5_start=$(date +%s)
out=$( ( PATH="$NOCURL"; PORT="${HTTP_PORT:-1}"; WBP="/ui3344/"; . "$FUNCS"
         detect_panel_url; echo "DETECTRC=$? URL=[$PANEL_URL]" ) 2>&1 )
s5_elapsed=$(( $(date +%s) - s5_start ))
check "S5 返回码" "$(printf '%s' "$out" | sed -n 's/.*DETECTRC=\([0-9]*\).*/\1/p')" "1"
check "S5 PANEL_URL 为空" "$(printf '%s' "$out" | sed -n 's/.*URL=\[\(.*\)\].*/\1/p')" ""
if [ "$s5_elapsed" -le 5 ]; then
  ok "S5 立刻返回（${s5_elapsed}s）"
else
  bad "S5 无 curl 时代码在等待（${s5_elapsed}s）"
fi

# ---------------------------------------------------------------- S6 口令变量
echo
echo "S6 口令传递：显式提供 → 标记为已提供；缺失 → 按默认试并打印提示"
out=$( ( UI3344_USER=admin UI3344_PASS="$SENTINEL"; . "$FUNCS"
         echo "USER=$PANEL_USER PROVIDED=$PANEL_PASS_PROVIDED PASSMATCH=$([ "$PANEL_PASS" = "$SENTINEL" ] && echo yes || echo no)"
         panel_credential_note ) 2>&1 )
check "S6 PANEL_USER" "$(printf '%s' "$out" | sed -n 's/.*USER=\([^ ]*\).*/\1/p')" "admin"
check "S6 PANEL_PASS_PROVIDED" "$(printf '%s' "$out" | sed -n 's/.*PROVIDED=\([0-9]*\).*/\1/p')" "1"
check "S6 PANEL_PASS 用的是环境变量值" "$(printf '%s' "$out" | sed -n 's/.*PASSMATCH=\([a-z]*\).*/\1/p')" "yes"
if printf '%s' "$out" | grep -q '提示：'; then bad "S6 已提供口令时不该再打印缺口令提示"; else ok "S6 已提供口令时不打扰"; fi
if printf '%s' "$out" | grep -qF "$SENTINEL"; then bad "S6 口令被打印出来了"; else ok "S6 输出里没有口令明文"; fi

out=$( ( unset UI3344_PASS; . "$FUNCS"
         echo "PROVIDED=$PANEL_PASS_PROVIDED PASS=$PANEL_PASS"; panel_credential_note ) 2>&1 )
check "S6b 缺失时标记未提供" "$(printf '%s' "$out" | sed -n 's/.*PROVIDED=\([0-9]*\).*/\1/p')" "0"
check "S6b 缺失时按默认口令试" "$(printf '%s' "$out" | sed -n 's/.*PASS=\([^ ]*\).*/\1/p')" "3344"
if printf '%s' "$out" | grep -q '口令若已改强'; then ok "S6b 打印了「口令若已改强」提示"; else bad "S6b 缺少提示：$out"; fi

echo
echo "S7 失败提示必须可照抄、且不含任何真实口令"
out=$( ( . "$FUNCS"; panel_credential_hint ) 2>&1 )
if printf '%s' "$out" | grep -qF "UI3344_PASS='<面板口令>' python3"; then
  ok "S7 给出了可照抄的补救命令"
else
  bad "S7 补救命令格式不对：$out"
fi
if printf '%s' "$out" | grep -qF "$SENTINEL"; then bad "S7 提示里出现了口令明文"; else ok "S7 提示里没有口令明文"; fi

# ---------------------------------------------------------------- S8 python 侧
echo
echo "S8 create-inbounds.py：缺口令时立刻给补救命令，且不触网（退出码 4）"
out=$( ( env -u UI3344_PASS UI3344_URL="http://127.0.0.1:1/ui3344" python3 "$CREATE_PY" ) 2>&1 )
rc=$?
check "S8 退出码" "$rc" "4"
if printf '%s' "$out" | grep -qF "UI3344_PASS='<面板口令>'"; then ok "S8 提示可照抄"; else bad "S8 提示不对：$out"; fi

echo
echo "S9 create-inbounds.py --help 可离线运行，并说明 UI3344_PASS 必填"
out=$( python3 "$CREATE_PY" --help 2>&1 ); rc=$?
check "S9 退出码" "$rc" "0"
if printf '%s' "$out" | grep -q 'UI3344_PASS'; then ok "S9 help 里有 UI3344_PASS"; else bad "S9 help 缺少说明"; fi

echo
echo "S10 口令错误（假面板返回 success:false）：必须提示「口令很可能已改强」，不能静默"
if [ -n "$HTTP_PORT" ]; then
  out=$( ( UI3344_URL="http://127.0.0.1:$HTTP_PORT/ui3344" UI3344_USER=admin UI3344_PASS="$SENTINEL" \
           python3 "$CREATE_PY" ) 2>&1 ); rc=$?
  check "S10 退出码" "$rc" "3"
  if printf '%s' "$out" | grep -q '口令很可能已经改强'; then ok "S10 指出了口令已改强的可能"; else bad "S10 缺少该提示：$out"; fi
  if printf '%s' "$out" | grep -qF "UI3344_PASS='<面板口令>'"; then ok "S10 给了补救命令"; else bad "S10 缺补救命令"; fi
  if printf '%s' "$out" | grep -qF "$SENTINEL"; then bad "S10 输出里出现了口令明文"; else ok "S10 没有泄露口令"; fi
else
  echo "  [SKIP] 假面板未启动"
fi

echo
echo "S11 domain-setup.py：缺口令时同样立刻给出补救命令（退出码 4）"
out=$( ( env -u UI3344_PASS python3 "$DOMAIN_PY" status ) 2>&1 ); rc=$?
check "S11 退出码" "$rc" "4"
if printf '%s' "$out" | grep -qF "UI3344_PASS='<面板口令>'"; then ok "S11 提示可照抄"; else bad "S11 提示不对：$out"; fi

echo
echo "============================================================"
echo " 通过 $PASS 项，失败 $FAIL 项"
echo "============================================================"
[ "$FAIL" -eq 0 ] || exit 1
exit 0