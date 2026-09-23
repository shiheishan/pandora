#!/usr/bin/env bash
# Native Linux/root crash and path-rebinding gate for CLIENT-AUTH attestation v2.
set -Eeuo pipefail
umask 077
export PATH='/usr/bin:/bin'

readonly EXIT_DENIED=78
SELF="$(/usr/bin/realpath "$0")"
ROOT="$(cd "$(/usr/bin/dirname "$0")/.." && pwd -P)"
CLI="$ROOT/deploy/client-auth-attestation-v2.sh"

fail() {
  printf 'client_auth_attestation_v2_linux_fault=FAIL reason=%s\n' "$1" >&2
  exit 1
}

INNER_MODE="${PANDORA_CA_V2_FAULT_INNER:-0}"
if [[ "$INNER_MODE" == '1' ]]; then
  [[ "$(/usr/bin/uname -s)" == 'Linux' ]] || fail 'inner_linux_required'
  [[ "$EUID" -eq 0 ]] || fail 'inner_root_required'
  [[ "$$" -eq 1 && "$PPID" -eq 0 ]] || fail 'inner_pid_namespace_not_init'
fi

if [[ "$INNER_MODE" != '1' ]]; then
  [[ "$(/usr/bin/uname -s)" == 'Linux' ]] || {
    printf 'client_auth_attestation_v2_linux_fault=NOT_RUN reason=linux_required\n' >&2
    exit "$EXIT_DENIED"
  }
  [[ "$EUID" -eq 0 ]] || {
    printf 'client_auth_attestation_v2_linux_fault=NOT_RUN reason=root_required\n' >&2
    exit "$EXIT_DENIED"
  }
  for tool in /usr/bin/cat /usr/bin/chmod /usr/bin/chown /usr/bin/cp /usr/bin/date \
    /usr/bin/dirname /usr/bin/env /usr/bin/findmnt /usr/bin/grep /usr/bin/ln \
    /usr/bin/mkdir /usr/bin/mount /usr/bin/mktemp /usr/bin/mv /usr/bin/openssl \
    /usr/bin/realpath /usr/bin/rm /usr/bin/sha256sum /usr/bin/stat /usr/bin/sync /usr/bin/touch \
    /usr/bin/umount /usr/bin/unshare /bin/bash; do
    [[ -x "$tool" ]] || fail "tool_missing:$tool"
  done
  OUTER_TMP="$(/usr/bin/mktemp -d /root/pandora-ca-v2-linux-fault.XXXXXX)" \
    || fail 'outer_tmp_create_failed'
  /usr/bin/chmod 0700 "$OUTER_TMP" || fail 'outer_tmp_chmod_failed'
  cleanup_outer() {
    local status=$?
    case "$OUTER_TMP" in
      /root/pandora-ca-v2-linux-fault.*)
        [[ ! -e "$OUTER_TMP" ]] || /usr/bin/rm -rf -- "$OUTER_TMP" || status=1
        ;;
      *) status=1 ;;
    esac
    exit "$status"
  }
  trap cleanup_outer EXIT
  /usr/bin/cp -- /usr/bin/ln "$OUTER_TMP/real-ln"
  /usr/bin/cp -- /usr/bin/sync "$OUTER_TMP/real-sync"
  /usr/bin/cp -- "$SELF" "$OUTER_TMP/runner.sh"
  /usr/bin/cp -- "$CLI" "$OUTER_TMP/client-auth-attestation-v2.sh"
  /usr/bin/chown 0:0 "$OUTER_TMP/real-ln" "$OUTER_TMP/real-sync" \
    "$OUTER_TMP/runner.sh" "$OUTER_TMP/client-auth-attestation-v2.sh"
  /usr/bin/chmod 0500 "$OUTER_TMP/real-ln" "$OUTER_TMP/real-sync" \
    "$OUTER_TMP/runner.sh" "$OUTER_TMP/client-auth-attestation-v2.sh"
  RUNNER_SHA256="$(/usr/bin/sha256sum "$OUTER_TMP/runner.sh")"
  RUNNER_SHA256="${RUNNER_SHA256%% *}"
  CORE_SHA256="$(/usr/bin/sha256sum "$OUTER_TMP/client-auth-attestation-v2.sh")"
  CORE_SHA256="${CORE_SHA256%% *}"
  INNER_CAPABILITY="$(/usr/bin/openssl rand -hex 32)" || fail 'inner_capability_failed'
  printf '%s\n' "$INNER_CAPABILITY" >"$OUTER_TMP/inner.cap"
  /usr/bin/chown 0:0 "$OUTER_TMP/inner.cap"
  /usr/bin/chmod 0400 "$OUTER_TMP/inner.cap"
  PARENT_MNT_NS="$(/usr/bin/stat -Lc '%d:%i' /proc/self/ns/mnt)" \
    || fail 'parent_mount_namespace_stat_failed'
  set +e
  /usr/bin/unshare --mount --pid --fork --mount-proc \
    /usr/bin/env -i PATH=/usr/bin:/bin HOME=/root \
    PANDORA_CA_V2_FAULT_INNER=1 PANDORA_CA_V2_FAULT_TMP="$OUTER_TMP" \
    PANDORA_CA_V2_INNER_CAPABILITY="$INNER_CAPABILITY" \
    PANDORA_CA_V2_PARENT_MNT_NS="$PARENT_MNT_NS" \
    PANDORA_CA_V2_RUNNER_SHA256="$RUNNER_SHA256" \
    PANDORA_CA_V2_CORE_SHA256="$CORE_SHA256" \
    /bin/bash "$OUTER_TMP/runner.sh"
  inner_status=$?
  set -e
  [[ "$inner_status" -eq 0 ]] || fail "inner_status:$inner_status"
  [[ ! -e "$OUTER_TMP" ]] || fail 'inner_tmp_residue'
  trap - EXIT
  printf 'client_auth_attestation_v2_linux_fault=PASS namespace=mount,pid cases=private_key_insecure_parent,attestation_insecure_parent,attestation_file_sync,attestation_directory_sync,attestation_parent_rebind,attestation_target_substitute,postlink_alias,sync_failure,parent_rebind residue=zero runner_sha256=%s core_sha256=%s\n' \
    "$RUNNER_SHA256" "$CORE_SHA256"
  exit 0
