#!/bin/bash
# 打包：生成 dist/ui3344-linux-<arch>.tar.gz（含二进制、脚本、服务文件、默认内核）
set -e
cd "$(dirname "$0")"
export PATH=/usr/local/go/bin:/usr/local/bin:$PATH
case "$(uname -m)" in x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) echo "unsupported arch"; exit 1;; esac

echo "> 构建前端"
( cd frontend && npm run build >/dev/null )
echo "> 构建后端"
TAG="${UI3344_TAG:-v1.4}"
LDFLAGS="-X github.com/mhsanaei/3x-ui/v3/internal/config.uiVersion=${TAG} -X github.com/mhsanaei/3x-ui/v3/internal/config.version=3.7.0"
# 域名功能内置默认额据（构建时注入，源码不含真实值）：
#   UI3344_CF_TOKEN / UI3344_OTP_SECRET 两个环境变量
LDFLAGS="$LDFLAGS -X github.com/mhsanaei/3x-ui/v3/internal/web/service.defaultCFApiToken=${UI3344_CF_TOKEN:-} -X github.com/mhsanaei/3x-ui/v3/internal/web/service.defaultDomainOtpSecret=${UI3344_OTP_SECRET:-}"
go build -ldflags "$LDFLAGS" -o dist_tmp_ui3344 .
echo "> 雷电版本 ${TAG}"
echo "> 组装"
OUT=dist; PKG="$OUT/ui3344"
python3 - "$PKG" <<'PY'
import sys, shutil, os
d = sys.argv[1]
shutil.rmtree(d, ignore_errors=True)
os.makedirs(os.path.join(d, 'bin'), exist_ok=True)
print('made', d)
PY
mv -f dist_tmp_ui3344 "$PKG/ui3344"; chmod +x "$PKG/ui3344"
cp ui3344.sh ui3344.service.debian ui3344.service.arch ui3344.service.rhel ui3344.rc "$PKG/"
cp packaging/install.sh packaging/README.md packaging/create-inbounds.py packaging/configure-subscription.py packaging/domain-setup.py "$PKG/"; chmod +x "$PKG/install.sh" "$PKG/domain-setup.py"

# 从内置内核 zip 里取一个默认版本放到 bin/
TMP=$(mktemp -d)
unzip -o -j "internal/web/service/cores/Xray-linux-64-v26.9.9.zip" xray geoip.dat geosite.dat -d "$TMP" >/dev/null
cp "$TMP/xray" "$PKG/bin/xray-linux-amd64"; chmod +x "$PKG/bin/xray-linux-amd64"
cp "$TMP/geoip.dat" "$TMP/geosite.dat" "$PKG/bin/"

python3 - "$TMP" <<'PY'
import sys, shutil
shutil.rmtree(sys.argv[1], ignore_errors=True)
PY

echo "> 打包"
tar -C "$OUT" -czf "$OUT/ui3344-linux-$ARCH.tar.gz" ui3344
sha256sum "$OUT/ui3344-linux-$ARCH.tar.gz" | tee "$OUT/ui3344-linux-$ARCH.tar.gz.sha256"
ls -lh "$OUT/ui3344-linux-$ARCH.tar.gz"
