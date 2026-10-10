#!/bin/bash
# 雷电面板(ui3344) 本地安装脚本 —— 从本目录的构建产物安装，不联网、不访问上游。
#
# 凭据（域名功能的 Cloudflare API Token / TOTP 密钥）走**安装期**注入，不走编译期：
# 公开制品里一个字节都没有，装的时候用环境变量写进数据库，体验仍是「装完即可一键启用域名」。
#
#   UI3344_CF_TOKEN=xxx UI3344_OTP_SECRET=yyy bash install.sh
#
# 不传这两个变量同样能装：面板正常启动，只是 cfConfigured / otpConfigured 为 false，
# 之后按脚本结尾提示的命令补写即可。设计原因见《雷电面板-凭据与发布流程.md》。
set -e
if [ "$(id -u)" != "0" ]; then echo "请用 root 运行本脚本"; exit 1; fi
HERE="$(cd "$(dirname "$0")" && pwd)"
APP=/usr/local/ui3344
# 面板数据库（settings 表所在）。XUI_DB_FOLDER 是面板自身的目录变量，这里也认。
DB="${UI3344_DB:-/etc/ui3344/ui3344.db}"
[ -x "$HERE/ui3344" ] || { echo "未找到 $HERE/ui3344，请在解包目录内运行"; exit 1; }

# 安装前数据库是否已存在（升级 vs 全新安装）。用于决定「面板口令」给什么提示：
# 全新安装时面板刚建库，口令一定是默认的；升级时口令可能已经被改强。
DB_EXISTED=0
[ -f "$DB" ] && DB_EXISTED=1

# ---------------------------------------------------------------- 参数
# 口令优先用环境变量（不出现在 ps 里）；--password 只是等价写法，命令行参数
# 会短暂出现在 ps/proc 中，能用环境变量就别用参数。
while [ $# -gt 0 ]; do
  case "$1" in
    --username=*|--user=*) UI3344_USER="${1#*=}" ;;
    --password=*|--pass=*) UI3344_PASS="${1#*=}" ;;
    --username|--user) shift; UI3344_USER="${1:-}" ;;
    --password|--pass) shift; UI3344_PASS="${1:-}" ;;
    --url=*) UI3344_URL_FORCE="${1#*=}" ;;
    --skip-presets) UI3344_SKIP_PRESETS=1 ;;
    *) echo "! 未知参数：$1（已忽略）" ;;
  esac
  shift
done
export UI3344_USER="${UI3344_USER:-3344}"
if [ -n "${UI3344_PASS:-}" ]; then export UI3344_PASS; fi

