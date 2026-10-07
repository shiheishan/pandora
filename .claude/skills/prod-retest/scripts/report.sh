#!/usr/bin/env bash
# 给拉回本机的一个场景生成成绩单的自动部分（在仓库根目录跑，需要本机 go 用于 pprof）：
#   report.sh <env.sh> <场景> [基线场景目录]
# 产物写在 $LOCAL_DIR/<场景>/：
#   auto-summary.md  runbook 9.2 及格线、cgroup、各进程、端点全表、pg_stat_activity、pg_stat_statements 前 10（summarize.py）
#   auto-targets.md  分档用户目标表 + 其他目标 + pprof CPU 前 10（targets.py targets）
#   auto-cpu.md      整机 CPU 拆分（cpu.py）
#   compare.md       与基线逐项对比（给了基线才有；两轮差别经 CMP_NOTE 环境变量写进表头）
# 结论、根因猜测与证据由人写进 summary.md，格式见 SKILL.md「成绩单」。
set -euo pipefail
ENV=${1:?env.sh}; S=${2:?场景名}; BASE=${3:-${BASELINE_DIR:-}}
# shellcheck disable=SC1090
set -a; . "$ENV"; set +a
HERE=$(cd "$(dirname "$0")" && pwd)
D="$LOCAL_DIR/$S"
python3 "$HERE/summarize.py" "$D" > "$D/auto-summary.md"
python3 "$HERE/targets.py" targets "$D" > "$D/auto-targets.md"
python3 "$HERE/cpu.py" "$D" > "$D/auto-cpu.md"
if [ -n "$BASE" ]; then python3 "$HERE/targets.py" compare "$D" "$BASE" > "$D/compare.md"; fi
# 自检：自动产物里不该有真实 IP 或后台前缀
for v in "${PANEL_IP:-}" "${LOADGEN_IP:-}" "$( [ -f "$ADMIN_PATH_FILE" ] && cat "$ADMIN_PATH_FILE")"; do
  [ -n "$v" ] && grep -lF "$v" "$D"/auto-*.md "$D"/compare.md 2>/dev/null && echo "警告：上面这些文件含真实值，打码后再外发" >&2
done
ls -la "$D"/auto-*.md "$D"/compare.md 2>/dev/null
