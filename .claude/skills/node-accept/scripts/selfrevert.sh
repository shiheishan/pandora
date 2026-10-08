#!/usr/bin/env bash
# 远端限时故障注入：先在远端布置好「N 秒后撤销」（setsid，与本机会话无关），再注入。
# 本机断网、总协调会话重启、桌面 app 退出都不影响按时撤销（10-08 节点验收 DROP 的撤销没送到，半开演练从 4 分钟拖成 31 分钟）。
# 用法：selfrevert.sh <ssh 别名> <秒> '<注入命令>' '<撤销命令>' [本机 stages.log]
#   例：selfrevert.sh <节点别名> 240 'iptables -I OUTPUT 1 -d <面板内网IP> -p tcp --dport 18080 -j DROP' \
#                                     'iptables -D OUTPUT -d <面板内网IP> -p tcp --dport 18080 -j DROP' ops-local/<轮次>/stages.log
#   远端记录在 /root/selfrevert/<时间>.{inject.sh,revert.sh,log}；回读：ssh <别名> 'tail -n2 /root/selfrevert/*.log'
# 只做这一次注入；撤销命令要能重复执行而无害（iptables -D 在规则已不在时只报错，不影响别的规则）。
set -euo pipefail
host="${1:?ssh 别名}"; sec="${2:?秒}"; inject="${3:?注入命令}"; revert="${4:?撤销命令}"; stages="${5:-}"
[[ "$sec" =~ ^[0-9]+$ ]] && [ "$sec" -gt 0 ] || { echo "秒数要是正整数：$sec" >&2; exit 2; }
b64() { printf '%s' "$1" | base64 | tr -d '\n'; }

out="$(ssh -o BatchMode=yes -o ConnectTimeout=15 "$host" bash -s -- "$sec" "$(b64 "$inject")" "$(b64 "$revert")" <<'REMOTE'
set -e
sec="$1"; d=/root/selfrevert; mkdir -p "$d"; ts="$(date -u +%Y%m%dT%H%M%SZ)"
echo "$2" | base64 -d > "$d/$ts.inject.sh"; echo "$3" | base64 -d > "$d/$ts.revert.sh"
# 撤销先布置：布置失败（set -e）就不会注入
setsid -f sh -c "sleep $sec; sh $d/$ts.revert.sh; echo \"\$(date -u +%FT%TZ) 已撤销 rc=\$?\" >> $d/$ts.log" > /dev/null 2>&1 < /dev/null
echo "$(date -u +%FT%TZ) 撤销已布置（${sec}s 后）" >> "$d/$ts.log"
sh "$d/$ts.inject.sh"
echo "$(date -u +%FT%TZ) 已注入" >> "$d/$ts.log"
echo "$ts"
REMOTE
)"
ts="$(printf '%s\n' "$out" | tail -1)"
now="$(date -u +%FT%TZ)"
echo "$now $host 注入，${sec}s 后远端自撤销（/root/selfrevert/$ts.log）"
[ -n "$stages" ] && echo "$now drill $host 注入 ${sec}s，远端自撤销 $ts" >> "$stages"
echo "回读：ssh $host 'cat /root/selfrevert/$ts.log'（到点后应有「已撤销 rc=0」）"
