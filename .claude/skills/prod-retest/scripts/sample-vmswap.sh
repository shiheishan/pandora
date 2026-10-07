#!/usr/bin/env bash
# 诊断用（面板机上以 root 跑）：每 N 秒按进程名汇总 VmSwap / VmRSS（kB）：sample-vmswap.sh OUT.csv [间隔=5]
# 一次 awk 读全部 /proc/*/status（不逐进程起子进程，开销可忽略）
OUT=$1; IV=${2:-5}; echo "ts_utc,proc,vmswap_kb,vmrss_kb" > $OUT
while :; do
  awk -v ts="$(date -u +%FT%TZ)" '
    FNR==1 { name="" }
    $1=="Name:"   { name=$2 }
    $1=="VmSwap:" { s[name]+=$2 }
    $1=="VmRSS:"  { r[name]+=$2 }
    END { for (k in r) if (s[k]>0 || k ~ /aegis|postgres|valkey|nginx/) printf "%s,%s,%d,%d\n", ts, k, s[k], r[k] }
  ' /proc/[0-9]*/status 2>/dev/null >> $OUT
  sleep $IV
done
