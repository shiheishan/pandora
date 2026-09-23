#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERIFY="$ROOT/deploy/verify-client-auth-00042-catalog.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-ca42-verify.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT
mkdir -p "$TMP/bin"

cat >"$TMP/bin/psql" <<'MOCK'
#!/usr/bin/env bash
set -eu
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
cat >"$mock_root/query.sql"
printf '%s\n' "$*" >"$mock_root/psql.argv"
[[ "${PGOPTIONS:-}" == *'default_transaction_read_only=on'* ]] || exit 90
scenario=pass
[ ! -f "$mock_root/scenario" ] || IFS= read -r scenario <"$mock_root/scenario"
case "$scenario" in
  pass) printf '%s\n' 'CLIENT-AUTH-00042-CATALOG=PASS' ;;
  forged_version|role_oid_replaced|acl_rls_tampered|same_count_replaced) exit 1 ;;
  ambiguous_pass) printf '%s\n' 'CLIENT-AUTH-00042-CATALOG=PASS' 'extra' ;;
  *) exit 91 ;;
esac
MOCK
chmod 0755 "$TMP/bin/psql"

run_verify() {
  printf '%s\n' "${1:-pass}" >"$TMP/scenario"
  PATH="$TMP/bin:$PATH" PSQL_BIN="$TMP/bin/psql" \
    AEGIS_VERIFY_DATABASE_URL='host=127.0.0.1 dbname=aegis user=verifier' \
    "$VERIFY"
}

run_verify pass >"$TMP/pass.out" 2>"$TMP/pass.err"
grep -Fxq 'CLIENT-AUTH-00042-CATALOG=PASS' "$TMP/pass.out"

# Contract surface: the SQL must verify much more than the Goose row.
for required in \
  "max(version_id) FILTER (WHERE is_applied)" \
  "app.client_auth_00042_meta" \
  "role_oid_manifest" \
  "pg_catalog.pg_authid" \
  "pg_catalog.pg_shdepend" \
  "pg_catalog.pg_get_constraintdef" \
  "pg_catalog.pg_get_indexdef" \
  "pg_catalog.pg_policy" \
  "pg_catalog.aclexplode" \
  "v_current_catalog IS DISTINCT FROM v_saved_catalog"; do
  grep -Fq "$required" "$TMP/query.sql" || {
    echo "missing verifier contract: $required" >&2
    exit 1
  }
done
grep -Fq 'BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY' "$TMP/query.sql"
grep -Fq 'default_transaction_read_only=on' "$VERIFY"

for scenario in forged_version role_oid_replaced acl_rls_tampered same_count_replaced ambiguous_pass; do
  set +e
  run_verify "$scenario" >"$TMP/$scenario.out" 2>"$TMP/$scenario.err"
  rc=$?
  set -e
  [ "$rc" -eq 78 ] || {
    echo "$scenario returned $rc, want 78" >&2
    exit 1
  }
  [ ! -s "$TMP/$scenario.out" ] || {
    echo "$scenario published a PASS marker" >&2
    exit 1
  }
done

# The frozen migration artifact is itself part of the verifier contract.
cp "$ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql" "$TMP/00042.sql"
printf '%s\n' '-- tampered' >>"$TMP/00042.sql"
set +e
PATH="$TMP/bin:$PATH" PSQL_BIN="$TMP/bin/psql" \
  AEGIS_VERIFY_DATABASE_URL='host=127.0.0.1 dbname=aegis user=verifier' \
  AEGIS_CLIENT_AUTH_00042_MIGRATION="$TMP/00042.sql" "$VERIFY" \
  >"$TMP/tampered.out" 2>"$TMP/tampered.err"
rc=$?
set -e
[ "$rc" -eq 78 ]

echo 'CLIENT-AUTH-00042 exact catalog verifier mock: PASS'
