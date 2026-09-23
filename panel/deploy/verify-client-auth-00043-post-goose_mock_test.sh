#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SQL="$ROOT/deploy/verify-client-auth-00043-post-goose.sql"
DESIGN="$ROOT/.ai-company/handoffs/client-auth-00043-post-goose-exact-20260731.md"
MIGRATION="$ROOT/migrations/frozen-client-auth/00043_client_auth_existing_parent_indexes.sql"

fail() {
  printf 'client_auth_00043_post_goose_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}
contains() { grep -Fq -- "$2" "$1" || fail "missing:$2"; }

if [[ ! -f "$DESIGN" || -L "$DESIGN" ]]; then
  printf 'client_auth_00043_post_goose_mock=NOT_RUN reason=design_handoff_missing\n'
  exit 77
fi
[[ -f "$SQL" ]] || fail artifact_missing
[[ -f "$MIGRATION" ]] || fail migration_missing

# The verifier is read-only; expected definitions are string literals, not DDL.
contains "$SQL" 'BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;'
if grep -Eq '^[[:space:]]*(CREATE|ALTER|DROP|TRUNCATE|INSERT|UPDATE|DELETE|MERGE|CALL)[[:space:]]' "$SQL"; then
  fail mutating_sql_present
fi

# Required caller identity is fail-closed and moved into transaction-local GUCs.
for variable in \
  verify_direction expected_source_system_identifier expected_database_name \
  expected_database_oid expected_release_id expected_runner_sha256 \
  expected_migration_sha256 expected_run_id expected_evidence_sha256 \
  future_gate_contract; do
  contains "$SQL" "\\if :{?${variable}}"
done
contains "$SQL" "pg_control_system()"
contains "$SQL" "database_oid::text=current_setting('aegis.verify_00043.database_oid')"
contains "$SQL" "v_direction='up'"
contains "$SQL" "v_direction='down'"
contains "$SQL" "version_id>43"
contains "$SQL" "version_id>42"
contains "$SQL" "pg_advisory_lock(420042,1)"
contains "$SQL" "pg_advisory_unlock(420042,1)"
if grep -Fq "pg_advisory_xact_lock(420042,1)" "$SQL"; then
  fail stale_transaction_lock_present
fi
LOCK_LINE="$(grep -nF "pg_advisory_lock(420042,1)" "$SQL" | head -n1 | cut -d: -f1)"
BEGIN_LINE="$(grep -nF "BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;" "$SQL" | head -n1 | cut -d: -f1)"
ROLLBACK_LINE="$(grep -nF "ROLLBACK;" "$SQL" | tail -n1 | cut -d: -f1)"
UNLOCK_LINE="$(grep -nF "pg_advisory_unlock(420042,1)" "$SQL" | tail -n1 | cut -d: -f1)"
(( LOCK_LINE < BEGIN_LINE && BEGIN_LINE < ROLLBACK_LINE && ROLLBACK_LINE < UNLOCK_LINE )) \
  || fail advisory_lock_snapshot_order_invalid

# Frozen allowlist: exactly 19 names in the normalization CTE and 19 ids in
# the V3 candidate manifest. Repeated use later in arrays is intentionally not
# counted by these bounded sections.
NORMALIZATION="$(
  sed -n '/WITH candidate_names(name) AS (/,/), target(ordinal/p' "$SQL"
)"
[[ "$(grep -c "^    ('[a-z0-9_]*')" <<<"$NORMALIZATION")" -eq 19 ]] \
  || fail normalization_candidate_count_not_19
[[ "$(grep -o "'U43-[0-9][0-9]'" "$SQL" | wc -l | tr -d ' ')" -eq 19 ]] \
  || fail candidate_id_count_not_19

# Fifteen exact protected relations are recomputed, with only the frozen U43
# names removed from index/constraint subdocuments.
TARGET_SECTION="$(
  sed -n '/), target(ordinal,schema_name,relation_name) AS (/,/), presence AS (/p' "$SQL"
)"
[[ "$(grep -Ec "^    \\([0-9]+,'(app|public)','[a-z0-9_]+'\\),?$" <<<"$TARGET_SECTION")" -eq 15 ]] \
  || fail protected_relation_count_not_15
contains "$SQL" "x.name=con.conname"
contains "$SQL" "x.name=ci.relname"
contains "$SQL" "current_surface.manifest=stored_surface.manifest"

# Up exact object/dependency/meta identity.
[[ "$(grep -c "^    'CREATE UNIQUE INDEX " "$SQL")" -eq 19 ]] \
  || fail expected_indexdef_count_not_19
contains "$SQL" "v_class_count<>1"
contains "$SQL" "v_constraint_count<>(CASE WHEN v_i<19 THEN 1 ELSE 0 END)"
contains "$SQL" "v_internal_count<>1 OR v_internal_total<>1"
contains "$SQL" "'attached',v_i<19"
contains "$SQL" "v_live_manifest := v_candidates"
for identity in \
  v3_contract_sha256 release_id runner_sha256 migration_sha256 run_id evidence_sha256 \
  source_system_identifier source_database_name source_database_oid catalog_manifest; do
  contains "$SQL" "$identity"
done
if grep -Fq 'redesign_contract_sha256' "$SQL"; then
  fail stale_meta_contract_column
fi
if grep -Eq 'AND database_(name|oid)' "$SQL"; then
  fail stale_meta_source_column
fi
for migration_column in \
  v3_contract_sha256 release_id runner_sha256 migration_sha256 run_id \
  evidence_sha256 source_system_identifier source_database_name \
  source_database_oid catalog_manifest; do
  contains "$MIGRATION" "$migration_column"
  contains "$SQL" "$migration_column"
done
contains "$MIGRATION" "'attached', v_index<19"
contains "$SQL" "'attached',v_i<19"
if grep -Fq "'format','client-auth-00043-catalog-v3'" "$SQL"; then
  fail incompatible_meta_manifest_wrapper
fi

# Down requires exact parents, all 19 class/constraint names absent and meta
# absent. Protected/runtime/future gates run before the direction-specific DO.
contains "$SQL" "v_parent_count<>1 OR NOT v_parent_exact"
contains "$SQL" "v_class_count<>0 OR v_constraint_count<>0"
contains "$SQL" "to_regclass('app.client_auth_00043_meta') IS NOT NULL"
[[ "$(grep -c "SELECT count(\\*) FROM public\\." "$SQL")" -eq 8 ]] \
  || fail runtime_table_count_not_8
contains "$SQL" "a.attgenerated<>''"
contains "$SQL" "client-auth-00043-known-pre00044-v1"

# This is deliberately a static local mock. It cannot validate PostgreSQL 18
# catalog spelling, OID/dependency behavior, or the not-yet-frozen 00044 list.
contains "$DESIGN" "real_pg18=NOT_RUN"
contains "$DESIGN" "integration NO-GO"

printf 'client_auth_00043_post_goose_mock=PASS candidates=19 constraints=18 protected_relations=15 runtime_tables=8 real_pg18=NOT_RUN\n'
