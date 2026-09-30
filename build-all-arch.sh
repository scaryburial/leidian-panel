#!/bin/bash
# 雷电面板(ui3344) 多架构构建：一次产出 linux-amd64 与 linux-arm64 安装包。
#
# 与仓库自带 build-release.sh 的区别：
#   1. 前端只构建一次，两个架构复用；
#   2. Go 二进制按 GOARCH 交叉编译（宿主机是 amd64，无需 qemu）；
#   3. 每个架构各自放对应架构的 Xray 内核与 geo 文件到 bin/；
#      build-release.sh 只会塞 amd64 内核，arm64 机器上会起不来；
#   4. 顶层目录固定为 ui3344，保证官方一键安装脚本的
#      `tar xzf ... && cd ui3344` 仍然成立。
set -e

ROOT=${1:-/root/build/ui3344}
cd "$ROOT"

export PATH=/usr/local/node-v24.21.0-linux-x64/bin:/usr/local/go/bin:$PATH

# 构建时注入的发布标签与域名功能凭据
if [ -f /root/build/build.env ]; then
  set -a
  . /root/build/build.env
  set +a
fi
TAG="${UI3344_TAG:-v1.7}"

CORE_VER=v26.9.9
CORE_DIR="$ROOT/.cores"
mkdir -p "$CORE_DIR"

echo "> [1/5] 构建前端（两架构复用）"
( cd frontend && npm run build >/dev/null )

echo "> [2/5] 准备各架构 Xray 内核与 geo"
# amd64：直接用仓库内置的那份
if [ ! -f "$CORE_DIR/xray-amd64" ]; then
  TMP=$(mktemp -d)
  unzip -o -j "internal/web/service/cores/Xray-linux-64-$CORE_VER.zip" xray geoip.dat geosite.dat -d "$TMP" >/dev/null
  cp "$TMP/xray" "$CORE_DIR/xray-amd64"
  cp "$TMP/geoip.dat" "$CORE_DIR/geoip.dat"
  cp "$TMP/geosite.dat" "$CORE_DIR/geosite.dat"
  rm -rf "$TMP"
fi
# arm64：从上游 release 取对应架构内核
if [ ! -f "$CORE_DIR/xray-arm64" ]; then
  URL="https://github.com/XTLS/Xray-core/releases/download/$CORE_VER/Xray-linux-arm64-v8a.zip"
  echo "    下载 arm64 内核: $URL"
  curl -fL --retry 3 -o "$CORE_DIR/arm64.zip" "$URL"
  TMP=$(mktemp -d)
  unzip -o -j "$CORE_DIR/arm64.zip" xray -d "$TMP" >/dev/null
  cp "$TMP/xray" "$CORE_DIR/xray-arm64"
  rm -rf "$TMP" "$CORE_DIR/arm64.zip"
fi
file "$CORE_DIR/xray-amd64" "$CORE_DIR/xray-arm64" | sed 's/^/    /'

LDFLAGS="-X github.com/mhsanaei/3x-ui/v3/internal/config.uiVersion=${TAG}"
LDFLAGS="$LDFLAGS -X github.com/mhsanaei/3x-ui/v3/internal/config.version=3.7.0"
LDFLAGS="$LDFLAGS -X github.com/mhsanaei/3x-ui/v3/internal/web/service.defaultCFApiToken=${UI3344_CF_TOKEN:-}"
LDFLAGS="$LDFLAGS -X github.com/mhsanaei/3x-ui/v3/internal/web/service.defaultDomainOtpSecret=${UI3344_OTP_SECRET:-}"

build_one() {
  local arch="$1"
  echo "> 构建 linux/$arch"
  # 数据库驱动 mattn/go-sqlite3 用到 cgo 的 Backup API，必须开启 cgo：
  # amd64 用本机 gcc；arm64 用 aarch64 交叉编译器。
  local cc=""
  if [ "$arch" = "arm64" ]; then
    cc="aarch64-linux-gnu-gcc"
    if ! command -v "$cc" >/dev/null 2>&1; then
      echo "!! 缺少交叉编译器 $cc（apt-get install -y gcc-aarch64-linux-gnu）" >&2
      return 1
    fi
  fi
  GOOS=linux GOARCH="$arch" CGO_ENABLED=1 CC="$cc" \
    go build -trimpath -ldflags "$LDFLAGS" -o "/tmp/ui3344-bin-$arch" .

  local OUT="$ROOT/dist"
  local PKG="$OUT/ui3344"
  rm -rf "$PKG"
  mkdir -p "$PKG/bin"

  mv -f "/tmp/ui3344-bin-$arch" "$PKG/ui3344"
  chmod +x "$PKG/ui3344"
  cp ui3344.sh ui3344.service.debian ui3344.service.arch ui3344.service.rhel ui3344.rc "$PKG/"
  cp packaging/install.sh packaging/README.md packaging/create-inbounds.py \
     packaging/configure-subscription.py packaging/domain-setup.py "$PKG/"
  chmod +x "$PKG/install.sh" "$PKG/domain-setup.py"

  cp "$CORE_DIR/xray-$arch" "$PKG/bin/xray-linux-$arch"
  chmod +x "$PKG/bin/xray-linux-$arch"
  cp "$CORE_DIR/geoip.dat" "$CORE_DIR/geosite.dat" "$PKG/bin/"

  tar -C "$OUT" -czf "$OUT/ui3344-linux-$arch.tar.gz" ui3344
  sha256sum "$OUT/ui3344-linux-$arch.tar.gz" | sed 's#dist/##' \
    | tee "$OUT/ui3344-linux-$arch.tar.gz.sha256"
}

echo "> [3/5] 编译 go 二进制并打包"
build_one amd64
build_one arm64

echo "> [4/5] 生成源码包"
# 用「工作区打包」而不是 git archive HEAD：这样补丁里尚未提交的改动
# 也会进入源码包，避免发布包与二进制不一致。
tar -C "$ROOT" \
    --exclude=./.git \
    --exclude=./node_modules \
    --exclude=./frontend/node_modules \
    --exclude=./frontend/dist \
    --exclude=./dist \
    --exclude=./.cores \
    -czf "$ROOT/dist/ui3344-source.tar.gz" .
sha256sum "$ROOT/dist/ui3344-source.tar.gz" | sed 's#.*/dist/##' \
  | tee "$ROOT/dist/ui3344-source.tar.gz.sha256"

echo "> [5/5] 完成，产物："
ls -lh "$ROOT/dist/"*.tar.gz "$ROOT/dist/"*.sha256
