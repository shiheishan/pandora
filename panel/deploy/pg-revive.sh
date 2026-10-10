#!/usr/bin/env bash
# postgresql@<版本>-<集群> 的 ExecStopPost（install.sh 写进 PostgreSQL 的 drop-in，以「+」即 root、不受沙箱限制跑）：
# postmaster 被杀（kill -9、OOM 杀到它本身）后约 5 秒自动拉起；有意停库不拉。
#
# 怎么分辨：postmaster 正常关库时，无论 fast、smart、immediate，也无论是 pg_ctlcluster stop 还是 systemctl stop，
# 都会删掉数据目录里的 postmaster.pid；只有它被杀或崩溃退出时才留下。所以只在「pid 文件还在、里面的 PID 已不是
# 活着的 postgres」时拉起。不能用 systemd 的 Restart=：这个 Type=forking 单元里 postmaster 被 kill -9 时 systemd
# 记为正常退出（on-abnormal 拉不起）；on-failure 又会因为 ExecStop 在库已停时报错，把有意停掉的库拉回来（panel2 实测）。
#
# 拉起用 systemd-run 排一个 5 秒后的一次性 start：ExecStopPost 在单元停止过程里跑，不能同步 start 自己。
# 60 秒内拉起过一次就不再拉（反复崩溃时不一直拉，留给巡检与人），都记进 journal（logger -t pandora-pg-revive）。
#   pg-revive.sh <实例，如 18-main>
# 测试覆盖：PANDORA_PG_DATA_ROOT、PANDORA_PROC_ROOT、PANDORA_REVIVE_STAMP（pg-revive_mock_test.sh）
set -u
inst="${1:-}"
[[ "$inst" =~ ^([0-9]+)-([a-z0-9_]+)$ ]] || exit 0
ver="${BASH_REMATCH[1]}" cluster="${BASH_REMATCH[2]}"
data_root="${PANDORA_PG_DATA_ROOT:-/var/lib/postgresql}"
proc_root="${PANDORA_PROC_ROOT:-/proc}"
stamp="${PANDORA_REVIVE_STAMP:-/run/pandora-pg-revive-$inst.stamp}"
pidfile="$data_root/$ver/$cluster/postmaster.pid"
log() { logger -t pandora-pg-revive -- "$*" 2>/dev/null || true; }

[ -f "$pidfile" ] || exit 0                       # 正常停库：pid 文件已删
pid="$(head -n 1 -- "$pidfile" 2>/dev/null | tr -d '[:space:]')"
[[ "$pid" =~ ^[0-9]+$ ]] || exit 0
# PID 还是活着的 postgres：不是被杀（比如停止还没走完），不动
[ "$(cat "$proc_root/$pid/comm" 2>/dev/null)" = postgres ] && exit 0
# 60 秒内拉起过：不再拉
last="$(stat -c %Y -- "$stamp" 2>/dev/null || stat -f %m -- "$stamp" 2>/dev/null || echo 0)"
if [ -f "$stamp" ] && [ $(( $(date +%s) - last )) -lt 60 ]; then
  log "postmaster $pid of $inst is gone again within 60 seconds; not reviving again (check journalctl -u postgresql@$inst)"
  exit 0
fi
: >"$stamp" 2>/dev/null || true
log "postmaster $pid of $inst is gone but $pidfile is still there (killed, not stopped): starting postgresql@$inst in 5 seconds"
systemd-run --quiet --collect --on-active=5s --unit="pandora-pg-revive-$inst" \
  /usr/bin/systemctl start "postgresql@$inst.service" >/dev/null 2>&1 \
  || log "scheduling the restart of postgresql@$inst failed"
exit 0
