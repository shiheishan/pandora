#!/usr/bin/env bash
# pg-revive.sh（postgresql@18-main 的 ExecStopPost）：postmaster 被信号杀掉才拉起，有意停库不拉起。
# 依据是 systemd 交给 ExecStopPost 的 EXIT_CODE（主进程怎么结束的）；panel2 实测四例：
#   kill -9 → killed/KILL；postgres 用户 pg_ctlcluster stop（fast 或 immediate）、systemctl stop → exited/0。
# 那时 ExecStop（pg_ctlcluster stop）已把残留的 postmaster.pid 删了，不能拿 pid 文件判断。
#   ① EXIT_CODE=killed 或 dumped：用 systemd-run 排 5 秒后 start postgresql@<实例>，并记 journal；
#   ② EXIT_CODE=exited（不论退出码）或没有：什么都不做；
#   ③ 本单元有 systemd 作业在跑（systemctl stop / restart，停库超时被 systemd 杀掉也是 killed）：不拉；
#      查不到作业表（systemctl 失败）：不拉，记日志；
#   ④ 60 秒内已拉起过一次：不再拉，只记日志（防反复崩溃时一直拉）；stamp 在将来（时钟往回跳）不算；
#   ⑤ 实例名不合法：什么都不做；
#   ⑥ 静态：PG drop-in 只有这一条 ExecStopPost（以「+」root 跑），发布与安装都带上这个脚本。
# 真机三例（kill -9 被拉起、postgres 用户 pg_ctlcluster stop 不拉、systemctl stop 不拉）在 panel2 实测，见报告。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$DEPLOY/pg-revive.sh"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-pg-revive.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'pg-revive: %s\n' "$*" >&2; exit 1; }
[ -f "$SCRIPT" ] || fail 'deploy/pg-revive.sh is missing'

mkdir -p "$T/bin"
for c in systemd-run logger; do
  printf '#!/usr/bin/env bash\nprintf "%s %%s\\n" "$*" >>"%s/calls"\n' "$c" "$T" >"$T/bin/$c"
done
# systemctl 桩：list-jobs 打印 $T/jobs 的内容；$T/jobs-fail 存在时失败
cat >"$T/bin/systemctl" <<EOF
#!/usr/bin/env bash
printf 'systemctl %s\n' "\$*" >>"$T/calls"
[ "\${1:-}" = list-jobs ] || exit 0
[ -e "$T/jobs-fail" ] && exit 1
cat "$T/jobs" 2>/dev/null || true
EOF
chmod 0755 "$T/bin/"*
# run <EXIT_CODE> <EXIT_STATUS> <实例>
run() {
  : >"$T/calls"
  env -u EXIT_CODE -u EXIT_STATUS PATH="$T/bin:$PATH" PANDORA_REVIVE_STAMP="$T/stamp" \
    ${1:+EXIT_CODE="$1"} ${2:+EXIT_STATUS="$2"} bash "$SCRIPT" "$3" >/dev/null 2>&1
}
revived() { grep -q '^systemd-run .*--on-active=5s .*systemctl start postgresql@18-main.service$' "$T/calls"; }
fresh() { rm -f "$T/stamp" "$T/jobs" "$T/jobs-fail"; }

# ① 被信号杀掉
fresh; run killed KILL 18-main || fail 'exit status must be 0'
revived || fail "not revived after kill -9: $(cat "$T/calls")"
grep -q '^logger ' "$T/calls" || fail 'the revive was not logged'
fresh; run dumped SEGV 18-main
revived || fail 'not revived after a crash with a core dump'
# ② 正常退出（fast、smart、immediate 停库都是 exited）
for st in 0 1 2; do
  fresh; run exited "$st" 18-main
  if revived; then fail "revived after the postmaster exited with status $st"; fi
done
fresh; run '' '' 18-main
if revived; then fail 'revived without EXIT_CODE'; fi
# ③ systemctl stop / restart 作业在跑：不拉；别的单元的作业不算
fresh; printf '67480 postgresql@18-main.service stop running\n' >"$T/jobs"; run killed KILL 18-main
if revived; then fail 'revived while systemctl stop of this unit was running'; fi
fresh; printf '67481 postgresql@18-main.service restart running\n' >"$T/jobs"; run killed KILL 18-main
if revived; then fail 'revived while a restart of this unit was running'; fi
fresh; printf '67482 postgresql@18-other.service stop running\n' >"$T/jobs"; run killed KILL 18-main
revived || fail 'a job of another unit blocked the revive'
fresh; : >"$T/jobs-fail"; run killed KILL 18-main
if revived; then fail 'revived although the job list could not be read'; fi
grep -q '^logger ' "$T/calls" || fail 'an unreadable job list was not logged'
# ④ 60 秒内拉过一次：不再拉
fresh; run killed KILL 18-main; revived || fail 'first revive missing'
run killed KILL 18-main
if revived; then fail 'revived twice within 60 seconds'; fi
grep -q '^logger .*not reviving again' "$T/calls" || fail "the skipped revive was not logged: $(cat "$T/calls")"
touch -t 202001010000 "$T/stamp"
run killed KILL 18-main
revived || fail 'not revived after the rate-limit window'
# 时钟往回跳过（stamp 的时间在将来）：不算 60 秒内，照拉
touch -t 203001010000 "$T/stamp"
run killed KILL 18-main
revived || fail 'not revived when the stamp is in the future (clock stepped back)'
# ⑤ 不合法的实例名
for inst in '18-main;x' '../18-main' '' ; do
  fresh; run killed KILL "$inst" || true
  if grep -q '^systemd-run ' "$T/calls"; then fail "scheduled a start for instance '$inst'"; fi
done

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
