#!/usr/bin/env bash
# 一条命令看两台机器的实时进度（在本机跑，只读）：peek.sh <env.sh> <场景>
#   压测机：timeline 最后两行、nodes / users 日志最后一行、负载与内存
#   面板机：timeline 最后一行、procs.csv 最近一格（整机 + 各进程）、pgact 最近一格、负载、内存、nginx 5xx 行数
# 输出里的 URL 与 IP 打码。
set -uo pipefail
ENV=${1:?env.sh}; S=${2:?场景名}
# shellcheck disable=SC1090
. "$ENV"
mask() { sed -E "s#https://[^ ]+#<URL>#g; s#${PANEL_IP:-^$}#<PANEL_IP>#g; s#${LOADGEN_IP:-^$}#<LOADGEN_IP>#g"; }
echo "== 压测机 $LOADGEN_HOST  $(date -u +%T)Z"
ssh -n -o BatchMode=yes -o ConnectTimeout=10 "$LOADGEN_HOST" "
  R=/root/lt-results/$S
  tail -n 2 \$R/timeline.txt 2>/dev/null
  echo -n 'nodes: '; grep -E '^nodes t=' \$R/nodes.log 2>/dev/null | tail -n 1 | cut -c1-200; echo
  echo -n 'users: '; tail -n 1 \$R/users.log 2>/dev/null | cut -c1-200; echo
  uptime | sed 's/.*load/load/'; free -m | awk 'NR==2{print \"mem used \"\$3\"M avail \"\$7\"M\"} NR==3{print \"swap used \"\$3\"M\"}'
" 2>&1 | mask
echo "== 面板机 $PANEL_HOST"
ssh -n -o BatchMode=yes -o ConnectTimeout=10 "$PANEL_HOST" "
  P=/root/lt-results/$S
  tail -n 1 \$P/timeline.txt 2>/dev/null
  if [ -f \$P/procs.csv ]; then
    last=\$(awk -F, '\$3==\"_system\"{t=\$2} END{print t}' \$P/procs.csv)   # 取最后一格完整采样（以 _system 行为准）
    echo 'procs（单核=100，整机两核=200）：'
    awk -F, -v t=\$last '\$2==t {printf \"  %-14s cpu %6s  rss %6.0fM\n\", \$3, \$5, \$6/1024}' \$P/procs.csv
  fi
  [ -f \$P/pgact.csv ] && { echo -n 'pgact: '; head -n 1 \$P/pgact.csv | cut -d, -f3-; echo -n '       '; tail -n 1 \$P/pgact.csv | cut -d, -f3-; }
  uptime | sed 's/.*load/load/'; free -m | awk 'NR==2{print \"mem used \"\$3\"M avail \"\$7\"M\"} NR==3{print \"swap used \"\$3\"M\"}'
  echo \"nginx 5xx 行（整份日志，含 seed 阶段）: \$(grep -c ' status=5' /var/log/nginx/aegis-access.log 2>/dev/null)\"
" 2>&1 | mask
