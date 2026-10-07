#!/usr/bin/env bash
# 列出任务分支在归属清单之外改动的文件。
# 用法：check-ownership.sh <基点> <分支> <归属清单文件>
# 归属清单一行一个 glob（相对仓库根，bash extglob/globstar 语法，如 panel/internal/domain/subscription/**）；# 开头为注释。
# 函数级归属（「只许改某函数」）脚本判断不了，命中的文件会标 [函数级]，需要人工看 diff。
set -euo pipefail
shopt -s globstar extglob
base="${1:?基点}"; branch="${2:?分支}"; list="${3:?归属清单}"
mapfile -t globs < <(grep -vE '^\s*(#|$)' "$list")
outside=0
while IFS= read -r f; do
  hit=""
  for g in "${globs[@]}"; do
    pat="${g%% *}"; note="${g#"$pat"}"
    # shellcheck disable=SC2053
    if [[ "$f" == $pat ]]; then hit="ok${note:+ [函数级]$note}"; break; fi
  done
  if [ -z "$hit" ]; then echo "越界  $f"; outside=$((outside+1));
  elif [[ "$hit" == *"[函数级]"* ]]; then echo "复核  $f ${hit#ok}"; fi
done < <(git diff --name-only "$base".."$branch")
echo "归属外文件数: $outside"
[ "$outside" -eq 0 ]
