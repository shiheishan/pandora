#!/usr/bin/env bash
# 压测机侧时间轴（runbook 第 8 节），在压测机上以 root 跑。
# 用法：run-load.sh <场景> <manifest> <T unix 秒> [users 时长=30m] [nodes 时长=37m] [burst 次数=1] [burst 间隔]
#   起跑即开 nodes（应在 T−5m 左右起，60 秒内错开）→ T 起 users → T+20m burst（多次时按间隔）
# 机器上要先有（setup 写入，0600）：/root/loadtest、/root/.lt-panel-url（https://<面板域名>）、
#   /root/.lt-admin-url（https://<面板域名>/<后台前缀>）、/root/.lt-admin-pw；邮箱默认 ltadmin@example.com。
# 可调环境变量：NB（节点代际，缺省 current）、SD（稳态长度，缺省 30m）、SUBI（每人订阅间隔，缺省 30m）、
#   PORTAL_RATE / ADMIN_RATE / LOGIN_RATE / PORTAL_USERS（缺省 5 / 0.5 / 0.05 / 200，与 2026-10 各轮一致，改了就没法和旧轮对比）。
set -uo pipefail
S=${1:?场景名} M=${2:?manifest} T=${3:?T unix 秒} UD=${4:-30m} ND=${5:-37m} BC=${6:-1} BI=${7:-}
R=/root/lt-results/$S; mkdir -p "$R"
DOM=${PANEL_URL:-$(cat /root/.lt-panel-url)}; ADM=$(cat /root/.lt-admin-url)
export LOADTEST_ADMIN_EMAIL=${ADMIN_EMAIL:-ltadmin@example.com} LOADTEST_ADMIN_PASSWORD=$(cat /root/.lt-admin-pw)
log() { echo "$(date -u +%FT%TZ) $*" >> "$R/timeline.txt"; }
at() { local w=$(( $1 - $(date +%s) )); (( w > 0 )) && sleep "$w"; }
log "start; T=$(date -u -d @"$T" +%FT%TZ)"
/root/loadtest nodes -manifest "$M" -node-url "$DOM" -node-behavior "${NB:-current}" -stagger 60s -duration "$ND" \
  -steady-start "$T" -steady-dur "${SD:-30m}" -out "$R" -strict > "$R/nodes.log" 2>&1 & NP=$!
log "nodes started pid $NP"
at "$T"
/root/loadtest users -manifest "$M" -public-url "$DOM" -admin-url "$ADM" -duration "$UD" -sub-interval "${SUBI:-30m}" \
  -portal-rate "${PORTAL_RATE:-5}" -admin-rate "${ADMIN_RATE:-0.5}" -login-rate "${LOGIN_RATE:-0.05}" -portal-users "${PORTAL_USERS:-200}" \
  -steady-start "$T" -steady-dur "${SD:-30m}" -out "$R" -strict > "$R/users.log" 2>&1 & UP=$!
log "T users started pid $UP"
at $((T+1200))
if [ -n "$BI" ]; then burst_args=(-count "$BC" -interval "$BI"); else burst_args=(-count "$BC"); fi
/root/loadtest burst -manifest "$M" -admin-url "$ADM" "${burst_args[@]}" -out "$R" > "$R/burst.log" 2>&1; log "T+20m burst rc=$?"
wait $UP; log "users rc=$?"
wait $NP; log "nodes rc=$?"
# 日志里的后台地址打码（含秘密前缀）
sed -i "s#$ADM#<ADMIN_URL>#g" "$R"/*.log 2>/dev/null
log done
