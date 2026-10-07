#!/usr/bin/env bash
# 面板机侧采集时间轴（runbook 第 8 节），在面板机 /root/lt/ 下以 root 跑。
# 用法：run-collect.sh <场景> <T unix 秒> [稳态分钟=30]
#   起跑即开采样器（应在 T−5m 左右起）→ T−1m pgstat reset + 内存快照 → T+W/2 抓 30 秒 pprof
#   → T+W pgstat 导出 + 内存快照 → 停采样器 → T+W+1m 拷 nginx 与网关日志并裁到窗口
# 时间点写进 $P/timeline.txt；压测机的 run-load.sh 用同一个 T 对齐。
# 依赖同目录：仓库 panel/tools/loadtest/scripts/ 的 sample-procs、sample-cgroup、pgstat、snapshot-mem、grab-pprof、lt-common，
# 以及本 skill 的 sample-vmswap、sample-pgact、sample-cpustat、trim-access（push-scripts.sh 一并推上来）。
set -uo pipefail
S=${1:?场景名}; T=${2:?T unix 秒}; W=${3:-30}
P=/root/lt-results/$S; mkdir -p "$P/cpu"; cd /root/lt
log() { echo "$(date -u +%FT%TZ) $*" | tee -a "$P/timeline.txt"; }
at() { local w=$(( $1 - $(date +%s) )); (( w > 0 )) && sleep "$w"; }
log "start; T=$(date -u -d @"$T" +%FT%TZ) W=${W}m"
# 机器规格写一份，summarize.py 读 MemTotal 与核数
{ grep MemTotal /proc/meminfo; echo "nproc: $(nproc)"; } > "$P/meminfo.txt"
./sample-procs.sh "$P/procs.csv" 5 & SP=$!
./sample-cgroup.sh "$P/cgroup.csv" 60 & SC=$!
./sample-vmswap.sh "$P/vmswap.csv" 5 & SV=$!
./sample-pgact.sh "$P/pgact.csv" 10 & SA=$!
./sample-cpustat.sh "$P/cpu" 5 & SU=$!
( exec vmstat -n -t 1 > "$P/cpu/vmstat.txt" ) & SVM=$!
log "samplers pid $SP $SC $SV $SA $SU $SVM"
at $((T-60)); ./pgstat.sh reset >>"$P/collect.log" 2>&1; ./snapshot-mem.sh "$P" before >>"$P/collect.log" 2>&1; log "T-1m reset+before rc=$?"
at "$T"; log "T"
at $((T+W*30)); ./grab-pprof.sh "$P" 30 >>"$P/collect.log" 2>&1; log "T+W/2 pprof rc=$?"
at $((T+W*60)); ./pgstat.sh export "$P" 50 >>"$P/collect.log" 2>&1; ./snapshot-mem.sh "$P" after >>"$P/collect.log" 2>&1; log "T+W export+after rc=$?"
kill $SP $SC $SV $SA $SU $SVM 2>/dev/null; wait 2>/dev/null; log "samplers stopped"
at $((T+W*60+60))
cp /var/log/nginx/*access*.log "$P/" 2>/dev/null; cp /var/log/aegis/*.log "$P/"
./trim-access.sh "$P" "$W" >/dev/null 2>&1
log "T+W+1m logs copied; done"
