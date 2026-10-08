#!/usr/bin/env bash
# 本月账单与流量池：vultr-billing.sh。只读。超额那行是 Bandwidth Pool Overage（$0.01/GB）。
# 判断删不删机：超额没抵完时，开着的机器按开机时长攒额度（2c4g 约 4.5GB/h 花 $0.030，1c1g 约 1.5GB/h 花 $0.0074），
# 比交超额（$0.01/GB）便宜；抵完以后再开就是纯开销。
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"; . "$here/vultr-lib.sh"
vapi GET /billing/pending-charges | jq -r '
  (.pending_charges | map(.total) | add) as $t
  | (.pending_charges | map(select(.product|test("Overage";"i"))) ) as $o
  | "本月待结合计 $\($t * 100 | round / 100)",
    "其中流量超额 \(($o|map(.units)|add) // 0)GB = $\(($o|map(.total)|add) // 0)",
    "机器费 $\(($t - (($o|map(.total)|add) // 0)) * 100 | round / 100)"'
echo "在跑的实例："
vapi GET '/instances?per_page=500' | jq -r '.instances[] | "  \(.label)\t\(.plan)\t\(.main_ip)\t\(.date_created[0:10])"'
