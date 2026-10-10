#!/usr/bin/env bash
# postgresql@<版本>-<集群> 的 ExecStopPost（install.sh 写进 PostgreSQL 的 drop-in，以「+」即 root、不受沙箱限制跑）：
# postmaster 被信号杀掉（kill -9、OOM 杀到它本身、崩溃）后约 5 秒自动拉起；有意停库不拉。
#
# 怎么分辨：systemd 把主进程（postmaster）怎么结束的交给 ExecStopPost：EXIT_CODE=killed / dumped 是被信号杀掉，
# exited 是它自己退出。有意停库（pg_ctlcluster stop 的 smart、fast、immediate，systemctl stop）postmaster 都是自己
# 退出，exited；只有 SIGKILL 与崩溃信号才是 killed / dumped（SIGTERM、SIGINT、SIGQUIT 是 PostgreSQL 的三种关库请求，
# 它会处理后自己退出）。panel2 实测四例见 pg-revive_mock_test.sh 开头。不能看 postmaster.pid：ExecStopPost 之前
# ExecStop（pg_ctlcluster stop）已把被杀后残留的 pid 文件删了。也不能用 systemd 的 Restart=：被杀之后 ExecStop 报
# 「Cluster is not running」，单元结果是 exit-code，on-abnormal 不拉；on-failure 又会把 postgres 用户有意停的库拉回来。
#
# 本单元有 systemd 作业在跑（systemctl stop / restart；停库超时被 systemd 杀掉也是 killed）时不拉。
# 拉起用 systemd-run 排一个 5 秒后的一次性 start：ExecStopPost 在单元停止过程里跑，不能同步 start 自己。
# 60 秒内拉起过一次就不再拉（反复崩溃时不一直拉，留给巡检与人），都记进 journal（logger -t pandora-pg-revive）。
#   pg-revive.sh <实例，如 18-main>
# 测试覆盖：PANDORA_REVIVE_STAMP（pg-revive_mock_test.sh）
set -u
inst="${1:-}"
[[ "$inst" =~ ^[0-9]+-[a-z0-9_]+$ ]] || exit 0
unit="postgresql@$inst.service"
stamp="${PANDORA_REVIVE_STAMP:-/run/pandora-pg-revive-$inst.stamp}"
log() { logger -t pandora-pg-revive -- "$*" 2>/dev/null || true; }

case "${EXIT_CODE:-}" in
  killed|dumped) ;;
  *) exit 0 ;;                                    # postmaster 自己退出：有意停库
esac
if ! jobs="$(systemctl list-jobs --no-legend --no-pager 2>/dev/null)"; then
  log "postmaster of $inst was killed (${EXIT_STATUS:-?}) but the systemd job list could not be read; not reviving"
  exit 0
fi
if awk -v u="$unit" '$2 == u { f = 1 } END { exit !f }' <<<"$jobs"; then
  exit 0                                          # systemctl stop / restart 正在处理这个单元
fi
# 60 秒内拉起过：不再拉。差值为负（时钟往回跳过，stamp 在将来）不算，否则会一直不拉
last="$(stat -c %Y -- "$stamp" 2>/dev/null || stat -f %m -- "$stamp" 2>/dev/null || echo 0)"
diff=$(( $(date +%s) - last ))
if [ -f "$stamp" ] && [ "$diff" -ge 0 ] && [ "$diff" -lt 60 ]; then
  log "postmaster of $inst was killed (${EXIT_STATUS:-?}) again within 60 seconds; not reviving again (check journalctl -u $unit)"
  exit 0
fi
: >"$stamp" 2>/dev/null || true
log "postmaster of $inst was killed (${EXIT_STATUS:-?}), not stopped: starting $unit in 5 seconds"
systemd-run --quiet --collect --on-active=5s --unit="pandora-pg-revive-$inst" \
  /usr/bin/systemctl start "$unit" >/dev/null 2>&1 \
  || log "scheduling the restart of $unit failed"
exit 0