fi

TMP="${PANDORA_CA_V2_FAULT_TMP:-}"
[[ "$TMP" == /root/pandora-ca-v2-linux-fault.* && -d "$TMP" && ! -L "$TMP" ]] \
  || fail 'inner_tmp_invalid'
[[ "$(/usr/bin/stat -c '%u:%a' -- "$TMP")" == '0:700' ]] || fail 'inner_tmp_metadata_invalid'
[[ -f "$TMP/inner.cap" && ! -L "$TMP/inner.cap" \
  && "$(/usr/bin/stat -c '%u:%a' -- "$TMP/inner.cap")" == '0:400' ]] \
  || fail 'inner_capability_metadata_invalid'
[[ "${PANDORA_CA_V2_INNER_CAPABILITY:-}" =~ ^[0-9a-f]{64}$ \
  && "$(<"$TMP/inner.cap")" == "$PANDORA_CA_V2_INNER_CAPABILITY" ]] \
  || fail 'inner_capability_invalid'
CURRENT_MNT_NS="$(/usr/bin/stat -Lc '%d:%i' /proc/self/ns/mnt)" \
  || fail 'inner_mount_namespace_stat_failed'
[[ "${PANDORA_CA_V2_PARENT_MNT_NS:-}" =~ ^[0-9]+:[0-9]+$ \
  && "$CURRENT_MNT_NS" != "$PANDORA_CA_V2_PARENT_MNT_NS" ]] \
  || fail 'inner_mount_namespace_not_isolated'
