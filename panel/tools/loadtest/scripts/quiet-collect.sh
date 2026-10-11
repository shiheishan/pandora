#!/usr/bin/env bash
# 静默测量的面板机采样（root）：
#   quiet-collect.sh <输出目录> <T unix 秒> <窗口分钟，缺省 15>
#
# 静默 = 0 活跃用户，只有模拟节点在拉取、上报、心跳。标准（用户 2026-10-09，分 A/B 两档）与判定在
# `loadtest quiet-report`（quiet/report.go 的 tiers），这里只管采。
#
# 口径与 5k-r4 静默轮次一致，刻意做得很轻——采样器自己的开销会把静默 CPU 抬过线（sample-procs / sample-pgact
# 在 5k-r4 实测各占 2–9 个百分点），所以窗口内只有一个 `vmstat 5`：
#   T 与 T+窗口 各读一次 /proc/stat、全部进程的 /proc/<pid>/stat（含 cutime/cstime）、system.slice 下各单元的 cpu.stat；
#   T−20 秒 与 T+窗口 各取一次内存快照（窗口外，不算进窗口的 CPU）：free、meminfo、vmstat 换页计数、关键进程 PSS、
#   各网关 cgroup 的 memory.current；
#   同一时刻另写 kern-<标签>.txt（内核记账：min_free_kbytes、THP、完整 meminfo、sockstat、slab 前 30、zoneinfo）
#   与 pg-smaps-<标签>.txt（PG 每个进程的 smaps_rollup），用来拆「已用」里内核水位、slab、PG 共享内存各占多少。
#   开头写一份 host.txt（machine-id、核数、内核、面板版本），perf-gate 据此核对改前改后是不是同一台机器。
# 产物目录交给 `loadtest quiet-report -dir <目录> -tier <A|B>` 出判定。
#
# 只认 deploy/install.sh 的直装布局（库与缓存是 system.slice 下的 postgresql@*-main、valkey-server / redis-server）；
# cgroup v2（Debian 13 / Ubuntu 24.04 缺省）。
set -uo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lt-common.sh
. "$SCRIPT_DIR/lt-common.sh"

OUT="${1:-}"; T="${2:-}"; W="${3:-15}"
[[ -n "$OUT" && "$T" =~ ^[0-9]+$ && "$W" =~ ^[1-9][0-9]*$ ]] || lt_die "用法：quiet-collect.sh <输出目录> <T unix 秒> [窗口分钟，缺省 15]"
lt_require_linux
lt_require_root
[[ -r /sys/fs/cgroup/system.slice/cpu.stat ]] || lt_die "找不到 cgroup v2 的 /sys/fs/cgroup/system.slice/cpu.stat"
mkdir -p "$OUT"

at() { local w=$(( $1 - $(date +%s) )); (( w > 0 )) && sleep "$w"; return 0; }
log() { echo "$(date -u +%FT%TZ) $*" >> "$OUT/timeline.txt"; }

mem() { # mem <标签>
  local f="$OUT/mem-$1.txt"
  {
    date -u +%FT%TZ
    free -m
    grep -E '^(MemTotal|MemAvailable|Cached|AnonPages|Shmem|SwapTotal|SwapFree):' /proc/meminfo
    grep -E '^pswp(in|out) ' /proc/vmstat
    echo "== PSS(kB) by comm"
    local p c v r
    for p in $(pgrep -x 'aegis-public|aegis-admin|aegis-node|postgres|valkey-server|redis-server|nginx'); do
      c="$(cat "/proc/$p/comm" 2>/dev/null)" || continue
      v="$(awk '$1=="Pss:"{print $2}' "/proc/$p/smaps_rollup" 2>/dev/null)"
      r="$(awk '$1=="VmRSS:"{print $2}' "/proc/$p/status" 2>/dev/null)"
      echo "$c $p ${v:-0} ${r:-0}"
    done | awk '{pss[$1]+=$3; rss[$1]+=$4; n[$1]++} END{for(k in pss) printf "%-26s n=%-3d pss_kb=%-8d rss_kb=%d\n", k, n[k], pss[k], rss[k]}' | sort
    echo "== gateway cgroup memory.current"
    local g
    for g in aegis-public aegis-admin aegis-node; do
      echo "$g $(cat "/sys/fs/cgroup/system.slice/$g.service/memory.current" 2>/dev/null)"
    done
  } > "$f" 2>&1
}

