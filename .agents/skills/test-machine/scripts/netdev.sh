#!/usr/bin/env bash
# 各机网卡累计收发（/proc/net/dev，开机以来）：标出默认路由那块（公网），其余是 VPC 等内网卡。只读。
# 用法：netdev.sh <ssh 别名>... [> ops-local/<轮次>/results/netdev_<时刻>.txt]
#   开跑前、稳态开始、收尾各跑一次，差值就是这一段的出流量；公网卡 tx 才计费（Vultr 只计出方向）。
set -uo pipefail
[ $# -gt 0 ] || { echo "用法：netdev.sh <ssh 别名>..." >&2; exit 2; }
printf '%-28s %-10s %-4s %12s %12s\n' 机器 网卡 公网 rx_GB tx_GB
for h in "$@"; do
  ssh -n -o BatchMode=yes -o ConnectTimeout=15 "$h" '
    def=$(ip route show default 2>/dev/null | awk "{print \$5; exit}")
    awk -v def="$def" -v h="'"$h"'" "NR>2 { gsub(/:/, \" \"); if (\$1 == \"lo\") next;
      printf \"%-28s %-10s %-4s %12.1f %12.1f\n\", h, \$1, (\$1 == def ? \"是\" : \"\"), \$2/1e9, \$10/1e9 }" /proc/net/dev
  ' || echo "$h 读取失败"
done
