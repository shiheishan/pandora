#!/usr/bin/env bash
# 两个安装器升级迁移的顺序桩测试：不需要 root、Docker 或数据库。
#
# install-lib.sh 的 pandora_run_migrations 是 install.sh 与 install-native.sh 共用的迁移步骤。
# 这里把 check-migrations.sh、migrate.sh、systemctl 换成往同一份事件日志里记账的桩，证明：
#   - 升级：完整预检（按停写口径演练、写凭据、不带「写入者已停」声明）在停服之前；
#     停服之后 migrate.sh 只带凭据（只做只读核对），「写入者已停」声明只在这一步给；
#   - 停服前预检失败：服务一个都没停、migrate.sh 没被调用；
#   - 迁移失败：服务被拉回来；
#   - 首装、或升级但库是全新的：不跑克隆预检。
# 再静态核对两个安装器真的走这个函数，没有绕开它另起一套；迁移手册 MIGRATION-RUNBOOK.md
# 进发布包（拷贝与归档两处）并由两个安装器装到 deploy/ 下。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install-migrate-order: %s\n' "$*" >&2; exit 1; }

mkdir -p "$T/bin" "$T/deploy" "$T/migrations"
EVENTS="$T/events.log"
export EVENTS

cat >"$T/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
printf 'systemctl %s\n' "$*" >>"$EVENTS"
MOCK
# 桩脚本经 env -i 调用，拿不到 EVENTS：事件日志按自身位置找
cat >"$T/deploy/check-migrations.sh" <<'MOCK'
#!/usr/bin/env bash
events="$(cd "$(dirname "$0")/.." && pwd)/events.log"
if [ "${1:-}" = --verify-attestation ]; then
  printf 'verify %s\n' "$2" >>"$events"; exit 0
fi
printf 'precheck rehearse=%s approved=%s attestation_out=%s env=%s dir=%s goose=%s\n' \
  "${PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER:-}" "${PANDORA_STOPPED_WRITER_UPGRADE_APPROVED:-}" \
  "${PANDORA_PRECHECK_ATTESTATION_OUT:-}" "${AEGIS_ENV_FILE:-}" "${AEGIS_MIGRATIONS_DIR:-}" "${GOOSE_BIN:-}" >>"$events"
[ ! -f "$(dirname "$0")/../fail_precheck" ] || { echo 'migration precheck: mock failure' >&2; exit 78; }
[ -n "${PANDORA_PRECHECK_ATTESTATION_OUT:-}" ] || exit 64
[ -f "$(dirname "$0")/../no_attestation" ] || printf 'format=mock\n' >"$PANDORA_PRECHECK_ATTESTATION_OUT"
echo 'migration precheck complete'
MOCK
cat >"$T/deploy/migrate.sh" <<'MOCK'
#!/usr/bin/env bash
events="$(cd "$(dirname "$0")/.." && pwd)/events.log"
att="${PANDORA_PRECHECK_ATTESTATION:-}"
printf 'migrate %s attestation=%s attestation_file=%s approved=%s skip=%s local=%s\n' "$*" \
  "${att:+set}" "$([ -n "$att" ] && [ -s "$att" ] && echo present || echo absent)" \
  "${PANDORA_STOPPED_WRITER_UPGRADE_APPROVED:-}" "${PANDORA_SKIP_PRECHECK_FRESH_DB:-}" \
  "${PANDORA_LOCAL_MIGRATION_APPROVED:-}" >>"$events"
if [ "${1:-}" = check-indexes ]; then
  [ ! -f "$(dirname "$0")/../invalid_index" ] || { echo 'migration: INVALID indexes found'; echo '  DROP INDEX CONCURRENTLY IF EXISTS public.x;'; exit 1; }
  exit 0
fi
[ ! -f "$(dirname "$0")/../fail_migrate" ] || { echo 'goose: mock failure' >&2; exit 1; }
echo 'migration precheck=attested'
MOCK
chmod 0755 "$T/bin/systemctl" "$T/deploy/check-migrations.sh" "$T/deploy/migrate.sh"
export PATH="$T/bin:$PATH" TMPDIR="$T"
: >"$T/env"

# shellcheck source=install-lib.sh
. "$DEPLOY/install-lib.sh"

reset() { : >"$EVENTS"; rm -f "$T/fail_precheck" "$T/fail_migrate" "$T/no_attestation" "$T/invalid_index"; }
lineno() { grep -n -m1 -e "$1" "$EVENTS" | cut -d: -f1; }
run() { pandora_run_migrations "$@" "$T/deploy" "$T/env" "$T/migrations" "$T/bin/goose" >"$T/out.log" 2>&1; }