# 服务一旦停下，之后任何一步失败都必须把它拉回来。
# 否则一次失败的升级会把面板留在停止状态（真实踩过：xray 子进程持有
# bin/xray-linux-amd64 导致 cp 报 "Text file busy"，脚本以 set -e 退出，
# 服务再没被启动，面板直接下线）。
SERVICE_STOPPED=0
# >>> tmp-cleanup >>>
# 清掉已经复制到 $APP 的大体积载荷（二进制 214MB）——这是 /tmp 残留的第二个来源。
# 只有在线安装器会设 UI3344_CLEAN_PAYLOAD=1（那个 mktemp 工作目录是它自己的，
# 装完由它的 EXIT trap 整目录回收）；手工解包安装时不动用户的文件。
clean_payload() {
  [ "${UI3344_CLEAN_PAYLOAD:-0}" = "1" ] || return 0
  [ "${UI3344_KEEP_TMP:-0}" = "1" ] && return 0
  freed=0
  for f in "$HERE/ui3344" "$HERE"/bin/*; do
    [ -f "$f" ] || continue
    sz=$(stat -c %s "$f" 2>/dev/null || echo 0)
    case "$sz" in ''|*[!0-9]*) sz=0 ;; esac
    freed=$((freed + sz))
    rm -f "$f" 2>/dev/null || true
  done
  if [ "$freed" -gt 0 ]; then
    echo "> 已清理安装载荷（约 $((freed / 1048576)) MB，减少 /tmp 残留）"
  fi
}
# <<< tmp-cleanup <<<
restore_service_on_error() {
  rc=$?
  if [ "$rc" -ne 0 ] && [ "$SERVICE_STOPPED" = "1" ]; then
    echo "! 安装过程出错(rc=$rc)，正在尝试把服务恢复…" >&2
    systemctl start ui3344 2>/dev/null || rc-service ui3344 start 2>/dev/null || true
    systemctl is-active --quiet ui3344 2>/dev/null && echo "! 服务已恢复运行" >&2
  fi
  clean_payload
}
trap restore_service_on_error EXIT INT TERM

# replace_file <源> <目标>：先删再拷。
# 运行中的进程（残留的 xray、未完全退出的子进程）会持有目标 inode，直接
# `cp -f` 覆盖必然报 "Text file busy"；而 unlink 总是成功——旧 inode 由持有者
# 继续用，新文件立刻可用。
replace_file() {
  rm -f "$2"
  cp -f "$1" "$2"
}

# >>> credential-bootstrap >>>
# 这一段（到下面的 <<< credential-bootstrap <<< 为止）只依赖 $DB / $HERE，
# 没有任何安装副作用，所以 packaging/test-install-credentials.sh 能把它单独抽出来，
# 在临时数据库上真跑一遍——不必真的安装，也不会碰线上服务与线上数据库。
#
# ------------------------------------------------- 凭据（安装期注入，见文件头说明）
# 公开制品不含凭据，凭据由环境变量在安装时写进 settings 表。
# 写入时机刻意分两段：
#   · 升级（数据库已存在）：**在启动服务之前**写好，面板一起来就带着凭据；
#   · 全新安装（数据库还没建，settings 表由面板首次启动创建）：
#     先起一次让面板建库 → 写入 → 再重启，让面板从库里读到。
# 两条路径最终都是「面板带着凭据在跑」，且全程只打印脱敏值（前 4 + 后 4）。
CREDS_PROVIDED=0
CREDS_DONE=0
CREDS_PENDING=0
if [ -n "${UI3344_CF_TOKEN:-}" ] || [ -n "${UI3344_OTP_SECRET:-}" ]; then
  CREDS_PROVIDED=1
fi

# 真正调用写入脚本。返回：0=完成 2=数据库还没就绪（应稍后重试） 其它=失败
run_credential_write() {
  if ! command -v python3 >/dev/null 2>&1; then
    echo "! 系统缺少 python3，无法自动写入凭据（可稍后在面板「域名」页手工填写）"
    return 4
  fi
  if [ ! -f "$HERE/set-credentials.py" ]; then
    echo "! 安装包内缺少 set-credentials.py，无法自动写入凭据（可稍后在面板「域名」页手工填写）"
    return 4
  fi
  UI3344_DB="$DB" python3 "$HERE/set-credentials.py"
}

# credential_step <阶段说明>
# 缺凭据时**什么也不做**（面板照常启动，只是域名功能未配置）——缺凭据不是错误。
credential_step() {
  if [ "$CREDS_PROVIDED" != "1" ]; then
    return 0
  fi
  if [ "$CREDS_DONE" = "1" ]; then
    return 0
  fi
  echo "> 写入域名功能凭据到 $DB（$1）…"
  # 必须这样显式接住退出码：写成 `if run_credential_write; then ... fi` 的话，
  # `fi` 之后的 $? 是整个 if 语句的状态（没有 else 分支时恒为 0），
  # 退出码 2 会被吞掉——「全新安装等建库后重试」这条路径就永远不会触发，
  # 凭据会被静默丢弃。这不是理论问题：S6/S7 用例就是靠它抓出来的。
  rc=0
  run_credential_write || rc=$?
  if [ "$rc" = "0" ]; then
    CREDS_DONE=1
    CREDS_PENDING=0
    return 0
  fi
  if [ "$rc" = "2" ]; then
    CREDS_PENDING=1
    echo "  · 数据库/表还不存在（面板首次启动时创建），稍后自动重试"
    return 1
  fi
  CREDS_PENDING=0
  echo "! 凭据写入失败(rc=$rc)：面板仍会正常启动，但域名功能暂不可用"
  return 1
}
# <<< credential-bootstrap <<<

# >>> panel-url-detect >>>
# 面板基址探测：**不能写死 http://127.0.0.1:33441**。
#
# 线上实测：启用域名功能后面板被搬到 8443 且是 HTTPS（Cloudflare Origin CA 证书），
# 此时拿明文 HTTP 去敲 TLS 端口，对端回的不是 HTTP，create-inbounds.py 直接抛
#
#   http.client.UnknownProtocol: HTTP/0.0
#
# 于是「创建预设协议」静默失败；「等待面板就绪」的循环也会白等满 30 秒。
# 这里 http / https 各试一次（回环关证书校验，Origin CA 证书本来也不该进系统信任库），
# 谁先返回 200 就用谁；都探测不到就**不设** UI3344_URL，交给 create-inbounds.py
# 自带的 detect_base() 兜底。
PANEL_URL=""
panel_probe() { # <base> → 0 = 面板在这个地址上应答
  curl -kfsS --max-time 3 "$1/csrf-token" >/dev/null 2>&1
}
detect_panel_url() {
  PANEL_URL=""
  command -v curl >/dev/null 2>&1 || return 1
  # 安全入口必须带尾斜杠，否则拼出来的探测路径是 /ui3344csrf-token。
  local wbp="$WBP"
  case "$wbp" in */) ;; *) wbp="$wbp/" ;; esac
  local base
  for base in "http://127.0.0.1:${PORT}${wbp}" "https://127.0.0.1:${PORT}${wbp}"; do
    if panel_probe "$base"; then PANEL_URL="$base"; return 0; fi
  done
  return 1
}
wait_panel_url() { # <最多等几秒>
  local i
  for i in $(seq 1 "${1:-30}"); do
    detect_panel_url && return 0
    sleep 1
  done
  PANEL_URL=""
  return 1
}
# <<< panel-url-detect <<<

