#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GENERATOR="$SCRIPT_DIR/generate-client-auth-00043-release-manifest.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/client-auth-00043-manifest-mock.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT HUP INT TERM
fail() { printf 'client_auth_00043_manifest_mock=FAIL reason=%s\n' "$1" >&2; exit 1; }

bash "$GENERATOR" --placeholder "$TMP/placeholder"
cmp -s "$TMP/placeholder" "$SCRIPT_DIR/client-auth-00043-v3.release-manifest" || fail placeholder_not_canonical
grep -Fq 'status=PLACEHOLDER_NO_GO' "$TMP/placeholder" || fail placeholder_not_no_go
if grep -q '^migration=' "$TMP/placeholder"; then fail placeholder_invented_inventory; fi
grep -Fq 'POST_GOOSE_VERIFIER' "$GENERATOR" || fail verifier_input_missing
grep -Fq 'post_goose_verifier_path=' "$GENERATOR" || fail verifier_path_manifest_missing
grep -Fq 'post_goose_verifier_sha256=' "$GENERATOR" || fail verifier_sha_manifest_missing
grep -Fq 'validate_trusted_artifact "$POST_GOOSE_VERIFIER_REAL"' "$GENERATOR" \
  || fail verifier_metadata_gate_missing
goose_version_line="$(grep -n "printf 'goose_version=" "$GENERATOR" | cut -d: -f1)"
verifier_path_line="$(grep -n "printf 'post_goose_verifier_path=" "$GENERATOR" | cut -d: -f1)"
verifier_sha_line="$(grep -n "printf 'post_goose_verifier_sha256=" "$GENERATOR" | cut -d: -f1)"
inventory_count_line="$(grep -n "printf 'inventory_count=" "$GENERATOR" | cut -d: -f1)"
[[ "$goose_version_line" -lt "$verifier_path_line" &&
   "$verifier_path_line" -lt "$verifier_sha_line" &&
   "$verifier_sha_line" -lt "$inventory_count_line" ]] \
  || fail verifier_manifest_order_invalid

set +e
bash "$GENERATOR" --generate "$TMP/trusted" >"$TMP/generate.out" 2>&1
status=$?
set -e
[[ "$status" -eq 78 ]] || fail unapproved_generation_not_rejected
[[ ! -e "$TMP/trusted" ]] || fail rejected_generation_published_file

set +e
bash "$GENERATOR" --placeholder "$TMP/placeholder" >"$TMP/clobber.out" 2>&1
status=$?
set -e
[[ "$status" -eq 78 ]] || fail no_clobber_not_enforced

printf 'client_auth_00043_manifest_mock=PASS placeholder_no_go=1 trusted_generation_refused=1\n'
