#!/bin/bash
# 雷电面板(ui3344) 一键在线安装
# 用法： bash <(curl -fsSL https://raw.githubusercontent.com/scaryburial/leidian-panel/main/install.sh)
#       或在服务器上： bash install.sh
set -e

REPO="scaryburial/leidian-panel"
# 明确的发布标签（发布新版本时更新这里；也可用环境变量覆盖： UI3344_TAG=v1.1 bash install.sh）
TAG="${UI3344_TAG:-v1.0}"
BASE="https://github.com/${REPO}/releases/download/${TAG}"
PKG="ui3344-linux-amd64.tar.gz"

if [ "$(id -u)" != "0" ]; then
  echo "请用 root 运行本脚本。"; exit 1
fi

dl() { # dl <url> <out>
  if command -v curl >/dev/null 2>&1; then curl -fL --retry 3 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then wget -O "$2" "$1"
  else echo "缺少 curl/wget，请先安装其一。"; exit 1; fi
}

WORK="$(mktemp -d)"; cd "$WORK"
echo "> 下载最新安装包（${BASE}）…"
dl "${BASE}/${PKG}" "${PKG}"
dl "${BASE}/${PKG}.sha256" "${PKG}.sha256"

if command -v sha256sum >/dev/null 2>&1; then
  EXPECTED="$(awk '{print $1}' "${PKG}.sha256")"
  ACTUAL="$(sha256sum "${PKG}" | awk '{print $1}')"
  if [ -n "$EXPECTED" ] && [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "! sha256 校验失败：期望 $EXPECTED，实际 $ACTUAL"; exit 1
  fi
  echo "> sha256 校验通过"
fi

echo "> 解压并安装…"
tar xzf "${PKG}"
cd ui3344
bash install.sh

# 确保面板已就绪并创建预设协议（兼容安装包内的启动竞态）
echo "> 确保预设协议…"
for i in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:33441/ui3344/csrf-token" >/dev/null 2>&1 && break
  sleep 1
done
if command -v python3 >/dev/null 2>&1; then
  curl -fsSL "https://cdn.jsdelivr.net/gh/scaryburial/leidian-panel@main/packaging/create-inbounds.py" -o /tmp/ui3344-create-inbounds.py 2>/dev/null && python3 /tmp/ui3344-create-inbounds.py || echo "! 预设创建失败，可稍后手动运行 create-inbounds.py"
fi

echo
echo "安装完成。访问： http://<服务器IP>:33441/ui3344/ （默认 3344 / 3344，请尽快改强）"
