#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

fail() {
  printf 'client_auth_trust_capsule_v1_linux=FAIL reason=%s\n' "$1" >&2
  exit 1
}

[[ "$(uname -s)" == Linux && "$(id -u)" == 0 ]] || fail linux_root_required
[[ $# -eq 2 ]] || fail usage_core_and_pathtrust_required
SOURCE_CORE="$1"
SOURCE_PATHTRUST="$2"
[[ -f "$SOURCE_CORE" && ! -L "$SOURCE_CORE" ]] || fail core_missing
[[ -f "$SOURCE_PATHTRUST" && ! -L "$SOURCE_PATHTRUST" ]] || fail pathtrust_missing

TEST_ROOT="$(mktemp -d /root/pandora-ca42-capsule.XXXXXX)" || fail test_root_create
cleanup() {
  local status=$?
  case "$TEST_ROOT" in
    /root/pandora-ca42-capsule.*) rm -rf -- "$TEST_ROOT" || status=1 ;;
    *) status=1 ;;
  esac
  exit "$status"
}
trap cleanup EXIT HUP INT TERM
chmod 0700 "$TEST_ROOT"
install -d -m 0700 "$TEST_ROOT/trusted" "$TEST_ROOT/evidence" "$TEST_ROOT/ledger" "$TEST_ROOT/ledger-other"
install -m 0500 "$SOURCE_CORE" "$TEST_ROOT/trusted/client-auth-attestation-v2.sh"
install -m 0500 "$SOURCE_PATHTRUST" "$TEST_ROOT/trusted/pandora-pathtrust"
CORE="$TEST_ROOT/trusted/client-auth-attestation-v2.sh"
PATHTRUST="$TEST_ROOT/trusted/pandora-pathtrust"

openssl genpkey -algorithm ED25519 -out "$TEST_ROOT/evidence/private.pem" >/dev/null 2>&1 || fail key_create
openssl pkey -in "$TEST_ROOT/evidence/private.pem" -pubout -out "$TEST_ROOT/evidence/public.pem" >/dev/null 2>&1 || fail public_key_create
chmod 0600 "$TEST_ROOT/evidence/private.pem" "$TEST_ROOT/evidence/public.pem"

NOW="$(date -u +%s)"
EXPIRES=$((NOW + 600))
NONCE="$(openssl rand -hex 32)"
CORE_SHA="$(sha256sum "$CORE" | awk '{print $1}')"
OPENSSL_SHA="$(sha256sum /usr/bin/openssl | awk '{print $1}')"
H1="$(printf one | sha256sum | awk '{print $1}')"
H2="$(printf two | sha256sum | awk '{print $1}')"
H3="$(printf three | sha256sum | awk '{print $1}')"
H4="$(printf four | sha256sum | awk '{print $1}')"
H5="$(printf five | sha256sum | awk '{print $1}')"
cat >"$TEST_ROOT/evidence/external-manifest.json" <<EOF
{
  "format": "client-auth-00042-external-manifest-v1",
  "status": "READY",
  "test_only": true
}
EOF
chmod 0600 "$TEST_ROOT/evidence/external-manifest.json"
H6="$(sha256sum "$TEST_ROOT/evidence/external-manifest.json" | awk '{print $1}')"

cat >"$TEST_ROOT/evidence/payload" <<EOF
format=client-auth-attestation-v2
signature_algorithm=ed25519
nonce=$NONCE
issued_at=$NOW
expires_at=$EXPIRES
release_id=release-ca42-test
source_container_id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
source_system_identifier=1234567890123456789
source_database_name=aegis
source_database_oid=16384
source_database_owner_oid=16385
source_database_owner_name=aegis_owner
source_goose_waterline=41
migration_set_sha256=$H1
client_auth_00042_sha256=$H2
goose_binary_sha256=$OPENSSL_SHA
preflight_runner_sha256=$CORE_SHA
globals_dump_sha256=$H3
database_dump_sha256=$H4
postgres_image_sha256=$H5
catalog_manifest_sha256=$H6
EOF
tail -n +6 "$TEST_ROOT/evidence/payload" >"$TEST_ROOT/evidence/expected"
chmod 0600 "$TEST_ROOT/evidence/payload" "$TEST_ROOT/evidence/expected" "$TEST_ROOT/evidence/external-manifest.json"
"$CORE" create --manifest "$TEST_ROOT/evidence/payload" \
  --private-key "$TEST_ROOT/evidence/private.pem" \
  --attestation "$TEST_ROOT/evidence/attestation" >/dev/null || fail attestation_create

CORE_DEV="$(stat -c '%d' "$CORE")"
ALLOWED_DEVS="$({ stat -c '%d' / /root "$TEST_ROOT" "$TEST_ROOT/trusted" "$CORE"; } | sort -nu | paste -sd, -)"
CHECK_OUTPUT="$($PATHTRUST check --path "$CORE" --expect-sha256 "$CORE_SHA" \
  --expect-mode 0500 --expect-device "$CORE_DEV" --allow-devices "$ALLOWED_DEVS")" || fail pathtrust_check
CORE_CHAIN="$(awk '{for(i=1;i<=NF;i++) if($i ~ /^chain_sha256=/){sub(/^chain_sha256=/,"",$i); print $i}}' <<<"$CHECK_OUTPUT")"
[[ "$CORE_CHAIN" =~ ^[0-9a-f]{64}$ ]] || fail core_chain_missing

