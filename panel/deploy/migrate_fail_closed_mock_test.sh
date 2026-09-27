#!/usr/bin/env bash
# [INPUT]: 依赖同目录 migrate.sh、仓库真实的 ../migrations/*.sql，goose 用桩脚本代替
# [OUTPUT]: migrate.sh 公开入口的拒绝矩阵，以及编号规则：真实目录能过、空号能过、同号与 00000 被拒
# [POS]: deploy 的桩测试，CI panel-deploy.yml 必跑；不需要数据库或 root
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
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

echo 'migrate public wrapper fail-closed matrix: PASS'
