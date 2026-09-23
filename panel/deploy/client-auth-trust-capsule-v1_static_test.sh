#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CORE="$ROOT/deploy/client-auth-attestation-v2.sh"
PATHTRUST="$ROOT/cmd/pandora-pathtrust/main_linux.go"
README="$ROOT/deploy/client-auth-trust-capsule-v1.README.md"

fail() {
  printf 'client_auth_trust_capsule_v1_static=FAIL reason=%s\n' "$1" >&2
  exit 1
}

[[ -f "$CORE" && -f "$PATHTRUST" && -f "$README" ]] || fail source_missing
bash -n "$CORE" "$ROOT/deploy/client-auth-trust-capsule-v1_linux_test.sh" || fail bash_syntax
grep -Fq 'verify-trusted --attestation FILE' "$CORE" || fail trusted_mode_usage_missing
grep -Fq "format=client-auth-00042-trust-capsule-v1" "$README" || fail grammar_missing
grep -Fq "'client-auth-00042-trust-capsule-v1'" "$CORE" || fail format_gate_missing
grep -Fq "'goose-41-to-42'" "$CORE" || fail transition_gate_missing
grep -Fq "'client-auth-00042-v1'" "$CORE" || fail namespace_gate_missing
grep -Fq 'pathtrust_context_required' "$CORE" || fail pathtrust_context_gate_missing
grep -Fq 'trusted_verification_root_required' "$CORE" || fail root_gate_missing
grep -Fq 'PANDORA_TRUSTED_CHAIN_SHA256=' "$PATHTRUST" || fail pathtrust_chain_export_missing
grep -Fq 'trust_capsule_core_sha256_mismatch' "$CORE" || fail core_hash_gate_missing
grep -Fq 'trust_capsule_attestation_sha256_mismatch' "$CORE" || fail attestation_hash_gate_missing
grep -Fq 'trust_capsule_expected_sha256_mismatch' "$CORE" || fail expected_hash_gate_missing
grep -Fq 'trust_capsule_public_key_sha256_mismatch' "$CORE" || fail public_key_hash_gate_missing
grep -Fq 'trust_capsule_external_manifest_sha256_mismatch' "$CORE" || fail external_manifest_hash_gate_missing
grep -Fq 'trust_capsule_expected_external_manifest_binding_mismatch' "$CORE" || fail expected_external_binding_gate_missing
grep -Fq 'external_manifest_placeholder_forbidden' "$CORE" || fail placeholder_gate_missing
grep -Fq 'external_manifest_not_ready' "$CORE" || fail external_manifest_ready_gate_missing
grep -Fq 'trust_capsule_release_id_mismatch' "$CORE" || fail release_gate_missing
grep -Fq 'trust_capsule_target_system_identifier_mismatch' "$CORE" || fail target_system_gate_missing
grep -Fq 'trust_capsule_target_database_name_mismatch' "$CORE" || fail target_database_gate_missing
grep -Fq 'trust_capsule_target_database_oid_mismatch' "$CORE" || fail target_oid_gate_missing
grep -Fq 'trust_capsule_ledger_directory_mismatch' "$CORE" || fail ledger_gate_missing
grep -Fq 'client_auth_attestation_v2=TRUSTED' "$CORE" || fail trusted_success_marker_missing
if grep -Eq '(^|[/[:space:]])(psql|docker)([[:space:]]|$)|GOOSE_BIN|GOOSE_DBSTRING' "$CORE"; then
  fail unexpected_database_tool_reference
fi

printf 'client_auth_trust_capsule_v1_static=PASS mode=read_only zero_goose_execution=PASS\n'
