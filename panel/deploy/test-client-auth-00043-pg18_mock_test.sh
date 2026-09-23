#!/usr/bin/env bash
# This mock proves only fail-closed/static harness behavior. It can never emit
# the real PG18 acceptance PASS marker.
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
HARNESS="$ROOT/deploy/test-client-auth-00043-pg18.sh"
RUNNER="$ROOT/deploy/run-client-auth-00043-indexes.sh"
MIGRATION="$ROOT/migrations/frozen-client-auth/00043_client_auth_existing_parent_indexes.sql"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-ca43-harness-mock.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT HUP INT TERM

fail() { printf 'client_auth_00043_pg18_mock=FAIL reason=%s\n' "$1" >&2; exit 1; }
sha_file() { sha256sum -- "$1" | awk '{print toupper($1)}'; }

[[ -f "$HARNESS" && ! -L "$HARNESS" ]] || fail harness_missing
[[ -f "$RUNNER" && ! -L "$RUNNER" ]] || fail runner_missing
[[ -f "$MIGRATION" && ! -L "$MIGRATION" ]] || fail migration_missing

# Missing implementation artifacts must fail closed before Docker mutation.
set +e
bash "$HARNESS" >"$TMP/missing.out" 2>"$TMP/missing.err"
rc=$?
set -e
[[ "$rc" -eq 78 ]] || fail "missing_artifact_status:$rc"
[[ ! -s "$TMP/missing.out" ]] || fail missing_artifact_published_pass

for required in \
  "native_linux_required" "linux_docker_engine_required" \
  "root_harness_required" "manifest_psql_major_18_required" "artifact_missing_or_unsafe" \
  "PLAN_SHA256='698b42ff" "PHASE_SHA256='7108291f" \
  "REDESIGN_SHA256='2d5015f43e" \
  "V3_CONTRACT_SHA256='e8c3237620" \
  "PLACEHOLDER_NO_GO" "trusted_release_manifest_placeholder_no_go" \
  "AEGIS_CA43_RELEASE_MANIFEST_FILE" "AEGIS_CA43_EXPECTED_RELEASE_MANIFEST_SHA256" \
  "PANDORA_CLIENT_AUTH_00043_RELEASE_MANIFEST_FILE" \
  "PANDORA_CLIENT_AUTH_00043_EXPECTED_RELEASE_MANIFEST_SHA256" \
  "post_goose_verifier_path" "post_goose_verifier_sha256" \
  "PANDORA_CLIENT_AUTH_00043_DOWN_EVIDENCE_OUT" \
  "PANDORA_CLIENT_AUTH_00043_PRIOR_UP_EVIDENCE_SHA256" \
  "PANDORA_CLIENT_AUTH_00043_CLEANUP_JOURNAL_FILE" \
  "family_lock_second_connection_not_blocked" \
  "pg_stat_progress_create_index" "pg_terminate_backend" \
  "direct_goose_up_bypassed_runner" "direct_goose_wrong_source_identity_accepted" \
  "direct_goose_down_removed_runner_objects" \
  "duplicate_left_invalid" "same_name_drift" "zero_row_drift" "attach_dependency_exact" \
  "attested_invalid_recovery_failed" "rollback_refusal" \
  "resume_up_keeps_unfinalized_waterline" "finalize_up_exact_waterline" \
  "partial_down_waterline_stays_43" "finalize_down_exact_waterline" \
  "missing_parent_down_u43_19" "missing_parent_down_drift" \
  "set_generated_column_future" "generated_future_refusal" \
  "set_runtime_future_row" "runtime_future_refusal" \
  "set_goose_00044_applied" "future_refusal execute-up" \
  "container_cleanup_not_confirmed" \
  "network_cleanup_not_confirmed"; do
  grep -Fq "$required" "$HARNESS" || fail "contract_surface_missing:$required"
done