[[ "$SELF" == "$TMP/runner.sh" \
  && "$(/usr/bin/stat -c '%u:%a' -- "$SELF")" == '0:500' ]] \
  || fail 'runner_snapshot_metadata_invalid'
CLI="$TMP/client-auth-attestation-v2.sh"
[[ -f "$CLI" && ! -L "$CLI" && "$(/usr/bin/stat -c '%u:%a' -- "$CLI")" == '0:500' ]] \
  || fail 'core_snapshot_metadata_invalid'
ACTUAL_RUNNER_SHA256="$(/usr/bin/sha256sum "$SELF")"
ACTUAL_RUNNER_SHA256="${ACTUAL_RUNNER_SHA256%% *}"
ACTUAL_CORE_SHA256="$(/usr/bin/sha256sum "$CLI")"
ACTUAL_CORE_SHA256="${ACTUAL_CORE_SHA256%% *}"
[[ "$ACTUAL_RUNNER_SHA256" == "${PANDORA_CA_V2_RUNNER_SHA256:-}" \
  && "$ACTUAL_CORE_SHA256" == "${PANDORA_CA_V2_CORE_SHA256:-}" ]] \
  || fail 'snapshot_hash_mismatch'

LN_MOUNTED=0
SYNC_MOUNTED=0
cleanup_inner() {
  local status=$?
  set +e
  if [[ "$SYNC_MOUNTED" -eq 1 ]]; then /usr/bin/umount /usr/bin/sync || status=1; fi
  if [[ "$LN_MOUNTED" -eq 1 ]]; then /usr/bin/umount /usr/bin/ln || status=1; fi
  case "$TMP" in
    /root/pandora-ca-v2-linux-fault.*) /usr/bin/rm -rf -- "$TMP" || status=1 ;;
    *) status=1 ;;
  esac
  exit "$status"
}
trap cleanup_inner EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

/usr/bin/mount --make-rprivate / || fail 'mount_tree_not_private'
/usr/bin/openssl genpkey -algorithm ED25519 -out "$TMP/private.pem" >/dev/null 2>&1 \
  || fail 'key_generation_failed'
/usr/bin/chmod 0600 "$TMP/private.pem"
/usr/bin/openssl pkey -in "$TMP/private.pem" -pubout -out "$TMP/public.pem" \
  >/dev/null 2>&1 || fail 'public_key_generation_failed'

H1='1111111111111111111111111111111111111111111111111111111111111111'
H2='2222222222222222222222222222222222222222222222222222222222222222'
H3='3333333333333333333333333333333333333333333333333333333333333333'
H4='4444444444444444444444444444444444444444444444444444444444444444'
H5='5555555555555555555555555555555555555555555555555555555555555555'
H6='6666666666666666666666666666666666666666666666666666666666666666'
H7='7777777777777777777777777777777777777777777777777777777777777777'
H8='8888888888888888888888888888888888888888888888888888888888888888'
CONTAINER='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'

write_payload() {
  local path="$1" nonce="$2" now
  now="$(/usr/bin/date -u +%s)"
  {
    printf 'format=client-auth-attestation-v2\n'
    printf 'signature_algorithm=ed25519\n'
    printf 'nonce=%s\n' "$nonce"
    printf 'issued_at=%s\n' "$((now - 1))"
    printf 'expires_at=%s\n' "$((now + 300))"
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

{
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
} >"$TMP/expected"

NONCE_LINK='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
NONCE_SYNC='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
NONCE_SWAP='dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
NONCE_CREATE_FILE_SYNC='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'
NONCE_CREATE_DIR_SYNC='ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
NONCE_CREATE_REBIND='abababababababababababababababababababababababababababababababab'
NONCE_CREATE_SUBSTITUTE="$H1"
NONCE_CREATE_INSECURE="$H2"
NONCE_KEY_INSECURE="$H3"
for name in link sync swap; do
  case "$name" in
    link) nonce="$NONCE_LINK" ;;
    sync) nonce="$NONCE_SYNC" ;;
    swap) nonce="$NONCE_SWAP" ;;
  esac
  write_payload "$TMP/$name.payload" "$nonce"
  "$CLI" create --manifest "$TMP/$name.payload" --private-key "$TMP/private.pem" \
    --attestation "$TMP/$name.attestation" >/dev/null || fail "attestation_create:$name"
