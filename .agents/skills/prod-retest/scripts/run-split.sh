#!/usr/bin/env bash
# 诊断负载拆分（压测机上以 root 跑）：run-split.sh <场景> <manifest> <T epoch> <a|b|c50>
# 混合负载不过线时，用它把节点侧与用户侧拆开各跑一次，看谁压垮了库（2026-10-06 5k-diag 用过）。
#   a   只跑 nodes，198 个，节拍由面板下发（15s），T−5m 起、T+16m 止
#   b   只跑 users，T 起 15 分钟（含预热）
#   c50 只跑 nodes 前 50 个（50×15s 的拉取约等于 198 个节点 60s 节拍的拉用户速率）
set -uo pipefail
S=$1 M=$2 T=$3 MODE=$4
R=/root/lt-results/$S; mkdir -p $R
DOM=${PANEL_URL:-$(cat /root/.lt-panel-url)}; ADM=$(cat /root/.lt-admin-url)
export LOADTEST_ADMIN_EMAIL=${ADMIN_EMAIL:-ltadmin@example.com} LOADTEST_ADMIN_PASSWORD=$(cat /root/.lt-admin-pw)
log() { echo "$(date -u +%FT%TZ) $*" >> $R/timeline.txt; }
at() { local w=$(( $1 - $(date +%s) )); (( w > 0 )) && sleep $w; }
log "start mode=$MODE; T=$(date -u -d @$T +%FT%TZ)"
case $MODE in
  a|c50)
    n=0; [ $MODE = c50 ] && n=50
    /root/loadtest nodes -manifest $M -node-url $DOM -nodes $n -stagger 60s -duration 21m -steady-start $T -steady-dur 15m -out $R -strict > $R/nodes.log 2>&1
    log "nodes rc=$?" ;;
  b)
    at $T
    /root/loadtest users -manifest $M -public-url $DOM -admin-url $ADM -duration 15m -sub-interval 30m -portal-rate 5 -admin-rate 0.5 -login-rate 0.05 -portal-users 200 -steady-start $T -steady-dur 15m -out $R -strict > $R/users.log 2>&1
    log "users rc=$?" ;;
esac
sed -i "s#$ADM#<ADMIN_URL>#g" $R/*.log 2>/dev/null
log done
