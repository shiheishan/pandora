#!/usr/bin/env bash
# 把 perf-gate 要用的脚本推上两端（在仓库根目录跑）：push.sh <env.sh>
#   先调 prod-retest 的 push-scripts.sh（仓库 loadtest/scripts 与 prod-retest 采集脚本、压测机的 0600 地址口令文件），
#   再推本 skill 的 gate-collect.sh、memdetail.sh、toggle-ab.sh 到面板机 /root/lt/，run-quiet.sh 到压测机 /root/。
# env.sh 的格式同 prod-retest（scripts/env.example.sh）。改前改后两个版本都用同一份脚本，推一次即可。
set -euo pipefail
ENV=${1:?env.sh}
# shellcheck disable=SC1090
. "$ENV"
HERE=$(cd "$(dirname "$0")" && pwd)
bash "$HERE/../../prod-retest/scripts/push-scripts.sh" "$ENV"
scp -q "$HERE"/{gate-collect.sh,memdetail.sh,toggle-ab.sh} "$PANEL_HOST:/root/lt/"
ssh -n "$PANEL_HOST" 'chmod 700 /root/lt/gate-collect.sh /root/lt/memdetail.sh /root/lt/toggle-ab.sh'
scp -q panel/tools/loadtest/scripts/run-quiet.sh "$LOADGEN_HOST:/root/"
ssh -n "$LOADGEN_HOST" 'chmod 700 /root/run-quiet.sh; test -x /root/loadtest || echo "压测机上还没有 /root/loadtest（见 prod-retest「准备」）" >&2'