done

/usr/bin/cat >"$TMP/ln-wrapper" <<'EOF'
#!/bin/bash
set -eu
"${PANDORA_NATIVE_REAL_LN:?}" "$@"
if [[ "${PANDORA_NATIVE_FAULT_PHASE:-}" == 'attestation-target-substitute' ]]; then
  last="${!#}"
  /usr/bin/touch "${PANDORA_NATIVE_CREATE_SUBSTITUTE_DONE:?}"
  /usr/bin/rm -f -- "$last"
  printf 'substituted-attestation\n' >"$last"
  /usr/bin/chmod 0600 -- "$last"
  exit 0
fi
if [[ "${PANDORA_NATIVE_FAULT_PHASE:-}" == 'postlink-kill' ]]; then
  kill -KILL "$PPID"
fi
EOF
/usr/bin/cat >"$TMP/sync-wrapper" <<'EOF'
#!/bin/bash
set -eu
last="${!#}"
if [[ "${PANDORA_NATIVE_FAULT_PHASE:-}" == 'attestation-parent-rebind' \
  && -f "$last" && "${last##*/}" == .pandora-attestation-v2.* \
  && ! -e "${PANDORA_NATIVE_CREATE_REBIND_DONE:?}" ]]; then
  "${PANDORA_NATIVE_REAL_SYNC:?}" "$@"
  /usr/bin/touch "${PANDORA_NATIVE_CREATE_REBIND_DONE:?}"
  /usr/bin/mv -- "${PANDORA_NATIVE_CREATE_REBIND_PARENT:?}" \
    "${PANDORA_NATIVE_CREATE_REBIND_MOVED:?}"
  /usr/bin/mkdir -m 0700 -- "${PANDORA_NATIVE_CREATE_REBIND_PARENT:?}"
  exit 0
fi
if [[ "${PANDORA_NATIVE_FAULT_PHASE:-}" == 'attestation-file-sync-fail' \
  && -f "$last" && "${last##*/}" == .pandora-attestation-v2.* ]]; then
  exit 74
fi
if [[ "${PANDORA_NATIVE_FAULT_PHASE:-}" == 'attestation-dir-sync-fail' && -d "$last" ]]; then
  exit 74
fi
if [[ "${PANDORA_NATIVE_FAULT_PHASE:-}" == 'ledger-sync-fail' && -d "$last" ]]; then
  exit 74
fi
"${PANDORA_NATIVE_REAL_SYNC:?}" "$@"
if [[ "${PANDORA_NATIVE_FAULT_PHASE:-}" == 'parent-rebind' \
  && -f "$last" && ! -e "${PANDORA_NATIVE_SWAP_DONE:?}" ]]; then
  /usr/bin/touch "${PANDORA_NATIVE_SWAP_DONE}"
  /usr/bin/mv -- "${PANDORA_NATIVE_SWAP_LEDGER:?}" "${PANDORA_NATIVE_SWAP_MOVED:?}"
  "${PANDORA_NATIVE_REAL_LN:?}" -s -- "${PANDORA_NATIVE_SWAP_OUTSIDE:?}" \
    "${PANDORA_NATIVE_SWAP_LEDGER:?}"
