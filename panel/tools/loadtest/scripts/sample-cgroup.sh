#!/usr/bin/env bash
# 用法（面板主机；cgroup 文件对所有用户可读，不必 root）：
#   sample-cgroup.sh OUT.csv [间隔秒，缺省 60] [总时长秒，缺省 0 = 直到 Ctrl-C / kill]
#
# 怎么读：计数都是单元启动以来的累计值，相邻两行做差。
#   nr_throttled 在稳态里一格一格往上涨 = 该网关在撞 CPUQuota（随包 public/node 60%、admin 80%）
#   mem_max_events / oom_kill 增加 = 撞了 MemoryMax；mem_high_events 只在设了 MemoryHigh 时有意义
# 网关重启后计数归零，做差时以单元重启为界分段。
set -euo pipefail
export LC_ALL=C

die() { printf 'sample-cgroup: %s\n' "$*" >&2; exit 1; }

OUT="${1:-}"
INTERVAL="${2:-60}"
DURATION="${3:-0}"
ROOT="${LT_CGROUP_ROOT:-/sys/fs/cgroup/system.slice}"
UNITS=(aegis-public aegis-admin aegis-node)

[[ -n "$OUT" ]] || die "用法：sample-cgroup.sh OUT.csv [间隔秒] [总时长秒]"
[[ "$INTERVAL" =~ ^[1-9][0-9]*$ ]] || die "间隔必须是正整数秒"
[[ "$DURATION" =~ ^[0-9]+$ ]] || die "总时长必须是非负整数秒"
[[ -r "$ROOT/aegis-public.service/cpu.stat" ]] || \
  die "读不到 $ROOT/aegis-public.service/cpu.stat：需要 cgroup v2 且网关以 systemd 单元运行"

# kv FILE KEY：取「key value」格式文件里某个键的值，缺失记空
kv() { awk -v k="$2" '$1 == k { print $2; found=1 } END { if (!found) print "" }' "$1" 2>/dev/null || true; }
one() { [[ -r "$1" ]] && tr -d '\n' < "$1" || true; }

[[ -s "$OUT" ]] || echo 'ts_utc,unix_s,unit,nr_periods,nr_throttled,throttled_usec,memory_current,memory_max,mem_high_events,mem_max_events,oom_kill' > "$OUT"
printf 'sample-cgroup: 每 %s 秒采样一次写入 %s\n' "$INTERVAL" "$OUT" >&2

start="$(date +%s)"
while :; do
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"; now="$(date +%s)"
  for u in "${UNITS[@]}"; do
    d="$ROOT/$u.service"
    [[ -d "$d" ]] || continue # 单元没在跑（重启间隙）就跳过这一格
    printf '%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n' "$ts" "$now" "$u" \
      "$(kv "$d/cpu.stat" nr_periods)" "$(kv "$d/cpu.stat" nr_throttled)" "$(kv "$d/cpu.stat" throttled_usec)" \
      "$(one "$d/memory.current")" "$(one "$d/memory.max")" \
      "$(kv "$d/memory.events" high)" "$(kv "$d/memory.events" max)" "$(kv "$d/memory.events" oom_kill)" >> "$OUT"
  done
  if (( DURATION > 0 && now - start >= DURATION )); then
    break
  fi
  sleep "$INTERVAL"
done
