#!/usr/bin/env bash
# HTTPS 证书续期 timer 的静态检查：单元与 edge-tls.sh 之间的名字与路径对得上。
#   - timer 每天至少两次、错峰、补跑（6 天的 IP 证书过半就续，要给失败留重试次数）；
#   - service 以 edge-tls.sh renew 读 /etc/pandora/deploy.env，沙箱放开的可写目录正好覆盖它要写的 /etc 目录；
#   - edge-tls.sh setup 启用的就是这个 timer。
# 进发布包、安装器装单元的检查归安装侧的测试。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'tls-renew static: %s\n' "$*" >&2; exit 1; }
svc="$DEPLOY/systemd/pandora-tls-renew.service"
tmr="$DEPLOY/systemd/pandora-tls-renew.timer"
[ -f "$svc" ] && [ -f "$tmr" ] || fail 'renewal units missing'

# --- timer ---
grep -qx 'Unit=pandora-tls-renew.service' "$tmr" || fail 'timer does not start the renewal service'
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
grep -qx 'ExecStart=/opt/pandora/deploy/edge-tls.sh renew /etc/pandora/deploy.env' "$svc" || fail 'ExecStart drifted'
grep -qx 'WorkingDirectory=/opt/pandora/deploy' "$svc" || fail 'WorkingDirectory drifted'
grep -Eq '^TimeoutStartSec=' "$svc" || fail 'service has no start timeout (a hung ACME request would block every later run)'
for d in NoNewPrivileges=true PrivateTmp=true ProtectSystem=full ProtectHome=true; do
  grep -qx "$d" "$svc" || fail "service lacks $d"
done
# edge-tls.sh 的 deploy.env 缺省值与单元给的一致
grep -Fqx 'ENV_FILE="${2:-/etc/pandora/deploy.env}"' "$DEPLOY/edge-tls.sh" || fail 'edge-tls.sh reads a different deploy.env by default'
# ProtectSystem=full 让 /etc 只读：edge-tls.sh 写的 /etc 目录必须在 ReadWritePaths 里
rw="$(sed -n 's/^ReadWritePaths=//p' "$svc")"
tls_dir="$(sed -n 's/^TLS_DIR="\${PANDORA_TLS_DIR:-\(.*\)}"$/\1/p' "$DEPLOY/edge-tls.sh")"
le_dir="$(sed -n 's/^LE_LIVE_DIR="\${PANDORA_LE_LIVE_DIR:-\(.*\)}"$/\1/p' "$DEPLOY/edge-tls.sh")"
[ -n "$tls_dir" ] && [ -n "$le_dir" ] || fail 'cannot read the default paths from edge-tls.sh'
grep -Eq "(^| )-?$tls_dir( |$)" <<<"$rw" || fail "ReadWritePaths does not cover $tls_dir"
grep -Eq "(^| )-?$(dirname "$le_dir")( |$)" <<<"$rw" || fail "ReadWritePaths does not cover $(dirname "$le_dir")"
# 写在 /var 下的状态、校验与备份目录不受 ProtectSystem=full 限制；/usr 与 /home 不放开
if grep -Eq '(^| )-?/(usr|home|root)' <<<"$rw"; then fail "ReadWritePaths opens too much: $rw"; fi

# --- 名字对得上 ---
grep -Fqx 'RENEW_TIMER=pandora-tls-renew.timer' "$DEPLOY/edge-tls.sh" || fail 'edge-tls.sh enables a different timer'

printf 'tls-renew static: PASS\n'