fi
EOF
/usr/bin/chown 0:0 "$TMP/ln-wrapper" "$TMP/sync-wrapper"
/usr/bin/chmod 0500 "$TMP/ln-wrapper" "$TMP/sync-wrapper"
/usr/bin/mount --bind "$TMP/ln-wrapper" /usr/bin/ln || fail 'ln_wrapper_bind_failed'
LN_MOUNTED=1
/usr/bin/mount --bind "$TMP/sync-wrapper" /usr/bin/sync || fail 'sync_wrapper_bind_failed'
SYNC_MOUNTED=1
/usr/bin/findmnt -rn -M /usr/bin/ln -o TARGET | /usr/bin/grep -Fxq /usr/bin/ln \
  || fail 'ln_bind_not_visible'
/usr/bin/findmnt -rn -M /usr/bin/sync -o TARGET | /usr/bin/grep -Fxq /usr/bin/sync \
  || fail 'sync_bind_not_visible'

run_cli() {
  /usr/bin/env PATH=/usr/bin:/bin \
    PANDORA_NATIVE_REAL_LN="$TMP/real-ln" PANDORA_NATIVE_REAL_SYNC="$TMP/real-sync" \
    "$CLI" "$@"
}

mkdir_ledger() {
  /usr/bin/mkdir -m 0700 -- "$1"
  /usr/bin/chown 0:0 -- "$1"
}

expect_78() {
  local label="$1"
  shift
  set +e
  "$@" >"$TMP/$label.out" 2>"$TMP/$label.err"
  local status=$?
  set -e
  [[ "$status" -eq 78 ]] || fail "$label:status=$status"
  [[ ! -s "$TMP/$label.out" ]] || fail "$label:success_output"
}

write_payload "$TMP/create-insecure.payload" "$NONCE_CREATE_INSECURE"
/usr/bin/mkdir -m 0777 -- "$TMP/create-insecure-parent"
expect_78 create_insecure_parent run_cli create \
  --manifest "$TMP/create-insecure.payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/create-insecure-parent/attestation"
[[ ! -e "$TMP/create-insecure-parent/attestation" \
  && ! -L "$TMP/create-insecure-parent/attestation" ]] \
  || fail 'create_insecure_parent_published_output'

write_payload "$TMP/key-insecure.payload" "$NONCE_KEY_INSECURE"
/usr/bin/mkdir -m 0777 -- "$TMP/key-insecure-parent"
/usr/bin/cp -- "$TMP/private.pem" "$TMP/key-insecure-parent/private.pem"
/usr/bin/chown 0:0 -- "$TMP/key-insecure-parent/private.pem"
/usr/bin/chmod 0600 -- "$TMP/key-insecure-parent/private.pem"
expect_78 private_key_insecure_parent run_cli create \
  --manifest "$TMP/key-insecure.payload" \
  --private-key "$TMP/key-insecure-parent/private.pem" \
  --attestation "$TMP/key-insecure.attestation"
[[ ! -e "$TMP/key-insecure.attestation" && ! -L "$TMP/key-insecure.attestation" ]] \
  || fail 'private_key_insecure_parent_published_output'

write_payload "$TMP/create-file-sync.payload" "$NONCE_CREATE_FILE_SYNC"
expect_78 create_file_sync /usr/bin/env PATH=/usr/bin:/bin \
  PANDORA_NATIVE_REAL_LN="$TMP/real-ln" PANDORA_NATIVE_REAL_SYNC="$TMP/real-sync" \
  PANDORA_NATIVE_FAULT_PHASE=attestation-file-sync-fail "$CLI" create \
  --manifest "$TMP/create-file-sync.payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/create-file-sync.attestation"
[[ ! -e "$TMP/create-file-sync.attestation" && ! -L "$TMP/create-file-sync.attestation" ]] \
  || fail 'create_file_sync_published_output'

write_payload "$TMP/create-dir-sync.payload" "$NONCE_CREATE_DIR_SYNC"
expect_78 create_directory_sync /usr/bin/env PATH=/usr/bin:/bin \
  PANDORA_NATIVE_REAL_LN="$TMP/real-ln" PANDORA_NATIVE_REAL_SYNC="$TMP/real-sync" \
  PANDORA_NATIVE_FAULT_PHASE=attestation-dir-sync-fail "$CLI" create \
  --manifest "$TMP/create-dir-sync.payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/create-dir-sync.attestation"
