#!/bin/bash
# 雷电面板(ui3344) 本地安装脚本 —— 从本目录的构建产物安装，不联网、不访问上游。
set -e
if [ "$(id -u)" != "0" ]; then echo "请用 root 运行本脚本"; exit 1; fi
HERE="$(cd "$(dirname "$0")" && pwd)"
APP=/usr/local/ui3344
[ -x "$HERE/ui3344" ] || { echo "未找到 $HERE/ui3344，请在解包目录内运行"; exit 1; }

echo "> 停止旧服务(如有)…"
systemctl stop ui3344 2>/dev/null || rc-service ui3344 stop 2>/dev/null || true

echo "> 安装文件到 $APP …"
install -d -m755 "$APP" "$APP/bin" /etc/ui3344 /var/log/ui3344
cp -f "$HERE/ui3344" "$APP/ui3344"; chmod +x "$APP/ui3344"
if [ -d "$HERE/bin" ]; then cp -a "$HERE"/bin/. "$APP/bin/"; fi
chmod +x "$APP"/bin/* 2>/dev/null || true
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
  sleep 2
  UI3344_URL="http://127.0.0.1:${PORT:-33441}${WBP:-/ui3344/}" python3 "$HERE/create-inbounds.py" || echo "! 预设创建失败，可稍后手动运行 create-inbounds.py"
fi

echo "========================================"
echo " 安装完成"
echo " 管理命令 : ui3344"
echo " 端口     : ${PORT:-33441}"
echo " 安全入口 : ${WBP:-/ui3344/}"
echo " 访问地址 : http://<服务器IP>:${PORT:-33441}${WBP:-/ui3344/}"
echo " 默认账号 : 3344 / 3344 （请登录后尽快改强）"
echo "========================================"
