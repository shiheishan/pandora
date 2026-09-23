#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

readonly EXIT_DENIED=78
readonly CONTRACT_SHA='E8C323762014B5F97D04CF861287026F0D3784D56B2324E1E6B0BC14D833DAC7'
deny() { printf 'client_auth_00043_manifest=DENY reason=%s\n' "$1" >&2; exit "$EXIT_DENIED"; }
safe_token() { [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; }
safe_sha() { [[ "$1" =~ ^[0-9a-f]{64}$ ]]; }
sha_file() { sha256sum -- "$1" | awk '{print $1}'; }
validate_trusted_artifact() {
  local path="$1" mode
  [[ -f "$path" && ! -L "$path" ]] || deny trusted_artifact_invalid
  [[ "$(stat -c '%u:%h' "$path")" == '0:1' ]] || deny trusted_artifact_owner_or_link_invalid
  mode="$(stat -c '%a' "$path")"
  (( (8#$mode & 8#022) == 0 )) || deny trusted_artifact_group_world_writable
}

[[ $# -eq 2 ]] || deny 'usage: --placeholder|--generate OUTPUT'
MODE="$1"; OUTPUT="$2"
[[ "$OUTPUT" == /* && ! -e "$OUTPUT" && ! -L "$OUTPUT" ]] || deny output_must_be_new_absolute_path
OUT_DIR="$(dirname "$OUTPUT")"
[[ -d "$OUT_DIR" && ! -L "$OUT_DIR" ]] || deny output_directory_invalid
STAGE="$(mktemp "$OUT_DIR/.client-auth-00043-manifest.XXXXXX")"
trap 'rm -f -- "$STAGE"' EXIT

if [[ "$MODE" == '--placeholder' ]]; then
  {
    printf 'format=client-auth-00043-v3-release-manifest\n'
    printf 'status=PLACEHOLDER_NO_GO\n'
    printf 'v3_contract_sha256=%s\n' "$CONTRACT_SHA"
    printf 'reason=trusted_amd64_arm64_goose_psql_and_install_root_not_frozen\n'
  } >"$STAGE"
else
  [[ "$MODE" == '--generate' ]] || deny unknown_mode
  [[ "$EUID" -eq 0 ]] || deny root_required
  [[ "${PANDORA_CLIENT_AUTH_00043_MANIFEST_GENERATE_APPROVED:-}" == 'approved-trusted-release-artifacts-v1' ]] || deny trusted_generation_approval_missing
  RELEASE_ID="${PANDORA_CLIENT_AUTH_00043_RELEASE_ID:-}"
  ARCH="${PANDORA_CLIENT_AUTH_00043_ARCHITECTURE:-}"
  INSTALL_ROOT="${PANDORA_CLIENT_AUTH_00043_INSTALL_ROOT:-}"
  MIGRATIONS_DIR="${PANDORA_CLIENT_AUTH_00043_MIGRATIONS_DIR:-}"
  RUNNER="${PANDORA_CLIENT_AUTH_00043_RUNNER_FILE:-}"
  MIGRATION43="${PANDORA_CLIENT_AUTH_00043_MIGRATION_FILE:-}"
  POST_GOOSE_VERIFIER="${PANDORA_CLIENT_AUTH_00043_POST_GOOSE_VERIFIER_FILE:-}"
  PSQL="${PANDORA_CLIENT_AUTH_00043_PSQL_BIN:-}"
  GOOSE="${PANDORA_CLIENT_AUTH_00043_GOOSE_BIN:-}"
  [[ "$RELEASE_ID" == 'client-auth-00043-v3' ]] || deny release_id_invalid
  [[ "$ARCH" == amd64 || "$ARCH" == arm64 ]] || deny architecture_invalid
  for path in "$INSTALL_ROOT" "$MIGRATIONS_DIR" "$RUNNER" "$MIGRATION43" "$POST_GOOSE_VERIFIER" "$PSQL" "$GOOSE"; do
    [[ "$path" == /* && -e "$path" && ! -L "$path" ]] || deny trusted_path_invalid
  done
  INSTALL_REAL="$(realpath -e -- "$INSTALL_ROOT")"; MIGRATIONS_REAL="$(realpath -e -- "$MIGRATIONS_DIR")"
  RUNNER_REAL="$(realpath -e -- "$RUNNER")"; MIGRATION_REAL="$(realpath -e -- "$MIGRATION43")"
  POST_GOOSE_VERIFIER_REAL="$(realpath -e -- "$POST_GOOSE_VERIFIER")"
  PSQL_REAL="$(realpath -e -- "$PSQL")"; GOOSE_REAL="$(realpath -e -- "$GOOSE")"
  [[ "$MIGRATIONS_REAL" == "$INSTALL_REAL"/* && "$RUNNER_REAL" == "$INSTALL_REAL"/* &&
     "$MIGRATION_REAL" == "$MIGRATIONS_REAL"/* &&
     "$POST_GOOSE_VERIFIER_REAL" == "$INSTALL_REAL/deploy/verify-client-auth-00043-post-goose.sql" ]] \
    || deny install_root_containment_failed
  validate_trusted_artifact "$RUNNER_REAL"
  validate_trusted_artifact "$MIGRATION_REAL"
  validate_trusted_artifact "$POST_GOOSE_VERIFIER_REAL"
  validate_trusted_artifact "$PSQL_REAL"
  validate_trusted_artifact "$GOOSE_REAL"
  PSQL_VERSION="$("$PSQL_REAL" --version | tr -d '\r\n')"
  GOOSE_VERSION="$("$GOOSE_REAL" -version | tr -d '\r\n')"
  [[ "$PSQL_VERSION" =~ ^psql[[:space:]] && ${#PSQL_VERSION} -le 160 ]] || deny psql_version_invalid
  [[ -n "$GOOSE_VERSION" && ${#GOOSE_VERSION} -le 160 ]] || deny goose_version_invalid
  INVENTORY="$(mktemp "$OUT_DIR/.client-auth-00043-inventory.XXXXXX")"
  trap 'rm -f -- "$STAGE" "${INVENTORY:-}"' EXIT
  while IFS= read -r sql_file; do
    [[ -f "$sql_file" && ! -L "$sql_file" ]] || deny migration_inventory_entry_unsafe
    relative="${sql_file#"$MIGRATIONS_REAL"/}"
    [[ "$relative" != "$sql_file" && "$relative" =~ ^[A-Za-z0-9._/-]+\.sql$ ]] || deny inventory_relative_path_invalid
    printf 'migration=%s|%s|%s\n' "$relative" "$(stat -c '%s' "$sql_file")" "$(sha_file "$sql_file")"
  done < <(find "$MIGRATIONS_REAL" -maxdepth 1 -name '*.sql' -print | LC_ALL=C sort) >"$INVENTORY"
  COUNT="$(wc -l <"$INVENTORY" | tr -d ' ')"
  ((COUNT > 0)) || deny empty_migration_inventory
  {
    printf 'format=client-auth-00043-v3-release-manifest\n'
    printf 'status=TRUSTED\n'
    printf 'release_id=%s\n' "$RELEASE_ID"
    printf 'architecture=%s\n' "$ARCH"
    printf 'v3_contract_sha256=%s\n' "$CONTRACT_SHA"
    printf 'install_root=%s\n' "$INSTALL_REAL"
    printf 'migrations_dir=%s\n' "$MIGRATIONS_REAL"
    printf 'runner_path=%s\n' "$RUNNER_REAL"
    printf 'runner_sha256=%s\n' "$(sha_file "$RUNNER_REAL")"
    printf 'migration_00043_path=%s\n' "$MIGRATION_REAL"
    printf 'migration_00043_sha256=%s\n' "$(sha_file "$MIGRATION_REAL")"
    printf 'psql_path=%s\n' "$PSQL_REAL"
    printf 'psql_sha256=%s\n' "$(sha_file "$PSQL_REAL")"
    printf 'psql_version=%s\n' "$PSQL_VERSION"
    printf 'goose_path=%s\n' "$GOOSE_REAL"
    printf 'goose_sha256=%s\n' "$(sha_file "$GOOSE_REAL")"
    printf 'goose_version=%s\n' "$GOOSE_VERSION"
    printf 'post_goose_verifier_path=%s\n' "$POST_GOOSE_VERIFIER_REAL"
    printf 'post_goose_verifier_sha256=%s\n' "$(sha_file "$POST_GOOSE_VERIFIER_REAL")"
    printf 'inventory_count=%s\n' "$COUNT"
    cat "$INVENTORY"
    printf 'inventory_sha256=%s\n' "$(sha_file "$INVENTORY")"
  } >"$STAGE"
  rm -f "$INVENTORY"
fi

chmod 0600 "$STAGE"
sync -f "$STAGE" 2>/dev/null || deny manifest_file_fsync_failed
ln -- "$STAGE" "$OUTPUT" || deny manifest_no_clobber_publish_failed
rm -f "$STAGE"
sync -f "$OUT_DIR" 2>/dev/null || deny manifest_directory_fsync_failed
trap - EXIT
printf 'client_auth_00043_manifest=PASS output=%s\n' "$OUTPUT"
