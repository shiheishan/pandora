#!/usr/bin/env bash
# Offline Ed25519 attestation signer/verifier/one-time consumer for CLIENT-AUTH-00042.
# Intentionally not wired into migrate, release, or preflight runners.
set -Eeuo pipefail
umask 077

readonly EXIT_DENIED=78
readonly FORMAT='client-auth-attestation-v2'
readonly SIGNATURE_ALGORITHM='ed25519'

deny() {
  printf 'client_auth_attestation_v2=DENY reason=%s\n' "$1" >&2
  exit "$EXIT_DENIED"
}

usage() {
  printf '%s\n' \
    'usage:' \
    '  client-auth-attestation-v2.sh create  --manifest FILE --private-key FILE --attestation FILE' \
    '  client-auth-attestation-v2.sh verify  --attestation FILE --public-key FILE --expected FILE' \
    '  client-auth-attestation-v2.sh verify-trusted --attestation FILE --public-key FILE --expected FILE --external-manifest FILE --ledger-dir DIR --capsule FILE --expect-capsule-sha256 HEX' \
    '  client-auth-attestation-v2.sh consume --attestation FILE --public-key FILE --expected FILE --ledger-dir DIR' \
    '  client-auth-attestation-v2.sh consume-or-confirm --attestation FILE --public-key FILE --expected FILE --ledger-dir DIR' >&2
  exit "$EXIT_DENIED"
}

[[ $# -ge 1 ]] || usage
MODE="$1"
shift

MANIFEST=''
PRIVATE_KEY=''
PUBLIC_KEY=''
ATTESTATION=''
EXPECTED=''
EXTERNAL_MANIFEST=''
LEDGER_DIR=''
CAPSULE=''
EXPECTED_CAPSULE_SHA256=''
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || usage
  case "$1" in
    --manifest) MANIFEST="$2" ;;
    --private-key) PRIVATE_KEY="$2" ;;
    --public-key) PUBLIC_KEY="$2" ;;
    --attestation) ATTESTATION="$2" ;;
    --expected) EXPECTED="$2" ;;
    --external-manifest) EXTERNAL_MANIFEST="$2" ;;
    --ledger-dir) LEDGER_DIR="$2" ;;
    --capsule) CAPSULE="$2" ;;
    --expect-capsule-sha256) EXPECTED_CAPSULE_SHA256="$2" ;;
    *) usage ;;
  esac
  shift 2
done

case "$MODE" in
  create)
    [[ -n "$MANIFEST" && -n "$PRIVATE_KEY" && -n "$ATTESTATION" ]] || usage
    [[ -z "$PUBLIC_KEY" && -z "$EXPECTED" && -z "$EXTERNAL_MANIFEST" && -z "$LEDGER_DIR" \
      && -z "$CAPSULE" && -z "$EXPECTED_CAPSULE_SHA256" ]] || usage
    ;;
  verify)
    [[ -n "$ATTESTATION" && -n "$PUBLIC_KEY" && -n "$EXPECTED" ]] || usage
    [[ -z "$MANIFEST" && -z "$PRIVATE_KEY" && -z "$EXTERNAL_MANIFEST" && -z "$LEDGER_DIR" && -z "$CAPSULE" \
      && -z "$EXPECTED_CAPSULE_SHA256" ]] || usage
    ;;
  verify-trusted)
    [[ -n "$ATTESTATION" && -n "$PUBLIC_KEY" && -n "$EXPECTED" && -n "$EXTERNAL_MANIFEST" && -n "$LEDGER_DIR" \
      && -n "$CAPSULE" && -n "$EXPECTED_CAPSULE_SHA256" ]] || usage
    [[ -z "$MANIFEST" && -z "$PRIVATE_KEY" ]] || usage
    ;;
  consume|consume-or-confirm)
    [[ -n "$ATTESTATION" && -n "$PUBLIC_KEY" && -n "$EXPECTED" && -n "$LEDGER_DIR" ]] || usage
    [[ -z "$MANIFEST" && -z "$PRIVATE_KEY" && -z "$EXTERNAL_MANIFEST" && -z "$CAPSULE" \
      && -z "$EXPECTED_CAPSULE_SHA256" ]] || usage
    ;;
  *) usage ;;
esac

for path_value in "$MANIFEST" "$PRIVATE_KEY" "$PUBLIC_KEY" "$ATTESTATION" "$EXPECTED" "$EXTERNAL_MANIFEST" "$LEDGER_DIR" "$CAPSULE"; do
  [[ "$path_value" != *$'\n'* && "$path_value" != *$'\r'* ]] || deny 'path_contains_newline'
done

[[ -x /usr/bin/uname ]] || deny 'platform_tool_missing'
TOOL_PLATFORM="$(/usr/bin/uname -s 2>/dev/null)" || deny 'platform_detection_failed'
case "$TOOL_PLATFORM" in
  Linux)
    export PATH='/usr/bin:/bin'
    OPENSSL_BIN='/usr/bin/openssl'
    SHA256_BIN='/usr/bin/sha256sum'
    STAT_BIN='/usr/bin/stat'
    DATE_BIN='/usr/bin/date'
    LN_BIN='/usr/bin/ln'
    SYNC_BIN='/usr/bin/sync'
    MKTEMP_BIN='/usr/bin/mktemp'
    RM_BIN='/usr/bin/rm'
    TMP_BASE='/tmp'
    ;;
  MINGW*|MSYS*|CYGWIN*)
    # Test-only portability branch. Release authority is Linux and uses the
    # fixed root-owned tool paths above instead of caller-controlled PATH.
    OPENSSL_BIN="$(command -v openssl 2>/dev/null)" || deny 'openssl_not_found'
    SHA256_BIN="$(command -v sha256sum 2>/dev/null)" || deny 'sha256sum_not_found'
    STAT_BIN="$(command -v stat 2>/dev/null)" || deny 'stat_not_found'
    DATE_BIN="$(command -v date 2>/dev/null)" || deny 'date_not_found'
    LN_BIN="$(command -v ln 2>/dev/null)" || deny 'ln_not_found'
    SYNC_BIN="$(command -v sync 2>/dev/null)" || deny 'sync_not_found'
    MKTEMP_BIN="$(command -v mktemp 2>/dev/null)" || deny 'mktemp_not_found'
    RM_BIN="$(command -v rm 2>/dev/null)" || deny 'rm_not_found'
    TMP_BASE="${TMPDIR:-/tmp}"
    ;;
  *) deny 'platform_unsupported' ;;
esac
[[ -x "$OPENSSL_BIN" && -x "$SHA256_BIN" && -x "$STAT_BIN" && -x "$DATE_BIN" \
  && -x "$LN_BIN" && -x "$SYNC_BIN" && -x "$MKTEMP_BIN" && -x "$RM_BIN" ]] \
  || deny 'required_tool_not_executable'