[[ -f "$TMP/create-dir-sync.attestation" && ! -L "$TMP/create-dir-sync.attestation" ]] \
  || fail 'create_directory_sync_output_missing'
[[ "$(/usr/bin/stat -c '%a:%h' "$TMP/create-dir-sync.attestation")" == '600:1' ]] \
  || fail 'create_directory_sync_output_metadata_invalid'
run_cli verify --attestation "$TMP/create-dir-sync.attestation" \
  --public-key "$TMP/public.pem" --expected "$TMP/expected" >/dev/null \
  || fail 'create_directory_sync_output_not_verifiable'

write_payload "$TMP/create-rebind.payload" "$NONCE_CREATE_REBIND"
/usr/bin/mkdir -m 0700 -- "$TMP/create-rebind-parent"
expect_78 create_parent_rebind /usr/bin/env PATH=/usr/bin:/bin \
  PANDORA_NATIVE_REAL_LN="$TMP/real-ln" PANDORA_NATIVE_REAL_SYNC="$TMP/real-sync" \
  PANDORA_NATIVE_FAULT_PHASE=attestation-parent-rebind \
  PANDORA_NATIVE_CREATE_REBIND_DONE="$TMP/create-rebind.done" \
  PANDORA_NATIVE_CREATE_REBIND_PARENT="$TMP/create-rebind-parent" \
  PANDORA_NATIVE_CREATE_REBIND_MOVED="$TMP/create-rebind-parent.moved" \
  "$CLI" create --manifest "$TMP/create-rebind.payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/create-rebind-parent/attestation"
[[ ! -e "$TMP/create-rebind-parent/attestation" && ! -L "$TMP/create-rebind-parent/attestation" ]] \
  || fail 'create_parent_rebind_redirected_output'
[[ -f "$TMP/create-rebind-parent.moved/attestation" \
  && ! -L "$TMP/create-rebind-parent.moved/attestation" ]] \
  || fail 'create_parent_rebind_fd_anchor_output_missing'

write_payload "$TMP/create-substitute.payload" "$NONCE_CREATE_SUBSTITUTE"
/usr/bin/mkdir -m 0700 -- "$TMP/create-substitute-parent"
expect_78 create_target_substitute /usr/bin/env PATH=/usr/bin:/bin \
  PANDORA_NATIVE_REAL_LN="$TMP/real-ln" PANDORA_NATIVE_REAL_SYNC="$TMP/real-sync" \
  PANDORA_NATIVE_FAULT_PHASE=attestation-target-substitute \
  PANDORA_NATIVE_CREATE_SUBSTITUTE_DONE="$TMP/create-substitute.done" \
  "$CLI" create --manifest "$TMP/create-substitute.payload" --private-key "$TMP/private.pem" \
  --attestation "$TMP/create-substitute-parent/attestation"
if [[ ! -f "$TMP/create-substitute.done" || -L "$TMP/create-substitute.done" ]]; then
  /usr/bin/cat "$TMP/create_target_substitute.err" >&2 || true
  fail 'create_target_substitution_not_exercised'
fi

mkdir_ledger "$TMP/link-ledger"
set +e
/usr/bin/env PATH=/usr/bin:/bin PANDORA_NATIVE_REAL_LN="$TMP/real-ln" \
  PANDORA_NATIVE_REAL_SYNC="$TMP/real-sync" PANDORA_NATIVE_FAULT_PHASE=postlink-kill \
  "$CLI" consume --attestation "$TMP/link.attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/link-ledger" \
  >"$TMP/postlink.out" 2>"$TMP/postlink.err"
