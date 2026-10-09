#!/usr/bin/env bash
# 本月账单与流量池：vultr-billing.sh。只读。超额那行是 Bandwidth Pool Overage（$0.01/GB）。
# 判断删不删机：超额没抵完时，开着的机器按开机时长攒额度（2c4g 约 4.5GB/h 花 $0.030，1c1g 约 1.5GB/h 花 $0.0074），
# 比交超额（$0.01/GB）便宜；抵完以后再开就是纯开销。
# 额度上限（可选）：ops-local/vultr/env 里设 VULTR_CREDIT_CAP=<美元数>（账户值，不进仓库）。设了才多打一行
# 「按在跑机器的时价，还剩多少小时到上限」；没设就什么都不多打。推算只算机器时价，不计流量额度抵扣和之后新增的超额。
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"; . "$here/vultr-lib.sh"
root="$(dirname "$(git -C "$here" rev-parse --path-format=absolute --git-common-dir)")"
# 额度上限：环境变量优先，其次 ops-local/vultr/env（读不到也不报错）
cap="${VULTR_CREDIT_CAP:-}"
if [ -z "$cap" ] && [ -r "$root/ops-local/vultr/env" ]; then
  cap="$(sed -n 's/^VULTR_CREDIT_CAP=//p' "$root/ops-local/vultr/env" | head -n1 | tr -d "\"' ")"
fi
pending="$(vapi GET /billing/pending-charges)"
printf '%s' "$pending" | jq -r '
  (.pending_charges | map(.total) | add) as $t
  | (.pending_charges | map(select(.product|test("Overage";"i"))) ) as $o
  | "本月待结合计 $\($t * 100 | round / 100)",
    "其中流量超额 \(($o|map(.units)|add) // 0)GB = $\(($o|map(.total)|add) // 0)",
    "机器费 $\(($t - (($o|map(.total)|add) // 0)) * 100 | round / 100)"'
echo "在跑的实例："
vapi GET '/instances?per_page=500' | jq -r '.instances[] | "  \(.label)\t\(.plan)\t\(.main_ip)\t\(.date_created[0:10])"'
if [ -n "$cap" ]; then
  total="$(printf '%s' "$pending" | jq '(.pending_charges | map(.total) | add) // 0')"
  # 在跑实例的小时价合计：套餐表的 hourly_cost（接口免密钥，这里经 vapi 一并走）
  rate="$(jq -n --argjson i "$(vapi GET '/instances?per_page=500')" --argjson p "$(vapi GET '/plans?type=all&per_page=500')" \
    '($p.plans | map({key: .id, value: .hourly_cost}) | from_entries) as $h | [$i.instances[] | $h[.plan] // 0] | add // 0')"
  jq -rn --argjson cap "$cap" --argjson t "$total" --argjson r "$rate" '
    ($cap - $t) as $left
    | if $left <= 0 then "额度上限 $\($cap)：本月待结 $\($t * 100 | round / 100)，已达上限"
      elif $r <= 0 then "额度上限 $\($cap)：本月待结 $\($t * 100 | round / 100)，剩 $\($left * 100 | round / 100)（没有在跑的机器）"
      else ($left / $r) as $h
        | "额度上限 $\($cap)：本月待结 $\($t * 100 | round / 100)，剩 $\($left * 100 | round / 100)；在跑机器时价合计 $\($r * 1000 | round / 1000)/h，约 \($h | floor) 小时后到上限（\(now + $h * 3600 | strftime("%m-%d %H:%M UTC"))，不计流量额度抵扣）"
      end'
fi