if [[ "$TOOL_PLATFORM" == 'Linux' ]]; then
  [[ -d "$TMP_BASE" && ! -L "$TMP_BASE" ]] || deny 'trusted_tmp_invalid'
  TMP_BASE_METADATA="$($STAT_BIN -c '%u:%a' -- "$TMP_BASE" 2>/dev/null)" \
    || deny 'trusted_tmp_stat_failed'
  TMP_BASE_OWNER="${TMP_BASE_METADATA%%:*}"
  TMP_BASE_MODE="${TMP_BASE_METADATA#*:}"
  [[ "$TMP_BASE_OWNER" == '0' && "$TMP_BASE_MODE" =~ ^[0-7]{3,4}$ ]] \
    || deny 'trusted_tmp_metadata_invalid'
  (( (8#$TMP_BASE_MODE & 01000) == 01000 )) || deny 'trusted_tmp_not_sticky'
fi

run_openssl() {
  env -i PATH=/usr/bin:/bin HOME=/nonexistent OPENSSL_CONF=/dev/null \
    "$OPENSSL_BIN" "$@"
}

TMP_DIR="$("$MKTEMP_BIN" -d "$TMP_BASE/pandora-client-auth-attest-v2.XXXXXX")" \
  || deny 'temporary_directory_failed'
OUTPUT_TMP=''
cleanup() {
  local status=$?
  if [[ -n "$OUTPUT_TMP" && -f "$OUTPUT_TMP" && ! -L "$OUTPUT_TMP" ]]; then
    "$RM_BIN" -f -- "$OUTPUT_TMP" || status="$EXIT_DENIED"
  fi
  case "$TMP_DIR" in
    "$TMP_BASE"/pandora-client-auth-attest-v2.*)
      "$RM_BIN" -rf -- "$TMP_DIR" || status="$EXIT_DENIED"
      ;;
    *) status="$EXIT_DENIED" ;;
  esac
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

readonly -a PAYLOAD_KEYS=(
  format signature_algorithm nonce issued_at expires_at release_id
  source_container_id source_system_identifier source_database_name
  source_database_oid source_database_owner_oid source_database_owner_name
  source_goose_waterline migration_set_sha256 client_auth_00042_sha256
  goose_binary_sha256 preflight_runner_sha256 globals_dump_sha256
  database_dump_sha256 postgres_image_sha256 catalog_manifest_sha256
)
readonly -a EXPECTED_KEYS=(
  release_id source_container_id source_system_identifier source_database_name
  source_database_oid source_database_owner_oid source_database_owner_name
  source_goose_waterline migration_set_sha256 client_auth_00042_sha256
  goose_binary_sha256 preflight_runner_sha256 globals_dump_sha256
  database_dump_sha256 postgres_image_sha256 catalog_manifest_sha256
)
readonly -a RECEIPT_KEYS=(
  format transition nonce release_id attestation_sha256 expected_sha256
  public_key_sha256 consumed_at
)
readonly -a CAPSULE_KEYS=(
  format transition release_id release_run_id
  core_sha256 core_chain_sha256 core_device core_mode
  attestation_sha256 expected_sha256 public_key_sha256 external_manifest_sha256
  target_system_identifier target_database_name target_database_oid
  ledger_namespace ledger_directory_sha256
)
declare -a PAYLOAD_VALUES=()
declare -a EXPECTED_VALUES=()
declare -a RECEIPT_VALUES=()
declare -a CAPSULE_VALUES=()

regular_readable_file() {
  [[ -f "$1" && ! -L "$1" && -r "$1" ]]
}

safe_token() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]
}

safe_database_identifier() {
  [[ "$1" =~ ^[A-Za-z_][A-Za-z0-9_.-]{0,62}$ ]]
}

safe_epoch() {
  [[ "$1" =~ ^[1-9][0-9]{0,9}$ ]]
}

safe_oid() {
  [[ "$1" =~ ^[1-9][0-9]{0,9}$ ]]
}

safe_system_identifier() {
  [[ "$1" =~ ^[1-9][0-9]{0,19}$ ]]
}

safe_sha256() {
  [[ "$1" =~ ^[0-9a-f]{64}$ ]]
}

