#!/usr/bin/env bash
# 一场静默的面板机采集（root，/root/lt/ 下跑；由 quiet-start.sh 远程拉起）：
#   gate-collect.sh <输出目录> <T unix 秒> [窗口分钟=15] [--diag]
# 正式轮：只跑仓库的 quiet-collect.sh（窗口 [T, T+W)），窗口后再记一份内存拆账 mem-detail.txt。
# 诊断轮（--diag，要先 pgstat.sh enable 与开 aegis-node 的 pprof）：另加 T−60 pgss reset、T+300 抓 aegis-node
#   60 秒 CPU profile 与堆、T+W 导出 pgss 前 30。诊断轮自身有开销，数不进判分，只用来找原因。
# 时间点记在 <输出目录>/gate-timeline.txt；quiet-collect 自己的时间轴在 timeline.txt。
set -uo pipefail
OUT=${1:?输出目录}; T=${2:?T unix 秒}; W=${3:-15}; DIAG=${4:-}
[[ "$T" =~ ^[0-9]+$ && "$W" =~ ^[1-9][0-9]*$ ]] || { echo "用法：gate-collect.sh <输出目录> <T> [窗口分钟] [--diag]" >&2; exit 2; }
[[ -z "$DIAG" || "$DIAG" == --diag ]] || { echo "第 4 个参数只能是 --diag" >&2; exit 2; }
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$OUT"; cd "$HERE"
log() { echo "$(date -u +%FT%TZ) $*" >> "$OUT/gate-timeline.txt"; }
at() { local w=$(( $1 - $(date +%s) )); (( w > 0 )) && sleep "$w"; return 0; }

log "start T=$(date -u -d @"$T" +%FT%TZ) W=${W}m mode=${DIAG:-formal}"
( ./quiet-collect.sh "$OUT" "$T" "$W" > "$OUT/quiet-collect.out" 2>&1 ) &
QC=$!
if [[ -n "$DIAG" ]]; then
  at $((T - 60)); ./pgstat.sh reset > "$OUT/pgstat-reset.out" 2>&1; log "pgss reset rc=$?"
  at $((T + 300))
  LT_PPROF_PUBLIC= LT_PPROF_ADMIN= LT_PPROF_NODE="${LT_PPROF_NODE:-127.0.0.1:6062}" \
    ./grab-pprof.sh "$OUT/pprof" 60 > "$OUT/pprof.out" 2>&1; log "pprof rc=$?"
  at $((T + W * 60)); ./pgstat.sh export "$OUT/pgss" 30 > "$OUT/pgstat-export.out" 2>&1; log "pgss export rc=$?"
fi
wait "$QC"; log "quiet-collect rc=$?"
./memdetail.sh > "$OUT/mem-detail.txt" 2>&1; log "memdetail rc=$?"
log done
