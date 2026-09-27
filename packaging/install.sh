#!/bin/bash
# 雷电面板(ui3344) 本地安装脚本 —— 从本目录的构建产物安装，不联网、不访问上游。
set -e
if [ "$(id -u)" != "0" ]; then echo "请用 root 运行本脚本"; exit 1; fi
HERE="$(cd "$(dirname "$0")" && pwd)"
APP=/usr/local/ui3344
[ -x "$HERE/ui3344" ] || { echo "未找到 $HERE/ui3344，请在解包目录内运行"; exit 1; }

# 服务一旦停下，之后任何一步失败都必须把它拉回来。
# 否则一次失败的升级会把面板留在停止状态（真实踩过：xray 子进程持有
# bin/xray-linux-amd64 导致 cp 报 "Text file busy"，脚本以 set -e 退出，
# 服务再没被启动，面板直接下线）。
SERVICE_STOPPED=0
restore_service_on_error() {
  rc=$?
  if [ "$rc" -ne 0 ] && [ "$SERVICE_STOPPED" = "1" ]; then
    echo "! 安装过程出错(rc=$rc)，正在尝试把服务恢复…" >&2
    systemctl start ui3344 2>/dev/null || rc-service ui3344 start 2>/dev/null || true
    systemctl is-active --quiet ui3344 2>/dev/null && echo "! 服务已恢复运行" >&2
  fi
}
trap restore_service_on_error EXIT

# replace_file <源> <目标>：先删再拷。
# 运行中的进程（残留的 xray、未完全退出的子进程）会持有目标 inode，直接
# `cp -f` 覆盖必然报 "Text file busy"；而 unlink 总是成功——旧 inode 由持有者
# 继续用，新文件立刻可用。
replace_file() {
  rm -f "$2"
  cp -f "$1" "$2"
}

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
# 保留辅助脚本，供安装后离线使用（预设重建 / 订阅配置 / 域名功能）
for f in create-inbounds.py configure-subscription.py domain-setup.py; do [ -f "$HERE/$f" ] && cp -f "$HERE/$f" "$APP/$f" && chmod +x "$APP/$f"; done
cp -f "$HERE/ui3344.sh" /usr/bin/ui3344; chmod +x /usr/bin/ui3344

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

PORT=$("$APP/ui3344" setting -show true 2>/dev/null | grep -Eo 'port: .+' | awk '{print $2}')
WBP=$("$APP/ui3344" setting -show true 2>/dev/null | grep -Eo 'webBasePath: .+' | awk '{print $2}')

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
  # 等待面板就绪（最多 30 秒），避免安装时面板还没起来导致预设创建失败
  for i in $(seq 1 30); do
    curl -fsS "http://127.0.0.1:${PORT:-33441}${WBP:-/ui3344/}csrf-token" >/dev/null 2>&1 && break
    sleep 1
  done
  UI3344_URL="http://127.0.0.1:${PORT:-33441}${WBP:-/ui3344/}" python3 "$HERE/create-inbounds.py" || { sleep 3; UI3344_URL="http://127.0.0.1:${PORT:-33441}${WBP:-/ui3344/}" python3 "$HERE/create-inbounds.py"; } || echo "! 预设创建失败，可稍后手动运行 create-inbounds.py"
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
echo "========================================"
