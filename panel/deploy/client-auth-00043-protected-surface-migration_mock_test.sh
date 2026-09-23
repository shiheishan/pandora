#!/usr/bin/env bash
# Static migration integration gate only. Real PostgreSQL 18 remains NOT_RUN.
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
MIGRATION="$ROOT/migrations/frozen-client-auth/00043_client_auth_existing_parent_indexes.sql"
BASELINE="$ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql"

fail() {
  printf 'client_auth_00043_protected_surface_migration_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}

[[ -f "$MIGRATION" && ! -L "$MIGRATION" ]] || fail migration_missing_or_unsafe
[[ -f "$BASELINE" && ! -L "$BASELINE" ]] || fail baseline_missing_or_unsafe

for required in \
  "CLIENT-AUTH-00043 protected-surface allowlist cardinality mismatch" \
  "v_excluded_index_count <> 19 OR v_excluded_constraint_count <> 18" \
  "CLIENT-AUTH-00043 Down protected-surface exclusion set not empty" \
  "v_excluded_index_count <> 0 OR v_excluded_constraint_count <> 0" \
  "CLIENT-AUTH-00043 protected-surface drift before Up mutation" \
  "CLIENT-AUTH-00043 protected-surface drift before Down meta delete" \
  "v_current_catalog_manifest IS DISTINCT FROM v_stored_catalog_manifest" \
  "v_current_catalog_sha256 IS DISTINCT FROM v_stored_catalog_sha256" \
  "pg_catalog.convert_to(v_current_catalog_manifest::text,'UTF8')" \
  "pg_catalog.convert_to(v_stored_catalog_manifest::text,'UTF8')" \
  "AND NOT (con.conname=ANY(v_names))" \
  "AND NOT (ci.relname=ANY(v_names))" \
  "AND d.objid=ic.oid AND d.deptype='i')=0" \
  "AND d.objid=ic.oid AND d.deptype='i')=1"; do
  grep -Fq "$required" "$MIGRATION" || fail "surface_missing:$required"
done

[[ "$(grep -Fc "v_current_catalog_manifest IS DISTINCT FROM v_stored_catalog_manifest" "$MIGRATION")" -eq 2 ]] \
  || fail manifest_exact_compare_not_up_and_down
[[ "$(grep -Fc "v_current_catalog_sha256 IS DISTINCT FROM v_stored_catalog_sha256" "$MIGRATION")" -eq 2 ]] \
  || fail hash_exact_compare_not_up_and_down
[[ "$(grep -Fc "AND NOT (con.conname=ANY(v_names))" "$MIGRATION")" -eq 2 ]] \
  || fail constraint_normalization_scope_invalid
[[ "$(grep -Fc "AND NOT (ci.relname=ANY(v_names))" "$MIGRATION")" -eq 2 ]] \
  || fail index_normalization_scope_invalid
[[ "$(grep -Ec "^[[:space:]]+\\([0-9]+,'(app|public)','[a-z0-9_]+'\\),?$" "$MIGRATION")" -eq 30 ]] \
  || fail protected_target_count_not_15_per_direction

canonical_catalog="$(
  awk '
    /^[[:space:]]*\), relation_catalog AS \($/ { capture=1 }
    capture && /^UPDATE app\.client_auth_00042_meta$/ { exit }
    capture { print }
  ' "$BASELINE" | tr -d '[:space:]'
)"
extract_catalog() {
  local wanted="$1"
  awk -v wanted="$wanted" '
    /^[[:space:]]*\), relation_catalog AS \($/ {
      block++
      if (block == wanted) capture=1
    }
    capture && /^[[:space:]]*SELECT jsonb_build_object\($/ { exit }
    capture { print }
  ' "$MIGRATION" | tr -d '[:space:]'
}
up_catalog="$(extract_catalog 1)"
down_catalog="$(extract_catalog 2)"
up_catalog="${up_catalog//ANDNOT(con.conname=ANY(v_names))/}"
up_catalog="${up_catalog//ANDNOT(ci.relname=ANY(v_names))/}"
down_catalog="${down_catalog//ANDNOT(con.conname=ANY(v_names))/}"
down_catalog="${down_catalog//ANDNOT(ci.relname=ANY(v_names))/}"
[[ -n "$canonical_catalog" && -n "$up_catalog" && -n "$down_catalog" ]] \
  || fail canonical_catalog_block_missing
[[ "$up_catalog" == "$canonical_catalog" ]] || fail up_catalog_block_drift
[[ "$down_catalog" == "$canonical_catalog" ]] || fail down_catalog_block_drift

line_classifier="$(grep -nF "CLIENT-AUTH-00043 finalizer object mismatch" "$MIGRATION" | head -n1 | cut -d: -f1)"
line_up_guard="$(grep -nF "CLIENT-AUTH-00043 protected-surface drift before Up mutation" "$MIGRATION" | cut -d: -f1)"
line_create_meta="$(grep -nF "CREATE TABLE app.client_auth_00043_meta" "$MIGRATION" | cut -d: -f1)"
line_removed="$(grep -nF "CLIENT-AUTH-00043 Down requires runner-removed objects" "$MIGRATION" | cut -d: -f1)"
line_down_guard="$(grep -nF "CLIENT-AUTH-00043 protected-surface drift before Down meta delete" "$MIGRATION" | cut -d: -f1)"
line_delete_meta="$(grep -nF "DELETE FROM app.client_auth_00043_meta" "$MIGRATION" | cut -d: -f1)"
[[ "$line_classifier" -lt "$line_up_guard" && "$line_up_guard" -lt "$line_create_meta" ]] \
  || fail up_guard_order_invalid
[[ "$line_removed" -lt "$line_down_guard" && "$line_down_guard" -lt "$line_delete_meta" ]] \
  || fail down_guard_order_invalid

if grep -Fq "1/0" "$MIGRATION"; then
  fail constant_division_guard_forbidden
fi
if grep -Eq '^[[:space:]]*(CREATE UNIQUE INDEX CONCURRENTLY|DROP INDEX CONCURRENTLY|ALTER TABLE .* (ADD|DROP) CONSTRAINT)' "$MIGRATION"; then
  fail migration_contains_data_plane_ddl
fi

printf 'client_auth_00043_protected_surface_migration_mock=PASS directions=2 relations=15 allowlist=19_indexes_18_constraints exact_manifest_hash=true data_plane_ddl=ABSENT real_pg18=NOT_RUN\n'
