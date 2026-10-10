#!/usr/bin/env bash
# pg-revive.sh（postgresql@18-main 的 ExecStopPost）：postmaster 被杀才拉起，有意停库不拉起。
#   ① 数据目录里没有 postmaster.pid（pg_ctlcluster stop、systemctl stop、immediate 关库都会删掉它）：什么都不做；
#   ② postmaster.pid 在、里面的 PID 还是活着的 postgres：什么都不做；
#   ③ postmaster.pid 在、PID 已不在（或被别的程序占了）：用 systemd-run 排 5 秒后 start postgresql@<实例>；
#   ④ 60 秒内已拉起过一次：不再拉，只记日志（防反复崩溃时一直拉）；
#   ⑤ 实例名不合法、pid 文件内容不是数字：什么都不做；
#   ⑥ 静态：PG drop-in 只有这一条 ExecStopPost（以「+」root 跑），发布与安装都带上这个脚本。
# 真机三例（kill -9 被拉起、postgres 用户 pg_ctlcluster stop 不拉、systemctl stop 不拉）在 panel2 实测，见报告。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$DEPLOY/pg-revive.sh"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-pg-revive.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'pg-revive: %s\n' "$*" >&2; exit 1; }
[ -f "$SCRIPT" ] || fail 'deploy/pg-revive.sh is missing'

mkdir -p "$T/bin" "$T/data/18/main" "$T/proc"
for c in systemd-run logger; do
  printf '#!/usr/bin/env bash\nprintf "%s %%s\\n" "$*" >>"%s/calls"\n' "$c" "$T" >"$T/bin/$c"
done
chmod 0755 "$T/bin/"*
run() {
  : >"$T/calls"
  PATH="$T/bin:$PATH" PANDORA_PG_DATA_ROOT="$T/data" PANDORA_PROC_ROOT="$T/proc" PANDORA_REVIVE_STAMP="$T/stamp" \
    bash "$SCRIPT" "$@" >/dev/null 2>&1
}
revived() { grep -q '^systemd-run .*--on-active=5s .*systemctl start postgresql@18-main.service$' "$T/calls"; }
pidfile="$T/data/18/main/postmaster.pid"

# ① 有意停库：没有 pid 文件
rm -f "$pidfile" "$T/stamp"
run 18-main || fail 'exit status must be 0'
if revived; then fail 'revived after a clean stop (no postmaster.pid)'; fi
# ② postmaster 还活着
printf '4242\n/var/lib/postgresql/18/main\n' >"$pidfile"
mkdir -p "$T/proc/4242"; printf 'postgres\n' >"$T/proc/4242/comm"
run 18-main
if revived; then fail 'revived although the postmaster is still running'; fi
# ③ 被杀：pid 文件留着、PID 不在
rm -rf "$T/proc/4242"
run 18-main
revived || fail "not revived after the postmaster was killed: $(cat "$T/calls")"
grep -q '^logger ' "$T/calls" || fail 'the revive was not logged'
# PID 被别的程序占了，也算 postmaster 不在
rm -f "$T/stamp"; mkdir -p "$T/proc/4242"; printf 'bash\n' >"$T/proc/4242/comm"
run 18-main
revived || fail 'not revived when the PID belongs to another program'
rm -rf "$T/proc/4242"
# ④ 60 秒内拉过一次：不再拉
run 18-main
if revived; then fail 'revived twice within 60 seconds'; fi
grep -q '^logger .*not reviving again' "$T/calls" || fail "the skipped revive was not logged: $(cat "$T/calls")"
touch -t 202001010000 "$T/stamp"
run 18-main
revived || fail 'not revived after the rate-limit window'
# ⑤ 不合法的输入
rm -f "$T/stamp"
for inst in '18-main;x' '../18-main' '' ; do
  run "$inst" || true
  if revived; then fail "revived for instance '$inst'"; fi
done
printf 'abc\n' >"$pidfile"
run 18-main
if revived; then fail 'revived with a non-numeric pid file'; fi

# ⑥ 静态：drop-in、发布、安装
. "$DEPLOY/install-lib.sh"
set -euo pipefail
for sw in 1 0; do
  full="$(NATIVE_HARDENING=$sw native_pg_dropin)"
  [ "$(grep -Ec '^[[:space:]]*ExecStopPost[[:space:]]*=' <<<"$full")" -eq 1 ] \
    && grep -qx 'ExecStopPost=+/opt/pandora/deploy/pg-revive.sh %i' <<<"$full" \
    || fail "pg drop-in (hardening=$sw) must have exactly the pg-revive ExecStopPost: $(grep -i execstoppost <<<"$full")"
done
[ "$(grep -c 'pg-revive.sh' "$DEPLOY/build-release.sh")" -ge 2 ] || fail 'build-release.sh does not ship pg-revive.sh'
grep -Eq 'for f in .*pg-revive\.sh' "$DEPLOY/install.sh" || fail 'install.sh does not install pg-revive.sh'

printf 'pg-revive mock: PASS\n'
