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
# 明确的发布标签（发布新版本时更新这里；也可用环境变量覆盖： UI3344_TAG=v1.7 bash install.sh）
TAG="${UI3344_TAG:-v2.1}"
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
# /tmp 残留：这个工作目录里是 137MB 的安装包 + 解包后 214MB 的二进制，
# 实测一次在线安装会在 /tmp 留下 399MB（`du -sh /tmp/tmp.*` 可复现）。
# 必须显式清理：之前的版本没有 trap，装完就永久留在那里。
#
# 与「失败要能排查」不冲突：保留下来的是**日志**（journalctl -u ui3344、
# /var/log/ui3344/、脚本自己打印的错误），大文件一律回收；确实要看现场就
# UI3344_KEEP_TMP=1 重跑一次，脚本会打印目录路径。
# >>> tmp-cleanup >>>
cleanup_workdir() {
  rc=$?
  if [ -z "$WORK" ] || [ ! -d "$WORK" ]; then return 0; fi
  if [ "${UI3344_KEEP_TMP:-0}" = "1" ]; then
    echo "> 按 UI3344_KEEP_TMP=1 保留临时目录（约 400MB）：$WORK"
    return 0
  fi
  cd / 2>/dev/null || true
  rm -rf "$WORK" 2>/dev/null || true
  if [ "$rc" -ne 0 ]; then
    echo "> 安装失败(rc=$rc)，已回收临时目录 $WORK（约 400MB）。排查用：journalctl -u ui3344 与 /var/log/ui3344/；要保留现场请 UI3344_KEEP_TMP=1 重跑" >&2
  fi
}
# <<< tmp-cleanup <<<
trap cleanup_workdir EXIT INT TERM

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
# UI3344_CLEAN_PAYLOAD=1：装完后让包内 install.sh 把已经复制到 /usr/local/ui3344
# 的大文件（二进制 214MB）从工作目录删掉；剩下的整目录由上面的 trap 回收。
# UI3344_USER/UI3344_PASS 原样传下去（面板口令已改强时必须给，见包内提示）。
UI3344_CLEAN_PAYLOAD=1 \
UI3344_USER="${UI3344_USER:-3344}" \
UI3344_PASS="${UI3344_PASS:-3344}" \
  bash install.sh

# 确保面板已就绪并创建预设协议（兼容安装包内的启动竞态）。
#
# 这里**不能写死 http://127.0.0.1:33441**：启用域名后（或用户改过端口）
# 面板跑在 8443/HTTPS 上，明文 HTTP 打 TLS 端口会拿到非 HTTP 字节，
# 探测永远不成功（白等 30 秒）。http/https 各试一次，探测不到就不设
# UI3344_URL，交给 create-inbounds.py 自己的 detect_base() 兜底。
PANEL_BASE=""
panel_ready() { # <base>
  curl -kfsS --max-time 3 "$1/csrf-token" >/dev/null 2>&1
}
if command -v curl >/dev/null 2>&1; then
  for _ in $(seq 1 30); do
    for base in "http://127.0.0.1:33441/ui3344/" "https://127.0.0.1:8443/ui3344/"; do
      if panel_ready "$base"; then PANEL_BASE="$base"; break; fi
    done
    [ -n "$PANEL_BASE" ] && break
    sleep 1
  done
else
  echo "! 系统缺少 curl，跳过面板地址探测（交给 create-inbounds.py 自行探测）"
fi
[ -n "$PANEL_BASE" ] && echo "> 面板地址：$PANEL_BASE"
if command -v python3 >/dev/null 2>&1; then
  # 用安装包内自带的脚本（build-release.sh 已把 packaging/create-inbounds.py 打进包里），
  # 不再从 jsDelivr 的 @main 拉取——可变引用 + 以 root 执行是明确的供应链风险。
  SCRIPT="$(pwd)/create-inbounds.py"
  [ -f "$SCRIPT" ] || SCRIPT="/usr/local/ui3344/create-inbounds.py"
  if [ -f "$SCRIPT" ]; then
    run_presets() {
      if [ -n "$PANEL_BASE" ]; then
        UI3344_URL="${PANEL_BASE%/}" UI3344_USER="${UI3344_USER:-3344}" UI3344_PASS="${UI3344_PASS:-3344}" \
          python3 "$SCRIPT"
      else
        UI3344_USER="${UI3344_USER:-3344}" UI3344_PASS="${UI3344_PASS:-3344}" python3 "$SCRIPT"
      fi
    }
    run_presets || { sleep 3; run_presets; } || {
      echo "! 预设创建失败。常见原因：面板管理员口令已改强（不再是默认 3344）。"
      echo "  请在服务器上补跑（把 <面板口令> 换成你自己设的口令，脚本不会打印也不会保存它）："
      echo "    UI3344_PASS='<面板口令>' python3 /usr/local/ui3344/create-inbounds.py"
    }
  else
    echo "! 未找到 create-inbounds.py，跳过预设创建"
  fi
fi

echo
echo "安装完成。访问： http://<服务器IP>:33441/ui3344/ （默认 3344 / 3344，请尽快改强）"