# >>> panel-credentials >>>
# 面板管理员口令。面板新版把口令存在 users 表，`ui3344 setting -show` **不打印**
# 用户名/口令（只有 -password 设置、-getApiToken），所以安装器读不出来，只能由
# 调用方给出或按默认值试。缺口令时**不静默失败**，直接打印可照抄的补救命令。
# APP 在这里再兜一次默认值：这一段要能被单独抽出来离线跑，不能依赖脚本上半部分。
APP="${APP:-/usr/local/ui3344}"
PANEL_USER="${UI3344_USER:-3344}"
# 未显式提供口令时按默认值试一次（全新安装时面板刚建库，口令必然是默认的）；
# 升级场景口令可能已被改强，这时 create-inbounds.py 会报 401/403，我们把补救
# 命令打出来，而不是悄悄跳过预设。
PANEL_PASS_PROVIDED=0
PANEL_PASS="${UI3344_PASS:-}"
if [ -n "$PANEL_PASS" ]; then PANEL_PASS_PROVIDED=1; else PANEL_PASS="3344"; fi

panel_credential_note() {
  if [ "$PANEL_PASS_PROVIDED" = "1" ]; then
    return 0
  fi
  echo "  提示：未提供 UI3344_PASS，按默认口令尝试；面板口令若已改强，预设创建会认证失败"
}
panel_credential_hint() {
  echo "! 预设创建失败。最常见原因：面板管理员口令已不是默认的 3344。"
  echo "  请在服务器上补跑（<面板口令> 换成你自己设的那个；本脚本不打印、不保存口令）："
  echo "    UI3344_PASS='<面板口令>' python3 $APP/create-inbounds.py"
  echo "  或者重跑安装： UI3344_PASS='<面板口令>' bash install.sh"
}
# <<< panel-credentials <<<

echo "> 停止旧服务(如有)…"
systemctl stop ui3344 2>/dev/null || rc-service ui3344 stop 2>/dev/null || true
# 给子进程一点退出时间；仍不退出的（例如手工起的残留 xray）不阻塞升级，
# 因为下面用的是「先删再拷」。
for _ in 1 2 3 4 5; do
  pgrep -f "$APP/bin/xray" >/dev/null 2>&1 || break
  sleep 1
done
SERVICE_STOPPED=1

