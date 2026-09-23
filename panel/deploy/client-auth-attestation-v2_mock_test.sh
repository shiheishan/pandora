#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLI="$ROOT/deploy/client-auth-attestation-v2.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-attest-v2-test.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT

fail() {
  printf 'client_auth_attestation_v2_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}

expect_78() {
  local name="$1"
  shift
  set +e
  "$@" >"$TMP/$name.out" 2>"$TMP/$name.err"
  local status=$?
  set -e
  [[ "$status" -eq 78 ]] || fail "$name:status=$status"
  [[ ! -s "$TMP/$name.out" ]] || fail "$name:published_success"
}

expect_crash_no_success() {
  local name="$1"
  shift
  set +e
  "$@" >"$TMP/$name.out" 2>"$TMP/$name.err"
  local status=$?
  set -e
  [[ "$status" -ne 0 ]] || fail "$name:unexpected_success"
  ! grep -Eq 'client_auth_attestation_v2=(CONSUMED|CONFIRMED)' "$TMP/$name.out" \
    || fail "$name:published_success"
}

command -v openssl >/dev/null 2>&1 || fail 'openssl_missing'
grep -Fq 'LEDGER_ANCHOR="/proc/$$/fd/$LEDGER_FD"' "$CLI" \
  || fail 'linux_fd_ledger_anchor_missing'
grep -Fq 'verify_ledger_path_binding' "$CLI" || fail 'ledger_rebind_check_missing'
grep -Fq 'nonce_receipt_alias_ambiguous' "$CLI" || fail 'hardlink_alias_recovery_missing'
openssl genpkey -algorithm ED25519 -out "$TMP/private.pem" >/dev/null 2>&1 \
  || fail 'ed25519_generation_failed'
chmod 0600 "$TMP/private.pem"
openssl pkey -in "$TMP/private.pem" -pubout -out "$TMP/public.pem" >/dev/null 2>&1 \
  || fail 'public_key_generation_failed'

H1='1111111111111111111111111111111111111111111111111111111111111111'
H2='2222222222222222222222222222222222222222222222222222222222222222'
H3='3333333333333333333333333333333333333333333333333333333333333333'
H4='4444444444444444444444444444444444444444444444444444444444444444'
H5='5555555555555555555555555555555555555555555555555555555555555555'
H6='6666666666666666666666666666666666666666666666666666666666666666'
H7='7777777777777777777777777777777777777777777777777777777777777777'
H8='8888888888888888888888888888888888888888888888888888888888888888'
CONTAINER='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
NONCE='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
NOW="$(date -u +%s)"
ISSUED=$((NOW - 1))
EXPIRES=$((NOW + 300))

write_payload() {
  local path="$1" issued="$2" expires="$3"
  {
    printf 'format=client-auth-attestation-v2\n'
    printf 'signature_algorithm=ed25519\n'
    printf 'nonce=%s\n' "$NONCE"
    printf 'issued_at=%s\n' "$issued"
    printf 'expires_at=%s\n' "$expires"
    printf 'release_id=release-00042-test\n'
    printf 'source_container_id=%s\n' "$CONTAINER"
    printf 'source_system_identifier=1234567890123456789\n'
    printf 'source_database_name=aegis\n'
    printf 'source_database_oid=16384\n'
    printf 'source_database_owner_oid=10\n'
    printf 'source_database_owner_name=aegis_owner\n'
    printf 'source_goose_waterline=41\n'
    printf 'migration_set_sha256=%s\n' "$H1"
    printf 'client_auth_00042_sha256=%s\n' "$H2"
    printf 'goose_binary_sha256=%s\n' "$H3"
    printf 'preflight_runner_sha256=%s\n' "$H4"
    printf 'globals_dump_sha256=%s\n' "$H5"
    printf 'database_dump_sha256=%s\n' "$H6"
    printf 'postgres_image_sha256=%s\n' "$H7"
    printf 'catalog_manifest_sha256=%s\n' "$H8"
  } >"$path"
}

write_expected() {
  local path="$1" release_id="$2" container_id="$3"
  {
    printf 'release_id=%s\n' "$release_id"
    printf 'source_container_id=%s\n' "$container_id"
    printf 'source_system_identifier=1234567890123456789\n'
    printf 'source_database_name=aegis\n'
    printf 'source_database_oid=16384\n'
    printf 'source_database_owner_oid=10\n'
    printf 'source_database_owner_name=aegis_owner\n'
    printf 'source_goose_waterline=41\n'
    printf 'migration_set_sha256=%s\n' "$H1"
    printf 'client_auth_00042_sha256=%s\n' "$H2"
    printf 'goose_binary_sha256=%s\n' "$H3"
    printf 'preflight_runner_sha256=%s\n' "$H4"
    printf 'globals_dump_sha256=%s\n' "$H5"
    printf 'database_dump_sha256=%s\n' "$H6"
    printf 'postgres_image_sha256=%s\n' "$H7"
    printf 'catalog_manifest_sha256=%s\n' "$H8"
  } >"$path"
}

write_payload "$TMP/payload" "$ISSUED" "$EXPIRES"
write_expected "$TMP/expected" 'release-00042-test' "$CONTAINER"

"$CLI" create --manifest "$TMP/payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/attestation" >"$TMP/create.out"
grep -Eq '^client_auth_attestation_v2=CREATED sha256=[0-9a-f]{64}$' "$TMP/create.out" \
  || fail 'create_marker_invalid'
[[ -s "$TMP/attestation" ]] || fail 'attestation_missing'
[[ "$(stat -c '%a:%h' "$TMP/attestation")" == '600:1' ]] \
  || fail 'attestation_metadata_invalid'
grep -Fq 'attestation_output_file_sync_failed' "$CLI" || fail 'create_file_sync_gate_missing'
grep -Fq 'attestation_output_directory_sync_failed' "$CLI" || fail 'create_directory_sync_gate_missing'

"$CLI" verify --attestation "$TMP/attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" >"$TMP/verify.out"
grep -Fxq "client_auth_attestation_v2=VERIFIED nonce=$NONCE release_id=release-00042-test" \
  "$TMP/verify.out" || fail 'verify_marker_invalid'

awk -v replacement="$H2" '
  /^migration_set_sha256=/ { print "migration_set_sha256=" replacement; next }
  { print }
' "$TMP/attestation" >"$TMP/tampered"
expect_78 tampered "$CLI" verify --attestation "$TMP/tampered" \
  --public-key "$TMP/public.pem" --expected "$TMP/expected"

write_payload "$TMP/expired-payload" '1' '61'
"$CLI" create --manifest "$TMP/expired-payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/expired-attestation" >/dev/null
expect_78 expired "$CLI" verify --attestation "$TMP/expired-attestation" \
  --public-key "$TMP/public.pem" --expected "$TMP/expected"

write_expected "$TMP/wrong-release" 'release-00042-wrong' "$CONTAINER"
expect_78 wrong_release "$CLI" verify --attestation "$TMP/attestation" \
  --public-key "$TMP/public.pem" --expected "$TMP/wrong-release"

WRONG_CONTAINER='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
write_expected "$TMP/wrong-identity" 'release-00042-test' "$WRONG_CONTAINER"
expect_78 wrong_identity "$CLI" verify --attestation "$TMP/attestation" \
  --public-key "$TMP/public.pem" --expected "$TMP/wrong-identity"

mkdir -m 0700 "$TMP/ledger"
chown 0:0 "$TMP/ledger"
"$CLI" consume --attestation "$TMP/attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/ledger" >"$TMP/consume.out"
grep -Fxq "client_auth_attestation_v2=CONSUMED nonce=$NONCE release_id=release-00042-test" \
  "$TMP/consume.out" || fail 'consume_marker_invalid'
[[ -f "$TMP/ledger/$NONCE" && ! -L "$TMP/ledger/$NONCE" ]] || fail 'consume_receipt_missing'
[[ "$(stat -c '%a' "$TMP/ledger/$NONCE")" == '600' ]] || fail 'consume_receipt_mode_invalid'
[[ "$(stat -c '%h' "$TMP/ledger/$NONCE")" == '1' ]] || fail 'consume_receipt_link_count_invalid'
grep -Fxq 'format=client-auth-attestation-v2-consumed-v2' "$TMP/ledger/$NONCE" \
  || fail 'consume_receipt_format_invalid'
grep -Fxq 'transition=goose-41-to-42' "$TMP/ledger/$NONCE" \
  || fail 'consume_receipt_transition_invalid'
expect_78 replay "$CLI" consume --attestation "$TMP/attestation" \
  --public-key "$TMP/public.pem" --expected "$TMP/expected" --ledger-dir "$TMP/ledger"

"$CLI" consume-or-confirm --attestation "$TMP/attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/ledger" >"$TMP/confirm.out"
grep -Fxq "client_auth_attestation_v2=CONFIRMED nonce=$NONCE release_id=release-00042-test" \
  "$TMP/confirm.out" || fail 'confirm_marker_invalid'

expect_78 confirm_tampered "$CLI" consume-or-confirm --attestation "$TMP/tampered" \
  --public-key "$TMP/public.pem" --expected "$TMP/expected" --ledger-dir "$TMP/ledger"
expect_78 confirm_wrong_expected "$CLI" consume-or-confirm --attestation "$TMP/attestation" \
  --public-key "$TMP/public.pem" --expected "$TMP/wrong-release" --ledger-dir "$TMP/ledger"
openssl genpkey -algorithm ED25519 -out "$TMP/wrong-private.pem" >/dev/null 2>&1 \
  || fail 'wrong_ed25519_generation_failed'
chmod 0600 "$TMP/wrong-private.pem"
openssl pkey -in "$TMP/wrong-private.pem" -pubout -out "$TMP/wrong-public.pem" >/dev/null 2>&1 \
  || fail 'wrong_public_key_generation_failed'
expect_78 confirm_wrong_key "$CLI" consume-or-confirm --attestation "$TMP/attestation" \
  --public-key "$TMP/wrong-public.pem" --expected "$TMP/expected" --ledger-dir "$TMP/ledger"

ORIGINAL_NONCE="$NONCE"
NONCE='dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
SHORT_NOW="$(date -u +%s)"
SHORT_EXPIRES=$((SHORT_NOW + 20))
write_payload "$TMP/short-payload" "$((SHORT_NOW - 1))" "$SHORT_EXPIRES"
"$CLI" create --manifest "$TMP/short-payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/short-attestation" >/dev/null
mkdir -m 0700 "$TMP/short-ledger"
chown 0:0 "$TMP/short-ledger"
"$CLI" consume --attestation "$TMP/short-attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/short-ledger" >/dev/null
WAIT_FOR_EXPIRY=$((SHORT_EXPIRES - $(date -u +%s) + 1))
(( WAIT_FOR_EXPIRY > 0 )) && sleep "$WAIT_FOR_EXPIRY"
"$CLI" consume-or-confirm --attestation "$TMP/short-attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/short-ledger" >"$TMP/expired-confirm.out"
grep -Fxq "client_auth_attestation_v2=CONFIRMED nonce=$NONCE release_id=release-00042-test" \
  "$TMP/expired-confirm.out" || fail 'expired_confirm_marker_invalid'

NONCE='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'
CRASH_NOW="$(date -u +%s)"
write_payload "$TMP/prelink-payload" "$((CRASH_NOW - 1))" "$((CRASH_NOW + 300))"
"$CLI" create --manifest "$TMP/prelink-payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/prelink-attestation" >/dev/null
mkdir -m 0700 "$TMP/prelink-ledger" "$TMP/prelink-bin"
chown 0:0 "$TMP/prelink-ledger"
cat >"$TMP/prelink-bin/ln" <<'EOF'
#!/usr/bin/env bash
kill -KILL "$PPID"
exit 99
EOF
chmod 0700 "$TMP/prelink-bin/ln"
expect_crash_no_success prelink_crash env PATH="$TMP/prelink-bin:$PATH" \
  "$CLI" consume --attestation "$TMP/prelink-attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/prelink-ledger"
[[ ! -e "$TMP/prelink-ledger/$NONCE" && ! -L "$TMP/prelink-ledger/$NONCE" ]] \
  || fail 'prelink_crash_published_receipt'
"$CLI" consume --attestation "$TMP/prelink-attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/prelink-ledger" >"$TMP/prelink-retry.out"
grep -Fxq "client_auth_attestation_v2=CONSUMED nonce=$NONCE release_id=release-00042-test" \
  "$TMP/prelink-retry.out" || fail 'prelink_retry_marker_invalid'

NONCE='ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
POSTLINK_NOW="$(date -u +%s)"
write_payload "$TMP/postlink-payload" "$((POSTLINK_NOW - 1))" "$((POSTLINK_NOW + 300))"
"$CLI" create --manifest "$TMP/postlink-payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/postlink-attestation" >/dev/null
mkdir -m 0700 "$TMP/postlink-ledger" "$TMP/postlink-bin"
chown 0:0 "$TMP/postlink-ledger"
REAL_LN="$(command -v ln)"
cat >"$TMP/postlink-bin/ln" <<'EOF'
#!/usr/bin/env bash
"$PANDORA_TEST_REAL_LN" "$@" || exit $?
kill -KILL "$PPID"
EOF
chmod 0700 "$TMP/postlink-bin/ln"
expect_crash_no_success postlink_crash env PATH="$TMP/postlink-bin:$PATH" \
  PANDORA_TEST_REAL_LN="$REAL_LN" "$CLI" consume \
  --attestation "$TMP/postlink-attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/postlink-ledger"
[[ -f "$TMP/postlink-ledger/$NONCE" && ! -L "$TMP/postlink-ledger/$NONCE" ]] \
  || fail 'postlink_crash_receipt_missing'
[[ "$(stat -c '%h' "$TMP/postlink-ledger/$NONCE")" == '2' ]] \
  || fail 'postlink_crash_alias_window_missing'
"$CLI" consume-or-confirm --attestation "$TMP/postlink-attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/postlink-ledger" >"$TMP/postlink-confirm.out"
grep -Fxq "client_auth_attestation_v2=CONFIRMED nonce=$NONCE release_id=release-00042-test" \
  "$TMP/postlink-confirm.out" || fail 'postlink_confirm_marker_invalid'
[[ "$(stat -c '%h' "$TMP/postlink-ledger/$NONCE")" == '1' ]] \
  || fail 'postlink_confirm_alias_not_normalized'

NONCE='cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd'
SYNCFAIL_NOW="$(date -u +%s)"
write_payload "$TMP/syncfail-payload" "$((SYNCFAIL_NOW - 1))" "$((SYNCFAIL_NOW + 300))"
"$CLI" create --manifest "$TMP/syncfail-payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/syncfail-attestation" >/dev/null
mkdir -m 0700 "$TMP/syncfail-ledger" "$TMP/syncfail-bin"
chown 0:0 "$TMP/syncfail-ledger"
REAL_SYNC="$(command -v sync)"
cat >"$TMP/syncfail-bin/sync" <<'EOF'
#!/usr/bin/env bash
last="${!#}"
if [[ -d "$last" ]]; then
  exit 74
fi
"$PANDORA_TEST_REAL_SYNC" "$@"
EOF
chmod 0700 "$TMP/syncfail-bin/sync"
expect_78 initial_ledger_sync_failure env PATH="$TMP/syncfail-bin:$PATH" \
  PANDORA_TEST_REAL_SYNC="$REAL_SYNC" "$CLI" consume \
  --attestation "$TMP/syncfail-attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/syncfail-ledger"
[[ -f "$TMP/syncfail-ledger/$NONCE" ]] || fail 'syncfail_receipt_missing'
expect_78 confirm_ledger_sync_failure env PATH="$TMP/syncfail-bin:$PATH" \
  PANDORA_TEST_REAL_SYNC="$REAL_SYNC" "$CLI" consume-or-confirm \
  --attestation "$TMP/syncfail-attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/syncfail-ledger"
"$CLI" consume-or-confirm --attestation "$TMP/syncfail-attestation" \
  --public-key "$TMP/public.pem" --expected "$TMP/expected" \
  --ledger-dir "$TMP/syncfail-ledger" >"$TMP/syncfail-confirm.out"
grep -Fxq "client_auth_attestation_v2=CONFIRMED nonce=$NONCE release_id=release-00042-test" \
  "$TMP/syncfail-confirm.out" || fail 'syncfail_recovery_marker_invalid'

NONCE='abababababababababababababababababababababababababababababababab'
LEGACY_NOW="$(date -u +%s)"
write_payload "$TMP/legacy-payload" "$((LEGACY_NOW - 1))" "$((LEGACY_NOW + 300))"
"$CLI" create --manifest "$TMP/legacy-payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/legacy-attestation" >/dev/null
mkdir -m 0700 "$TMP/legacy-ledger"
chown 0:0 "$TMP/legacy-ledger"
mkdir -m 0700 "$TMP/legacy-ledger/$NONCE"
expect_78 legacy_directory "$CLI" consume-or-confirm --attestation "$TMP/legacy-attestation" \
  --public-key "$TMP/public.pem" --expected "$TMP/expected" --ledger-dir "$TMP/legacy-ledger"
NONCE="$ORIGINAL_NONCE"

bash "$ROOT/deploy/client-auth-trust-capsule-v1_static_test.sh"
printf 'client_auth_attestation_v2_mock=PASS cases=create,verify,tamper,expired,wrong_release,wrong_identity,consume,replay,confirm,mismatch,expired_confirm,prelink_crash,postlink_alias_recovery,syncfail_confirm,legacy_directory\n'
