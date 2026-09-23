#!/usr/bin/env bash
# Static contract test only. It never claims a real PostgreSQL 18 result.
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
PROBE="$ROOT/deploy/probe-client-auth-00042-protected-surface.sql"
MIGRATION="$ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql"

fail() {
  printf 'client_auth_00042_protected_surface_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}

[[ -f "$PROBE" && ! -L "$PROBE" ]] || fail probe_missing_or_unsafe
[[ -f "$MIGRATION" && ! -L "$MIGRATION" ]] || fail migration_missing_or_unsafe

for required in \
  "BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY" \
  "SET LOCAL search_path = pg_catalog" \
  "server_version_num" \
  "SELECT 1 / CASE" \
  "client-auth-00042-catalog-v1" \
  "count(DISTINCT (schema_name,relation_name))=15" \
  "bool_and(catalog_rows=1)" \
  "pg_catalog.pg_attribute" \
  "pg_catalog.pg_attrdef" \
  "pg_catalog.pg_get_constraintdef" \
  "pg_catalog.pg_index" \
  "pg_catalog.pg_policy" \
  "pg_catalog.pg_trigger" \
  "pg_catalog.aclexplode" \
  "pg_catalog.pg_sequence" \
  "pg_catalog.pg_depend" \
  "protected_surface_current_sha256=" \
  "protected_surface_stored_sha256=" \
  "protected_surface_match=" \
  "protected_surface_manifest=" \
  "ROLLBACK;"; do
  grep -Fq "$required" "$PROBE" || fail "surface_missing:$required"
done

relations=(
  app.client_auth_00042_meta
  public.devices
  public.device_authorizations
  public.config_bundles
  public.refresh_tokens
  public.config_bundle_nodes
  public.refresh_families
  public.device_proof_nonces
  public.client_access_token_jtis
  public.device_issuance_response_replays
  public.client_refresh_response_replays
  public.device_issuance_replay_uses
  public.client_refresh_replay_uses
  public.device_proof_nonces_id_seq
  public.client_access_token_jtis_id_seq
)
for qualified in "${relations[@]}"; do
  schema="${qualified%%.*}"
  relation="${qualified#*.}"
  [[ "$(grep -Ec "\\('[0-9]+'?,'?${schema}','${relation}'\\)|\\([0-9]+,'${schema}','${relation}'\\)" "$PROBE")" -eq 1 ]] \
    || fail "relation_not_exact_once:$qualified"
done

[[ "$(grep -Ec "^[[:space:]]+\\([0-9]+,'(app|public)','[a-z0-9_]+'\\),?$" "$PROBE")" -eq 15 ]] \
  || fail target_relation_count_not_15

migration_catalog="$(
  awk '
    /^\), relation_catalog AS \($/ { capture=1 }
    capture && /^UPDATE app\.client_auth_00042_meta$/ { exit }
    capture { print }
  ' "$MIGRATION" | tr -d '[:space:]'
)"
probe_catalog="$(
  awk '
    /^\), relation_catalog AS \($/ { capture=1 }
    capture && /^\), current_surface AS \($/ { exit }
    capture { print }
  ' "$PROBE" | tr -d '[:space:]'
)"
[[ -n "$migration_catalog" && -n "$probe_catalog" ]] \
  || fail canonical_catalog_block_missing
[[ "${probe_catalog})" == "$migration_catalog" ]] \
  || fail canonical_catalog_block_drift

if grep -Eq "'(oid|xmin|relfilenode)'[[:space:]]*," "$PROBE"; then
  fail nonportable_identity_in_manifest
fi
if grep -Fq "1/0" "$PROBE"; then
  fail constant_division_guard_forbidden
fi
if grep -Eq '^[[:space:]]*(INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|GRANT|REVOKE|TRUNCATE)[[:space:]]' "$PROBE"; then
  fail mutating_sql_forbidden
fi

printf 'client_auth_00042_protected_surface_mock=PASS relations=15 deterministic=STATIC_ONLY real_pg18=NOT_RUN\n'
