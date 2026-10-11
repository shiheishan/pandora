#!/usr/bin/env bash
# HTTPS 证书续期 timer 的静态检查：单元、发布包、安装器、edge-tls.sh 之间的名字与路径对得上。
#   - timer 每天至少两次、错峰、补跑（6 天的 IP 证书过半就续，要给失败留重试次数）；
#   - service 以 edge-tls.sh renew 读 /opt/pandora/deploy/.env，沙箱放开的可写目录正好覆盖它要写的；
#   - 两个单元进 build-release.sh 的拷贝与归档清单，install.sh 随 UNITS 原样装上；
#     edge-tls.sh setup 启用的就是这个 timer。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'tls-renew static: %s\n' "$*" >&2; exit 1; }
svc="$DEPLOY/systemd/aegis-tls-renew.service"
tmr="$DEPLOY/systemd/aegis-tls-renew.timer"
[ -f "$svc" ] && [ -f "$tmr" ] || fail 'renewal units missing'

# --- timer ---
grep -qx 'Unit=aegis-tls-renew.service' "$tmr" || fail 'timer does not start the renewal service'
grep -qx 'Persistent=true' "$tmr" || fail 'timer does not catch up after downtime'
grep -Eqx 'RandomizedDelaySec=[0-9]+(h|min|m|s)?' "$tmr" || fail 'timer has no randomized delay (Let'\''s Encrypt asks clients to spread load)'
grep -qx 'WantedBy=timers.target' "$tmr" || fail 'timer is not wanted by timers.target'
cal="$(sed -n 's/^OnCalendar=//p' "$tmr")"
[ -n "$cal" ] || fail 'timer has no OnCalendar'
hours="$(sed -E 's/^\*-\*-\* ([0-9,]+):.*/\1/' <<<"$cal")"
[ "$hours" != "$cal" ] || fail "unexpected OnCalendar form: $cal"
[ "$(tr ',' '\n' <<<"$hours" | grep -c .)" -ge 2 ] || fail "timer runs fewer than twice a day: $cal"

# --- service ---
grep -qx 'Type=oneshot' "$svc" || fail 'service is not oneshot'
grep -qx 'ExecStart=/opt/pandora/deploy/edge-tls.sh renew /opt/pandora/deploy/.env' "$svc" || fail 'ExecStart drifted'
grep -qx 'WorkingDirectory=/opt/pandora/deploy' "$svc" || fail 'WorkingDirectory drifted'
grep -Eq '^TimeoutStartSec=' "$svc" || fail 'service has no start timeout (a hung ACME request would block every later run)'
for d in NoNewPrivileges=true PrivateTmp=true ProtectSystem=full ProtectHome=true; do
  grep -qx "$d" "$svc" || fail "service lacks $d"
done
# ProtectSystem=full 让 /etc 只读：edge-tls.sh 写的 /etc 目录必须在 ReadWritePaths 里
rw="$(sed -n 's/^ReadWritePaths=//p' "$svc")"
tls_dir="$(sed -n 's/^TLS_DIR="\${PANDORA_TLS_DIR:-\(.*\)}"$/\1/p' "$DEPLOY/edge-tls.sh")"
le_dir="$(sed -n 's/^LE_LIVE_DIR="\${PANDORA_LE_LIVE_DIR:-\(.*\)}"$/\1/p' "$DEPLOY/edge-tls.sh")"
[ -n "$tls_dir" ] && [ -n "$le_dir" ] || fail 'cannot read the default paths from edge-tls.sh'
grep -Eq "(^| )-?$tls_dir( |$)" <<<"$rw" || fail "ReadWritePaths does not cover $tls_dir"
grep -Eq "(^| )-?$(dirname "$le_dir")( |$)" <<<"$rw" || fail "ReadWritePaths does not cover $(dirname "$le_dir")"
# 写在 /var 下的状态与校验目录不受 ProtectSystem=full 限制；/usr 与 /home 不放开
if grep -Eq '(^| )-?/(usr|home|root)' <<<"$rw"; then fail "ReadWritePaths opens too much: $rw"; fi

# --- 名字对得上 ---
grep -Fq 'RENEW_TIMER=aegis-tls-renew.timer' "$DEPLOY/edge-tls.sh" || fail 'edge-tls.sh enables a different timer'
build="$DEPLOY/build-release.sh"
[ "$(grep -c 'for script in .* edge-tls\.sh .*; do' "$build")" = 2 ] || fail 'build-release.sh lists edge-tls.sh in fewer than 2 script loops'
for u in aegis-tls-renew.service aegis-tls-renew.timer; do
  grep -Fq "cp \"\$ROOT/deploy/systemd/$u\" \"\$target/deploy/systemd/$u\"" "$build" || fail "build-release.sh does not copy $u"
  grep -Fq "\"\$target_base/deploy/systemd/$u\"" "$build" || fail "build-release.sh does not archive $u"
done
inst="$DEPLOY/install.sh"
grep -Fq '"$SCRIPT_DIR/edge-tls.sh"' "$inst" || fail 'install.sh does not install edge-tls.sh'
for u in aegis-tls-renew.service aegis-tls-renew.timer; do
  bash -c '. "$1"; printf "%s\n" "${UNITS[@]}"' _ "$DEPLOY/install-lib.sh" | grep -qx "$u" || fail "install.sh does not install $u"
done
grep -Fq 'install -m 0644 "$SCRIPT_DIR/systemd/$u" "/etc/systemd/system/$u"' "$inst" || fail 'install.sh does not install the units as shipped'

printf 'tls-renew static: PASS\n'