# --- ① 升级：预检在停服之前，停服后只核凭据 -----------------------------------------
reset
run upgrade no || fail "upgrade returned $?: $(cat "$T/out.log")"
idx="$(lineno '^migrate check-indexes ')"; pre="$(lineno '^precheck ')"; stop="$(lineno '^systemctl stop ')"; mig="$(lineno '^migrate up ')"
[ -n "$idx" ] && [ -n "$pre" ] && [ -n "$stop" ] && [ -n "$mig" ] || fail "missing events: $(cat "$EVENTS")"
[ "$idx" -lt "$pre" ] && [ "$pre" -lt "$stop" ] && [ "$stop" -lt "$mig" ] || fail "order is not check-indexes < precheck < stop < migrate: $(cat "$EVENTS")"
# 索引检查只读：不带「写入者已停」声明
grep -qx 'migrate check-indexes attestation= attestation_file=absent approved= skip= local=yes' "$EVENTS" \
  || fail "check-indexes env: $(grep '^migrate check-indexes' "$EVENTS")"
[ "$(grep -c '^precheck ' "$EVENTS")" = 1 ] || fail 'the full precheck ran more than once'
# 停服前：按停写口径演练、写凭据；绝不带「线上写入者已停」声明
grep -q '^precheck rehearse=yes approved= attestation_out=/' "$EVENTS" || fail "precheck env: $(grep '^precheck' "$EVENTS")"
grep -q "env=$T/env dir=$T/migrations goose=$T/bin/goose\$" "$EVENTS" || fail "precheck paths: $(grep '^precheck' "$EVENTS")"
# 停服后：migrate.sh up 带着那张凭据（文件还在），声明写入者已停，不跳过预检
grep -qx 'migrate up attestation=set attestation_file=present approved=yes skip= local=yes' "$EVENTS" \
  || fail "migrate env: $(grep '^migrate' "$EVENTS")"
grep -qx 'systemctl stop aegis-public aegis-admin aegis-node' "$EVENTS" || fail "stopped: $(grep '^systemctl' "$EVENTS")"
if grep -q '^systemctl start' "$EVENTS"; then fail 'services restarted by the migration step on success'; fi
# 凭据用完即删，不留在临时目录
if ls "$T"/pandora-precheck.* >/dev/null 2>&1; then fail 'attestation directory left behind'; fi

# --- ② 停服前预检失败：不停服、不迁移 -----------------------------------------------
reset; touch "$T/fail_precheck"
rc=0; run upgrade no || rc=$?
[ "$rc" = 10 ] || fail "precheck failure should return 10, got $rc"
if grep -q -e '^systemctl' -e '^migrate up' "$EVENTS"; then fail "precheck failure touched services or ran migrate: $(cat "$EVENTS")"; fi
grep -q 'mock failure' "$T/out.log" || fail 'precheck output was not shown'
# 预检说成功却没写凭据，同样当失败
reset; touch "$T/no_attestation"
rc=0; run upgrade no || rc=$?
[ "$rc" = 10 ] || fail "missing attestation should return 10, got $rc"
if grep -q -e '^systemctl' -e '^migrate up' "$EVENTS"; then fail 'missing attestation still stopped services'; fi
# 库里有 INVALID 索引：停服之前就拦下，不预检、不停服、不迁移，清理命令给到屏幕上
reset; touch "$T/invalid_index"
rc=0; run upgrade no || rc=$?
[ "$rc" = 10 ] || fail "invalid index should return 10, got $rc"
if grep -q -e '^systemctl' -e '^precheck' -e '^migrate up' "$EVENTS"; then fail "invalid index still went on: $(cat "$EVENTS")"; fi
grep -q 'DROP INDEX CONCURRENTLY' "$T/out.log" || fail 'index check output was not shown'

# --- ③ 迁移失败：服务拉回来 --------------------------------------------------------
reset; touch "$T/fail_migrate"
rc=0; run upgrade no || rc=$?
[ "$rc" = 11 ] || fail "migrate failure should return 11, got $rc"
[ "$(lineno '^migrate up ')" -lt "$(lineno '^systemctl start ')" ] || fail "services not restored after failure: $(cat "$EVENTS")"
grep -qx 'systemctl start aegis-public aegis-admin aegis-node' "$EVENTS" || fail 'restore did not start all three services'

# --- ④ 首装：没有服务可停，全新库跳过预检 --------------------------------------------
reset
run install yes || fail "install returned $?"
if grep -q -e '^precheck' -e '^systemctl' -e '^migrate check-indexes' "$EVENTS"; then fail "install ran precheck or touched services: $(cat "$EVENTS")"; fi
grep -qx 'migrate up attestation= attestation_file=absent approved=yes skip=yes-empty-database local=yes' "$EVENTS" \
  || fail "install migrate env: $(cat "$EVENTS")"
reset; touch "$T/fail_migrate"
rc=0; run install yes || rc=$?
[ "$rc" = 12 ] || fail "install migrate failure should return 12, got $rc"
if grep -q '^systemctl' "$EVENTS"; then fail 'install failure touched services'; fi

# --- ⑤ 升级但库是全新的：停服、跳过预检 ----------------------------------------------
reset
run upgrade yes || fail "fresh upgrade returned $?"
if grep -q '^precheck' "$EVENTS"; then fail 'fresh database ran the clone precheck'; fi
[ "$(lineno '^systemctl stop ')" -lt "$(lineno '^migrate up ')" ] || fail "fresh upgrade order: $(cat "$EVENTS")"
grep -q 'skip=yes-empty-database' "$EVENTS" || fail 'fresh upgrade did not skip the precheck'

