#!/usr/bin/env bash
# 在本机同时起一个场景的两端（面板机采集 + 压测机负载），共用一个 T：
#   start.sh <env.sh> <场景> <压测机上的 manifest 路径> [提前秒数=330] [run-load 的额外参数...]
# 远端一律写成「cd …; setsid -f <简单命令> > 日志 2>&1 < /dev/null」：
#   写成「cd … && setsid nohup x > 日志 &」时，& 作用于整个 && 列表，bash 会 fork 一个子 shell，
#   它的 stdout/stderr 仍是 ssh 的管道并且一直等 x 结束，ssh 因此挂住，同一条本机命令里的下一条 ssh 发不出去
#   （2026-10-07 踩了三次）。
# 场景目录已存在就拒绝，免得覆盖旧结果。
set -euo pipefail
ENV=${1:?env.sh}; S=${2:?场景名}; M=${3:?manifest}; LEAD=${4:-330}; shift $(( $# < 4 ? $# : 4 ))
# shellcheck disable=SC1090
. "$ENV"
for h in "$PANEL_HOST" "$LOADGEN_HOST"; do
  if ssh -n -o BatchMode=yes "$h" "test -e /root/lt-results/$S"; then echo "$h 上已有 /root/lt-results/$S，换个场景名" >&2; exit 1; fi
done
ssh -n -o BatchMode=yes "$LOADGEN_HOST" "test -s $M" || { echo "压测机上没有 $M" >&2; exit 1; }
T=$(( $(date +%s) + LEAD ))
echo "T=$T ($(date -u -r "$T" +%FT%TZ 2>/dev/null || date -u -d @"$T" +%FT%TZ))"
mkdir -p "$LOCAL_DIR"; echo "$T" > "$LOCAL_DIR/$S.T"
ssh -n -o BatchMode=yes "$PANEL_HOST" "mkdir -p /root/lt-results; cd /root/lt; setsid -f ./run-collect.sh $S $T > /root/lt-results/run-collect-$S.out 2>&1 < /dev/null; sleep 2; tail -n 2 /root/lt-results/$S/timeline.txt"
# 采集先起约 1.5 分钟再起节点：T−5m 开采样，节点约 T−3.5m 起跑、60 秒内错开，T 前已稳定 2 分钟以上
sleep 90
ssh -n -o BatchMode=yes "$LOADGEN_HOST" "setsid -f /root/run-load.sh $S $M $T $* > /root/lt-results/run-load-$S.out 2>&1 < /dev/null; sleep 2; cat /root/lt-results/$S/timeline.txt"
echo "进度：$(dirname "$0")/peek.sh $ENV $S"
