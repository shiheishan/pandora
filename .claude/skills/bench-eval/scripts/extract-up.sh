#!/usr/bin/env bash
# 把编号在 [起始, 结束] 内的迁移文件的 goose Up 段（-- +goose Up 与 -- +goose Down 之间）抽到一个目录，文件名保持原样。
# 迁移号分段派给各任务，中间有空号是常态，所以按「范围内有哪些文件」取，不要求连续。
# 给 ops-local/bench/tools/build_w5l_templates.sh 当「迁移 Up 段目录」：评测模板停在旧迁移号时，用它补到改前提交的迁移号。
# 用法：extract-up.sh <输出目录> <起始编号> <结束编号> [迁移目录，默认 panel/migrations] [git 提交，默认读工作区]
#   例：extract-up.sh /tmp/up-s9051135 98 123 panel/migrations 9051135
# 给了提交就用 git show 读那个提交里的文件（不碰工作区），否则读工作区。
set -euo pipefail
out="${1:?输出目录}"; from=$((10#${2:?起始编号})); to=$((10#${3:?结束编号}))
dir="${4:-panel/migrations}"; rev="${5:-}"
if [ -n "$rev" ]; then files=$(git ls-tree --name-only "$rev" "$dir/"); else files=$(ls "$dir"/*.sql); fi
mkdir -p "$out"; n=0
while IFS= read -r f; do
  b=$(basename "$f")
  [[ "$b" =~ ^([0-9]{5})_.*\.sql$ ]] || continue
  num=$((10#${BASH_REMATCH[1]}))
  { [ "$num" -ge "$from" ] && [ "$num" -le "$to" ]; } || continue
  if [ -n "$rev" ]; then git show "$rev:$f"; else cat "$f"; fi \
    | awk '/^-- \+goose Up/{g=1;next} /^-- \+goose Down/{g=0} g' > "$out/$b"
  [ -s "$out/$b" ] || { echo "没取到 Up 段: $f" >&2; exit 1; }
  n=$((n+1))
done <<< "$files"
[ "$n" -gt 0 ] || { echo "范围 $from-$to 内没有迁移文件" >&2; exit 1; }
echo "已抽 $n 个迁移的 Up 段到 $out"