echo "> 安装文件到 $APP …"
install -d -m755 "$APP" "$APP/bin" /etc/ui3344 /var/log/ui3344
replace_file "$HERE/ui3344" "$APP/ui3344"; chmod +x "$APP/ui3344"
if [ -d "$HERE/bin" ]; then
  for f in "$HERE"/bin/*; do
    [ -e "$f" ] || continue
    replace_file "$f" "$APP/bin/$(basename "$f")"
  done
fi
chmod +x "$APP"/bin/* 2>/dev/null || true
# 残留进程仍在读旧内核时给出提示（不致命，新文件已就位）
pgrep -f "$APP/bin/xray" >/dev/null 2>&1 && \
  echo "! 仍有进程占用 $APP/bin/xray（多为手工启动的残留内核）；新文件已就位，重启服务后即生效"
# 保留辅助脚本，供安装后离线使用（预设重建 / 订阅配置 / 域名功能 / 补写凭据）
for f in create-inbounds.py configure-subscription.py domain-setup.py set-credentials.py; do [ -f "$HERE/$f" ] && cp -f "$HERE/$f" "$APP/$f" && chmod +x "$APP/$f"; done
cp -f "$HERE/ui3344.sh" /usr/bin/ui3344; chmod +x /usr/bin/ui3344

# 阶段 A：升级场景——数据库已存在，趁服务还停着把凭据写好，
# 这样面板重启时就已经能读到凭据，不需要第二次重启。
if [ -f "$DB" ]; then
  credential_step "启动前写入（升级场景）" || true
fi

if [ -d /run/systemd/system ]; then
  echo "> 安装 systemd 服务…"
  cp -f "$HERE/ui3344.service.debian" /etc/systemd/system/ui3344.service
  chmod 644 /etc/systemd/system/ui3344.service
  systemctl daemon-reload
  systemctl enable ui3344 >/dev/null 2>&1 || true
  systemctl restart ui3344
  sleep 2
  systemctl is-active --quiet ui3344 && echo "> ui3344 服务已启动" || { echo "! 服务启动失败，请用 journalctl -u ui3344 查看"; exit 1; }
elif [ -f /sbin/openrc-run ]; then
  echo "> 安装 openrc 服务…"
  cp -f "$HERE/ui3344.rc" /etc/init.d/ui3344; chmod +x /etc/init.d/ui3344
  rc-update add ui3344 default >/dev/null 2>&1 || true
  rc-service ui3344 restart
else
  echo "! 未识别 init 系统，请手动运行 $APP/ui3344"; exit 1
fi

# 阶段 B：全新安装——settings 表由面板首次启动创建，所以先等库建好，
# 写入凭据后再重启一次让面板读到。等待最多 60 秒；等不到就放弃（不阻塞安装）。
if [ "$CREDS_PENDING" = "1" ] && [ "$CREDS_DONE" != "1" ]; then
  echo "> 等待面板建库（最多 60 秒）…"
  for i in $(seq 1 60); do
    if [ -f "$DB" ] && python3 -c 'import sqlite3,sys; sqlite3.connect("file:"+sys.argv[1]+"?mode=ro",uri=True).execute("select 1 from settings limit 1")' "$DB" >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  if credential_step "建库后写入（全新安装）"; then
    echo "> 重启面板使凭据生效…"
    systemctl restart ui3344 2>/dev/null || rc-service ui3344 restart 2>/dev/null || true
    sleep 2
  fi
fi

PORT=$("$APP/ui3344" setting -show true 2>/dev/null | grep -Eo 'port: .+' | awk '{print $2}')
WBP=$("$APP/ui3344" setting -show true 2>/dev/null | grep -Eo 'webBasePath: .+' | awk '{print $2}')
PORT="${PORT:-33441}"
WBP="${WBP:-/ui3344/}"
case "$WBP" in */) ;; *) WBP="$WBP/" ;; esac
[ -n "${UI3344_URL_FORCE:-}" ] && PANEL_URL="${UI3344_URL_FORCE%/}"

# 生成自签证书（供 VMess-TLS 档使用）
if command -v openssl >/dev/null 2>&1; then
  install -d -m 700 /root/cert/ui3344-selfsigned
  if [ ! -f /root/cert/ui3344-selfsigned/fullchain.pem ]; then
    openssl req -x509 -newkey rsa:2048 -nodes \
      -keyout /root/cert/ui3344-selfsigned/privkey.pem \
      -out /root/cert/ui3344-selfsigned/fullchain.pem \
      -days 3650 -subj "/CN=ui3344" >/dev/null 2>&1 || true
  fi
