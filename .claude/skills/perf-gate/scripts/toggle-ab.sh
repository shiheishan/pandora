#!/usr/bin/env bash
# 只改部署参数（环境变量、systemd 资源上限）时的一场内 A-B-A（面板机 root，/root/lt/ 下 setsid 起）：
#   toggle-ab.sh <输出文件> <单元> <drop-in 内容文件> [每段秒数=480] [切换后等待秒数=180]
# 例：drop-in 内容文件里写
#   [Service]
#   Environment=GOMAXPROCS=1
# 流程：缺省段 → 写 /etc/systemd/system/<单元>.d/perf-gate-ab.conf 并重启 → 等待 → 变体段 → 删 drop-in 并重启
#   → 等待 → 缺省段。每段一行：各单元 cgroup CPU（单核 = 100）、Δnr_throttled、面板 + 数据库合计（不含 nginx）。
# 前后两个缺省段之差就是这一场的噪声；变体段要落在两段之外才算有差别。
# 退出（含被 kill）时一定删掉 drop-in、daemon-reload 并重启单元，不会把变体留在机器上。
# 节点要在这之前就起好并进入稳态（quiet-start.sh 照常起一场，面板机不起 gate-collect，改起本脚本）。
set -uo pipefail
OUT=${1:?输出文件}; UNIT=${2:?单元，如 aegis-node}; CONTENT=${3:?drop-in 内容文件}; SEG=${4:-480}; SETTLE=${5:-180}
UNIT=${UNIT%.service}
[[ -r "$CONTENT" ]] || { echo "读不到 $CONTENT" >&2; exit 2; }
DROP=/etc/systemd/system/$UNIT.service.d/perf-gate-ab.conf
[[ -e "$DROP" ]] && { echo "$DROP 已存在：上一次没收尾，先人工核对" >&2; exit 2; }

cleanup() {
  if [[ -e "$DROP" ]]; then
    rm -f "$DROP"; systemctl daemon-reload; systemctl restart "$UNIT.service"
    echo "$(date -u +%FT%TZ) cleanup: drop-in removed, $UNIT restarted" >> "$OUT"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# 一段窗口的 cgroup CPU：system.slice 下各单元 usage_usec / nr_throttled 做差；docker 容器的 scope 翻成容器名
snap() {
  local c
  for c in /sys/fs/cgroup/system.slice/*/cpu.stat; do
    echo "$(basename "$(dirname "$c")") $(awk '$1=="usage_usec"{u=$2} $1=="nr_throttled"{t=$2} END{print u+0, t+0}' "$c")"
  done
}
window() { # window <标签> <秒数>
  local label=$1 secs=$2 k u t line tot=0 name pct
  declare -A U0=() T0=() N=()
  while read -r k u t; do U0[$k]=$u; T0[$k]=$t; done < <(snap)
  local t0 t1; t0=$(date +%s.%N); sleep "$secs"; t1=$(date +%s.%N)
  if command -v docker >/dev/null 2>&1; then
    while read -r id nm; do N[docker-$id.scope]=$nm; done < <(docker ps --no-trunc --format '{{.ID}} {{.Names}}' 2>/dev/null)
  fi
  line="$(date -u +%FT%TZ) $label"
  while read -r k u t; do
    [[ -n "${U0[$k]:-}" ]] || continue
    name=${N[$k]:-$k}
    case "$name" in
      aegis-public.service|aegis-admin.service|aegis-node.service|aegis-postgres|aegis-valkey|postgresql*.service|valkey*.service|redis*.service|nginx.service) ;;
      *) continue ;;
    esac
    pct=$(awk -v a="${U0[$k]}" -v b="$u" -v t0="$t0" -v t1="$t1" 'BEGIN{printf "%.2f", (b-a)/1e4/(t1-t0)}')
    line+=" $name=$pct thr=$(( t - T0[$k] ))"
    [[ $name == nginx.service ]] || tot=$(awk -v a="$tot" -v b="$pct" 'BEGIN{printf "%.2f", a+b}')
  done < <(snap)
  echo "$line panel+db=$tot" >> "$OUT"
}

echo "$(date -u +%FT%TZ) start unit=$UNIT seg=${SEG}s settle=${SETTLE}s variant=$(tr '\n' ' ' < "$CONTENT")" >> "$OUT"
window default-1 "$SEG"
mkdir -p "$(dirname "$DROP")"; cp "$CONTENT" "$DROP"; systemctl daemon-reload; systemctl restart "$UNIT.service"
echo "$(date -u +%FT%TZ) variant applied, $UNIT restarted" >> "$OUT"
sleep "$SETTLE"; window variant "$SEG"
cleanup
sleep "$SETTLE"; window default-2 "$SEG"
echo "$(date -u +%FT%TZ) done" >> "$OUT"
