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
run_migrate status >/dev/null
run_migrate version >/dev/null
[ "$(grep -Ec '^(status|version)$' "$TMP/goose.exec")" -eq 2 ]

echo 'migrate public wrapper fail-closed matrix: PASS'
