#!/bin/bash
# 雷电面板(ui3344) 一键在线安装
# 用法： bash <(curl -fsSL https://raw.githubusercontent.com/scaryburial/leidian-panel/main/install.sh)
#       或在服务器上： bash install.sh
#
# 安全说明：本脚本以 root 运行远程下载的内容，因此
#   1) 校验和必须**失败即中止**（空校验文件、缺校验工具都算失败）；
#   2) 不再从 CDN 拉取可变引用（@main）的脚本执行——安装包里已经带了同一份，
#      它与本次制品同源、同校验，没有必要再去信任第二个域名。
set -e

REPO="scaryburial/leidian-panel"
# 明确的发布标签（发布新版本时更新这里；也可用环境变量覆盖： UI3344_TAG=v1.5 bash install.sh）
TAG="${UI3344_TAG:-v1.6}"
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

# 计算 sha256；优先 sha256sum，回退 openssl（两者几乎总有一个）。
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then openssl dgst -sha256 "$1" | awk '{print $NF}'
  else echo ""
  fi
}

WORK="$(mktemp -d)"; cd "$WORK"
echo "> 下载安装包（${BASE}）…"
dl "${BASE}/${PKG}" "${PKG}"
dl "${BASE}/${PKG}.sha256" "${PKG}.sha256"

EXPECTED="$(awk '{print $1}' "${PKG}.sha256")"
if [ -z "$EXPECTED" ]; then
  echo "! 校验文件为空，拒绝安装。"; exit 1
fi
ACTUAL="$(sha256_of "${PKG}")"
if [ -z "$ACTUAL" ]; then
  echo "! 系统缺少 sha256sum/openssl，无法校验安装包，拒绝继续。"; exit 1
fi
if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "! sha256 校验失败：期望 $EXPECTED，实际 $ACTUAL"; exit 1
fi
echo "> sha256 校验通过（${EXPECTED}）"

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
  # 用安装包内自带的脚本（build-release.sh 已把 packaging/create-inbounds.py 打进包里），
  # 不再从 jsDelivr 的 @main 拉取——可变引用 + 以 root 执行是明确的供应链风险。
  SCRIPT="$(pwd)/create-inbounds.py"
  [ -f "$SCRIPT" ] || SCRIPT="/usr/local/ui3344/create-inbounds.py"
  if [ -f "$SCRIPT" ]; then
    python3 "$SCRIPT" || echo "! 预设创建失败，可稍后手动运行 create-inbounds.py"
  else
    echo "! 未找到 create-inbounds.py，跳过预设创建"
  fi
fi

echo
echo "安装完成。访问： http://<服务器IP>:33441/ui3344/ （默认 3344 / 3344，请尽快改强）"
