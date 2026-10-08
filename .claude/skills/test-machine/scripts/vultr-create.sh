#!/usr/bin/env bash
# 开一台测试机并登记：vultr-create.sh [--no-vpc] <别名> <套餐> "<用途一句话>"
#   例：vultr-create.sh vultr-sgp-pt-node7 vc2-4c-8gb "pandora 节点 VPC 复测"（4c8g 独享 = voc-c-4c-8gb-75s-amd；其他套餐 id 用 curl -s "https://api.vultr.com/v2/plans?type=all" 查，免密钥）
# 用户授权 agent 开机（10-08）；开之前在对话里报：套餐、台数、预计时长、公网出流量估算。
# 默认挂 ops-local/vultr/env 的 VPC；不注入口令（返回体里的 default_password 丢掉不存）；关自动备份。
set -euo pipefail
vpc=1; [ "${1:-}" = --no-vpc ] && { vpc=0; shift; }
alias="${1:?别名}"; plan="${2:?套餐}"; purpose="${3:?用途}"
[[ "$alias" =~ ^vultr-sgp-pt-[a-z0-9-]+$ ]] || { echo "别名要是 vultr-sgp-pt-<角色><序号>：$alias" >&2; exit 2; }
here="$(cd "$(dirname "$0")" && pwd)"; . "$here/vultr-lib.sh"
root="$(dirname "$(git -C "$here" rev-parse --path-format=absolute --git-common-dir)")"; . "$root/ops-local/vultr/env"
[ -e "$HOME/ai/servers/$alias" ] && { echo "已存在 ~/ai/servers/$alias（删过的旧机目录保留作记录，换个序号）" >&2; exit 1; }
tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
jq -n --arg r "$VULTR_REGION" --arg p "$plan" --argjson os "$VULTR_OS_ID" --arg l "$alias" --arg k "$VULTR_SSHKEY_ID" --arg v "$VULTR_VPC_ID" --argjson vpc "$vpc" \
  '{region:$r, plan:$p, os_id:$os, label:$l, hostname:$l, sshkey_id:[$k], backups:"disabled", enable_ipv6:false, tags:["pandora-test"]}
   + (if $vpc == 1 then {attach_vpc:[$v]} else {} end)' > "$tmp"
id="$(vapi POST /instances "$tmp" | jq -r '.instance.id // empty | tostring' )"
[ -n "$id" ] || { echo "创建失败（再跑一次 vultr.sh POST /instances 看报错）" >&2; exit 1; }
echo "已下单 $alias（$plan），id $id；等开机…"
for _ in $(seq 1 60); do
  st="$(vapi GET "/instances/$id" | jq -r '.instance | "\(.status) \(.power_status) \(.server_status) \(.main_ip)"')"
  case "$st" in "active running ok "*) break;; esac; sleep 10
done
ip="${st##* }"; [[ "$ip" =~ ^[0-9.]+$ && "$ip" != 0.0.0.0 ]] || { echo "10 分钟还没开好：$st" >&2; exit 1; }
args=()
if [ "$vpc" = 1 ]; then
  pip="$(vapi GET "/instances/$id/vpcs" | jq -r '.vpcs[0].ip_address // empty')"
  [ -n "$pip" ] && args=(--vpc "$pip")
fi
echo "开好了：$ip ${pip:+内网 $pip}；登记…"
sleep 20
bash "$here/register.sh" "${args[@]}" "$alias" "$ip" "$plan" "$purpose"
