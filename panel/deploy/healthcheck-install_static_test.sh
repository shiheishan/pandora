#!/usr/bin/env bash
# 健康巡检（healthcheck.sh + aegis-health.service/timer）进发布包与两个安装器的检查，不需要 root、systemd、网络：
#   - 单元在 systemd/ 下（与其它单元同处，装单元的循环按这个路径取），路径与 timer 形状对；
#   - build-release.sh 的两处脚本清单、拷贝与归档清单都有它；install-linux-binaries.sh 的安装事务装它；
#   - install.sh / install-native.sh 在服务起来之后 enable --now timer（首装与升级都走），
#     install-native.sh 把单元里的 /opt/aegispanel 换成安装目录；
#   - 脚本取安装根目录自己的位置、两种布局都查得了库（docker 布局 psql.sh，直装布局 runuser）。
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
grep -qx 'ExecStart=/opt/aegispanel/deploy/healthcheck.sh' "$svc" || fail 'ExecStart drifted'
grep -qx 'WorkingDirectory=/opt/aegispanel' "$svc" || fail 'WorkingDirectory drifted'
grep -Eq '^TimeoutStartSec=' "$svc" || fail 'service has no start timeout'
grep -qx 'OnUnitActiveSec=10min' "$tmr" || fail 'timer is not every 10 minutes'
grep -qx 'OnActiveSec=10min' "$tmr" || fail 'timer must first fire 10 minutes after activation'
# OnBootSec 在 enable --now 时（开机早超过时限）会立刻触发，装完服务还没稳就跑一次
if grep -q '^OnBootSec=' "$tmr"; then fail 'OnBootSec would fire immediately on enable --now'; fi
grep -qx 'WantedBy=timers.target' "$tmr" || fail 'timer is not wanted by timers.target'
grep -Eq '^Unit=|^\[Timer\]' "$tmr" || fail 'timer has no [Timer] section'
[ -x "$DEPLOY/healthcheck.sh" ] || fail 'healthcheck.sh is not executable'

# --- 发布包 ---
build="$DEPLOY/build-release.sh"
[ "$(grep -c 'for script in .* healthcheck\.sh .*; do' "$build")" = 2 ] || fail 'build-release.sh lists healthcheck.sh in fewer than 2 script loops'
for u in aegis-health.service aegis-health.timer; do
  grep -Fq "cp \"\$ROOT/deploy/systemd/$u\" \"\$target/deploy/systemd/$u\"" "$build" || fail "build-release.sh does not copy $u"
  grep -Fq "\"\$target_base/deploy/systemd/$u\"" "$build" || fail "build-release.sh does not archive $u"
done

# --- install-linux-binaries.sh（install.sh 的安装事务） ---
inst="$DEPLOY/install-linux-binaries.sh"
grep -Eq '^for script in .*\bhealthcheck\.sh\b.*; do' "$inst" || fail 'install-linux-binaries.sh does not stage healthcheck.sh'
grep -Eq '^for unit in aegis-health\.service aegis-health\.timer; do' "$inst" || fail 'install-linux-binaries.sh does not stage the health units'

# --- install.sh：服务重启之后启用 timer ---
dock="$DEPLOY/install.sh"
grep -Fq 'systemctl enable --now aegis-health.timer' "$dock" || fail 'install.sh does not enable the health timer'
[ "$(line "$dock" 'systemctl enable --now aegis-health.timer')" -gt "$(line "$dock" 'systemctl restart "$s"')" ] \
  || fail 'install.sh enables the health timer before the gateways are restarted'

# --- install-native.sh ---
nat="$DEPLOY/install-native.sh"
grep -Fq 'install -m 0755 "$SCRIPT_DIR/healthcheck.sh" "$INSTALL_DIR/deploy/healthcheck.sh"' "$nat" || fail 'install-native.sh does not install healthcheck.sh'
grep -Fq 'for u in aegis-health.service aegis-health.timer; do' "$nat" || fail 'install-native.sh does not install the health units'
grep -Fq 'sed "s|/opt/aegispanel|${INSTALL_DIR}|g" "$SCRIPT_DIR/systemd/$u"' "$nat" || fail 'install-native.sh does not rewrite the install directory in the units'
grep -Fq 'systemctl enable --now aegis-health.timer' "$nat" || fail 'install-native.sh does not enable the health timer'
[ "$(line "$nat" 'systemctl enable --now aegis-health.timer')" -gt "$(line "$nat" 'systemctl start "$s"')" ] \
  || fail 'install-native.sh enables the health timer before the gateways are started'

# --- 脚本本身：布局无关 ---
if grep -nE '^ROOT=/opt/' "$DEPLOY/healthcheck.sh"; then fail 'healthcheck.sh hard-codes the install root'; fi
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
for layout in docker native; do
  mkdir -p "$T/$layout/deploy" "$T/$layout/bin"
  cp "$DEPLOY/healthcheck.sh" "$T/$layout/deploy/healthcheck.sh"
done
# docker 布局有 psql.sh（记下它收到的参数）；直装布局没有，靠 PATH 里的桩 runuser
printf '#!/usr/bin/env bash\nprintf "psql.sh %%s\\n" "$*" >>"$CALLS"\n' >"$T/docker/deploy/psql.sh"
printf '#!/usr/bin/env bash\nprintf "runuser %%s\\n" "$*" >>"$CALLS"\n' >"$T/native/bin/runuser"
chmod +x "$T/docker/deploy/psql.sh" "$T/native/bin/runuser"
export CALLS="$T/calls"
: >"$CALLS"
(
  HEALTHCHECK_LIB=1 . "$T/docker/deploy/healthcheck.sh"
  [ "$ROOT" = "$T/docker" ] || { echo "ROOT=$ROOT, want $T/docker" >&2; exit 1; }
  db_query 'SELECT 1'
) || fail 'docker layout: root or query path wrong'
grep -qx 'psql.sh -tAc SELECT 1' "$CALLS" || fail "docker layout did not go through psql.sh: $(cat "$CALLS")"
: >"$CALLS"
(
  PATH="$T/native/bin:$PATH"
  HEALTHCHECK_LIB=1 . "$T/native/deploy/healthcheck.sh"
  [ "$ROOT" = "$T/native" ] || { echo "ROOT=$ROOT, want $T/native" >&2; exit 1; }
  db_query 'SELECT 2'
) || fail 'native layout: root or query path wrong'
grep -qx 'runuser -u postgres -- psql -X -d aegis -tAc SELECT 2' "$CALLS" || fail "native layout did not query as postgres: $(cat "$CALLS")"

printf 'healthcheck install static: PASS\n'
