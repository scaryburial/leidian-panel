#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# 余额悬浮窗 —— 一键搭建 Android 构建环境并编译 APK（Debian，幂等）
#
#   ./build.sh
#
# 按 CPU 架构自适应：
#   x86_64  ：Android SDK 取 Google 官方源，build-tools 原生工具直接可用。
#   aarch64 ：Google dl.google.com 常不可达，改取腾讯镜像；build-tools 的
#             aapt2/aidl/zipalign 只有 x86_64 版，替换为 GitHub Commit451 的
#             arm64 版本。
#
# 可覆盖变量：
#   SDK_ROOT    Android SDK 目录   默认 /opt/android-sdk
#   OUT         产物输出目录       默认 $HOME/workspace/dist
#   KS          签名密钥           默认 /opt/balance-overlay.jks（不存在则生成）
#   STORE_PASS / KEY_ALIAS / KEY_PASS  默认 balance123 / balance / balance123
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

PROJ="$(cd "$(dirname "$0")" && pwd)"
SDK_ROOT="${SDK_ROOT:-/opt/android-sdk}"
OUT="${OUT:-$HOME/workspace/dist}"
KS="${KS:-/opt/balance-overlay.jks}"
STORE_PASS="${STORE_PASS:-balance123}"
KEY_ALIAS="${KEY_ALIAS:-balance}"
KEY_PASS="${KEY_PASS:-balance123}"
TOOLS_TAG="${TOOLS_TAG:-platform-tools-37.0.0}"
BT_VERSION="36.0.0"          # aapt2 覆盖用；35.0.0 是 AGP 默认，也必须装
DL="/tmp/balance-build-dl"

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)
        SDK_MIRROR="${SDK_MIRROR:-https://dl.google.com/android/repository}"
        USE_ARM_TOOLS=0 ;;
    aarch64)
        SDK_MIRROR="${SDK_MIRROR:-https://mirrors.cloud.tencent.com/AndroidSDK}"
        USE_ARM_TOOLS=1 ;;
    *) echo "不支持的架构：$ARCH" >&2; exit 1 ;;
esac

log() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "需要 root 权限"

# ── 1. 基础工具 + JDK17 ───────────────────────────────────────────────────────
log "安装系统依赖 (openjdk-17-jdk-headless / curl / unzip …)"
export DEBIAN_FRONTEND=noninteractive
if ! command -v java >/dev/null 2>&1; then
    apt-get update -qq
    apt-get install -y --no-install-recommends openjdk-17-jdk-headless curl unzip zip >/dev/null
fi
for c in java keytool curl unzip; do command -v "$c" >/dev/null || die "缺少命令：$c"; done

# ── 2. Android SDK ───────────────────────────────────────────────────────────
if [ -f "$SDK_ROOT/platforms/android-36/android.jar" ] \
   && [ -x "$SDK_ROOT/build-tools/35.0.0/aapt2" ] \
   && [ -x "$SDK_ROOT/build-tools/$BT_VERSION/aapt2" ]; then
    log "Android SDK 已就绪，跳过：$SDK_ROOT"
else
    log "下载并安装 Android SDK 到 $SDK_ROOT（源：$SDK_MIRROR）"
    mkdir -p "$SDK_ROOT/platforms" "$SDK_ROOT/build-tools" "$SDK_ROOT/licenses" "$DL"
    cd "$DL"
    curl -fsSL -o platform-36.zip    "$SDK_MIRROR/platform-36_r02.zip"
    curl -fsSL -o build-tools-35.zip "$SDK_MIRROR/build-tools_r35_linux.zip"
    curl -fsSL -o build-tools-36.zip "$SDK_MIRROR/build-tools_r36_linux.zip"
    unzip -q -o platform-36.zip    -d p36
    unzip -q -o build-tools-35.zip -d b35
    unzip -q -o build-tools-36.zip -d b36
    [ -e "$SDK_ROOT/platforms/android-36" ]    || mv p36/*/ "$SDK_ROOT/platforms/android-36"
    [ -e "$SDK_ROOT/build-tools/35.0.0" ]      || mv b35/*/ "$SDK_ROOT/build-tools/35.0.0"
    [ -e "$SDK_ROOT/build-tools/$BT_VERSION" ] || mv b36/*/ "$SDK_ROOT/build-tools/$BT_VERSION"

    if [ "$USE_ARM_TOOLS" = "1" ]; then
        for v in 35.0.0 "$BT_VERSION"; do
            B="$SDK_ROOT/build-tools/$v"
            for t in aapt2 aidl zipalign; do
                [ -f "$B/$t.real" ] || mv "$B/$t" "$B/$t.real"
                curl -fsSL -o "$B/$t.real" \
                    "https://github.com/Commit451/android-arm-build-tools/releases/download/$TOOLS_TAG/$t"
                chmod +x "$B/$t.real"
                printf '#!/bin/sh\nexec "%s" "$@"\n' "$B/$t.real" > "$B/$t"
                chmod +x "$B/$t"
            done
            echo "  - build-tools $v: $("$B/aapt2" version)"
        done
    else
        for v in 35.0.0 "$BT_VERSION"; do
            "$SDK_ROOT/build-tools/$v/aapt2" version | sed "s/^/  - build-tools $v: /"
        done
    fi
    printf '\n24333f8a63b6825ea9c5514f83c2829b004d1fee' > "$SDK_ROOT/licenses/android-sdk-license"
    printf '\n8933bad161af4178b1185d1a37fbf41ea5269c55\nd56f5187479451eabf01fb78af6dfcb131a6481e' > "$SDK_ROOT/licenses/android-sdk-preview-license"
fi
echo "sdk.dir=$SDK_ROOT" > "$PROJ/local.properties"

# ── 3. 签名密钥 ──────────────────────────────────────────────────────────────
if [ -f "$KS" ]; then
    log "签名密钥已存在，跳过：$KS"
else
    log "生成签名密钥 $KS"
    keytool -genkeypair -keystore "$KS" -alias "$KEY_ALIAS" -keyalg RSA -keysize 2048 \
        -validity 10000 -storepass "$STORE_PASS" -keypass "$KEY_PASS" \
        -dname "CN=Balance Overlay,O=AiCode,C=CN"
fi

# ── 4. 编译 ──────────────────────────────────────────────────────────────────
log "开始编译 APK"
export ANDROID_HOME="$SDK_ROOT" ANDROID_SDK_ROOT="$SDK_ROOT"
cd "$PROJ"
./gradlew :app:assembleRelease \
    "-Pandroid.aapt2FromMavenOverride=$SDK_ROOT/build-tools/$BT_VERSION/aapt2" \
    "-PRELEASE_STORE_FILE=$KS" \
    "-PRELEASE_STORE_PASSWORD=$STORE_PASS" \
    "-PRELEASE_KEY_ALIAS=$KEY_ALIAS" \
    "-PRELEASE_KEY_PASSWORD=$KEY_PASS"

# ── 5. 取产物 ────────────────────────────────────────────────────────────────
mkdir -p "$OUT"
APK="$(ls -t "$PROJ"/app/build/outputs/apk/release/*.apk | head -1)"
cp "$APK" "$OUT/ADeep.apk"
log "完成：$OUT/ADeep.apk"
