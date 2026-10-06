#!/usr/bin/env bash
# [INPUT]: 依赖 Linux 的 /proc（各进程的 stat、status、smaps_rollup 与整机的 /proc/stat、/proc/meminfo）与 pgrep；不连数据库、不读 .env
# [OUTPUT]: 按固定间隔把各进程组的 CPU 与内存写成 CSV：ts_utc,unix_s,proc,pids,cpu_pct,rss_kb,pss_kb
# [POS]: tools/loadtest/scripts 的资源占用采样器，压测全程在面板主机上后台跑；与 snapshot-mem.sh（数据库与缓存内部视角）互补
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
#
# 用法（面板主机，root 以读到 postgres / valkey 等其他用户进程的 smaps_rollup）：
#   sample-procs.sh OUT.csv [间隔秒，缺省 5] [总时长秒，缺省 0 = 直到 Ctrl-C / kill]
#
# 进程组（按进程名精确匹配，Docker 里的 postgres / valkey 在宿主机上同样可见）：
#   aegis-public aegis-admin aegis-node postgres valkey（valkey-server 与 redis-server 合并） nginx
#   以及 _system：整机，cpu_pct 为全部核的忙碌合计，rss_kb 为 MemTotal - MemAvailable
#
# 口径：
#   cpu_pct  上一间隔内的 CPU 时间 / 间隔，单核满载 = 100（与 top 的进程列同口径），按 /proc/<pid>/stat
#            的 utime+stime 增量算；间隔内新出现的进程整段计入，间隔内退出的进程最后一段丢失（略低估）
#   rss_kb   组内 VmRSS 之和；postgres 每个后端都把触碰过的 shared_buffers 算进自己的 RSS，求和会重复计
#   pss_kb   组内 smaps_rollup 的 Pss 之和，共享页按进程数均摊，是该组真实占用的近似；读不到时为空
#   第一行样本只建基线，从第二个间隔起才输出。
set -euo pipefail
export LC_ALL=C # EPOCHREALTIME 与 awk 的小数点不受区域设置影响

die() { printf 'sample-procs: %s\n' "$*" >&2; exit 1; }

OUT="${1:-}"
INTERVAL="${2:-5}"
DURATION="${3:-0}"
[[ -n "$OUT" ]] || die "用法：sample-procs.sh OUT.csv [间隔秒] [总时长秒]"
[[ "$INTERVAL" =~ ^[1-9][0-9]*$ ]] || die "间隔必须是正整数秒"
[[ "$DURATION" =~ ^[0-9]+$ ]] || die "总时长必须是非负整数秒"
[[ -r /proc/self/stat ]] || die "只能在 Linux 上运行（需要 /proc）"
command -v pgrep >/dev/null 2>&1 || die "需要 pgrep（procps）"

CLK_TCK="$(getconf CLK_TCK)"
PROC_GROUPS=(aegis-public aegis-admin aegis-node postgres valkey nginx)

# group_pids NAME：组内全部 pid（每行一个）
group_pids() {
  case "$1" in
    valkey) pgrep -x valkey-server || true; pgrep -x redis-server || true ;;
    *) pgrep -x "$1" || true ;;
  esac
}

# proc_ticks PID：utime+stime；comm 可能含空格与括号，从最后一个 ')' 之后数字段
proc_ticks() {
  local stat rest
  stat="$(cat "/proc/$1/stat" 2>/dev/null)" || return 1
  rest="${stat##*) }"
  # rest 的第 1 个字段是原第 3 字段 state，utime/stime 是原第 14、15 字段
  awk '{ print $12 + $13 }' <<<"$rest"
}

proc_kb() { # proc_kb PID FILE KEY
  awk -v k="$3:" '$1 == k { print $2; found = 1; exit } END { if (!found) print "" }' "/proc/$1/$2" 2>/dev/null || true
}

system_busy_ticks() { awk '$1 == "cpu" { print $2 + $3 + $4 + $7 + $8 + $9; exit }' /proc/stat; }
system_used_kb() { awk '$1 == "MemTotal:" { t = $2 } $1 == "MemAvailable:" { a = $2 } END { print t - a }' /proc/meminfo; }

