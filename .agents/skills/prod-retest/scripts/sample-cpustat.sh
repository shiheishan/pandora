#!/usr/bin/env bash
# 整机 CPU 拆分（面板机上以 root 跑）：sample-cpustat.sh <目录> [间隔=5]
#   <目录>/procstat.csv  每 N 秒一行 /proc/stat 的 cpu 累计 tick（user nice system idle iowait irq softirq steal）
#   <目录>/softirqs.csv  每 N 秒一行 /proc/softirqs 各类累计次数（两核合计）
#   <目录>/allprocs.csv  每 3N 秒一次：所有进程按 comm 汇总的 utime+stime 累计 tick；child_ticks = 已回收子进程的 cutime+cstime（短命子进程的 CPU 记在父进程这里）
# 事后做差即可得到各类占比；tick 单位是 1/100 秒（CLK_TCK=100）。
D=$1; IV=${2:-5}; mkdir -p $D
echo "unix_s,user,nice,system,idle,iowait,irq,softirq,steal" > $D/procstat.csv
echo "unix_s,$(awk 'NR>1{printf "%s%s", (NR>2?",":""), tolower($1)}' /proc/softirqs | tr -d ':')" > $D/softirqs.csv
echo "unix_s,comm,ticks,nproc,child_ticks" > $D/allprocs.csv
i=0
while :; do
  us=$(date +%s)
  awk -v t=$us '$1=="cpu"{print t","$2","$3","$4","$5","$6","$7","$8","$9}' /proc/stat >> $D/procstat.csv
  awk -v t=$us 'NR>1{s=0; for(i=2;i<=NF;i++) if ($i ~ /^[0-9]+$/) s+=$i; v=v (NR>2?",":"") s} END{print t","v}' /proc/softirqs >> $D/softirqs.csv
  if (( i % 3 == 0 )); then
    # /proc/<pid>/stat：comm 在括号里，可能含空格；取最后一个 ")" 之后的字段，utime=第 14、stime=第 15 字段
    for f in /proc/[0-9]*/stat; do
      read -r line < $f 2>/dev/null || continue
      comm=${line#*(}; comm=${comm%)*}; rest=${line##*) }
      set -- $rest
      echo "${comm// /_} $(( ${12} + ${13} )) $(( ${14} + ${15} ))"
    done 2>/dev/null | awk -v t=$us '{s[$1]+=$2; n[$1]++; c[$1]+=$3} END{for(k in s) print t","k","s[k]","n[k]","c[k]}' >> $D/allprocs.csv
  fi
  i=$((i+1)); sleep $IV
done