# The V3 harness must never bypass the manifest by injecting executables,
# migrations, or TEST_MODE into the production runner.
for forbidden in \
  PANDORA_CLIENT_AUTH_00043_PSQL_BIN \
  PANDORA_CLIENT_AUTH_00043_GOOSE_BIN \
  PANDORA_CLIENT_AUTH_00043_MIGRATIONS_DIR \
  PANDORA_CLIENT_AUTH_00043_TEST_MODE; do
  ! grep -Fq "$forbidden" "$HARNESS" || fail "harness_forbidden_runner_env:$forbidden"
done

RUNNER_INTERFACE='V3_READY'
[[ "$(sha_file "$RUNNER")" == '509935A161D2716EDD4DA6FEEFE87775DE36D5DC1B3850BFF4DB94A942776DF7' ]] \
  || RUNNER_INTERFACE='NOT_READY'
[[ "$(sha_file "$MIGRATION")" == '5E8D9CA3B18B3D4DBA833B02395D94C9A27740E31746DF44ADE9673874018844' ]] \
  || RUNNER_INTERFACE='NOT_READY'
for command in status plan-up execute-up resume-up cleanup-invalid \
  finalize-up plan-down execute-down resume-down finalize-down; do
  grep -Eq "(^|[^A-Za-z0-9_-])${command}([^A-Za-z0-9_-]|$)" "$RUNNER" \
    || RUNNER_INTERFACE='NOT_READY'
done
for runner_surface in \
  "V3_CONTRACT_SHA='E8C3237620" \
  "envv RELEASE_MANIFEST_FILE" "envv EXPECTED_RELEASE_MANIFEST_SHA256" \
  "envv DOWN_EVIDENCE_OUT" "envv PRIOR_UP_EVIDENCE_SHA256" \
  "test_mode_forbidden" "root_required" \
  "format=client-auth-00043-evidence-v2" \
  "client-auth-00043-cleanup-journal-v1" \
  "evidence_line_count_invalid" "evidence_header_order_invalid" \
  "goose_exact_postcheck_failed" "expected_migration_sha256" "REMOVED_EXACT" \
  "parent_count=1" "is_generated='ALWAYS'" "runtime_rows=0"; do
  grep -Fq "$runner_surface" "$RUNNER" || RUNNER_INTERFACE='NOT_READY'
done
grep -Fq "RELEASE_ID\" == 'client-auth-00043-v3'" "$HARNESS" \
  || RUNNER_INTERFACE='NOT_READY'

for name in \
  subscriptions_tenant_id_id_user_id_key \
  devices_tenant_id_id_user_id_key \
  sessions_tenant_id_id_key \
  sessions_tenant_id_id_user_id_key \
  sessions_tenant_id_id_device_id_key \
  sessions_tenant_id_id_user_id_device_id_key \
  device_authorizations_tenant_id_id_key \
  device_authorizations_tenant_id_id_device_id_key \
  device_authorizations_tenant_user_code_mac_key \
  device_tokens_tenant_id_id_key \
  device_tokens_tenant_device_id_id_key \
  subscription_credentials_tenant_id_id_key \
  subscription_credentials_tenant_id_id_sub_user_key \
  config_bundles_tenant_id_id_key \
  refresh_tokens_tenant_id_id_key \
  refresh_tokens_tenant_family_id_id_key \
  refresh_tokens_tenant_family_session_device_id_key \
  refresh_tokens_tenant_family_generation_key \
  refresh_tokens_one_active_family; do
  grep -Fq "$name" "$HARNESS" || fail "allowlist_name_missing:$name"
done

if grep -Fq 'client_auth_00043_pg18=PASS' "$TMP/missing.out"; then
  fail mock_path_forged_real_pass
fi
printf 'client_auth_00043_pg18_mock=PASS scope=v3_fail_closed_and_static_only runner_interface=%s trusted_manifest=PLACEHOLDER_NO_GO real_pg18=NOT_RUN\n' \
  "$RUNNER_INTERFACE"
