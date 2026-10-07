#!/usr/bin/env bash
# 观测工具自身开销对照（面板机 /root/lt/ 下以 root 跑，压测停了以后跑）：overhead.sh <输出目录> [每段秒数=90]
# 依次量：不开任何采样器（基线）→ 全部采样器 → 各采样器单独开；每段用 /proc/stat 做差，
# 输出 <输出目录>/overhead.csv（单核 = 100）。差值 = 该段 − 基线，就是那个采样器的开销。
set -uo pipefail
O=${1:?输出目录}; SEG=${2:-90}; mkdir -p "$O"; cd /root/lt
if pgrep -f 'sample-(procs|cgroup|vmswap|pgact|cpustat)' >/dev/null || pgrep -x vmstat >/dev/null; then
  echo "还有采样器在跑，先停掉" >&2; exit 1
fi
echo "phase,seconds,busy,user,system,softirq,steal" > "$O/overhead.csv"
snap() { awk '$1=="cpu"{print $2+$3, $4, $7, $8, $9, $2+$3+$4+$5+$6+$7+$8+$9}' /proc/stat; }
measure() {  # measure <段名> <命令…>：后台起命令，等 SEG 秒，停掉，记一行
  local name=$1; shift
  local pids=()
  if (( $# > 0 )); then
    for c in "$@"; do bash -c "exec $c" >/dev/null 2>&1 & pids+=($!); done
    sleep 3   # 让采样器先跑起来，避开启动瞬间
  fi
  read -r u0 s0 q0 t0 st0 a0 < <(snap); sleep "$SEG"; read -r u1 s1 q1 t1 st1 a1 < <(snap)
  (( ${#pids[@]} )) && { kill "${pids[@]}" 2>/dev/null; pkill -P "$$" -f 'sample-' 2>/dev/null; wait 2>/dev/null; }
  local tot=$(( a1 - a0 ))
  awk -v n="$name" -v sec="$SEG" -v u=$((u1-u0)) -v s=$((s1-s0)) -v q=$((q1-q0)) -v t=$((t1-t0)) -v st=$((st1-st0)) -v tot=$tot -v c="$(nproc)" \
    'BEGIN{f=c*100/tot; printf "%s,%d,%.2f,%.2f,%.2f,%.2f,%.2f\n", n, sec, (u+s+q+t+st)*f, u*f, s*f, q*f, st*f}' >> "$O/overhead.csv"
  sleep 5
}
T=$(mktemp -d)
measure baseline
measure all "./sample-procs.sh $T/p.csv 5" "./sample-cgroup.sh $T/c.csv 60" "./sample-vmswap.sh $T/v.csv 5" "./sample-pgact.sh $T/a.csv 10" "./sample-cpustat.sh $T/u 5" "vmstat -n -t 1 > $T/vm.txt"
measure procs "./sample-procs.sh $T/p1.csv 5"
measure pgact "./sample-pgact.sh $T/a1.csv 10"
measure cpustat "./sample-cpustat.sh $T/u1 5"
measure vmswap "./sample-vmswap.sh $T/v1.csv 5"
measure baseline2
rm -rf "$T"
cat "$O/overhead.csv"