fi

# 创建预设协议（9 个入站）；可用 UI3344_SKIP_PRESETS=1 跳过
if [ "${UI3344_SKIP_PRESETS:-0}" != "1" ] && command -v python3 >/dev/null 2>&1 && [ -f "$HERE/create-inbounds.py" ]; then
  echo "> 创建预设协议(9 个入站)…"
  # 等待面板就绪：http/https 都探（最多 30 秒）。探不到就不再白等，
  # 交给 create-inbounds.py 自己的 detect_base() 兜底。
  if [ -z "$PANEL_URL" ]; then
    if wait_panel_url 30; then
      echo "  · 面板地址：$PANEL_URL"
    else
      echo "! 30 秒内没探测到面板地址（http/https 都试过）；交给 create-inbounds.py 自行探测"
    fi
  else
    echo "  · 面板地址（--url 指定）：$PANEL_URL"
  fi
  panel_credential_note

  # 口令只经环境变量传给子进程，绝不出现在命令行参数里，也不打印。
  run_presets() {
    if [ -n "$PANEL_URL" ]; then
      UI3344_URL="$PANEL_URL" UI3344_USER="$PANEL_USER" UI3344_PASS="$PANEL_PASS" \
        python3 "$HERE/create-inbounds.py"
    else
      UI3344_USER="$PANEL_USER" UI3344_PASS="$PANEL_PASS" python3 "$HERE/create-inbounds.py"
    fi
  }
  if run_presets; then
    :
  else
    sleep 3
    run_presets || panel_credential_hint
  fi
fi

# 配置订阅服务（开启 Clash/JSON 订阅 + 内置离线分流规则）；可用 UI3344_SKIP_PRESETS=1 跳过
if [ "${UI3344_SKIP_PRESETS:-0}" != "1" ] && command -v python3 >/dev/null 2>&1 && [ -f "$HERE/configure-subscription.py" ]; then
  echo "> 配置订阅服务(Clash/JSON + 内置分流规则)…"
  python3 "$HERE/configure-subscription.py" || echo "! 订阅配置失败，可稍后手动运行 configure-subscription.py"
  # 订阅服务在启动时读取这些设置，重启使其生效
  systemctl restart ui3344 2>/dev/null || rc-service ui3344 restart 2>/dev/null || true
  sleep 2
fi

echo "========================================"
echo " 安装完成"
echo " 管理命令 : ui3344"
echo " 端口     : ${PORT:-33441}"
echo " 安全入口 : ${WBP:-/ui3344/}"
echo " 访问地址 : http://<服务器IP>:${PORT:-33441}${WBP:-/ui3344/}"
echo " 默认账号 : 3344 / 3344 （请登录后尽快改强）"
if [ "$CREDS_DONE" = "1" ]; then
  echo " 域名凭据 : 已写入（CF Token + TOTP），域名功能可一键启用"
elif [ "$CREDS_PROVIDED" = "1" ]; then
  echo " 域名凭据 : ! 已提供但写入未成功，域名功能暂不可用"
else
  echo " 域名凭据 : 未配置（cfConfigured / otpConfigured = false）"
fi
echo "========================================"

# 未配置凭据时给出确切补写命令。凭据不再编译进二进制，只能安装期注入或事后补写：
if [ "$CREDS_DONE" != "1" ]; then
  echo
  echo "补配置域名凭据（二选一）："
  echo "  A) 重跑安装脚本，带上环境变量（幂等，不会覆盖已有非空值）："
  echo "       UI3344_CF_TOKEN=<cfat_...> UI3344_OTP_SECRET=<base32密钥> bash install.sh"
  echo "  B) 只写数据库，不重装（面板无需停机，SQLite 会自行串行化写入）："
  echo "       UI3344_CF_TOKEN=<cfat_...> UI3344_OTP_SECRET=<base32密钥> \\"
  echo "         python3 $APP/set-credentials.py"
  echo "     · 默认不覆盖已有非空值；确实要换值加 --force"
  echo "     · 查看当前状态： python3 $APP/set-credentials.py --check"
  echo "  也可以登录面板在「域名」页手工填写（需要 TOTP 动态码授权）。"
fi

echo "========================================"
