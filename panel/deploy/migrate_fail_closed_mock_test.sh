#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MIGRATE="$ROOT/deploy/migrate.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-migrate-fail-closed.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/migrations"

cat >"$TMP/env" <<'ENV'
AEGIS_MIGRATION_DATABASE_URL=host=127.0.0.1 port=5432 user=test dbname=test sslmode=disable
ENV
cp "$ROOT"/migrations/*.sql "$TMP/migrations/"
cat >"$TMP/bin/goose" <<'MOCK'
#!/usr/bin/env bash
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s\n' "$*" >>"$mock_root/goose.exec"
MOCK
chmod 0755 "$TMP/bin/goose"

run_migrate() {
  AEGIS_ENV_FILE="$TMP/env" AEGIS_MIGRATIONS_DIR="$TMP/migrations" \
    GOOSE_BIN="$TMP/bin/goose" bash "$MIGRATE" "$@"
}
expect_78() {
  local label=$1
  shift
  set +e
  run_migrate "$@" >"$TMP/$label.out" 2>&1
  local status=$?
  set -e
  [ "$status" -eq 78 ] || { echo "$label expected exit 78, got $status" >&2; exit 1; }
}

: >"$TMP/goose.exec"
expect_78 down down
expect_78 redo redo
expect_78 skip_as_command --skip-check
expect_78 skip_as_argument up --skip-check
expect_78 arbitrary_flag -v up
expect_78 leading_zero up-to 058
expect_78 oversized up-to 9999999999
[ ! -s "$TMP/goose.exec" ] || { echo 'a rejected command reached goose' >&2; exit 1; }

# Read-only commands remain available after the destructive surface is closed.
# 这两条跑的是仓库里真实的 migrations/（上面整目录拷贝）：真实目录带着 00073、
# 00091、00092 三个历史空号，脚本拒绝它就在这里变红。
run_migrate status >/dev/null
run_migrate version >/dev/null
[ "$(grep -Ec '^(status|version)$' "$TMP/goose.exec")" -eq 2 ]

# 编号规则：严格递增、不重复，允许空号。
sequence_case() {
  rm -rf -- "$TMP/seq"
  mkdir -p "$TMP/seq"
  local file
  for file in "$@"; do
    printf '%s\n' '-- +goose Up' 'SELECT 1;' >"$TMP/seq/$file"
  done
  : >"$TMP/goose.exec"
  set +e
  AEGIS_ENV_FILE="$TMP/env" AEGIS_MIGRATIONS_DIR="$TMP/seq" \
    GOOSE_BIN="$TMP/bin/goose" bash "$MIGRATE" status >"$TMP/seq.out" 2>&1
  SEQ_STATUS=$?
  set -e
}
sequence_case 00001_a.sql 00003_c.sql 00007_g.sql
[ "$SEQ_STATUS" -eq 0 ] || { echo "gapped sequence was rejected: $(cat "$TMP/seq.out")" >&2; exit 1; }
grep -Fxq status "$TMP/goose.exec"
sequence_case 00001_a.sql 00002_b.sql 00002_c.sql
[ "$SEQ_STATUS" -eq 78 ] || { echo "duplicate version expected exit 78, got $SEQ_STATUS" >&2; exit 1; }
grep -Fq 'duplicate migration version: 00002_c.sql' "$TMP/seq.out"
[ ! -s "$TMP/goose.exec" ] || { echo 'a duplicate-version set reached goose' >&2; exit 1; }
sequence_case 00000_zero.sql 00001_a.sql
[ "$SEQ_STATUS" -eq 78 ] || { echo "version 00000 expected exit 78, got $SEQ_STATUS" >&2; exit 1; }
[ ! -s "$TMP/goose.exec" ] || { echo 'a version-0 set reached goose' >&2; exit 1; }

# ---------------------------------------------------------------------------
# 无效索引护栏：CREATE INDEX CONCURRENTLY 失败留下的 INVALID 索引，重跑时会被 IF NOT EXISTS
# 跳过、goose 照样记版本（w8walk 在 5k 副本上复现过 00136）。up 前后各查一次 pg_index。
# 桩 psql 经 PANDORA_PSQL_BIN 接入：invalid 文件在就把它当查询结果，psql_fail 在就报错；
# 桩 goose 在 goose_after 文件在时，执行 up 之后才「留下」无效索引。
# ---------------------------------------------------------------------------
cat >"$TMP/bin/psql" <<'MOCK'
#!/usr/bin/env bash
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'psql %s\n' "$(printf '%s ' "$@" | tr '\n' ' ')" >>"$mock_root/goose.exec"
[ ! -f "$mock_root/psql_fail" ] || { echo 'psql: connection refused' >&2; exit 2; }
[ ! -f "$mock_root/invalid" ] || cat "$mock_root/invalid"
MOCK
cat >"$TMP/bin/goose" <<'MOCK'
#!/usr/bin/env bash
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s\n' "$*" >>"$mock_root/goose.exec"
[ ! -f "$mock_root/goose_fail" ] || { echo 'goose run: ERROR 00136: could not create unique index' >&2; exit 1; }
[ ! -f "$mock_root/goose_after" ] || cp "$mock_root/goose_after" "$mock_root/invalid"
MOCK
chmod 0755 "$TMP/bin/psql" "$TMP/bin/goose"
INVALID_ROW='public.subscriptions_user_label_unique on public.subscriptions'
index_case() {
  local label=$1; shift
  : >"$TMP/goose.exec"
  set +e
  PANDORA_SKIP_PRECHECK_FRESH_DB=yes-empty-database PANDORA_PSQL_BIN="$TMP/bin/psql" \
    run_migrate "$@" >"$TMP/$label.out" 2>&1
  INDEX_STATUS=$?
  set -e
}
reset_index_state() { rm -f "$TMP/invalid" "$TMP/psql_fail" "$TMP/goose_fail" "$TMP/goose_after"; }
ran_goose_up() { grep -Eq '^up( |$)' "$TMP/goose.exec"; }

# 干净的库：执行前后各查一次，goose 照常执行，退出 0；psql 拿到的是迁移 DSN
reset_index_state
index_case clean up
[ "$INDEX_STATUS" -eq 0 ] || { echo "clean up failed: $(cat "$TMP/clean.out")" >&2; exit 1; }
ran_goose_up || { echo 'clean up did not reach goose' >&2; exit 1; }
[ "$(grep -c '^psql ' "$TMP/goose.exec")" -eq 2 ] || { echo "expected two index checks: $(cat "$TMP/goose.exec")" >&2; exit 1; }
# 桩 env 文件按 shell 语法 source，DSN 只剩第一个词
grep -q '^psql .*-d host=127.0.0.1 -c ' "$TMP/goose.exec" \
  || { echo "psql did not get the migration DSN: $(grep '^psql' "$TMP/goose.exec")" >&2; exit 1; }
[ "$(sed -n '1p;3p' "$TMP/goose.exec" | cut -c1-5 | tr '\n' ' ')" = 'psql  psql  ' ] \
  || { echo "checks must wrap goose: $(cat "$TMP/goose.exec")" >&2; exit 1; }

# 上次失败留下的 INVALID 索引还在：拒绝重跑（退出 1），goose 一次都没执行，给出清理命令
reset_index_state
printf '%s\n' "$INVALID_ROW" >"$TMP/invalid"
for cmd in up up-by-one 'up-to 136'; do
  # shellcheck disable=SC2086
  index_case before $cmd
  [ "$INDEX_STATUS" -eq 1 ] || { echo "$cmd with an invalid index expected exit 1, got $INDEX_STATUS" >&2; exit 1; }
  if grep -Eq '^up' "$TMP/goose.exec"; then echo "$cmd ran goose over an invalid index" >&2; exit 1; fi
done
grep -Fq "$INVALID_ROW" "$TMP/before.out" || { cat "$TMP/before.out" >&2; exit 1; }
grep -Fq 'DROP INDEX CONCURRENTLY IF EXISTS public.subscriptions_user_label_unique;' "$TMP/before.out" \
  || { echo "missing fix command: $(cat "$TMP/before.out")" >&2; exit 1; }
grep -Fq 'nothing was executed' "$TMP/before.out" || { cat "$TMP/before.out" >&2; exit 1; }

# 迁移跑完才出现 INVALID 索引：失败（退出 1），说明索引不生效
reset_index_state
printf '%s\n' "$INVALID_ROW" >"$TMP/goose_after"
index_case after up
[ "$INDEX_STATUS" -eq 1 ] || { echo "invalid index after up expected exit 1, got $INDEX_STATUS" >&2; exit 1; }
ran_goose_up || { echo 'after-case did not reach goose' >&2; exit 1; }
grep -Fq 'not in effect' "$TMP/after.out" && grep -Fq 'DROP INDEX CONCURRENTLY IF EXISTS public.subscriptions_user_label_unique;' "$TMP/after.out" \
  || { cat "$TMP/after.out" >&2; exit 1; }

# 迁移本身失败：保留 goose 的退出码，并把半成品的清理命令打出来
reset_index_state
touch "$TMP/goose_fail"; printf '%s\n' "$INVALID_ROW" >"$TMP/goose_after"
cp "$TMP/goose_after" "$TMP/pending_invalid"
cat >"$TMP/bin/goose" <<'MOCK'
#!/usr/bin/env bash
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s\n' "$*" >>"$mock_root/goose.exec"
cp "$mock_root/pending_invalid" "$mock_root/invalid"
echo 'goose run: ERROR 00136: could not create unique index' >&2
exit 1
MOCK
index_case failed up
[ "$INDEX_STATUS" -eq 1 ] || { echo "failed goose expected exit 1, got $INDEX_STATUS" >&2; exit 1; }
grep -Fq 'could not create unique index' "$TMP/failed.out" && grep -Fq 'left them behind' "$TMP/failed.out" \
  && grep -Fq 'DROP INDEX CONCURRENTLY IF EXISTS public.subscriptions_user_label_unique;' "$TMP/failed.out" \
  || { cat "$TMP/failed.out" >&2; exit 1; }
rm -f "$TMP/pending_invalid"

# 查不了也不放行：psql 报错时 up 不执行
reset_index_state
touch "$TMP/psql_fail"
index_case unreachable up
[ "$INDEX_STATUS" -eq 1 ] || { echo "unverifiable index state expected exit 1, got $INDEX_STATUS" >&2; exit 1; }
if grep -Eq '^up' "$TMP/goose.exec"; then echo 'up ran although the index check could not run' >&2; exit 1; fi
grep -Fq 'cannot query pg_index' "$TMP/unreachable.out" || { cat "$TMP/unreachable.out" >&2; exit 1; }
# 指定的 psql 不存在：同样不放行
: >"$TMP/goose.exec"
set +e
PANDORA_SKIP_PRECHECK_FRESH_DB=yes-empty-database PANDORA_PSQL_BIN="$TMP/bin/no-such-psql" run_migrate up >"$TMP/nopsql.out" 2>&1
status=$?
set -e
[ "$status" -eq 1 ] || { echo "missing psql expected exit 1, got $status" >&2; exit 1; }
if grep -Eq '^up' "$TMP/goose.exec"; then echo 'up ran without an index checker' >&2; exit 1; fi

# check-indexes：只查不迁移；安装器停服前用它
reset_index_state
index_case check_clean check-indexes
[ "$INDEX_STATUS" -eq 0 ] && grep -Fq 'no invalid indexes' "$TMP/check_clean.out" || { cat "$TMP/check_clean.out" >&2; exit 1; }
printf '%s\n' "$INVALID_ROW" >"$TMP/invalid"
index_case check_dirty check-indexes
[ "$INDEX_STATUS" -eq 1 ] || { echo "check-indexes with an invalid index expected exit 1, got $INDEX_STATUS" >&2; exit 1; }
if grep -Evq '^psql ' "$TMP/goose.exec"; then echo "check-indexes reached goose: $(cat "$TMP/goose.exec")" >&2; exit 1; fi
index_case check_args check-indexes extra
[ "$INDEX_STATUS" -eq 78 ] || { echo "check-indexes with arguments expected exit 78, got $INDEX_STATUS" >&2; exit 1; }
reset_index_state

echo 'migrate public wrapper fail-closed matrix: PASS'