postlink_status=$?
set -e
[[ "$postlink_status" -eq 137 ]] || fail "postlink_status:$postlink_status"
[[ -f "$TMP/link-ledger/$NONCE_LINK" ]] || fail 'postlink_receipt_missing'
[[ "$(/usr/bin/stat -c '%h' "$TMP/link-ledger/$NONCE_LINK")" == '2' ]] \
  || fail 'postlink_nlink_not_two'
run_cli consume-or-confirm --attestation "$TMP/link.attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/link-ledger" >"$TMP/postlink-confirm.out"
/usr/bin/grep -Fxq "client_auth_attestation_v2=CONFIRMED nonce=$NONCE_LINK release_id=release-00042-test" \
  "$TMP/postlink-confirm.out" || fail 'postlink_confirm_marker'
[[ "$(/usr/bin/stat -c '%h' "$TMP/link-ledger/$NONCE_LINK")" == '1' ]] \
  || fail 'postlink_nlink_not_normalized'

mkdir_ledger "$TMP/sync-ledger"
expect_78 sync_initial /usr/bin/env PATH=/usr/bin:/bin \
  PANDORA_NATIVE_REAL_LN="$TMP/real-ln" PANDORA_NATIVE_REAL_SYNC="$TMP/real-sync" \
  PANDORA_NATIVE_FAULT_PHASE=ledger-sync-fail "$CLI" consume \
  --attestation "$TMP/sync.attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/sync-ledger"
[[ -f "$TMP/sync-ledger/$NONCE_SYNC" ]] || fail 'sync_failure_receipt_missing'
expect_78 sync_confirm /usr/bin/env PATH=/usr/bin:/bin \
  PANDORA_NATIVE_REAL_LN="$TMP/real-ln" PANDORA_NATIVE_REAL_SYNC="$TMP/real-sync" \
  PANDORA_NATIVE_FAULT_PHASE=ledger-sync-fail "$CLI" consume-or-confirm \
  --attestation "$TMP/sync.attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/sync-ledger"
run_cli consume-or-confirm --attestation "$TMP/sync.attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/sync-ledger" >"$TMP/sync-recovery.out"
/usr/bin/grep -Fq 'client_auth_attestation_v2=CONFIRMED' "$TMP/sync-recovery.out" \
  || fail 'sync_recovery_marker'

mkdir_ledger "$TMP/swap-ledger"
mkdir_ledger "$TMP/swap-outside"
expect_78 parent_rebind /usr/bin/env PATH=/usr/bin:/bin \
  PANDORA_NATIVE_REAL_LN="$TMP/real-ln" PANDORA_NATIVE_REAL_SYNC="$TMP/real-sync" \
  PANDORA_NATIVE_FAULT_PHASE=parent-rebind PANDORA_NATIVE_SWAP_DONE="$TMP/swap.done" \
  PANDORA_NATIVE_SWAP_LEDGER="$TMP/swap-ledger" \
  PANDORA_NATIVE_SWAP_MOVED="$TMP/swap-ledger.moved" \
  PANDORA_NATIVE_SWAP_OUTSIDE="$TMP/swap-outside" "$CLI" consume \
  --attestation "$TMP/swap.attestation" --public-key "$TMP/public.pem" \
  --expected "$TMP/expected" --ledger-dir "$TMP/swap-ledger"
[[ ! -e "$TMP/swap-outside/$NONCE_SWAP" && ! -L "$TMP/swap-outside/$NONCE_SWAP" ]] \
  || fail 'parent_rebind_redirected_receipt'
[[ -f "$TMP/swap-ledger.moved/$NONCE_SWAP" ]] || fail 'fd_anchor_receipt_missing'

/usr/bin/umount /usr/bin/sync || fail 'sync_wrapper_unmount_failed'
SYNC_MOUNTED=0
/usr/bin/umount /usr/bin/ln || fail 'ln_wrapper_unmount_failed'
LN_MOUNTED=0
/usr/bin/rm -rf -- "$TMP" || fail 'inner_tmp_cleanup_failed'
trap - EXIT
exit 0
