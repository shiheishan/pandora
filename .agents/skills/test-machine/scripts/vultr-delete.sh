#!/usr/bin/env bash
# 删测试机：vultr-delete.sh <别名>... 只列出要删的（id、标签、地址、套餐）；加 --yes 才真删，删完撤登记（unregister.sh）。
# 只有用户在对话里点名同意删这几台才加 --yes。结果先拉回 ops-local/ 再删。删前先 vultr-billing.sh 看超额。
set -euo pipefail
yes=0; [ "${1:-}" = --yes ] && { yes=1; shift; }
[ $# -gt 0 ] || { echo "用法：vultr-delete.sh [--yes] <别名>..." >&2; exit 2; }
here="$(cd "$(dirname "$0")" && pwd)"; . "$here/vultr-lib.sh"
for a in "$@"; do
  [[ "$a" =~ ^vultr-sgp-pt- ]] || { echo "只删 pandora 测试机（vultr-sgp-pt-*）：$a" >&2; exit 2; }
  read -r id label ip plan < <(instance_of "$a")
  echo "$a → id $id 标签 $label 地址 $ip 套餐 $plan"
  [ "$yes" = 1 ] || continue
  vapi DELETE "/instances/$id" >/dev/null; sleep 3
  vapi GET "/instances/$id" | jq -e '.instance' >/dev/null 2>&1 && { echo "$a 删除没生效" >&2; exit 1; }
  bash "$here/unregister.sh" "$a"
done
[ "$yes" = 1 ] || echo "（只列出，未删除；用户同意后加 --yes）"
