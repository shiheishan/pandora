#!/usr/bin/env bash
# 健康巡检单元（pandora-health.service / .timer）的静态检查，不需要 root、systemd、网络：
#   - 单元在 systemd/ 下（与其它单元同处），路径是安装目录 /opt/pandora，timer 形状对；
#   - 以 root 跑但只读，唯一可写的是 healthcheck.sh 写 health.log 的日志目录；
#   - 脚本取安装根目录自己的位置，不写死；查的是新栈的两个服务。
# 进发布包、安装器装单元并 enable --now timer 的检查归安装侧的测试；脚本怎么查、怎么告警在 healthcheck_mock_test.sh。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'healthcheck install: %s\n' "$*" >&2; exit 1; }

svc="$DEPLOY/systemd/pandora-health.service"
tmr="$DEPLOY/systemd/pandora-health.timer"
[ -f "$svc" ] && [ -f "$tmr" ] || fail 'health units are not under systemd/'
[ ! -e "$DEPLOY/pandora-health.service" ] && [ ! -e "$DEPLOY/pandora-health.timer" ] || fail 'stale unit copies left directly under deploy/'
grep -qx 'Type=oneshot' "$svc" || fail 'service is not oneshot'
grep -qx 'User=root' "$svc" || fail 'service must run as root (it reads the root-only deploy.env and secrets.env)'
grep -qx 'ExecStart=/opt/pandora/deploy/healthcheck.sh' "$svc" || fail 'ExecStart drifted'
grep -qx 'WorkingDirectory=/opt/pandora' "$svc" || fail 'WorkingDirectory drifted'
grep -Eq '^TimeoutStartSec=' "$svc" || fail 'service has no start timeout'
for d in NoNewPrivileges=true PrivateTmp=true ProtectSystem=strict ProtectHome=true; do
  grep -qx "$d" "$svc" || fail "service lacks $d"
done
# ProtectSystem=strict 下唯一可写的是巡检日志目录，且与 healthcheck.sh 的 LOG_DIR 缺省值一致
log_dir="$(sed -n 's/^LOG_DIR="\${HEALTHCHECK_LOG_DIR:-\(.*\)}"$/\1/p' "$DEPLOY/healthcheck.sh")"
[ -n "$log_dir" ] || fail 'cannot read LOG_DIR from healthcheck.sh'
grep -qx "ReadWritePaths=-$log_dir" "$svc" || fail "ReadWritePaths is not exactly the log directory $log_dir"
grep -qx 'Unit=pandora-health.service' "$tmr" || fail 'timer does not start the health service'
grep -qx 'OnUnitActiveSec=10min' "$tmr" || fail 'timer is not every 10 minutes'
grep -qx 'OnActiveSec=10min' "$tmr" || fail 'timer must first fire 10 minutes after activation'
# OnBootSec 在 enable --now 时（开机早超过时限）会立刻触发，装完服务还没稳就跑一次
if grep -q '^OnBootSec=' "$tmr"; then fail 'OnBootSec would fire immediately on enable --now'; fi
grep -qx 'WantedBy=timers.target' "$tmr" || fail 'timer is not wanted by timers.target'
[ -x "$DEPLOY/healthcheck.sh" ] || fail 'healthcheck.sh is not executable'
# 脚本取安装根目录自己的位置，不写死
if grep -nE '^ROOT=/opt/' "$DEPLOY/healthcheck.sh"; then fail 'healthcheck.sh hard-codes the install root'; fi
grep -Fqx 'for s in pandora-serve pandora-worker; do' "$DEPLOY/healthcheck.sh" || fail 'healthcheck.sh does not check pandora-serve and pandora-worker'

printf 'healthcheck install static: PASS\n'
