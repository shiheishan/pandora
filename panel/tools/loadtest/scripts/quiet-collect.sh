#!/usr/bin/env bash
# 静默测量的面板机采样（root）：
#   quiet-collect.sh <输出目录> <T unix 秒> <窗口分钟，缺省 15>
#
# 静默 = 0 活跃用户，只有模拟节点在拉取、上报、心跳。标准（用户 2026-10-07 / 10-08）：
#   面板 + 数据库（三网关 + postgres + valkey，cgroup 口径）≤ 单核 30%；整机已用内存 ≤ 1 GiB（free 的 used，不含 cache）。
#
# 口径与 5k-r4 静默轮次一致，刻意做得很轻——采样器自己的开销会把静默 CPU 抬过线（sample-procs / sample-pgact
# 在 5k-r4 实测各占 2–9 个百分点），所以窗口内只有一个 `vmstat 5`：
#   T 与 T+窗口 各读一次 /proc/stat、全部进程的 /proc/<pid>/stat（含 cutime/cstime）、system.slice 下各单元的 cpu.stat；
#   T−20 秒 与 T+窗口 各取一次内存快照（窗口外，不算进窗口的 CPU）：free、meminfo、vmstat 换页计数、关键进程 PSS、
#   Docker 布局另记 docker stats 与各网关 cgroup 的 memory.current。
# 产物目录交给 `loadtest quiet-report -dir <目录>` 出判定。
#
# 同时兼容 install.sh 的 Docker 布局与 install-native.sh 的直装布局；cgroup v2（Debian 13 / Ubuntu 24.04 缺省）。
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
    for p in $(pgrep -x 'aegis-public|aegis-admin|aegis-node|postgres|valkey-server|redis-server|nginx|docker-proxy|dockerd|containerd|containerd-shim|containerd-shim-runc-v2'); do
      c="$(cat "/proc/$p/comm" 2>/dev/null)" || continue
      v="$(awk '$1=="Pss:"{print $2}' "/proc/$p/smaps_rollup" 2>/dev/null)"
      r="$(awk '$1=="VmRSS:"{print $2}' "/proc/$p/status" 2>/dev/null)"
      echo "$c $p ${v:-0} ${r:-0}"
    done | awk '{pss[$1]+=$3; rss[$1]+=$4; n[$1]++} END{for(k in pss) printf "%-26s n=%-3d pss_kb=%-8d rss_kb=%d\n", k, n[k], pss[k], rss[k]}' | sort
    if command -v docker >/dev/null 2>&1; then
      echo "== docker stats"
      docker stats --no-stream --format '{{.Name}} {{.MemUsage}} {{.CPUPerc}}' 2>&1
    fi
    echo "== gateway cgroup memory.current"
    local g
    for g in aegis-public aegis-admin aegis-node; do
      echo "$g $(cat "/sys/fs/cgroup/system.slice/$g.service/memory.current" 2>/dev/null)"
    done
  } > "$f" 2>&1
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

# 容器全 ID → 名字，quiet-report 据此把 docker-<id>.scope 认成 aegis-postgres / aegis-valkey
if command -v docker >/dev/null 2>&1; then
  docker ps --no-trunc --format '{{.ID}} {{.Names}}' > "$OUT/containers.txt" 2>/dev/null || true
fi

log "start T=$(date -u -d @"$T" +%FT%TZ) window=${W}m"
at $((T - 20)); mem before; log "mem before"
( exec vmstat -n -t 5 > "$OUT/vmstat.txt" ) & VM=$!
at "$T"; cpusnap A; log "T cpu A"
at $((T + W * 60)); cpusnap B; log "T+W cpu B"
kill "$VM" 2>/dev/null
mem after; log "mem after; done"
lt_say "完成：loadtest quiet-report -dir $OUT"
