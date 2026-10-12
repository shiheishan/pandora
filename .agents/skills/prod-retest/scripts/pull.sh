#!/usr/bin/env bash
# 把一个场景两端的结果目录原样打包拉回本机，并解到 $LOCAL_DIR/<场景>/{panel,loadgen}/：pull.sh <env.sh> <场景>
# 两台机器上的结果目录不删；tgz 留在两端 /root/lt-results/ 与本机场景目录各一份。
set -euo pipefail
ENV=${1:?env.sh}; S=${2:?场景名}
# shellcheck disable=SC1090
. "$ENV"
D="$LOCAL_DIR/$S"; umask 077; mkdir -p "$D/panel" "$D/loadgen"
ssh -n -o BatchMode=yes "$PANEL_HOST" "grep -q done /root/lt-results/$S/timeline.txt" || { echo "面板机采集还没 done" >&2; exit 1; }
ssh -n -o BatchMode=yes "$LOADGEN_HOST" "grep -q ' done' /root/lt-results/$S/timeline.txt" || { echo "压测机还没 done" >&2; exit 1; }
ssh -n "$PANEL_HOST" "tar czf /root/lt-results/$S-panel.tgz -C /root/lt-results/$S ."
ssh -n "$LOADGEN_HOST" "tar czf /root/lt-results/$S-loadgen.tgz -C /root/lt-results/$S ."
scp -q "$PANEL_HOST:/root/lt-results/$S-panel.tgz" "$LOADGEN_HOST:/root/lt-results/$S-loadgen.tgz" "$D/" 2>/dev/null || {
  scp -q "$PANEL_HOST:/root/lt-results/$S-panel.tgz" "$D/"; scp -q "$LOADGEN_HOST:/root/lt-results/$S-loadgen.tgz" "$D/"; }
tar xzf "$D/$S-panel.tgz" -C "$D/panel"; tar xzf "$D/$S-loadgen.tgz" -C "$D/loadgen"
# 自检：后台前缀不该出现在任何拉回的文件里
if [ -f "$ADMIN_PATH_FILE" ] && grep -rqF "$(cat "$ADMIN_PATH_FILE")" "$D/panel" "$D/loadgen"; then
  echo "警告：拉回的文件里出现了后台前缀，先打码再往外发" >&2
fi
du -sh "$D"/*
