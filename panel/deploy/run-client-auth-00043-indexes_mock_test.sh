#!/usr/bin/env bash
# Windows-safe local contract/negative gate. Real PostgreSQL 18 remains mandatory.
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUNNER="$ROOT/deploy/run-client-auth-00043-indexes.sh"
MIGRATION="$ROOT/migrations/frozen-client-auth/00043_client_auth_existing_parent_indexes.sql"
MANIFEST="$ROOT/deploy/client-auth-00043-v3.release-manifest"
V3="$ROOT/.ai-company/handoffs/client-auth-00043-runner-goose-redesign-v3-20260731.md"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/client-auth-00043-v3-mock.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT

if [[ ! -f "$V3" || -L "$V3" ]]; then
  printf 'client_auth_00043_v3_mock=NOT_RUN reason=handoff_artifact_missing\n'
  exit 77
fi

fail() { printf 'client_auth_00043_v3_mock=FAIL reason=%s\n' "$1" >&2; exit 1; }
contains() { grep -Fq -- "$2" "$1" || fail "missing:$2"; }

bash -n "$RUNNER" || fail runner_bash_syntax

[[ "$(sha256sum "$V3" | awk '{print toupper($1)}')" == E8C323762014B5F97D04CF861287026F0D3784D56B2324E1E6B0BC14D833DAC7 ]] \
  || fail v3_contract_hash

# Migration is control-plane only. Quoted catalog definitions do not count as DDL.
if grep -Eq '^[[:space:]]*(CREATE UNIQUE INDEX CONCURRENTLY|DROP INDEX CONCURRENTLY|ALTER TABLE .* (ADD|DROP) CONSTRAINT)' "$MIGRATION"; then
  fail migration_contains_data_plane_ddl
fi
contains "$MIGRATION" 'pg_advisory_xact_lock(420042,1)'
contains "$MIGRATION" 'v3_contract_sha256'
contains "$MIGRATION" 'down_evidence_sha256'
contains "$MIGRATION" "d.deptype='i'"
contains "$MIGRATION" 'Down refuses future/runtime evidence'
contains "$MIGRATION" 'Down parent relation mismatch'

# Plan output proves the frozen data-plane inventory without connecting to a DB.
bash "$RUNNER" plan-up >"$TMP/plan-up.sql" || fail plan_up_failed
bash "$RUNNER" plan-down >"$TMP/plan-down.sql" || fail plan_down_failed
[[ "$(grep -c '^CREATE UNIQUE INDEX CONCURRENTLY ' "$TMP/plan-up.sql")" -eq 19 ]] || fail up_cic_count
[[ "$(grep -c '^ALTER TABLE .* ADD CONSTRAINT .* UNIQUE USING INDEX ' "$TMP/plan-up.sql")" -eq 18 ]] || fail up_attach_count
[[ "$(grep -c '^ALTER TABLE .* DROP CONSTRAINT .* RESTRICT;' "$TMP/plan-down.sql")" -eq 18 ]] || fail down_constraint_count
[[ "$(grep -c '^ DROP INDEX CONCURRENTLY public.refresh_tokens_one_active_family RESTRICT;' "$TMP/plan-down.sql")" -eq 1 ]] || fail down_partial_count
contains "$TMP/plan-up.sql" 'FROM (VALUES(1)) seed(dummy) LEFT JOIN target ON true'
contains "$TMP/plan-up.sql" "ELSE 'DRIFT' END state"
contains "$TMP/plan-up.sql" "d.deptype='i'"
contains "$TMP/plan-down.sql" 'is_generated='
contains "$TMP/plan-down.sql" 'client_auth_00043_meta'

# Removed legacy CLI must fail with the frozen denial code.
set +e
bash "$RUNNER" --execute >"$TMP/old-cli.out" 2>&1
rc=$?
set -e
[[ "$rc" -eq 78 ]] || fail old_cli_not_denied

# TEST_MODE is checked before privilege/artifact access and can never widen production authority.
set +e
env PANDORA_CLIENT_AUTH_00043_TEST_MODE=1 bash "$RUNNER" execute-up >"$TMP/test-mode.out" 2>&1
rc=$?
set -e
[[ "$rc" -eq 78 ]] || fail test_mode_not_denied
contains "$TMP/test-mode.out" 'reason=test_mode_forbidden'

