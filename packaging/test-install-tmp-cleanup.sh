#!/bin/bash
# 测试「/tmp 残留清理」逻辑（install.sh 与 packaging/install.sh 两处）。
#
# 背景（线上实测）：在线安装器把安装包下载/解包到 `mktemp -d` 出来的目录里，
# 里面是 137MB 的 tar.gz 加 214MB 的二进制。旧版本没有 trap，装完这个目录就
# 永久留在 /tmp —— `/tmp/tmp.mx3zLul2hv` 实测 399MB。
#
# 清理逻辑包在 `# >>> tmp-cleanup >>>` 标记之间，本脚本把它抽出来真跑一遍：
#   · 成功 / 失败都要回收大文件（失败时保留的是日志，不是这些大文件）；
#   · `UI3344_KEEP_TMP=1` 必须能保留现场（排查用）；
#   · 重复执行、目录已不存在时不能报错（幂等）；
#   · 被清理的只能是安装载荷，脚本/配置类文件不能删。
#
# 用法：bash test-install-tmp-cleanup.sh     （退出码 0 = 全部通过）
# 安全护栏：所有文件操作都在 mktemp -d 出来的临时目录内，不碰 /usr/local、
# 不碰线上服务与数据库。
set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
PKG_SH="$HERE/install.sh"
ONLINE_SH="$(cd "$HERE/.." && pwd)/install.sh"

for f in "$PKG_SH" "$ONLINE_SH"; do
  [ -f "$f" ] || { echo "缺少 $f"; exit 1; }
done

TMP="$(mktemp -d)"; chmod 700 "$TMP"
trap 'rm -rf "$TMP"' EXIT

PASS=0; FAIL=0
ok()  { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
bad() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }
check() { if [ "$2" = "$3" ]; then ok "$1 = [$2]"; else bad "$1：期望 [$3]，实际 [$2]"; fi }

extract() { # <文件> <输出文件>
  sed -n '/^# >>> tmp-cleanup >>>$/,/^# <<< tmp-cleanup <<<$/p' "$1" > "$2"
}

PKG_FUNCS="$TMP/pkg-funcs.sh"; extract "$PKG_SH" "$PKG_FUNCS"
ONLINE_FUNCS="$TMP/online-funcs.sh"; extract "$ONLINE_SH" "$ONLINE_FUNCS"
for pair in "$PKG_FUNCS:clean_payload" "$ONLINE_FUNCS:cleanup_workdir"; do
  f="${pair%%:*}"; fn="${pair##*:}"
  grep -q "$fn()" "$f" || { echo "!! 无法从对应 install.sh 抽出 $fn（标记被改动过？）"; exit 1; }
done

echo "============================================================"
echo " /tmp 残留清理逻辑测试"
echo " 包内脚本 : $PKG_SH"
echo " 在线安装器: $ONLINE_SH"
echo " 临时目录 : $TMP"
echo "============================================================"

# ---------------------------------------------------------------- 包内：clean_payload
mkpayload() { # <目录>：造一个「解包目录」
  mkdir -p "$1/bin"
  head -c 1048576 /dev/zero > "$1/ui3344"
  head -c 524288 /dev/zero > "$1/bin/xray-linux-amd64"
  echo "python" > "$1/create-inbounds.py"
  echo "service" > "$1/ui3344.service.debian"
}

echo
echo "T1 手工解包安装（没有 UI3344_CLEAN_PAYLOAD）：不动用户的文件"
d="$TMP/t1"; mkpayload "$d"
( HERE="$d"; . "$PKG_FUNCS"; clean_payload ) >/dev/null 2>&1
check "T1 ui3344 仍在" "$([ -f "$d/ui3344" ] && echo yes || echo no)" "yes"
check "T1 bin/xray 仍在" "$([ -f "$d/bin/xray-linux-amd64" ] && echo yes || echo no)" "yes"

echo
echo "T2 在线安装（UI3344_CLEAN_PAYLOAD=1）：回收大载荷，但保留脚本/配置"
d="$TMP/t2"; mkpayload "$d"
out=$( ( HERE="$d"; UI3344_CLEAN_PAYLOAD=1; . "$PKG_FUNCS"; clean_payload ) 2>&1 )
check "T2 ui3344 已删除" "$([ -f "$d/ui3344" ] && echo yes || echo no)" "no"
check "T2 bin/ 已清空" "$(ls -A "$d/bin" 2>/dev/null | wc -l | tr -d ' ')" "0"
check "T2 create-inbounds.py 保留" "$([ -f "$d/create-inbounds.py" ] && echo yes || echo no)" "yes"
check "T2 systemd unit 保留" "$([ -f "$d/ui3344.service.debian" ] && echo yes || echo no)" "yes"
if printf '%s' "$out" | grep -q '已清理安装载荷'; then ok "T2 打印了清理说明：$(printf '%s' "$out" | head -1)"; else bad "T2 没打印清理说明"; fi

echo
echo "T3 UI3344_KEEP_TMP=1：即使在线安装也保留现场"
d="$TMP/t3"; mkpayload "$d"
( HERE="$d"; UI3344_CLEAN_PAYLOAD=1; UI3344_KEEP_TMP=1; . "$PKG_FUNCS"; clean_payload ) >/dev/null 2>&1
check "T3 ui3344 仍在" "$([ -f "$d/ui3344" ] && echo yes || echo no)" "yes"
check "T3 bin/xray 仍在" "$([ -f "$d/bin/xray-linux-amd64" ] && echo yes || echo no)" "yes"

