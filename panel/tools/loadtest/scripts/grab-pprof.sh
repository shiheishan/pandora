#!/usr/bin/env bash
# 用法（面板主机，root 以读 0600 的 .env）：
#   grab-pprof.sh DIR [CPU 采样秒数，缺省 30]
# 也可在别处经 SSH 隧道跑，用环境变量直接给地址、跳过 .env：
#   ssh -N -L 16060:127.0.0.1:6060 panel-host &
#   LT_PPROF_PUBLIC=127.0.0.1:16060 LT_PPROF_ADMIN= LT_PPROF_NODE= grab-pprof.sh ./out
# 只抓开了 pprof 的网关；三个都没开就报错退出。先在 .env 填 AEGIS_*_PPROF_ADDR 并重启对应网关。
# 看结果：go tool pprof -top DIR/aegis-public-cpu-*.pprof；go tool pprof -sample_index=inuse_space -top DIR/aegis-public-heap-*.pprof
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lt-common.sh
. "$SCRIPT_DIR/lt-common.sh"

DIR="${1:-}"
SECONDS_CPU="${2:-30}"
[[ -n "$DIR" ]] || lt_die "用法：grab-pprof.sh DIR [CPU 采样秒数]"
[[ "$SECONDS_CPU" =~ ^[1-9][0-9]*$ ]] && (( SECONDS_CPU <= 300 )) || lt_die "CPU 采样秒数必须是 1–300"
command -v curl >/dev/null 2>&1 || lt_die "需要 curl"

# 地址：LT_PPROF_<网关> 设了（哪怕为空）就用它，否则读 .env
declare -A ADDR=()
need_env=0
for gw in PUBLIC ADMIN NODE; do
  var="LT_PPROF_$gw"
  [[ -n "${!var+set}" ]] || need_env=1
done
if (( need_env )); then
  lt_init_env
fi
for gw in PUBLIC ADMIN NODE; do
  var="LT_PPROF_$gw"
  if [[ -n "${!var+set}" ]]; then
    ADDR[$gw]="${!var}"
  else
    ADDR[$gw]="$(lt_env "AEGIS_${gw}_PPROF_ADDR")"
  fi
done

mkdir -p -- "$DIR"
STAMP="$(lt_stamp)"
declare -A NAME=([PUBLIC]=aegis-public [ADMIN]=aegis-admin [NODE]=aegis-node)

# fetch URL FILE：失败时删掉半截文件并返回非零
fetch() {
  if curl -fsS --max-time "$(( SECONDS_CPU + 30 ))" -o "$2" "$1"; then
    return 0
  fi
  rm -f -- "$2"
  printf 'grab-pprof: 取 %s 失败\n' "$1" >&2
  return 1
}

pids=()
active=()
for gw in PUBLIC ADMIN NODE; do
  addr="${ADDR[$gw]}"
  [[ -n "$addr" ]] || continue
  active+=("$gw")
  lt_say "${NAME[$gw]}（$addr）：CPU profile ${SECONDS_CPU}s"
  fetch "http://$addr/debug/pprof/profile?seconds=$SECONDS_CPU" "$DIR/${NAME[$gw]}-cpu-$STAMP.pprof" &
  pids+=("$!")
done
(( ${#active[@]} > 0 )) || lt_die "三个网关都没开 pprof：在 .env 填 AEGIS_PUBLIC_PPROF_ADDR 等并重启网关，或用 LT_PPROF_* 指定地址"

failed=0
for pid in "${pids[@]}"; do
  wait "$pid" || failed=1
done

# CPU 采完再取堆：同时取会把 heap 的 GC 算进 CPU profile
for gw in "${active[@]}"; do
  addr="${ADDR[$gw]}"
  fetch "http://$addr/debug/pprof/heap?gc=1" "$DIR/${NAME[$gw]}-heap-$STAMP.pprof" || failed=1
  fetch "http://$addr/debug/pprof/allocs" "$DIR/${NAME[$gw]}-allocs-$STAMP.pprof" || failed=1
  fetch "http://$addr/debug/pprof/goroutine" "$DIR/${NAME[$gw]}-goroutine-$STAMP.pprof" || failed=1
done

lt_say "产物在 $DIR（时间戳 $STAMP）"
exit "$failed"