kern() { # kern <标签>：内核侧内存记账，quiet-report 不读，拆账时人看
  {
    date -u +%FT%TZ
    echo "== vm.min_free_kbytes $(cat /proc/sys/vm/min_free_kbytes 2>/dev/null)"
    echo "== vm.watermark_scale_factor $(cat /proc/sys/vm/watermark_scale_factor 2>/dev/null)"
    local x
    for x in enabled defrag khugepaged/defrag; do
      echo "== THP $x: $(cat "/sys/kernel/mm/transparent_hugepage/$x" 2>/dev/null)"
    done
    echo "== meminfo"; cat /proc/meminfo
    echo "== sockstat"; cat /proc/net/sockstat /proc/net/sockstat6 2>/dev/null
    echo "== slab（按占用排序前 30）"
    if command -v slabtop >/dev/null 2>&1; then
      slabtop -o -s c 2>/dev/null | head -n 37
    else
      # 没装 procps 的 slabtop 时按 /proc/slabinfo 算：num_objs × objsize
      awk 'NR > 2 { printf "%-28s %10.1f KiB  objs=%s size=%s\n", $1, $3 * $4 / 1024, $3, $4 }' /proc/slabinfo | sort -k2 -nr | head -n 30
    fi
    echo "== zoneinfo"; cat /proc/zoneinfo
  } > "$OUT/kern-$1.txt" 2>&1
  # PG 每个进程一行（kB）：后端各自的匿名页、共享缓冲摊到谁身上，PSS 汇总看不出来
  {
    echo "pid rss pss pss_anon pss_file pss_shmem private_dirty swap cmd"
    local p
    for p in $(pgrep -x postgres); do
      awk -v p="$p" -v c="$(tr '\0' ' ' < "/proc/$p/cmdline" 2>/dev/null | cut -c1-80)" '
        $1=="Rss:"{r=$2} $1=="Pss:"{s=$2} $1=="Pss_Anon:"{a=$2} $1=="Pss_File:"{f=$2} $1=="Pss_Shmem:"{h=$2}
        $1=="Private_Dirty:"{d=$2} $1=="Swap:"{w=$2}
        END{print p, r+0, s+0, a+0, f+0, h+0, d+0, w+0, c}' "/proc/$p/smaps_rollup" 2>/dev/null
    done
  } > "$OUT/pg-smaps-$1.txt" 2>&1
}

host() { # 机器指纹：perf-gate 核对改前改后同机同配置；不含 IP、主机名
  local f=/opt/pandora/deploy/release-artifact.env rel=""
  [[ -r "$f" ]] && rel="$(awk -F= '$1=="PANDORA_NATIVE_RELEASE_VERSION"{print $2}' "$f")"
  {
    echo "machine_id=$(cat /etc/machine-id 2>/dev/null)"
    echo "nproc=$(nproc)"
    echo "mem_total_kb=$(awk '$1=="MemTotal:"{print $2}' /proc/meminfo)"
    echo "kernel=$(uname -r)"
    echo "panel_release=$rel"
    echo "swap_total_kb=$(awk '$1=="SwapTotal:"{print $2}' /proc/meminfo)"
    echo "swap_free_kb=$(awk '$1=="SwapFree:"{print $2}' /proc/meminfo)"
  } > "$OUT/host.txt"
}

cpusnap() { # cpusnap <标签>
  local d="$OUT/cpu-$1" c f l
  mkdir -p "$d"
  date +%s.%N > "$d/at"
  grep '^cpu ' /proc/stat > "$d/procstat"
  for f in /proc/[0-9]*/stat; do read -r l < "$f" 2>/dev/null && echo "$l"; done > "$d/pidstat" 2>/dev/null
  for c in /sys/fs/cgroup/system.slice/*/cpu.stat; do
    echo "$(basename "$(dirname "$c")") $(awk '$1=="usage_usec"||$1=="user_usec"||$1=="system_usec"||$1=="nr_throttled"||$1=="throttled_usec"{printf "%s=%s ", $1, $2}' "$c")"
  done > "$d/cgroups"
}

log "start T=$(date -u -d @"$T" +%FT%TZ) window=${W}m"
host
at $((T - 20)); mem before; kern before; log "mem before"
( exec vmstat -n -t 5 > "$OUT/vmstat.txt" ) & VM=$!
at "$T"; cpusnap A; log "T cpu A"
at $((T + W * 60)); cpusnap B; log "T+W cpu B"
kill "$VM" 2>/dev/null
mem after; kern after; log "mem after; done"
lt_say "完成：loadtest quiet-report -dir $OUT"
