#!/usr/bin/env bash
# 静默场景的压测机一侧：只起模拟节点，不起 users、不做 burst。
#   run-quiet.sh <manifest> <node-url> <T unix 秒> [节点数，缺省 0 = 清单全部] [结果目录，缺省 ~/lt-results/quiet]
#
# 时间线（T 为面板机 quiet-collect.sh 的窗口起点，两边约定同一个 T）：
#   T−LEAD  节点起跑，在 STAGGER 内随机错开，之后进入稳态
#   T       稳态窗口开始，量 15 分钟
#   T+15m   窗口结束；节点再多跑一小段后随 -duration 结束
# 环境变量（可选）：LT_BIN（loadtest 路径，缺省 ./loadtest）、STAGGER、LEAD、WINDOW_MIN、LT_BEHAVIOR。
#
# 1000 节点时每个节点要拉两份全量名单（REST 一份、事件流一份），起跑段比 200 节点重得多：缺省错开 180 秒、
# 提前 8 分钟起跑，保证 T 到来时 1000 个节点都已进入稳态（进度行 started=N 要等于节点数，不等就推迟 T）。
# 节拍与 pdnd 当前代码一致（-node-behavior current）；在线比例保持缺省 0.3（与 5k-r4 静默同口径，
# 每个节点仍有少量 push / alive）；要测「节点上报完全空转」另加 -online-ratio 0，那是另一套口径，不能和旧轮直接比。
set -euo pipefail
M="${1:?manifest}"; URL="${2:?node-url}"; T="${3:?T unix 秒}"; N="${4:-0}"; R="${5:-$HOME/lt-results/quiet}"
LT_BIN="${LT_BIN:-./loadtest}"; STAGGER="${STAGGER:-180s}"; LEAD="${LEAD:-480}"; WINDOW_MIN="${WINDOW_MIN:-15}"
BEHAVIOR="${LT_BEHAVIOR:-current}"
mkdir -p "$R"
log() { echo "$(date -u +%FT%TZ) $*" >> "$R/timeline.txt"; }

now="$(date +%s)"
start=$(( T - LEAD ))
(( start > now )) && sleep $(( start - now ))
# 起跑到窗口结束，再留 2 分钟收尾
dur=$(( LEAD + WINDOW_MIN * 60 + 120 ))
log "start nodes=$N T=$(date -u -d @"$T" +%FT%TZ) stagger=$STAGGER duration=${dur}s"
set +e
"$LT_BIN" nodes -manifest "$M" -node-url "$URL" -nodes "$N" -node-behavior "$BEHAVIOR" -stagger "$STAGGER" \
  -duration "${dur}s" -steady-start "$T" -steady-dur "$(( WINDOW_MIN * 60 ))s" -progress 30s -out "$R" -strict > "$R/nodes.log" 2>&1
rc=$?
set -e
log "nodes rc=$rc"
exit "$rc"