validate_payload_values() {
  [[ "${PAYLOAD_VALUES[0]}" == "$FORMAT" ]] || deny 'payload_format_invalid'
  [[ "${PAYLOAD_VALUES[1]}" == "$SIGNATURE_ALGORITHM" ]] || deny 'signature_algorithm_invalid'
  [[ "${PAYLOAD_VALUES[2]}" =~ ^[0-9a-f]{64}$ ]] || deny 'nonce_invalid'
  safe_epoch "${PAYLOAD_VALUES[3]}" || deny 'issued_at_invalid'
  safe_epoch "${PAYLOAD_VALUES[4]}" || deny 'expires_at_invalid'
  (( 10#${PAYLOAD_VALUES[4]} > 10#${PAYLOAD_VALUES[3]} )) || deny 'validity_window_invalid'
  (( 10#${PAYLOAD_VALUES[4]} - 10#${PAYLOAD_VALUES[3]} <= 3600 )) || deny 'validity_window_too_long'
  safe_token "${PAYLOAD_VALUES[5]}" || deny 'release_id_invalid'
  [[ "${PAYLOAD_VALUES[6]}" =~ ^[0-9a-f]{64}$ ]] || deny 'source_container_id_invalid'
  safe_system_identifier "${PAYLOAD_VALUES[7]}" || deny 'source_system_identifier_invalid'
  safe_database_identifier "${PAYLOAD_VALUES[8]}" || deny 'source_database_name_invalid'
  safe_oid "${PAYLOAD_VALUES[9]}" || deny 'source_database_oid_invalid'
  safe_oid "${PAYLOAD_VALUES[10]}" || deny 'source_database_owner_oid_invalid'
  safe_database_identifier "${PAYLOAD_VALUES[11]}" || deny 'source_database_owner_name_invalid'
  [[ "${PAYLOAD_VALUES[12]}" == '41' ]] || deny 'source_goose_waterline_not_exact_41'
  local index
  for index in {13..20}; do
    safe_sha256 "${PAYLOAD_VALUES[$index]}" || deny "payload_sha256_invalid:${PAYLOAD_KEYS[$index]}"
  done
}

load_payload_file() {
  local source_file="$1" line index key value canonical="$TMP_DIR/payload.canonical"
  regular_readable_file "$source_file" || deny 'payload_file_invalid'
  mapfile -t payload_lines <"$source_file" || deny 'payload_read_failed'
  [[ "${#payload_lines[@]}" -eq "${#PAYLOAD_KEYS[@]}" ]] || deny 'payload_line_count_invalid'
  PAYLOAD_VALUES=()
  for index in "${!PAYLOAD_KEYS[@]}"; do
    line="${payload_lines[$index]}"
    key="${PAYLOAD_KEYS[$index]}"
    [[ "$line" == "$key="* ]] || deny "payload_key_order_invalid:${key}"
    value="${line#*=}"
    [[ -n "$value" && "$value" != *$'\r'* ]] || deny "payload_value_invalid:${key}"
    PAYLOAD_VALUES+=("$value")
  done
  validate_payload_values
  : >"$canonical"
  for index in "${!PAYLOAD_KEYS[@]}"; do
    printf '%s=%s\n' "${PAYLOAD_KEYS[$index]}" "${PAYLOAD_VALUES[$index]}" >>"$canonical"
  done
  cmp -s -- "$source_file" "$canonical" || deny 'payload_not_canonical'
}

load_attestation() {
  local source_file="$1" line index key payload="$TMP_DIR/payload.from-attestation"
  regular_readable_file "$source_file" || deny 'attestation_file_invalid'
  mapfile -t attestation_lines <"$source_file" || deny 'attestation_read_failed'
  [[ "${#attestation_lines[@]}" -eq $((${#PAYLOAD_KEYS[@]} + 1)) ]] \
    || deny 'attestation_line_count_invalid'
  : >"$payload"
  for index in "${!PAYLOAD_KEYS[@]}"; do
    line="${attestation_lines[$index]}"
    key="${PAYLOAD_KEYS[$index]}"
    [[ "$line" == "$key="* ]] || deny "attestation_key_order_invalid:${key}"
    printf '%s\n' "$line" >>"$payload"
  done
  SIGNATURE_B64="${attestation_lines[${#PAYLOAD_KEYS[@]}]}"
  [[ "$SIGNATURE_B64" =~ ^signature_b64=([A-Za-z0-9+/]{86}==)$ ]] \
    || deny 'signature_encoding_invalid'
  SIGNATURE_B64="${BASH_REMATCH[1]}"
  load_payload_file "$payload"
  {
    cat "$payload"
    printf 'signature_b64=%s\n' "$SIGNATURE_B64"
  } >"$TMP_DIR/attestation.canonical"
  cmp -s -- "$source_file" "$TMP_DIR/attestation.canonical" || deny 'attestation_not_canonical'
}

load_expected() {
  local source_file="$1" line index key value canonical="$TMP_DIR/expected.canonical"
  regular_readable_file "$source_file" || deny 'expected_file_invalid'
  mapfile -t expected_lines <"$source_file" || deny 'expected_read_failed'
  [[ "${#expected_lines[@]}" -eq "${#EXPECTED_KEYS[@]}" ]] || deny 'expected_line_count_invalid'
  EXPECTED_VALUES=()
  : >"$canonical"
  for index in "${!EXPECTED_KEYS[@]}"; do
    line="${expected_lines[$index]}"
    key="${EXPECTED_KEYS[$index]}"
    [[ "$line" == "$key="* ]] || deny "expected_key_order_invalid:${key}"
    value="${line#*=}"
    [[ -n "$value" && "$value" != *$'\r'* ]] || deny "expected_value_invalid:${key}"
    EXPECTED_VALUES+=("$value")
    printf '%s=%s\n' "$key" "$value" >>"$canonical"
  done
  cmp -s -- "$source_file" "$canonical" || deny 'expected_not_canonical'
}

load_trust_capsule() {
  local source_file="$1" line index key value canonical="$TMP_DIR/capsule.canonical"
  regular_readable_file "$source_file" || deny 'trust_capsule_file_invalid'
  mapfile -t capsule_lines <"$source_file" || deny 'trust_capsule_read_failed'
  [[ "${#capsule_lines[@]}" -eq "${#CAPSULE_KEYS[@]}" ]] || deny 'trust_capsule_line_count_invalid'
  CAPSULE_VALUES=()
  : >"$canonical"
  for index in "${!CAPSULE_KEYS[@]}"; do
    line="${capsule_lines[$index]}"
    key="${CAPSULE_KEYS[$index]}"
    [[ "$line" == "$key="* ]] || deny "trust_capsule_key_order_invalid:${key}"
    value="${line#*=}"
    [[ -n "$value" && "$value" != *$'\r'* ]] || deny "trust_capsule_value_invalid:${key}"
    CAPSULE_VALUES+=("$value")
    printf '%s=%s\n' "$key" "$value" >>"$canonical"
  done
  cmp -s -- "$source_file" "$canonical" || deny 'trust_capsule_not_canonical'

  [[ "${CAPSULE_VALUES[0]}" == 'client-auth-00042-trust-capsule-v1' ]] || deny 'trust_capsule_format_invalid'
  [[ "${CAPSULE_VALUES[1]}" == 'goose-41-to-42' ]] || deny 'trust_capsule_transition_invalid'
  safe_token "${CAPSULE_VALUES[2]}" || deny 'trust_capsule_release_id_invalid'
  safe_token "${CAPSULE_VALUES[3]}" || deny 'trust_capsule_release_run_id_invalid'
  for index in 4 5 8 9 10 11 16; do
    safe_sha256 "${CAPSULE_VALUES[$index]}" || deny "trust_capsule_sha256_invalid:${CAPSULE_KEYS[$index]}"
  done
  [[ "${CAPSULE_VALUES[6]}" =~ ^[1-9][0-9]{0,19}$ ]] || deny 'trust_capsule_core_device_invalid'
  [[ "${CAPSULE_VALUES[7]}" == '0500' ]] || deny 'trust_capsule_core_mode_invalid'
  safe_system_identifier "${CAPSULE_VALUES[12]}" || deny 'trust_capsule_target_system_identifier_invalid'
  safe_database_identifier "${CAPSULE_VALUES[13]}" || deny 'trust_capsule_target_database_name_invalid'
  safe_oid "${CAPSULE_VALUES[14]}" || deny 'trust_capsule_target_database_oid_invalid'
  safe_token "${CAPSULE_VALUES[15]}" || deny 'trust_capsule_ledger_namespace_invalid'
  [[ "${CAPSULE_VALUES[15]}" == 'client-auth-00042-v1' ]] || deny 'trust_capsule_ledger_namespace_unsupported'

  safe_sha256 "$EXPECTED_CAPSULE_SHA256" || deny 'expected_trust_capsule_sha256_invalid'
  CAPSULE_SHA256="$($SHA256_BIN "$canonical" | awk '{print $1}')" || deny 'trust_capsule_hash_failed'
  [[ "$CAPSULE_SHA256" == "$EXPECTED_CAPSULE_SHA256" ]] || deny 'trust_capsule_hash_mismatch'
}

load_external_manifest_snapshot() {
  local source_file="$1" snapshot="$TMP_DIR/external-manifest.snapshot"
  regular_readable_file "$source_file" || deny 'external_manifest_file_invalid'
  cat -- "$source_file" >"$snapshot" || deny 'external_manifest_snapshot_failed'
  chmod 0600 "$snapshot" || deny 'external_manifest_snapshot_chmod_failed'
  [[ -s "$snapshot" ]] || deny 'external_manifest_empty'
  EXTERNAL_MANIFEST_SHA256="$($SHA256_BIN "$snapshot" | awk '{print $1}')" \
    || deny 'external_manifest_hash_failed'
  safe_sha256 "$EXTERNAL_MANIFEST_SHA256" || deny 'external_manifest_hash_invalid'
  [[ "$EXTERNAL_MANIFEST_SHA256" != '67e620dee3b75bcc95e4209aae70987c0ceb37416fb961c7d3c5bf0b31201581' ]] \
    || deny 'external_manifest_placeholder_forbidden'
  [[ "$(grep -Ec '^[[:space:]]*"format":[[:space:]]*"client-auth-00042-external-manifest-v1",?[[:space:]]*$' "$snapshot")" -eq 1 ]] \
    || deny 'external_manifest_format_invalid'
  [[ "$(grep -Ec '^[[:space:]]*"status":[[:space:]]*"READY",?[[:space:]]*$' "$snapshot")" -eq 1 ]] \
    || deny 'external_manifest_not_ready'
}

payload_value_for_key() {
  local wanted="$1" index
  for index in "${!PAYLOAD_KEYS[@]}"; do
    if [[ "${PAYLOAD_KEYS[$index]}" == "$wanted" ]]; then
      printf '%s' "${PAYLOAD_VALUES[$index]}"
      return 0
    fi
  done
  return 1
}

verify_expected_binding() {
  local index key actual
  load_expected "$EXPECTED"
  for index in "${!EXPECTED_KEYS[@]}"; do
    key="${EXPECTED_KEYS[$index]}"
    actual="$(payload_value_for_key "$key")" || deny "payload_binding_missing:${key}"
    [[ "$actual" == "${EXPECTED_VALUES[$index]}" ]] || deny "expected_binding_mismatch:${key}"
  done
}

verify_public_key_type() {
  local public_key="$1"
  regular_readable_file "$public_key" || deny 'public_key_invalid'
  run_openssl pkey -pubin -in "$public_key" -text_pub -noout \
    >"$TMP_DIR/public-key.info" 2>/dev/null || deny 'public_key_parse_failed'
  grep -Eqi 'ED25519' "$TMP_DIR/public-key.info" || deny 'public_key_not_ed25519'
}

verify_signature() {
  PUBLIC_KEY_SNAPSHOT="$TMP_DIR/public-key.input"
  regular_readable_file "$PUBLIC_KEY" || deny 'public_key_invalid'
  cat -- "$PUBLIC_KEY" >"$PUBLIC_KEY_SNAPSHOT" || deny 'public_key_snapshot_failed'
  chmod 0600 "$PUBLIC_KEY_SNAPSHOT" || deny 'public_key_snapshot_chmod_failed'
  verify_public_key_type "$PUBLIC_KEY_SNAPSHOT"
  printf '%s' "$SIGNATURE_B64" | run_openssl base64 -d -A \
    >"$TMP_DIR/signature.bin" 2>/dev/null || deny 'signature_decode_failed'
  [[ "$(wc -c <"$TMP_DIR/signature.bin")" -eq 64 ]] || deny 'signature_length_invalid'
  run_openssl pkeyutl -verify -rawin -pubin -inkey "$PUBLIC_KEY_SNAPSHOT" \
    -sigfile "$TMP_DIR/signature.bin" -in "$TMP_DIR/payload.from-attestation" \
    >/dev/null 2>&1 || deny 'signature_verification_failed'
}

verify_current_time_window() {
  local now issued expires
  now="$($DATE_BIN -u +%s 2>/dev/null)" || deny 'clock_unavailable'
  safe_epoch "$now" || deny 'clock_invalid'
  issued="${PAYLOAD_VALUES[3]}"
  expires="${PAYLOAD_VALUES[4]}"
  (( 10#$issued <= 10#$now )) || deny 'attestation_not_yet_valid'
  (( 10#$now < 10#$expires )) || deny 'attestation_expired'
}

if [[ "$MODE" == 'create' ]]; then
  load_payload_file "$MANIFEST"
  regular_readable_file "$PRIVATE_KEY" || deny 'private_key_invalid'
  PRIVATE_KEY_ANCHOR="$PRIVATE_KEY"
  if [[ "$TOOL_PLATFORM" == 'Linux' ]]; then
    [[ "$EUID" -eq 0 ]] || deny 'attestation_create_root_required'
    [[ "$PRIVATE_KEY" == /* ]] || deny 'private_key_path_not_absolute'
    PRIVATE_KEY_PARENT="$(dirname -- "$PRIVATE_KEY")"
    [[ -d "$PRIVATE_KEY_PARENT" && ! -L "$PRIVATE_KEY_PARENT" ]] \
      || deny 'private_key_parent_invalid'
    PRIVATE_KEY_PARENT_REAL="$(cd "$PRIVATE_KEY_PARENT" && pwd -P)" \
      || deny 'private_key_parent_resolution_failed'
    PRIVATE_KEY_BASENAME="$(basename -- "$PRIVATE_KEY")"
    if [[ "$PRIVATE_KEY_PARENT_REAL" == '/' ]]; then
      PRIVATE_KEY_CANONICAL="/$PRIVATE_KEY_BASENAME"
    else
      PRIVATE_KEY_CANONICAL="$PRIVATE_KEY_PARENT_REAL/$PRIVATE_KEY_BASENAME"
    fi
    [[ "$PRIVATE_KEY" == "$PRIVATE_KEY_CANONICAL" ]] || deny 'private_key_path_not_canonical'
    PRIVATE_CHAIN_CURRENT="$PRIVATE_KEY_PARENT_REAL"
    while :; do
      [[ -d "$PRIVATE_CHAIN_CURRENT" && ! -L "$PRIVATE_CHAIN_CURRENT" ]] \
        || deny 'private_key_chain_component_invalid'
      PRIVATE_CHAIN_METADATA="$($STAT_BIN -c '%u:%a' -- "$PRIVATE_CHAIN_CURRENT" 2>/dev/null)" \
        || deny 'private_key_chain_stat_failed'
      PRIVATE_CHAIN_OWNER="${PRIVATE_CHAIN_METADATA%%:*}"
      PRIVATE_CHAIN_MODE="${PRIVATE_CHAIN_METADATA#*:}"
      [[ "$PRIVATE_CHAIN_OWNER" == '0' && "$PRIVATE_CHAIN_MODE" =~ ^[0-7]{3,4}$ ]] \
        || deny 'private_key_chain_metadata_invalid'
      (( (8#$PRIVATE_CHAIN_MODE & 8#022) == 0 )) || deny 'private_key_chain_writable'
      [[ "$PRIVATE_CHAIN_CURRENT" == '/' ]] && break
      PRIVATE_CHAIN_CURRENT="${PRIVATE_CHAIN_CURRENT%/*}"
      [[ -n "$PRIVATE_CHAIN_CURRENT" ]] || PRIVATE_CHAIN_CURRENT='/'
    done
    PRIVATE_KEY_PARENT_IDENTITY="$($STAT_BIN -Lc '%d:%i' -- "$PRIVATE_KEY_PARENT_REAL" 2>/dev/null)" \
      || deny 'private_key_parent_identity_failed'
    exec {PRIVATE_KEY_FD}<"$PRIVATE_KEY" || deny 'private_key_descriptor_failed'
    PRIVATE_KEY_ANCHOR="/proc/$$/fd/$PRIVATE_KEY_FD"
    [[ -f "$PRIVATE_KEY_ANCHOR" ]] || deny 'private_key_descriptor_invalid'
  fi
  PRIVATE_KEY_IDENTITY="$($STAT_BIN -Lc '%d:%i' -- "$PRIVATE_KEY_ANCHOR" 2>/dev/null)" \
    || deny 'private_key_identity_failed'
  PRIVATE_METADATA="$($STAT_BIN -Lc '%u:%a:%h' -- "$PRIVATE_KEY_ANCHOR" 2>/dev/null)" \
    || deny 'private_key_stat_failed'
  if [[ "$TOOL_PLATFORM" == 'Linux' ]]; then
    [[ "$PRIVATE_METADATA" == '0:400:1' || "$PRIVATE_METADATA" == '0:600:1' ]] \
      || deny 'private_key_metadata_invalid'
  else
    PRIVATE_MODE="${PRIVATE_METADATA#*:}"
    PRIVATE_MODE="${PRIVATE_MODE%:*}"
    [[ "$PRIVATE_MODE" == '400' || "$PRIVATE_MODE" == '600' ]] \
      || deny 'private_key_permissions_invalid'
  fi
  run_openssl pkey -in "$PRIVATE_KEY_ANCHOR" -pubout -out "$TMP_DIR/derived-public.pem" \
    >/dev/null 2>&1 || deny 'private_key_parse_failed'
  verify_public_key_type "$TMP_DIR/derived-public.pem"
  run_openssl pkeyutl -sign -rawin -inkey "$PRIVATE_KEY_ANCHOR" \
    -in "$TMP_DIR/payload.canonical" -out "$TMP_DIR/signature.bin" \
    >/dev/null 2>&1 || deny 'signature_creation_failed'
  [[ "$(wc -c <"$TMP_DIR/signature.bin")" -eq 64 ]] || deny 'created_signature_length_invalid'
  run_openssl pkeyutl -verify -rawin -pubin -inkey "$TMP_DIR/derived-public.pem" \
    -sigfile "$TMP_DIR/signature.bin" -in "$TMP_DIR/payload.canonical" \
    >/dev/null 2>&1 || deny 'created_signature_self_verification_failed'
  if [[ "$TOOL_PLATFORM" == 'Linux' ]]; then
    [[ -d "$PRIVATE_KEY_PARENT_REAL" && ! -L "$PRIVATE_KEY_PARENT_REAL" \
      && "$($STAT_BIN -Lc '%d:%i' -- "$PRIVATE_KEY_PARENT_REAL" 2>/dev/null)" == "$PRIVATE_KEY_PARENT_IDENTITY" ]] \
      || deny 'private_key_parent_identity_changed'
    [[ -f "$PRIVATE_KEY" && ! -L "$PRIVATE_KEY" \
      && "$($STAT_BIN -Lc '%d:%i' -- "$PRIVATE_KEY" 2>/dev/null)" == "$PRIVATE_KEY_IDENTITY" \
      && "$($STAT_BIN -Lc '%d:%i' -- "$PRIVATE_KEY_ANCHOR" 2>/dev/null)" == "$PRIVATE_KEY_IDENTITY" ]] \
      || deny 'private_key_identity_changed'
  fi
  SIGNATURE_B64="$(run_openssl base64 -A -in "$TMP_DIR/signature.bin" 2>/dev/null)" \
    || deny 'signature_encoding_failed'
  [[ "$SIGNATURE_B64" =~ ^[A-Za-z0-9+/]{86}==$ ]] || deny 'created_signature_encoding_invalid'
  [[ "$ATTESTATION" == /* ]] || deny 'attestation_output_path_not_absolute'
  [[ ! -e "$ATTESTATION" && ! -L "$ATTESTATION" ]] || deny 'attestation_output_exists'
  OUTPUT_PARENT="$(dirname -- "$ATTESTATION")"
  [[ -d "$OUTPUT_PARENT" && ! -L "$OUTPUT_PARENT" ]] || deny 'attestation_output_parent_invalid'
  OUTPUT_PARENT_REAL="$(cd "$OUTPUT_PARENT" && pwd -P)" || deny 'attestation_output_parent_resolution_failed'
  ATTESTATION_BASENAME="$(basename -- "$ATTESTATION")"
  if [[ "$OUTPUT_PARENT_REAL" == '/' ]]; then
    ATTESTATION_CANONICAL="/$ATTESTATION_BASENAME"
  else
    ATTESTATION_CANONICAL="$OUTPUT_PARENT_REAL/$ATTESTATION_BASENAME"
  fi
  [[ "$ATTESTATION" == "$ATTESTATION_CANONICAL" ]] \
    || deny 'attestation_output_parent_not_canonical'
  if [[ "$TOOL_PLATFORM" == 'Linux' ]]; then
    [[ "$EUID" -eq 0 ]] || deny 'attestation_create_root_required'
    OUTPUT_CHAIN_CURRENT="$OUTPUT_PARENT_REAL"
    while :; do
      [[ -d "$OUTPUT_CHAIN_CURRENT" && ! -L "$OUTPUT_CHAIN_CURRENT" ]] \
        || deny 'attestation_output_parent_chain_component_invalid'
      OUTPUT_CHAIN_METADATA="$($STAT_BIN -c '%u:%a' -- "$OUTPUT_CHAIN_CURRENT" 2>/dev/null)" \
        || deny 'attestation_output_parent_chain_stat_failed'
      OUTPUT_CHAIN_OWNER="${OUTPUT_CHAIN_METADATA%%:*}"
      OUTPUT_CHAIN_MODE="${OUTPUT_CHAIN_METADATA#*:}"
      [[ "$OUTPUT_CHAIN_OWNER" == '0' && "$OUTPUT_CHAIN_MODE" =~ ^[0-7]{3,4}$ ]] \
        || deny 'attestation_output_parent_chain_metadata_invalid'
      (( (8#$OUTPUT_CHAIN_MODE & 8#022) == 0 )) \
        || deny 'attestation_output_parent_chain_writable'
      [[ "$OUTPUT_CHAIN_CURRENT" == '/' ]] && break
      OUTPUT_CHAIN_CURRENT="${OUTPUT_CHAIN_CURRENT%/*}"
      [[ -n "$OUTPUT_CHAIN_CURRENT" ]] || OUTPUT_CHAIN_CURRENT='/'
    done
  fi
  OUTPUT_PARENT_IDENTITY="$($STAT_BIN -Lc '%d:%i' -- "$OUTPUT_PARENT_REAL" 2>/dev/null)" \
    || deny 'attestation_output_parent_identity_failed'
  OUTPUT_PARENT_ANCHOR="$OUTPUT_PARENT_REAL"
  if [[ "$TOOL_PLATFORM" == 'Linux' ]]; then
    exec {OUTPUT_PARENT_FD}<"$OUTPUT_PARENT_REAL" || deny 'attestation_output_parent_descriptor_failed'
    OUTPUT_PARENT_ANCHOR="/proc/$$/fd/$OUTPUT_PARENT_FD"
    [[ -d "$OUTPUT_PARENT_ANCHOR" ]] || deny 'attestation_output_parent_descriptor_missing'
    [[ "$($STAT_BIN -Lc '%d:%i' -- "$OUTPUT_PARENT_ANCHOR" 2>/dev/null)" == "$OUTPUT_PARENT_IDENTITY" ]] \
      || deny 'attestation_output_parent_descriptor_identity_mismatch'
  fi
  OUTPUT_TARGET="$OUTPUT_PARENT_ANCHOR/$ATTESTATION_BASENAME"
  [[ ! -e "$OUTPUT_TARGET" && ! -L "$OUTPUT_TARGET" ]] || deny 'attestation_output_exists'
  OUTPUT_TMP="$("$MKTEMP_BIN" "$OUTPUT_PARENT_ANCHOR/.pandora-attestation-v2.XXXXXX")" \
    || deny 'attestation_output_temporary_failed'
  chmod 0600 "$OUTPUT_TMP" || deny 'attestation_output_chmod_failed'
  {
    cat "$TMP_DIR/payload.canonical"
    printf 'signature_b64=%s\n' "$SIGNATURE_B64"
  } >"$OUTPUT_TMP" || deny 'attestation_output_write_failed'
  "$SYNC_BIN" -f "$OUTPUT_TMP" >/dev/null 2>&1 \
    || deny 'attestation_output_file_sync_failed'
  OUTPUT_TMP_IDENTITY="$($STAT_BIN -Lc '%d:%i' -- "$OUTPUT_TMP" 2>/dev/null)" \
    || deny 'attestation_output_temporary_identity_failed'
  OUTPUT_FILE_ANCHOR="$OUTPUT_TMP"
  if [[ "$TOOL_PLATFORM" == 'Linux' ]]; then
    exec {OUTPUT_FILE_FD}<"$OUTPUT_TMP" || deny 'attestation_output_descriptor_failed'
    OUTPUT_FILE_ANCHOR="/proc/$$/fd/$OUTPUT_FILE_FD"
    [[ "$($STAT_BIN -Lc '%d:%i' -- "$OUTPUT_FILE_ANCHOR" 2>/dev/null)" == "$OUTPUT_TMP_IDENTITY" ]] \
      || deny 'attestation_output_descriptor_identity_mismatch'
  fi
  "$LN_BIN" -- "$OUTPUT_TMP" "$OUTPUT_TARGET" 2>/dev/null \
    || deny 'attestation_output_publish_failed'
  [[ "$($STAT_BIN -Lc '%d:%i' -- "$OUTPUT_TARGET" 2>/dev/null)" == "$OUTPUT_TMP_IDENTITY" ]] \
    || deny 'attestation_output_publish_identity_mismatch'
  [[ "$($STAT_BIN -c '%h' -- "$OUTPUT_TMP" 2>/dev/null)" == '2' \
    && "$($STAT_BIN -c '%h' -- "$OUTPUT_TARGET" 2>/dev/null)" == '2' ]] \
    || deny 'attestation_output_publish_link_count_invalid'
  "$RM_BIN" -f -- "$OUTPUT_TMP" || deny 'attestation_output_temporary_cleanup_failed'
  OUTPUT_TMP=''
  if [[ "$TOOL_PLATFORM" != 'Linux' ]]; then
    OUTPUT_FILE_ANCHOR="$OUTPUT_TARGET"
  fi
  "$SYNC_BIN" -f "$OUTPUT_PARENT_ANCHOR" >/dev/null 2>&1 \
    || deny 'attestation_output_directory_sync_failed'
  [[ "$($STAT_BIN -Lc '%d:%i' -- "$OUTPUT_PARENT_ANCHOR" 2>/dev/null)" == "$OUTPUT_PARENT_IDENTITY" ]] \
    || deny 'attestation_output_parent_descriptor_identity_changed'
  [[ -d "$OUTPUT_PARENT_REAL" && ! -L "$OUTPUT_PARENT_REAL" ]] \
    || deny 'attestation_output_parent_rebound'
  [[ "$($STAT_BIN -Lc '%d:%i' -- "$OUTPUT_PARENT_REAL" 2>/dev/null)" == "$OUTPUT_PARENT_IDENTITY" ]] \
    || deny 'attestation_output_parent_identity_changed'
  [[ -f "$OUTPUT_TARGET" && ! -L "$OUTPUT_TARGET" \
    && -f "$ATTESTATION" && ! -L "$ATTESTATION" ]] || deny 'attestation_output_not_regular'
  OUTPUT_TARGET_IDENTITY="$($STAT_BIN -Lc '%d:%i' -- "$OUTPUT_TARGET" 2>/dev/null)" \
    || deny 'attestation_output_identity_failed'
  [[ "$OUTPUT_TARGET_IDENTITY" == "$OUTPUT_TMP_IDENTITY" ]] \
    || deny 'attestation_output_identity_changed'
  [[ "$($STAT_BIN -Lc '%d:%i' -- "$OUTPUT_FILE_ANCHOR" 2>/dev/null)" == "$OUTPUT_TMP_IDENTITY" ]] \
    || deny 'attestation_output_descriptor_identity_changed'
  [[ "$($STAT_BIN -Lc '%d:%i' -- "$ATTESTATION" 2>/dev/null)" == "$OUTPUT_TARGET_IDENTITY" ]] \
    || deny 'attestation_output_path_identity_changed'
  [[ "$($STAT_BIN -c '%a:%h' -- "$OUTPUT_TARGET" 2>/dev/null)" == '600:1' ]] \
    || deny 'attestation_output_metadata_invalid'
  ATTESTATION_SHA256="$($SHA256_BIN "$OUTPUT_FILE_ANCHOR" | awk '{print $1}')" \
    || deny 'attestation_hash_failed'
  [[ "$($STAT_BIN -Lc '%d:%i' -- "$OUTPUT_TARGET" 2>/dev/null)" == "$OUTPUT_TMP_IDENTITY" \
    && "$($STAT_BIN -Lc '%d:%i' -- "$ATTESTATION" 2>/dev/null)" == "$OUTPUT_TMP_IDENTITY" ]] \
    || deny 'attestation_output_identity_changed_after_hash'
  printf 'client_auth_attestation_v2=CREATED sha256=%s\n' "$ATTESTATION_SHA256"
  exit 0
fi

load_attestation "$ATTESTATION"
verify_signature
verify_expected_binding
if [[ "$MODE" == 'verify-trusted' ]]; then
  [[ "$TOOL_PLATFORM" == 'Linux' ]] || deny 'trusted_verification_linux_required'
  [[ "$EUID" -eq 0 ]] || deny 'trusted_verification_root_required'
  load_trust_capsule "$CAPSULE"
  load_external_manifest_snapshot "$EXTERNAL_MANIFEST"
fi

NONCE="${PAYLOAD_VALUES[2]}"
RELEASE_ID="${PAYLOAD_VALUES[5]}"
if [[ "$MODE" == 'verify' ]]; then
  verify_current_time_window
  printf 'client_auth_attestation_v2=VERIFIED nonce=%s release_id=%s\n' "$NONCE" "$RELEASE_ID"
  exit 0
fi

[[ "$LEDGER_DIR" == /* ]] || deny 'ledger_path_not_absolute'
[[ -d "$LEDGER_DIR" && ! -L "$LEDGER_DIR" ]] || deny 'ledger_directory_invalid'
LEDGER_OWNER="$($STAT_BIN -c '%u' -- "$LEDGER_DIR" 2>/dev/null)" || deny 'ledger_owner_stat_failed'
LEDGER_MODE="$($STAT_BIN -c '%a' -- "$LEDGER_DIR" 2>/dev/null)" || deny 'ledger_mode_stat_failed'
LEDGER_EXPECTED_OWNER='0'
[[ -x /usr/bin/uname && -x /usr/bin/id ]] || deny 'ledger_platform_tools_missing'
LEDGER_PLATFORM="$TOOL_PLATFORM"
case "$LEDGER_PLATFORM" in
  Linux) ;;
  MINGW*|MSYS*|CYGWIN*)
    # Windows test hosts do not expose a meaningful Linux UID 0. Linux remains
    # hard-bound to root ownership; this branch only maps the local test owner.
    LEDGER_EXPECTED_OWNER="$(/usr/bin/id -u 2>/dev/null)" || deny 'ledger_test_owner_detection_failed'
    ;;
  *) deny 'ledger_platform_unsupported' ;;
esac
[[ "$LEDGER_OWNER" == "$LEDGER_EXPECTED_OWNER" ]] || deny 'ledger_not_root_owned'
[[ "$LEDGER_MODE" == '700' ]] || deny 'ledger_permissions_invalid'
LEDGER_REAL="$(cd "$LEDGER_DIR" && pwd -P)" || deny 'ledger_resolution_failed'
[[ -n "$LEDGER_REAL" && "$LEDGER_REAL" == /* ]] || deny 'ledger_resolution_invalid'
LEDGER_PATH_IDENTITY="$($STAT_BIN -Lc '%d:%i' -- "$LEDGER_REAL" 2>/dev/null)" \
  || deny 'ledger_identity_stat_failed'

validate_ledger_chain() {
  local current="$LEDGER_REAL" owner mode mode_value
  while :; do
    [[ -d "$current" && ! -L "$current" ]] || deny 'ledger_chain_component_invalid'
    owner="$($STAT_BIN -c '%u' -- "$current" 2>/dev/null)" || deny 'ledger_chain_owner_stat_failed'
    mode="$($STAT_BIN -c '%a' -- "$current" 2>/dev/null)" || deny 'ledger_chain_mode_stat_failed'
    [[ "$owner" == "$LEDGER_EXPECTED_OWNER" ]] || deny 'ledger_chain_not_trusted_owner'
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || deny 'ledger_chain_mode_invalid'
    mode_value=$((8#$mode))
    (( (mode_value & 8#022) == 0 )) || deny 'ledger_chain_writable'
    [[ "$current" == '/' ]] && break
    current="${current%/*}"
    [[ -n "$current" ]] || current='/'
  done
}
if [[ "$LEDGER_PLATFORM" == 'Linux' ]]; then
  validate_ledger_chain
fi

# Enter the already-validated inode and retain a directory descriptor. On
# Linux, all receipt operations are anchored through /proc to that open inode;
# renaming or replacing any pathname parent cannot redirect publication.
cd "$LEDGER_REAL" || deny 'ledger_enter_failed'
[[ "$($STAT_BIN -Lc '%d:%i' -- . 2>/dev/null)" == "$LEDGER_PATH_IDENTITY" ]] \
  || deny 'ledger_identity_changed_before_open'
if [[ "$LEDGER_PLATFORM" == 'Linux' ]]; then
  exec {LEDGER_FD}<. || deny 'ledger_descriptor_open_failed'
  LEDGER_ANCHOR="/proc/$$/fd/$LEDGER_FD"
  [[ -d "$LEDGER_ANCHOR" ]] || deny 'ledger_descriptor_path_missing'
  [[ "$($STAT_BIN -Lc '%d:%i' -- "$LEDGER_ANCHOR" 2>/dev/null)" == "$LEDGER_PATH_IDENTITY" ]] \
    || deny 'ledger_descriptor_identity_mismatch'
else
  # MSYS is only a local behavior gate; native release authority remains Linux.
  LEDGER_ANCHOR="$LEDGER_REAL"
fi

verify_ledger_path_binding() {
  [[ -d "$LEDGER_REAL" && ! -L "$LEDGER_REAL" ]] || deny 'ledger_path_rebound'
  [[ "$($STAT_BIN -Lc '%d:%i' -- "$LEDGER_REAL" 2>/dev/null)" == "$LEDGER_PATH_IDENTITY" ]] \
    || deny 'ledger_path_identity_changed'
  [[ "$($STAT_BIN -Lc '%d:%i' -- "$LEDGER_ANCHOR" 2>/dev/null)" == "$LEDGER_PATH_IDENTITY" ]] \
    || deny 'ledger_descriptor_identity_changed'
}
CONSUME_NOW="$($DATE_BIN -u +%s 2>/dev/null)" || deny 'consume_clock_unavailable'
safe_epoch "$CONSUME_NOW" || deny 'consume_clock_invalid'
MARKER="$LEDGER_ANCHOR/$NONCE"
ATTESTATION_SHA256="$($SHA256_BIN "$TMP_DIR/attestation.canonical" | awk '{print $1}')" \
  || deny 'attestation_hash_failed'
EXPECTED_SHA256="$($SHA256_BIN "$TMP_DIR/expected.canonical" | awk '{print $1}')" \
  || deny 'expected_hash_failed'
PUBLIC_KEY_SHA256="$($SHA256_BIN "$PUBLIC_KEY_SNAPSHOT" | awk '{print $1}')" \
  || deny 'public_key_hash_failed'
safe_sha256 "$ATTESTATION_SHA256" || deny 'attestation_hash_invalid'
safe_sha256 "$EXPECTED_SHA256" || deny 'expected_hash_invalid'
safe_sha256 "$PUBLIC_KEY_SHA256" || deny 'public_key_hash_invalid'

if [[ "$MODE" == 'verify-trusted' ]]; then
  [[ "${PANDORA_PATHTRUST_VERSION:-}" == '1' && "${PANDORA_TRUSTED_FD:-}" == '3' ]] \
    || deny 'pathtrust_context_required'
  safe_sha256 "${PANDORA_TRUSTED_SHA256:-}" || deny 'pathtrust_core_sha256_invalid'
  safe_sha256 "${PANDORA_TRUSTED_CHAIN_SHA256:-}" || deny 'pathtrust_core_chain_sha256_invalid'
  [[ "${PANDORA_TRUSTED_DEVICE:-}" =~ ^[1-9][0-9]{0,19}$ ]] || deny 'pathtrust_core_device_invalid'
  [[ "${PANDORA_TRUSTED_MODE:-}" == '0500' ]] || deny 'pathtrust_core_mode_invalid'
  CORE_FD_PATH="/proc/$$/fd/3"
  [[ -f "$CORE_FD_PATH" && -r "$CORE_FD_PATH" ]] || deny 'pathtrust_core_descriptor_missing'
  CORE_FD_SHA256="$($SHA256_BIN "$CORE_FD_PATH" | awk '{print $1}')" \
    || deny 'pathtrust_core_descriptor_hash_failed'
  LEDGER_DIRECTORY_SHA256="$(printf '%s' "$LEDGER_REAL" | "$SHA256_BIN" | awk '{print $1}')" \
    || deny 'ledger_directory_hash_failed'
  safe_sha256 "$LEDGER_DIRECTORY_SHA256" || deny 'ledger_directory_hash_invalid'

  [[ "$CORE_FD_SHA256" == "${PANDORA_TRUSTED_SHA256}" \
    && "$CORE_FD_SHA256" == "${CAPSULE_VALUES[4]}" ]] || deny 'trust_capsule_core_sha256_mismatch'
  [[ "${PANDORA_TRUSTED_CHAIN_SHA256}" == "${CAPSULE_VALUES[5]}" ]] \
    || deny 'trust_capsule_core_chain_sha256_mismatch'
  [[ "${PANDORA_TRUSTED_DEVICE}" == "${CAPSULE_VALUES[6]}" ]] \
    || deny 'trust_capsule_core_device_mismatch'
  [[ "${PANDORA_TRUSTED_MODE}" == "${CAPSULE_VALUES[7]}" ]] \
    || deny 'trust_capsule_core_mode_mismatch'
  [[ "$ATTESTATION_SHA256" == "${CAPSULE_VALUES[8]}" ]] \
    || deny 'trust_capsule_attestation_sha256_mismatch'
  [[ "$EXPECTED_SHA256" == "${CAPSULE_VALUES[9]}" ]] \
    || deny 'trust_capsule_expected_sha256_mismatch'
  [[ "$PUBLIC_KEY_SHA256" == "${CAPSULE_VALUES[10]}" ]] \
    || deny 'trust_capsule_public_key_sha256_mismatch'
  [[ "$EXTERNAL_MANIFEST_SHA256" == "${CAPSULE_VALUES[11]}" ]] \
    || deny 'trust_capsule_external_manifest_sha256_mismatch'
  [[ "${PAYLOAD_VALUES[20]}" == "$EXTERNAL_MANIFEST_SHA256" ]] \
    || deny 'trust_capsule_expected_external_manifest_binding_mismatch'
  [[ "$RELEASE_ID" == "${CAPSULE_VALUES[2]}" ]] || deny 'trust_capsule_release_id_mismatch'
  [[ "${PAYLOAD_VALUES[7]}" == "${CAPSULE_VALUES[12]}" ]] \
    || deny 'trust_capsule_target_system_identifier_mismatch'
  [[ "${PAYLOAD_VALUES[8]}" == "${CAPSULE_VALUES[13]}" ]] \
    || deny 'trust_capsule_target_database_name_mismatch'
  [[ "${PAYLOAD_VALUES[9]}" == "${CAPSULE_VALUES[14]}" ]] \
    || deny 'trust_capsule_target_database_oid_mismatch'
  [[ "$LEDGER_DIRECTORY_SHA256" == "${CAPSULE_VALUES[16]}" ]] \
    || deny 'trust_capsule_ledger_directory_mismatch'
  verify_current_time_window
  verify_ledger_path_binding
  printf 'client_auth_attestation_v2=TRUSTED capsule_sha256=%s release_id=%s release_run_id=%s ledger_namespace=%s\n' \
    "$CAPSULE_SHA256" "$RELEASE_ID" "${CAPSULE_VALUES[3]}" "${CAPSULE_VALUES[15]}"
  exit 0
fi

confirm_existing_receipt() {
  local line index key value canonical="$TMP_DIR/receipt.canonical"
  [[ -f "$MARKER" && ! -L "$MARKER" && -r "$MARKER" ]] || deny 'nonce_receipt_not_regular'
  local owner mode links
  owner="$($STAT_BIN -c '%u' -- "$MARKER" 2>/dev/null)" || deny 'nonce_receipt_owner_stat_failed'
  mode="$($STAT_BIN -c '%a' -- "$MARKER" 2>/dev/null)" || deny 'nonce_receipt_mode_stat_failed'
  links="$($STAT_BIN -c '%h' -- "$MARKER" 2>/dev/null)" || deny 'nonce_receipt_links_stat_failed'
  [[ "$owner" == "$LEDGER_EXPECTED_OWNER" ]] || deny 'nonce_receipt_owner_invalid'
  [[ "$mode" == '600' ]] || deny 'nonce_receipt_permissions_invalid'
  if [[ "$links" == '2' ]]; then
    local marker_identity candidate candidate_owner candidate_mode
    local -a aliases=()
    marker_identity="$($STAT_BIN -Lc '%d:%i' -- "$MARKER" 2>/dev/null)" \
      || deny 'nonce_receipt_identity_stat_failed'
    shopt -s nullglob
    for candidate in "$LEDGER_ANCHOR"/.pandora-attestation-v2-receipt.*; do
      [[ -f "$candidate" && ! -L "$candidate" ]] || continue
      if [[ "$($STAT_BIN -Lc '%d:%i' -- "$candidate" 2>/dev/null)" == "$marker_identity" ]]; then
        aliases+=("$candidate")
      fi
    done
    shopt -u nullglob
    [[ "${#aliases[@]}" -eq 1 ]] || deny 'nonce_receipt_alias_ambiguous'
    candidate="${aliases[0]}"
    candidate_owner="$($STAT_BIN -c '%u' -- "$candidate" 2>/dev/null)" \
      || deny 'nonce_receipt_alias_owner_stat_failed'
    candidate_mode="$($STAT_BIN -c '%a' -- "$candidate" 2>/dev/null)" \
      || deny 'nonce_receipt_alias_mode_stat_failed'
    [[ "$candidate_owner" == "$LEDGER_EXPECTED_OWNER" && "$candidate_mode" == '600' ]] \
      || deny 'nonce_receipt_alias_metadata_invalid'
    "$RM_BIN" -f -- "$candidate" || deny 'nonce_receipt_alias_cleanup_failed'
    "$SYNC_BIN" -f "$MARKER" >/dev/null 2>&1 || deny 'nonce_receipt_recovery_sync_failed'
    "$SYNC_BIN" -f "$LEDGER_ANCHOR" >/dev/null 2>&1 || deny 'nonce_ledger_recovery_sync_failed'
    links="$($STAT_BIN -c '%h' -- "$MARKER" 2>/dev/null)" || deny 'nonce_receipt_links_restat_failed'
  fi
  [[ "$links" == '1' ]] || deny 'nonce_receipt_link_count_invalid'
  mapfile -t receipt_lines <"$MARKER" || deny 'nonce_receipt_read_failed'
  [[ "${#receipt_lines[@]}" -eq "${#RECEIPT_KEYS[@]}" ]] || deny 'nonce_receipt_line_count_invalid'
  RECEIPT_VALUES=()
  : >"$canonical"
  for index in "${!RECEIPT_KEYS[@]}"; do
    line="${receipt_lines[$index]}"
    key="${RECEIPT_KEYS[$index]}"
    [[ "$line" == "$key="* ]] || deny "nonce_receipt_key_order_invalid:${key}"
    value="${line#*=}"
    [[ -n "$value" && "$value" != *$'\r'* ]] || deny "nonce_receipt_value_invalid:${key}"
    RECEIPT_VALUES+=("$value")
    printf '%s=%s\n' "$key" "$value" >>"$canonical"
  done
  cmp -s -- "$MARKER" "$canonical" || deny 'nonce_receipt_not_canonical'
  [[ "${RECEIPT_VALUES[0]}" == 'client-auth-attestation-v2-consumed-v2' ]] || deny 'nonce_receipt_format_invalid'
  [[ "${RECEIPT_VALUES[1]}" == 'goose-41-to-42' ]] || deny 'nonce_receipt_transition_invalid'
  [[ "${RECEIPT_VALUES[2]}" == "$NONCE" ]] || deny 'nonce_receipt_nonce_mismatch'
  [[ "${RECEIPT_VALUES[3]}" == "$RELEASE_ID" ]] || deny 'nonce_receipt_release_mismatch'
  [[ "${RECEIPT_VALUES[4]}" == "$ATTESTATION_SHA256" ]] || deny 'nonce_receipt_attestation_mismatch'
  [[ "${RECEIPT_VALUES[5]}" == "$EXPECTED_SHA256" ]] || deny 'nonce_receipt_expected_mismatch'
  [[ "${RECEIPT_VALUES[6]}" == "$PUBLIC_KEY_SHA256" ]] || deny 'nonce_receipt_public_key_mismatch'
  safe_epoch "${RECEIPT_VALUES[7]}" || deny 'nonce_receipt_consumed_at_invalid'
  (( 10#${PAYLOAD_VALUES[3]} <= 10#${RECEIPT_VALUES[7]} )) || deny 'nonce_receipt_before_validity'
  (( 10#${RECEIPT_VALUES[7]} < 10#${PAYLOAD_VALUES[4]} )) || deny 'nonce_receipt_after_expiry'
  (( 10#${RECEIPT_VALUES[7]} <= 10#$CONSUME_NOW )) || deny 'nonce_receipt_from_future'
  "$SYNC_BIN" -f "$MARKER" >/dev/null 2>&1 || deny 'nonce_receipt_confirm_sync_failed'
  "$SYNC_BIN" -f "$LEDGER_ANCHOR" >/dev/null 2>&1 || deny 'nonce_ledger_confirm_sync_failed'
  verify_ledger_path_binding
  printf 'client_auth_attestation_v2=CONFIRMED nonce=%s release_id=%s\n' "$NONCE" "$RELEASE_ID"
  exit 0
}

if [[ -e "$MARKER" || -L "$MARKER" ]]; then
  [[ "$MODE" == 'consume-or-confirm' ]] || deny 'nonce_already_consumed_or_ledger_failed'
  confirm_existing_receipt
fi

verify_current_time_window
CONSUME_NOW="$($DATE_BIN -u +%s 2>/dev/null)" || deny 'consume_clock_unavailable'
safe_epoch "$CONSUME_NOW" || deny 'consume_clock_invalid'
(( 10#${PAYLOAD_VALUES[3]} <= 10#$CONSUME_NOW )) || deny 'attestation_not_yet_valid_before_consume'
(( 10#$CONSUME_NOW < 10#${PAYLOAD_VALUES[4]} )) || deny 'attestation_expired_before_consume'
OUTPUT_TMP="$("$MKTEMP_BIN" "$LEDGER_ANCHOR/.pandora-attestation-v2-receipt.XXXXXX")" \
  || deny 'nonce_receipt_temporary_failed'
chmod 0600 "$OUTPUT_TMP" || deny 'nonce_receipt_chmod_failed'
{
  printf 'format=client-auth-attestation-v2-consumed-v2\n'
  printf 'transition=goose-41-to-42\n'
  printf 'nonce=%s\n' "$NONCE"
  printf 'release_id=%s\n' "$RELEASE_ID"
  printf 'attestation_sha256=%s\n' "$ATTESTATION_SHA256"
  printf 'expected_sha256=%s\n' "$EXPECTED_SHA256"
  printf 'public_key_sha256=%s\n' "$PUBLIC_KEY_SHA256"
  printf 'consumed_at=%s\n' "$CONSUME_NOW"
} >"$OUTPUT_TMP" || deny 'nonce_receipt_write_failed'
"$SYNC_BIN" -f "$OUTPUT_TMP" >/dev/null 2>&1 || deny 'nonce_receipt_sync_failed'
if ! "$LN_BIN" -- "$OUTPUT_TMP" "$MARKER" 2>/dev/null; then
  "$RM_BIN" -f -- "$OUTPUT_TMP" || deny 'nonce_receipt_temporary_cleanup_failed'
  OUTPUT_TMP=''
  [[ "$MODE" == 'consume-or-confirm' ]] || deny 'nonce_already_consumed_or_ledger_failed'
  confirm_existing_receipt
fi
"$RM_BIN" -f -- "$OUTPUT_TMP" || deny 'nonce_receipt_temporary_cleanup_failed'
OUTPUT_TMP=''
"$SYNC_BIN" -f "$LEDGER_ANCHOR" >/dev/null 2>&1 || deny 'nonce_ledger_sync_failed'
verify_ledger_path_binding
printf 'client_auth_attestation_v2=CONSUMED nonce=%s release_id=%s\n' "$NONCE" "$RELEASE_ID"
