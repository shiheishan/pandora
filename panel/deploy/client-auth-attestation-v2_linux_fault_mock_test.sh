#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUNNER="$ROOT/deploy/client-auth-attestation-v2_linux_fault_test.sh"
CLI="$ROOT/deploy/client-auth-attestation-v2.sh"

fail() {
  printf 'client_auth_attestation_v2_linux_fault_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}

bash -n "$RUNNER" || fail 'runner_syntax'
for literal in \
  '/usr/bin/unshare --mount --pid --fork --mount-proc' \
  '/usr/bin/mount --make-rprivate /' \
  '/usr/bin/mount --bind "$TMP/ln-wrapper" /usr/bin/ln' \
  '/usr/bin/mount --bind "$TMP/sync-wrapper" /usr/bin/sync' \
  'PANDORA_NATIVE_FAULT_PHASE=postlink-kill' \
  'PANDORA_NATIVE_FAULT_PHASE=attestation-file-sync-fail' \
  'PANDORA_NATIVE_FAULT_PHASE=attestation-dir-sync-fail' \
  'PANDORA_NATIVE_FAULT_PHASE=attestation-parent-rebind' \
  'PANDORA_NATIVE_FAULT_PHASE=attestation-target-substitute' \
  'create_file_sync_published_output' \
  'create_insecure_parent_published_output' \
  'private_key_insecure_parent_published_output' \
  'create_directory_sync_output_not_verifiable' \
  'create_parent_rebind_redirected_output' \
  'create_parent_rebind_fd_anchor_output_missing' \
  'create_target_substitution_not_exercised' \
  'postlink_nlink_not_two' \
  'postlink_nlink_not_normalized' \
  'PANDORA_NATIVE_FAULT_PHASE=ledger-sync-fail' \
  'sync_failure_receipt_missing' \
  'PANDORA_NATIVE_FAULT_PHASE=parent-rebind' \
  'parent_rebind_redirected_receipt' \
  'fd_anchor_receipt_missing' \
  'inner_tmp_residue'; do
  grep -Fq "$literal" "$RUNNER" || fail "contract_missing:$literal"
done
grep -Fq 'PANDORA_CA_V2_FAULT_INNER=1' "$RUNNER" || fail 'inner_mode_missing'
grep -Fq 'inner_pid_namespace_not_init' "$RUNNER" || fail 'pid_namespace_proof_missing'
grep -Fq 'inner_mount_namespace_not_isolated' "$RUNNER" || fail 'mount_namespace_proof_missing'
grep -Fq 'inner_capability_invalid' "$RUNNER" || fail 'inner_capability_proof_missing'
grep -Fq '/usr/bin/cp -- "$SELF" "$OUTER_TMP/runner.sh"' "$RUNNER" \
  || fail 'runner_snapshot_missing'
grep -Fq '/usr/bin/cp -- "$CLI" "$OUTER_TMP/client-auth-attestation-v2.sh"' "$RUNNER" \
  || fail 'core_snapshot_missing'
grep -Fq 'snapshot_hash_mismatch' "$RUNNER" || fail 'snapshot_hash_binding_missing'
grep -Fq 'runner_sha256=%s core_sha256=%s' "$RUNNER" || fail 'pass_hashes_missing'
grep -Fq '/usr/bin/umount /usr/bin/sync' "$RUNNER" || fail 'sync_unmount_missing'
grep -Fq '/usr/bin/umount /usr/bin/ln' "$RUNNER" || fail 'ln_unmount_missing'
grep -Fq '/usr/bin/rm -rf -- "$TMP"' "$RUNNER" || fail 'exact_cleanup_missing'
grep -Fq "TMP_BASE='/tmp'" "$CLI" || fail 'linux_fixed_tmp_base_missing'
grep -Fq "trusted_tmp_not_sticky" "$CLI" || fail 'linux_trusted_tmp_validation_missing'
grep -Fq 'OUTPUT_PARENT_ANCHOR="/proc/$$/fd/$OUTPUT_PARENT_FD"' "$CLI" \
  || fail 'create_parent_fd_anchor_missing'
grep -Fq 'attestation_output_parent_identity_changed' "$CLI" \
  || fail 'create_parent_identity_recheck_missing'
grep -Fq 'attestation_output_path_identity_changed' "$CLI" \
  || fail 'create_target_identity_recheck_missing'
grep -Fq 'attestation_output_publish_identity_mismatch' "$CLI" \
  || fail 'create_temp_target_identity_binding_missing'
grep -Fq 'attestation_output_identity_changed_after_hash' "$CLI" \
  || fail 'create_post_hash_identity_recheck_missing'
grep -Fq 'attestation_output_parent_chain_writable' "$CLI" \
  || fail 'create_parent_chain_writable_gate_missing'
grep -Fq 'PRIVATE_KEY_ANCHOR="/proc/$$/fd/$PRIVATE_KEY_FD"' "$CLI" \
  || fail 'private_key_fd_anchor_missing'
grep -Fq 'private_key_chain_writable' "$CLI" || fail 'private_key_chain_gate_missing'
grep -Fq 'created_signature_self_verification_failed' "$CLI" \
  || fail 'created_signature_self_verify_missing'
if grep -Fq '"${TMPDIR:-/tmp}/pandora-client-auth-attest-v2.' "$CLI"; then
  fail 'linux_caller_tmpdir_still_used'
fi
printf 'client_auth_attestation_v2_linux_fault_mock=PASS static=PASS native_linux_root=NOT_RUN\n'