echo
echo "T4 载荷已不存在时重复执行不报错（幂等）"
d="$TMP/t4"; mkdir -p "$d"
out=$( ( HERE="$d"; UI3344_CLEAN_PAYLOAD=1; . "$PKG_FUNCS"; clean_payload; echo "RC=$?" ) 2>&1 )
check "T4 返回码" "$(printf '%s' "$out" | sed -n 's/.*RC=\([0-9]*\).*/\1/p')" "0"
if printf '%s' "$out" | grep -qi 'no such file\|error'; then bad "T4 报错了：$out"; else ok "T4 无报错"; fi

# ---------------------------------------------------------------- 在线安装器：cleanup_workdir
echo
echo "T5 安装成功：工作目录（含 137MB 包 + 214MB 解包结果）被回收"
d="$TMP/t5"; mkdir -p "$d"; head -c 65536 /dev/zero > "$d/ui3344-linux-amd64.tar.gz"
( WORK="$d"; . "$ONLINE_FUNCS"; true; cleanup_workdir ) >/dev/null 2>&1
check "T5 目录已删除" "$([ -d "$d" ] && echo yes || echo no)" "no"

echo
echo "T6 安装失败：大文件照样回收，但要留下可排查的提示（stderr）"
d="$TMP/t6"; mkdir -p "$d"; head -c 65536 /dev/zero > "$d/ui3344-linux-amd64.tar.gz"
out=$( ( WORK="$d"; . "$ONLINE_FUNCS"; false; cleanup_workdir ) 2>&1 )
check "T6 目录已删除" "$([ -d "$d" ] && echo yes || echo no)" "no"
if printf '%s' "$out" | grep -q '安装失败'; then ok "T6 有失败提示"; else bad "T6 缺失败提示：$out"; fi
if printf '%s' "$out" | grep -q 'journalctl -u ui3344'; then ok "T6 提示了去哪看日志"; else bad "T6 没指向日志"; fi
if printf '%s' "$out" | grep -q 'UI3344_KEEP_TMP=1'; then ok "T6 提示了保留现场的办法"; else bad "T6 没说怎么保留现场"; fi

echo
echo "T7 UI3344_KEEP_TMP=1：保留工作目录并打印路径"
d="$TMP/t7"; mkdir -p "$d"
out=$( ( WORK="$d"; UI3344_KEEP_TMP=1; . "$ONLINE_FUNCS"; true; cleanup_workdir ) 2>&1 )
check "T7 目录仍在" "$([ -d "$d" ] && echo yes || echo no)" "yes"
if printf '%s' "$out" | grep -qF "$d"; then ok "T7 打印了保留的目录路径"; else bad "T7 没打印路径：$out"; fi
rm -rf "$d"

echo
echo "T8 幂等：WORK 为空或目录已不存在时不报错"
out=$( ( WORK=""; . "$ONLINE_FUNCS"; true; cleanup_workdir; echo "RC=$?" ) 2>&1 )
check "T8 WORK 为空 → 返回码" "$(printf '%s' "$out" | sed -n 's/.*RC=\([0-9]*\).*/\1/p')" "0"
out=$( ( WORK="$TMP/does-not-exist-$$"; . "$ONLINE_FUNCS"; true; cleanup_workdir; echo "RC=$?" ) 2>&1 )
check "T8 目录不存在 → 返回码" "$(printf '%s' "$out" | sed -n 's/.*RC=\([0-9]*\).*/\1/p')" "0"

echo
echo "T9 回归护栏：两个安装脚本都必须真的注册了 trap（只留函数不算修好）"
if grep -qE '^trap cleanup_workdir EXIT' "$ONLINE_SH"; then ok "T9 在线安装器注册了 trap cleanup_workdir EXIT"; else bad "T9 在线安装器没注册 trap"; fi
if grep -qE '^trap restore_service_on_error EXIT' "$PKG_SH" && grep -q 'clean_payload$' "$PKG_SH"; then
  ok "T9 包内脚本在 EXIT trap 里调用 clean_payload"
else
  bad "T9 包内脚本的 EXIT trap 没有调用 clean_payload"
fi
if grep -qE 'UI3344_CLEAN_PAYLOAD=1' "$ONLINE_SH"; then ok "T9 在线安装器把 UI3344_CLEAN_PAYLOAD=1 传给了包内脚本"; else bad "T9 没传 UI3344_CLEAN_PAYLOAD"; fi

echo
echo "T10 语法自检：两个脚本 bash -n 都能过（trap/函数改动不引入语法错误）"
for f in "$PKG_SH" "$ONLINE_SH"; do
  if bash -n "$f" 2>"$TMP/n.err"; then ok "T10 $(basename "$(dirname "$f")")/$(basename "$f") 语法 OK"; else bad "T10 $f 语法错误：$(cat "$TMP/n.err")"; fi
done

echo
echo "============================================================"
echo " 通过 $PASS 项，失败 $FAIL 项"
echo "============================================================"
[ "$FAIL" -eq 0 ] || exit 1
exit 0