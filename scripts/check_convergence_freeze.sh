#!/usr/bin/env bash
# check_convergence_freeze.sh — 收敛冻结巡查（Phase 0 产出）。
#
# 规则（见 docs/convergence/freeze-manifest.txt）：
#   R1  examples/ 顶层条目必须是 manifest [examples] 的子集（禁新增 demo 目录）。
#   R2  internal/ 顶层包必须是 manifest [internal] 的子集（禁新增顶层包）。
#   R3  生产代码不得 import internal/fabric 占位包（Phase 2b 前）。
# 计划内删除只告警、不失败；新增一律 exit 1。
#
# 用法：scripts/check_convergence_freeze.sh [repo_root]（默认脚本所在目录的上级）

set -u

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
MANIFEST="$ROOT/docs/convergence/freeze-manifest.txt"
FAIL=0

if [ ! -f "$MANIFEST" ]; then
  echo "freeze-check: manifest not found: $MANIFEST" >&2
  exit 1
fi

# 读取 manifest 某节的条目（去注释、去空行、排序）。
read_section() {
  awk -v sec="[$1]" '
    /^\[/ { in_sec = ($0 == sec); next }
    in_sec && !/^#/ && !/^$/ { print }
  ' "$MANIFEST" | sort
}

check_dir() {
  local dir="$1" section="$2" label="$3"
  local actual allowed extra missing
  actual=$(ls "$ROOT/$dir" 2>/dev/null | sort)
  if [ -z "$actual" ]; then
    echo "freeze-check: cannot list $ROOT/$dir" >&2
    FAIL=1
    return
  fi
  allowed=$(read_section "$section")
  extra=$(comm -23 <(printf '%s\n' "$actual") <(printf '%s\n' "$allowed"))
  missing=$(comm -13 <(printf '%s\n' "$actual") <(printf '%s\n' "$allowed"))
  if [ -n "$extra" ]; then
    echo "freeze-check FAIL: new $label entries not in manifest (R1/R2):" >&2
    printf '%s\n' "$extra" | sed 's/^/  + /' >&2
    FAIL=1
  else
    echo "freeze-check OK: no new $label entries."
  fi
  if [ -n "$missing" ]; then
    echo "freeze-check INFO: planned removals (not a failure):" >&2
    printf '%s\n' "$missing" | sed 's/^/  - /' >&2
  fi
}

check_dir "examples" "examples" "examples/"
check_dir "internal" "internal" "internal/"

# R3：生产代码不得引用 fabric 占位包。
FABRIC_REFS=$(grep -rln "Timwood0x10/ares/internal/fabric" \
  "$ROOT/cmd" "$ROOT/internal" "$ROOT/sdk" "$ROOT/services" "$ROOT/api" \
  --include="*.go" 2>/dev/null | grep -v "^$ROOT/internal/fabric/" || true)
if [ -n "$FABRIC_REFS" ]; then
  echo "freeze-check FAIL: production code imports internal/fabric placeholder (R3):" >&2
  printf '%s\n' "$FABRIC_REFS" | sed 's/^/  * /' >&2
  FAIL=1
else
  echo "freeze-check OK: no production imports of internal/fabric."
fi

exit "$FAIL"
