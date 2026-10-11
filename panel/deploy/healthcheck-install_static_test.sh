#!/usr/bin/env bash
# 健康巡检（healthcheck.sh + aegis-health.service/timer）进发布包与安装器的检查，不需要 root、systemd、网络：
#   - 单元在 systemd/ 下（与其它单元同处，装单元的循环按这个路径取），路径是安装目录 /opt/pandora，timer 形状对；
#   - build-release.sh 的两处脚本清单、拷贝与归档清单都有它；
#   - install.sh 装脚本、随 UNITS 原样装单元，在服务起来之后 enable --now timer（首装与升级都走）。
# 脚本本身怎么查库、怎么告警在 healthcheck_mock_test.sh。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'healthcheck install: %s\n' "$*" >&2; exit 1; }
line() { grep -nF -- "$2" "$1" | head -1 | cut -d: -f1; }

# --- 单元 ---
svc="$DEPLOY/systemd/aegis-health.service"
tmr="$DEPLOY/systemd/aegis-health.timer"
[ -f "$svc" ] && [ -f "$tmr" ] || fail 'health units are not under systemd/'
[ ! -e "$DEPLOY/aegis-health.service" ] && [ ! -e "$DEPLOY/aegis-health.timer" ] || fail 'stale unit copies left directly under deploy/'
grep -qx 'Type=oneshot' "$svc" || fail 'service is not oneshot'
grep -qx 'ExecStart=/opt/pandora/deploy/healthcheck.sh' "$svc" || fail 'ExecStart drifted'
grep -qx 'WorkingDirectory=/opt/pandora' "$svc" || fail 'WorkingDirectory drifted'
grep -Eq '^TimeoutStartSec=' "$svc" || fail 'service has no start timeout'
grep -qx 'OnUnitActiveSec=10min' "$tmr" || fail 'timer is not every 10 minutes'
grep -qx 'OnActiveSec=10min' "$tmr" || fail 'timer must first fire 10 minutes after activation'
# OnBootSec 在 enable --now 时（开机早超过时限）会立刻触发，装完服务还没稳就跑一次
if grep -q '^OnBootSec=' "$tmr"; then fail 'OnBootSec would fire immediately on enable --now'; fi
grep -qx 'WantedBy=timers.target' "$tmr" || fail 'timer is not wanted by timers.target'
grep -Eq '^Unit=|^\[Timer\]' "$tmr" || fail 'timer has no [Timer] section'
[ -x "$DEPLOY/healthcheck.sh" ] || fail 'healthcheck.sh is not executable'
# 脚本取安装根目录自己的位置，不写死
if grep -nE '^ROOT=/opt/' "$DEPLOY/healthcheck.sh"; then fail 'healthcheck.sh hard-codes the install root'; fi

# --- 发布包 ---
build="$DEPLOY/build-release.sh"
[ "$(grep -c 'for script in .* healthcheck\.sh .*; do' "$build")" = 2 ] || fail 'build-release.sh lists healthcheck.sh in fewer than 2 script loops'
for u in aegis-health.service aegis-health.timer; do
  grep -Fq "cp \"\$ROOT/deploy/systemd/$u\" \"\$target/deploy/systemd/$u\"" "$build" || fail "build-release.sh does not copy $u"
  grep -Fq "\"\$target_base/deploy/systemd/$u\"" "$build" || fail "build-release.sh does not archive $u"
done

# --- install.sh ---
inst="$DEPLOY/install.sh"
lib="$DEPLOY/install-lib.sh"
grep -Fq 'install -m 0755 "$SCRIPT_DIR/healthcheck.sh" "$INSTALL_DIR/deploy/healthcheck.sh"' "$inst" || fail 'install.sh does not install healthcheck.sh'
for u in aegis-health.service aegis-health.timer; do
  bash -c '. "$1"; printf "%s\n" "${UNITS[@]}"' _ "$lib" | grep -qx "$u" || fail "UNITS does not install $u"
done
grep -Fq 'for u in "${UNITS[@]}"; do' "$inst" || fail 'install.sh does not install the units in UNITS'
grep -Fq 'systemctl enable --now aegis-health.timer' "$inst" || fail 'install.sh does not enable the health timer'
[ "$(line "$inst" 'systemctl enable --now aegis-health.timer')" -gt "$(line "$inst" 'systemctl start "$s"')" ] \
  || fail 'install.sh enables the health timer before the gateways are started'

printf 'healthcheck install static: PASS\n'