# On this Windows/Git-Bash host the real production root positive path is unavailable.
# When the current EUID is non-root we still execute the non-root denial.
nonroot_evidence=STATIC_ONLY
if [[ "$EUID" -ne 0 ]]; then
  set +e
  bash "$RUNNER" execute-up >"$TMP/nonroot.out" 2>&1
  rc=$?
  set -e
  [[ "$rc" -eq 78 ]] || fail nonroot_not_denied
  contains "$TMP/nonroot.out" 'reason=root_required'
  nonroot_evidence=EXECUTED
fi

# Canonical evidence V2 is reconstructed then strictly parsed; old optional grep success is forbidden.
contains "$RUNNER" 'validate_evidence_v2()'
contains "$RUNNER" 'evidence_line_count_invalid'
contains "$RUNNER" 'evidence_candidate_count_invalid'
contains "$RUNNER" 'evidence_header_order_invalid'
contains "$RUNNER" 'evidence_trailer_invalid'
if grep -Fq "grep '^evidence_' \"\$raw\" || true" "$RUNNER"; then fail optional_evidence_grep_present; fi
contains "$RUNNER" 'cleanup_journal_metadata_invalid'
contains "$RUNNER" 'format=client-auth-00043-cleanup-journal-v1'
contains "$RUNNER" 'post_goose_verifier_path'
contains "$RUNNER" 'post_goose_verifier_sha256'
contains "$RUNNER" '$((21 + RELEASE[inventory_count]))'
contains "$RUNNER" 'goose_version post_goose_verifier_path post_goose_verifier_sha256 inventory_count)'
contains "$RUNNER" 'verify-client-auth-00043-post-goose.sql'
contains "$RUNNER" 'expected_migration_sha256'
contains "$RUNNER" 'post_goose_verifier_changed_after_goose'
contains "$RUNNER" 'goose_exact_postcheck_failed'
contains "$RUNNER" '--file="$POST_GOOSE_VERIFIER"'
contains "$RUNNER" "FUTURE_GATE_CONTRACT='client-auth-00043-known-pre00044-v1'"
contains "$RUNNER" 'EXPECTED_RELEASE_MANIFEST_SHA256'
contains "$RUNNER" 'release_manifest_inventory_drift'
contains "$RUNNER" '"${RELEASE[release_id]:-}" == client-auth-00043-v3'
if grep -Fq 'postcheck="SELECT' "$RUNNER"; then fail legacy_inline_postcheck_present; fi
goose_line="$(grep -n 'GOOSE_DRIVER=postgres' "$RUNNER" | tail -n1 | cut -d: -f1)"
recheck_line="$(grep -n 'post_goose_verifier_changed_after_goose' "$RUNNER" | tail -n1 | cut -d: -f1)"
verifier_line="$(grep -n -- '--file="$POST_GOOSE_VERIFIER"' "$RUNNER" | tail -n1 | cut -d: -f1)"
[[ "$goose_line" =~ ^[0-9]+$ && "$recheck_line" =~ ^[0-9]+$ &&
   "$verifier_line" =~ ^[0-9]+$ && "$goose_line" -lt "$recheck_line" &&
   "$recheck_line" -lt "$verifier_line" ]] \
  || fail post_goose_verifier_call_order_invalid
for verifier_variable in \
  verify_direction expected_source_system_identifier expected_database_name \
  expected_database_oid expected_release_id expected_runner_sha256 \
  expected_migration_sha256 expected_run_id expected_evidence_sha256 \
  future_gate_contract; do
  [[ "$(grep -c -- "--set=\"${verifier_variable}=" "$RUNNER")" -eq 1 ]] \
    || fail "post_goose_variable_count_invalid:${verifier_variable}"
done

# Checked-in manifest must be incapable of authorizing any execution.
contains "$MANIFEST" 'status=PLACEHOLDER_NO_GO'
contains "$RUNNER" 'release_manifest_placeholder_no_go'

printf 'client_auth_00043_v3_mock=PASS scope=static_and_negative nonroot=%s real_pg18=NOT_RUN trusted_manifest=PLACEHOLDER_NO_GO\n' "$nonroot_evidence"
