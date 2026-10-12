#!/usr/bin/env bash
# 把两端要用的脚本与机器上的 0600 秘密文件推上去（在仓库根目录跑）：push-scripts.sh <env.sh>
#   面板机 /root/lt/：仓库 panel/tools/loadtest/scripts/* + 本 skill 的 run-collect、sample-vmswap、sample-pgact、sample-cpustat、trim-access
#   压测机 /root/run-load.sh、/root/run-split.sh；/root/.lt-panel-url、/root/.lt-admin-url、/root/.lt-admin-pw（0600，值从 ops-local 读，不回显）
# loadtest 二进制不在这里：两端各自用同一份源码 go build（见 SKILL.md「准备」）。
set -euo pipefail
ENV=${1:?env.sh}
# shellcheck disable=SC1090
. "$ENV"
HERE=$(cd "$(dirname "$0")" && pwd)
REPO_SCRIPTS=panel/tools/loadtest/scripts
[ -d "$REPO_SCRIPTS" ] || { echo "请在仓库根目录跑" >&2; exit 1; }
ssh -n "$PANEL_HOST" 'install -d -m 0700 /root/lt /root/lt-results'
scp -q "$REPO_SCRIPTS"/* "$HERE"/{run-collect.sh,sample-vmswap.sh,sample-pgact.sh,sample-cpustat.sh,trim-access.sh} "$PANEL_HOST:/root/lt/"
ssh -n "$PANEL_HOST" 'chmod 700 /root/lt/*.sh; ls /root/lt'
ssh -n "$LOADGEN_HOST" 'install -d -m 0700 /root/lt-results'
scp -q "$HERE"/{run-load.sh,run-split.sh} "$LOADGEN_HOST:/root/"
ssh -n "$LOADGEN_HOST" 'chmod 700 /root/run-load.sh /root/run-split.sh'
# 秘密只经 stdin 写到对端文件，不出现在命令行参数里
printf 'https://%s' "$PANEL_DOMAIN" | ssh "$LOADGEN_HOST" 'umask 077; cat > /root/.lt-panel-url'
printf 'https://%s/%s' "$PANEL_DOMAIN" "$(cat "$ADMIN_PATH_FILE")" | ssh "$LOADGEN_HOST" 'umask 077; cat > /root/.lt-admin-url'
sed -n 2p "$ADMIN_CRED_FILE" | tr -d '\n' | ssh "$LOADGEN_HOST" 'umask 077; cat > /root/.lt-admin-pw'
ssh -n "$LOADGEN_HOST" 'stat -c "%a %s %n" /root/.lt-panel-url /root/.lt-admin-url /root/.lt-admin-pw'
