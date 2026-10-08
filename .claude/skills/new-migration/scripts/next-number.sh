#!/usr/bin/env bash
# 只读：打印下一个可用的迁移号。
#   - 主线（默认 feat/panel-redesign）最近 5 个迁移与最大号；
#   - 当前工作区 panel/migrations 的最大号（在任务 worktree 里跑时会比主线大）；
#   - 各任务分支（<主线>-*）上有、主线还没有的迁移：在途号段，取号要跳过；
#   - 主线最大号以下的空号：历史空号，不要回填。
# 用法：next-number.sh [主线分支]
set -euo pipefail

MAIN="${1:-feat/panel-redesign}"
ROOT="$(git rev-parse --show-toplevel)"
DIR=panel/migrations

# 列出某个 ref 上的迁移文件名（已排序）
list_ref() {
  git -C "$ROOT" ls-tree --name-only "$1" "$DIR/" \
    | sed -n "s#^$DIR/\([0-9]\{5\}_[A-Za-z0-9._-]*\.sql\)\$#\1#p" | sort
}
max_of() { sed -n 's/^\([0-9]\{5\}\)_.*/\1/p' | sort -n | tail -1; }

git -C "$ROOT" rev-parse --verify -q "$MAIN" >/dev/null || { echo "找不到分支 $MAIN" >&2; exit 2; }

main_files="$(list_ref "$MAIN")"
main_max="$(printf '%s\n' "$main_files" | max_of)"
main_max="${main_max:-00000}"

echo "主线 $MAIN 最近 5 个迁移："
printf '%s\n' "$main_files" | tail -5 | sed 's/^/  /'

local_max="$( (ls "$ROOT/$DIR" 2>/dev/null || true) | grep -E '^[0-9]{5}_[A-Za-z0-9._-]+\.sql$' | max_of || true)"
local_max="${local_max:-00000}"
echo "当前工作区最大号：$local_max（$(git -C "$ROOT" rev-parse --abbrev-ref HEAD)）"

inflight_max=00000
echo "在途（任务分支上有、主线没有）："
found=0
while IFS= read -r branch; do
  [ -n "$branch" ] || continue
  extra="$(comm -13 <(printf '%s\n' "$main_files") <(list_ref "$branch") | sed '/^$/d')"
  [ -n "$extra" ] || continue
  found=1
  echo "  $branch：$(printf '%s\n' "$extra" | tr '\n' ' ')"
  m="$(printf '%s\n' "$extra" | max_of)"
  if [ -n "$m" ] && [ "$((10#$m))" -gt "$((10#$inflight_max))" ]; then inflight_max="$m"; fi
done < <(git -C "$ROOT" for-each-ref --format='%(refname:short)' "refs/heads/$MAIN-*")
[ "$found" -eq 1 ] || echo "  （无）"

# 主线最大号以下的空号
gaps=""
prev=0
while IFS= read -r v; do
  [ -n "$v" ] || continue
  n=$((10#$v))
  for ((g = prev + 1; g < n; g++)); do gaps+="$(printf '%05d' "$g") "; done
  prev=$n
done < <(printf '%s\n' "$main_files" | sed -n 's/^\([0-9]\{5\}\)_.*/\1/p')
echo "主线空号（历史空号，不回填）：${gaps:-无}"

next=$((10#$main_max))
for m in "$local_max" "$inflight_max"; do
  if [ "$((10#$m))" -gt "$next" ]; then next=$((10#$m)); fi
done
printf '下一个可用号：%05d\n' "$((next + 1))"
echo "在途分支可能已经废弃，也可能还要合：拿不准就问总协调（.claude/TASKS.md 记着各路号段）。"