# --- ⑥ 服务清单可由调用方给（install-native.sh 传自己的 SERVICES）-----------------------
reset
PANDORA_SERVICES='svc-a svc-b' run upgrade no || fail 'custom service list failed'
grep -qx 'systemctl stop svc-a svc-b' "$EVENTS" || fail "custom services: $(grep '^systemctl' "$EVENTS")"

# --- ⑦ 静态：两个安装器都走这个函数 ------------------------------------------------
inst="$DEPLOY/install.sh"; native="$DEPLOY/install-native.sh"
for f in "$inst" "$native"; do
  grep -q 'pandora_run_migrations "\$MODE"' "$f" || fail "${f##*/} does not use pandora_run_migrations"
  grep -Fq 'install-lib.sh' "$f" || fail "${f##*/} does not source install-lib.sh"
  # 「写入者已停」只由共用函数在停服之后递交，安装器自己不再直接调 migrate.sh / check-migrations.sh
  if grep -nE '(migrate|check-migrations)\.sh"? +up|bash \./migrate\.sh|/deploy/migrate\.sh" up' "$f"; then
    fail "${f##*/} still runs migrate.sh directly"
  fi
  if grep -n 'PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes' "$f"; then
    fail "${f##*/} declares stopped writers by itself"
  fi
done
# install.sh：备份 < 迁移 < 装程序；check-migrations.sh 与迁移文件一起先就位
line() { grep -nF "$2" "$1" | head -1 | cut -d: -f1; }
[ "$(line "$inst" 'pg_restore --list /tmp/pre-upgrade.dump')" -lt "$(line "$inst" 'pandora_run_migrations "$MODE"')" ] \
  || fail 'install.sh migrates before the pre-upgrade backup'
[ "$(line "$inst" 'pandora_run_migrations "$MODE"')" -lt "$(line "$inst" 'install-linux-binaries.sh" "$RELEASE_ROOT"')" ] \
  || fail 'install.sh installs binaries before migrating'
[ "$(line "$inst" 'for f in migrate.sh platform.sh check-migrations.sh; do')" -lt "$(line "$inst" 'step "备份数据库"')" ] \
  || fail 'install.sh copies the migration scripts too late for a pre-stop precheck'
# install-native.sh：备份 < 迁移 < 装程序 < 启动；不再把「写入者已停」写进 .env
[ "$(line "$native" 'pg_dump -Fc -d aegis')" -lt "$(line "$native" 'pandora_run_migrations "$MODE"')" ] \
  || fail 'install-native.sh migrates before the pre-upgrade backup'
[ "$(line "$native" 'pandora_run_migrations "$MODE"')" -lt "$(line "$native" 'cp -f "$RELEASE_BIN"/* "$INSTALL_DIR/bin/"')" ] \
  || fail 'install-native.sh installs binaries before migrating (a failed upgrade would restart new code on the old schema)'
[ "$(line "$native" 'cp -f "$RELEASE_BIN"/* "$INSTALL_DIR/bin/"')" -lt "$(line "$native" 'systemctl start "$s"')" ] \
  || fail 'install-native.sh starts services before installing the new binaries'
if grep -n '^PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=' "$native"; then
  fail 'install-native.sh writes a permanent stopped-writer declaration into .env'
fi

# --- ⑧ 迁移手册跟着发布包走，装到 deploy/ 下：迁移失败时人在服务器上照着做 ----------------
build="$DEPLOY/build-release.sh"
[ -s "$DEPLOY/MIGRATION-RUNBOOK.md" ] || fail 'MIGRATION-RUNBOOK.md is missing from the source tree'
grep -Fq 'cp "$ROOT/deploy/MIGRATION-RUNBOOK.md" "$target/deploy/MIGRATION-RUNBOOK.md"' "$build" \
  || fail 'build-release.sh does not copy MIGRATION-RUNBOOK.md into the release tree'
# 归档按类分 tar：手册是数据文件（0644），必须在 release_data 里，否则 SHA256SUMS 有而 tar 里没有
awk '/^  release_data=\(/{p=1} p{print} p&&/^  \)/{exit}' "$build" | grep -Fq '"$target_base/deploy/MIGRATION-RUNBOOK.md"' \
  || fail 'build-release.sh does not archive MIGRATION-RUNBOOK.md as a 0644 data file'
grep -Fq 'cp "$RELEASE_ROOT/deploy/MIGRATION-RUNBOOK.md" "$DEST/deploy/"' "$inst" || fail 'install.sh does not install the runbook'
grep -Fq '"$SCRIPT_DIR/MIGRATION-RUNBOOK.md"' "$native" || fail 'install-native.sh does not install the runbook'
# 迁移失败的提示指向机器上的这份手册
grep -Fq '$DEST/deploy/MIGRATION-RUNBOOK.md' "$inst" || fail 'install.sh failure message does not point at the runbook'

printf 'install-migrate-order mock: PASS\n'
