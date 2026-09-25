#!/bin/bash
# 雷电面板(ui3344) 一键在线安装
# 用法： bash <(curl -fsSL https://raw.githubusercontent.com/scaryburial/leidian-panel/main/install.sh)
#       或在服务器上： bash install.sh
set -e

REPO="scaryburial/leidian-panel"
BASE="https://github.com/${REPO}/releases/latest/download"
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

echo
echo "安装完成。访问： http://<服务器IP>:33441/ui3344/ （默认 3344 / 3344，请尽快改强）"