ATTESTATION_SHA="$(sha256sum "$TEST_ROOT/evidence/attestation" | awk '{print $1}')"
EXPECTED_SHA="$(sha256sum "$TEST_ROOT/evidence/expected" | awk '{print $1}')"
PUBLIC_SHA="$(sha256sum "$TEST_ROOT/evidence/public.pem" | awk '{print $1}')"
EXTERNAL_SHA="$(sha256sum "$TEST_ROOT/evidence/external-manifest.json" | awk '{print $1}')"
LEDGER_REAL="$(cd "$TEST_ROOT/ledger" && pwd -P)"
LEDGER_SHA="$(printf '%s' "$LEDGER_REAL" | sha256sum | awk '{print $1}')"
cat >"$TEST_ROOT/evidence/capsule" <<EOF
format=client-auth-00042-trust-capsule-v1
transition=goose-41-to-42
release_id=release-ca42-test
release_run_id=run-ca42-test-1
core_sha256=$CORE_SHA
core_chain_sha256=$CORE_CHAIN
core_device=$CORE_DEV
core_mode=0500
attestation_sha256=$ATTESTATION_SHA
expected_sha256=$EXPECTED_SHA
public_key_sha256=$PUBLIC_SHA
external_manifest_sha256=$EXTERNAL_SHA
target_system_identifier=1234567890123456789
target_database_name=aegis
target_database_oid=16384
ledger_namespace=client-auth-00042-v1
ledger_directory_sha256=$LEDGER_SHA
EOF
chmod 0600 "$TEST_ROOT/evidence/capsule"
CAPSULE_SHA="$(sha256sum "$TEST_ROOT/evidence/capsule" | awk '{print $1}')"

TRUSTED_OUTPUT="$($PATHTRUST exec --path "$CORE" --expect-sha256 "$CORE_SHA" \
  --expect-mode 0500 --expect-device "$CORE_DEV" --allow-devices "$ALLOWED_DEVS" \
  --expect-chain-sha256 "$CORE_CHAIN" -- verify-trusted \
  --attestation "$TEST_ROOT/evidence/attestation" --public-key "$TEST_ROOT/evidence/public.pem" \
  --expected "$TEST_ROOT/evidence/expected" --external-manifest "$TEST_ROOT/evidence/external-manifest.json" \
  --ledger-dir "$TEST_ROOT/ledger" \
  --capsule "$TEST_ROOT/evidence/capsule" --expect-capsule-sha256 "$CAPSULE_SHA")" || fail trusted_verify
[[ "$TRUSTED_OUTPUT" == "client_auth_attestation_v2=TRUSTED capsule_sha256=$CAPSULE_SHA release_id=release-ca42-test release_run_id=run-ca42-test-1 ledger_namespace=client-auth-00042-v1" ]] \
  || fail trusted_output_invalid

set +e
WRONG_HASH_OUTPUT="$($PATHTRUST exec --path "$CORE" --expect-sha256 "$CORE_SHA" \
  --expect-mode 0500 --expect-device "$CORE_DEV" --allow-devices "$ALLOWED_DEVS" \
  --expect-chain-sha256 "$CORE_CHAIN" -- verify-trusted \
  --attestation "$TEST_ROOT/evidence/attestation" --public-key "$TEST_ROOT/evidence/public.pem" \
  --expected "$TEST_ROOT/evidence/expected" --external-manifest "$TEST_ROOT/evidence/external-manifest.json" \
  --ledger-dir "$TEST_ROOT/ledger" \
  --capsule "$TEST_ROOT/evidence/capsule" --expect-capsule-sha256 "$H1" 2>&1)"
WRONG_HASH_STATUS=$?
WRONG_LEDGER_OUTPUT="$($PATHTRUST exec --path "$CORE" --expect-sha256 "$CORE_SHA" \
  --expect-mode 0500 --expect-device "$CORE_DEV" --allow-devices "$ALLOWED_DEVS" \
  --expect-chain-sha256 "$CORE_CHAIN" -- verify-trusted \
  --attestation "$TEST_ROOT/evidence/attestation" --public-key "$TEST_ROOT/evidence/public.pem" \
  --expected "$TEST_ROOT/evidence/expected" --external-manifest "$TEST_ROOT/evidence/external-manifest.json" \
  --ledger-dir "$TEST_ROOT/ledger-other" \
  --capsule "$TEST_ROOT/evidence/capsule" --expect-capsule-sha256 "$CAPSULE_SHA" 2>&1)"
WRONG_LEDGER_STATUS=$?
DIRECT_OUTPUT="$($CORE verify-trusted --attestation "$TEST_ROOT/evidence/attestation" \
  --public-key "$TEST_ROOT/evidence/public.pem" --expected "$TEST_ROOT/evidence/expected" \
  --external-manifest "$TEST_ROOT/evidence/external-manifest.json" \
  --ledger-dir "$TEST_ROOT/ledger" --capsule "$TEST_ROOT/evidence/capsule" \
  --expect-capsule-sha256 "$CAPSULE_SHA" 2>&1)"
DIRECT_STATUS=$?
set -e
[[ "$WRONG_HASH_STATUS" -eq 78 && "$WRONG_HASH_OUTPUT" == *'reason=trust_capsule_hash_mismatch'* ]] \
  || fail wrong_capsule_hash_accepted
[[ "$WRONG_LEDGER_STATUS" -eq 78 && "$WRONG_LEDGER_OUTPUT" == *'reason=trust_capsule_ledger_directory_mismatch'* ]] \
  || fail wrong_ledger_accepted
[[ "$DIRECT_STATUS" -eq 78 && "$DIRECT_OUTPUT" == *'reason=pathtrust_context_required'* ]] \
  || fail direct_execution_accepted

printf 'client_auth_trust_capsule_v1_linux=PASS core_sha256=%s capsule_sha256=%s negatives=wrong_capsule_hash,wrong_ledger,direct_execution\n' \
  "$CORE_SHA" "$CAPSULE_SHA"