declare -A PREV_TICKS=()
declare -A SEEN=()
PREV_SYSTEM=""
PREV_CLOCK=""

sample() { # sample EMIT(0|1)
  local emit="$1" now ts clock elapsed group pid ticks delta count rss pss rss_sum pss_sum pss_ok cpu_ticks busy
  now="$(date +%s)"
  ts="$(date -u +%FT%TZ)"
  # 用实际流逝的时间作分母：sleep 之外还有采样本身的开销，按名义间隔算会偏高
  clock="${EPOCHREALTIME:-$now}"
  elapsed="$(awk -v a="$clock" -v b="${PREV_CLOCK:-$clock}" -v iv="$INTERVAL" 'BEGIN { d = a - b; print (d > 0 ? d : iv) }')"
  declare -A next=()
  for group in "${PROC_GROUPS[@]}"; do
    count=0 rss_sum=0 pss_sum=0 pss_ok=1 cpu_ticks=0
    while read -r pid; do
      [[ -n "$pid" ]] || continue
      ticks="$(proc_ticks "$pid")" || continue
      next[$pid]="$ticks"
      if [[ -n "${SEEN[$pid]:-}" ]]; then
        delta=$(( ticks - PREV_TICKS[$pid] ))
        (( delta >= 0 )) || delta=0
      else
        delta="$ticks"
      fi
      cpu_ticks=$(( cpu_ticks + delta ))
      rss="$(proc_kb "$pid" status VmRSS)"
      pss="$(proc_kb "$pid" smaps_rollup Pss)"
      rss_sum=$(( rss_sum + ${rss:-0} ))
      if [[ -n "$pss" ]]; then pss_sum=$(( pss_sum + pss )); else pss_ok=0; fi
      count=$(( count + 1 ))
    done < <(group_pids "$group")
    (( count > 0 )) || pss_ok=0
    if [[ "$emit" == 1 ]]; then
      awk -v ts="$ts" -v now="$now" -v g="$group" -v n="$count" -v t="$cpu_ticks" \
        -v hz="$CLK_TCK" -v iv="$elapsed" -v rss="$rss_sum" -v pss="$pss_sum" -v ok="$pss_ok" \
        'BEGIN { printf "%s,%s,%s,%d,%.1f,%d,%s\n", ts, now, g, n, t * 100 / (hz * iv), rss, (ok ? pss : "") }' >> "$OUT"
    fi
  done
  busy="$(system_busy_ticks)"
  if [[ "$emit" == 1 ]]; then
    awk -v ts="$ts" -v now="$now" -v t="$(( busy - ${PREV_SYSTEM:-busy} ))" -v hz="$CLK_TCK" -v iv="$elapsed" \
      -v used="$(system_used_kb)" -v cpus="$(nproc)" \
      'BEGIN { printf "%s,%s,_system,%d,%.1f,%d,\n", ts, now, cpus, t * 100 / (hz * iv), used }' >> "$OUT"
  fi
  PREV_SYSTEM="$busy"
  PREV_CLOCK="$clock"
  PREV_TICKS=()
  SEEN=()
  for pid in "${!next[@]}"; do
    PREV_TICKS[$pid]="${next[$pid]}"
    SEEN[$pid]=1
  done
}

mkdir -p -- "$(dirname -- "$OUT")"
[[ -s "$OUT" ]] || echo 'ts_utc,unix_s,proc,pids,cpu_pct,rss_kb,pss_kb' > "$OUT"
printf 'sample-procs: 每 %s 秒采样一次写入 %s（_system 行的 pids 列是 CPU 核数）\n' "$INTERVAL" "$OUT" >&2

trap 'printf "sample-procs: 停止\n" >&2; exit 0' INT TERM
sample 0
start="$(date +%s)"
while :; do
  sleep "$INTERVAL"
  sample 1
  if (( DURATION > 0 )) && (( $(date +%s) - start >= DURATION )); then
    break
  fi
done
