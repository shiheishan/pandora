#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
PROBE="$ROOT/deploy/probe-client-auth-00043-pre00044-future-v2.sql"
EXACT="$ROOT/deploy/verify-client-auth-00043-post-goose.sql"

fail() { printf 'pre00044_future_gate_v2_mock=FAIL reason=%s\n' "$1" >&2; exit 1; }
contains() { grep -Fq -- "$2" "$1" || fail "missing:$2"; }

[[ -f "$PROBE" && ! -L "$PROBE" ]] || fail probe_missing_or_symlink
[[ -f "$EXACT" && ! -L "$EXACT" ]] || fail exact_verifier_missing_or_symlink
[[ "$(grep -c '^BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;$' "$PROBE")" -eq 1 ]] \
  || fail read_only_transaction_missing
[[ "$(grep -c '^ROLLBACK;$' "$PROBE")" -eq 1 ]] || fail rollback_missing

contains "$PROBE" "client_auth_pre00044_future_gate_v2=DENY reason=exact_target_catalog_manifest_not_integrated"
contains "$PROBE" '\quit 78'
contains "$PROBE" '\ir verify-client-auth-00043-post-goose.sql'
contains "$PROBE" "SELECT pg_catalog.pg_advisory_lock(420042,1);"
contains "$PROBE" "WHEN pg_catalog.pg_advisory_unlock(420042,1) THEN 1"
contains "$PROBE" "client_auth_pre00044_future_gate_v2=INTERNAL_DRAFT_CHECKS_COMPLETE exact_00043=stacked release_status=NOT_WIRED_NOT_APPROVED"
contains "$PROBE" "version_id>43"
contains "$PROBE" "client_auth_00044_meta"
contains "$PROBE" "client_auth_00044_legacy_evidence"
contains "$PROBE" "a.attgenerated<>''"
contains "$PROBE" "public_key_spki IS NOT NULL OR key_fingerprint IS NOT NULL"
contains "$PROBE" "authority IS NOT NULL OR family_id IS NOT NULL"
contains "$PROBE" "subscription_credentials_tenant_id_id_sub_user_binding_key"
contains "$PROBE" "devices_tenant_id_key_fingerprint_key"
contains "$PROBE" "expected_columns(table_name,column_count)"
contains "$PROBE" "expected_fks(table_name,fk_count)"
contains "$PROBE" "expected_user_triggers(table_name,trigger_name)"
contains "$PROBE" "NOT EXISTS (SELECT 1 FROM structural_drift)"
contains "$PROBE" "NOT EXISTS (SELECT 1 FROM semantic_backfill)"
contains "$PROBE" "legacy_revocation_at IS NOT NULL OR first_client_write_at IS NOT NULL"
contains "$PROBE" "p.proname ~ '^client_auth_0004[4-7]_'"

# The stacked exact verifier is the normative equality proof.  Keep these
# adversarially relevant dimensions pinned so the v2 wrapper cannot silently
# regress to counts/names-only inspection.
contains "$EXACT" "current_surface.manifest=stored_surface.manifest"
contains "$EXACT" "v3_contract_sha256=v_contract"
contains "$EXACT" "runner_sha256=current_setting('aegis.verify_00043.runner_sha256')"
contains "$EXACT" "migration_sha256=current_setting('aegis.verify_00043.migration_sha256')"
contains "$EXACT" "v_meta_manifest IS DISTINCT FROM v_live_manifest"
contains "$EXACT" "ix.indisvalid AND ix.indisready AND ix.indislive"
contains "$EXACT" "con.conindid=ic.oid"
contains "$EXACT" "pg_get_indexdef"
contains "$EXACT" "pg_catalog.aclexplode(c.relacl)"
contains "$EXACT" "pg_catalog.aclexplode(a.attacl)"

if grep -Eq '(^|[[:space:]])(INSERT|UPDATE|DELETE|TRUNCATE|ALTER|CREATE|DROP|GRANT|REVOKE)([[:space:]]|$)' "$PROBE"; then
  fail mutating_sql_present
fi

if grep -Fq 'client_auth_pre00044_future_gate_v2=PASS' "$PROBE"; then
  fail authorizing_pass_present
fi

deny_line="$(grep -n -m1 'exact_target_catalog_manifest_not_integrated' "$PROBE" | cut -d: -f1)"
quit_line="$(grep -n -m1 '^\\quit 78$' "$PROBE" | cut -d: -f1)"
lock_line="$(grep -n -m1 'SELECT pg_catalog.pg_advisory_lock(420042,1);' "$PROBE" | cut -d: -f1)"
include_line="$(grep -n -m1 '^\\ir verify-client-auth-00043-post-goose.sql$' "$PROBE" | cut -d: -f1)"
begin_line="$(grep -n -m1 '^BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;$' "$PROBE" | cut -d: -f1)"
rollback_line="$(grep -n -m1 '^ROLLBACK;$' "$PROBE" | cut -d: -f1)"
unlock_line="$(grep -n -m1 'WHEN pg_catalog.pg_advisory_unlock(420042,1) THEN 1' "$PROBE" | cut -d: -f1)"
[[ "$deny_line" -lt "$quit_line" && "$quit_line" -lt "$lock_line" ]] || fail fail_closed_order
[[ "$lock_line" -lt "$include_line" && "$include_line" -lt "$begin_line" ]] || fail verifier_order
[[ "$begin_line" -lt "$rollback_line" && "$rollback_line" -lt "$unlock_line" ]] || fail transaction_order

printf 'pre00044_future_gate_v2_mock=PASS contract=v2 fail_closed=true exact_target_manifest=NOT_INTEGRATED wired=false\n'
