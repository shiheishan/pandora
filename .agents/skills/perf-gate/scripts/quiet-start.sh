#!/usr/bin/env bash
# 本机同时起一场静默（面板机 gate-collect + 压测机 1000 节点），共用一个 T：
#   quiet-start.sh <env.sh> <场景> <压测机上的 manifest 路径> <A|B> [--diag]
# A 档 ONLINE_RATIO=0，B 档 0.3。起之前做测前清理：压测机上不能还有 loadtest 在跑（上一场的节点会叠加），
# 面板机 swapoff -a && swapon -a（swap 已用超过 MemAvailable 的一半时拒绝，交人处理）。
# T = 现在 + 9 分钟（节点 T−8m 起跑、180 秒错开）；结果在两端 /root/lt-results/<场景>/，T+17m 后
# 用 prod-retest 的 pull.sh <env.sh> <场景> 拉回。远端起后台一律 setsid -f，写法见根 CLAUDE.md「环境与工具坑」。
set -euo pipefail
ENV=${1:?env.sh}; S=${2:?场景名}; M=${3:?manifest}; TIER=${4:?A 或 B}; DIAG=${5:-}
case "$TIER" in A) RATIO=0 ;; B) RATIO=0.3 ;; *) echo "档位只能是 A 或 B" >&2; exit 2 ;; esac
[[ -z "$DIAG" || "$DIAG" == --diag ]] || { echo "第 5 个参数只能是 --diag" >&2; exit 2; }
# shellcheck disable=SC1090
. "$ENV"
for h in "$PANEL_HOST" "$LOADGEN_HOST"; do
  if ssh -n -o BatchMode=yes "$h" "test -e /root/lt-results/$S"; then echo "$h 上已有 /root/lt-results/$S，换个场景名" >&2; exit 1; fi
done
ssh -n -o BatchMode=yes "$LOADGEN_HOST" "test -s $M" || { echo "压测机上没有 $M" >&2; exit 1; }
if ssh -n -o BatchMode=yes "$LOADGEN_HOST" 'pgrep -x loadtest >/dev/null'; then
  echo "压测机上还有 loadtest 在跑（上一场没结束），等它退出再起" >&2; exit 1
fi
echo "== 面板机测前清理 swap"
ssh -n -o BatchMode=yes "$PANEL_HOST" '
  used=$(awk "\$1==\"SwapTotal:\"{t=\$2} \$1==\"SwapFree:\"{f=\$2} END{print t-f}" /proc/meminfo)
  avail=$(awk "\$1==\"MemAvailable:\"{print \$2}" /proc/meminfo)
  if [ "$used" -gt 0 ] && [ "$used" -ge $((avail / 2)) ]; then echo "swap 已用 ${used} kB，MemAvailable ${avail} kB，不敢 swapoff，交人处理" >&2; exit 1; fi
  swapoff -a && swapon -a && echo "swap 已清（原已用 ${used} kB）"'
T=$(( $(date +%s) + 540 ))
echo "T=$T ($(date -u -r "$T" +%FT%TZ 2>/dev/null || date -u -d @"$T" +%FT%TZ)) 档位 $TIER online_ratio=$RATIO ${DIAG:-正式轮}"
mkdir -p "$LOCAL_DIR"; echo "$T $TIER ${DIAG:-formal}" > "$LOCAL_DIR/$S.T"
ssh -n -o BatchMode=yes "$PANEL_HOST" "mkdir -p /root/lt-results; cd /root/lt; setsid -f ./gate-collect.sh /root/lt-results/$S $T 15 $DIAG > /root/lt-results/gate-$S.out 2>&1 < /dev/null; sleep 2; cat /root/lt-results/$S/gate-timeline.txt"
ssh -n -o BatchMode=yes "$LOADGEN_HOST" "mkdir -p /root/lt-results; cd /root; ONLINE_RATIO=$RATIO setsid -f ./run-quiet.sh $M \"\$(cat /root/.lt-panel-url)\" $T 1000 /root/lt-results/$S > /root/lt-results/run-quiet-$S.out 2>&1 < /dev/null; sleep 2; ls /root/lt-results/$S"
echo "T+17m 后：bash .claude/skills/prod-retest/scripts/pull.sh $ENV $S"
