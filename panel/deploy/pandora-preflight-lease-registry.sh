#!/usr/bin/env bash
set -euo pipefail
umask 077

readonly EXIT_DENIED=78
readonly FORMAT_VERSION='1'
readonly DEFAULT_REGISTRY_ROOT='/var/lib/pandora/preflight-leases'
readonly MAX_EPOCH='253402300799'
readonly MAX_PID='4194304'
readonly MAX_LEASE_SECONDS='3600'

deny() {
  printf 'pandora_lease_registry=DENY reason=%s\n' "$1" >&2
  exit "$EXIT_DENIED"
}

TEST_MODE="${PANDORA_LEASE_REGISTRY_TEST_MODE:-0}"
if [[ "$TEST_MODE" == '1' ]]; then
  [[ "$EUID" -ne 0 ]] || deny 'test_mode_forbidden_for_root'
  REGISTRY_ROOT="${PANDORA_LEASE_REGISTRY_TEST_ROOT:-}"
  PROC_ROOT="${PANDORA_LEASE_REGISTRY_TEST_PROC_ROOT:-}"
  [[ "$REGISTRY_ROOT" == /* && "$PROC_ROOT" == /* ]] || deny 'test_paths_must_be_absolute'
  EXPECTED_UID="$EUID"
else
  [[ "$EUID" -eq 0 ]] || deny 'root_required'
  PATH='/usr/sbin:/usr/bin:/sbin:/bin'
  export PATH
  REGISTRY_ROOT="$DEFAULT_REGISTRY_ROOT"
  PROC_ROOT='/proc'
  EXPECTED_UID='0'
fi

readonly ACTIVE_DIR="$REGISTRY_ROOT/active"
readonly TOMBSTONE_DIR="$REGISTRY_ROOT/tombstones"
readonly LOCK_DIR="$REGISTRY_ROOT/locks"

durable_registry_sync() {
  [[ "$TEST_MODE" == '1' ]] && return 0
  command -v sync >/dev/null 2>&1 || deny 'sync_not_found'
  sync -f -- "$REGISTRY_ROOT" || deny 'registry_durable_sync_failed'
}

has_line_break() {
  [[ "$1" == *$'\n'* || "$1" == *$'\r'* ]]
}

valid_run_id() {
  [[ "$1" =~ ^pandoraisolatedpg18[A-Za-z0-9]{6}-[1-9][0-9]{0,9}$ ]]
}

valid_capability() {
  [[ "$1" =~ ^[0-9a-f]{64}$ ]]
}

valid_object_id() {
  [[ "$1" =~ ^[0-9a-f]{64}$ ]]
}

valid_sha256_identity() {
  [[ "$1" =~ ^sha256:[0-9a-f]{64}$ ]]
}

valid_positive_decimal() {
  local value="$1"
  local max="$2"
  local max_digits="${#max}"
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || return 1
  ((${#value} < max_digits)) && return 0
  ((${#value} > max_digits)) && return 1
  [[ "$value" < "$max" || "$value" == "$max" ]]
}

now_epoch() {
  local value
  if [[ "$TEST_MODE" == '1' && -n "${PANDORA_LEASE_REGISTRY_TEST_NOW:-}" ]]; then
    value="$PANDORA_LEASE_REGISTRY_TEST_NOW"
  else
    value="$(date -u +%s 2>/dev/null)" || deny 'clock_unavailable'
  fi
  valid_positive_decimal "$value" "$MAX_EPOCH" || deny 'clock_invalid_or_overflow'
  printf '%s\n' "$value"
}

stat_value() {
  stat -c "$1" -- "$2" 2>/dev/null
}

validate_directory() {
  local path="$1"
  [[ -d "$path" && ! -L "$path" ]] || deny "registry_directory_invalid:${path}"
  [[ "$(stat_value '%u' "$path")" == "$EXPECTED_UID" ]] || deny "registry_directory_wrong_owner:${path}"
  [[ "$(stat_value '%a' "$path")" == '700' ]] || deny "registry_directory_wrong_mode:${path}"
}

ensure_directory() {
  local path="$1"
  if [[ ! -e "$path" && ! -L "$path" ]]; then
    mkdir -m 0700 -- "$path" || deny "registry_directory_create_failed:${path}"
  fi
  validate_directory "$path"
}

ensure_registry() {
  ensure_directory "$REGISTRY_ROOT"
  ensure_directory "$ACTIVE_DIR"
  ensure_directory "$TOMBSTONE_DIR"
  ensure_directory "$LOCK_DIR"
}

validate_regular_file() {
  local path="$1"
  [[ -f "$path" && ! -L "$path" ]] || deny "registry_file_invalid:${path}"
  [[ "$(stat_value '%u' "$path")" == "$EXPECTED_UID" ]] || deny "registry_file_wrong_owner:${path}"
  [[ "$(stat_value '%a' "$path")" == '600' ]] || deny "registry_file_wrong_mode:${path}"
  [[ "$(stat_value '%h' "$path")" == '1' ]] || deny "registry_file_unexpected_links:${path}"
}

declare -A ARGS=()
parse_args() {
  while (($#)); do
    case "$1" in
      --orphan)
        [[ -z "${ARGS[orphan]:-}" ]] || deny 'duplicate_argument:orphan'
        ARGS[orphan]='1'
        shift
        ;;
      --run-id|--capability|--source-identity|--owner-pid|--owner-start-ticks|--expires-at|--container-id|--network-id|--image-id)
        local key="${1#--}"
        key="${key//-/_}"
        (($# >= 2)) || deny "missing_argument_value:${key}"
        [[ -z "${ARGS[$key]:-}" ]] || deny "duplicate_argument:${key}"
        has_line_break "$2" && deny "argument_contains_line_break:${key}"
        [[ -n "$2" ]] || deny "empty_argument:${key}"
        ARGS["$key"]="$2"
        shift 2
        ;;
      *) deny "unknown_argument:$1" ;;
    esac
  done
}

require_arg() {
  [[ -n "${ARGS[$1]:-}" ]] || deny "required_argument_missing:$1"
}

reject_unexpected_args() {
  local allowed=" $* "
  local key
  for key in "${!ARGS[@]}"; do
    [[ "$allowed" == *" $key "* ]] || deny "argument_not_allowed:$key"
  done
}

owner_start_ticks() {
  local pid="$1"
  local stat_file="$PROC_ROOT/$pid/stat"
  local line rest
  [[ -f "$stat_file" && ! -L "$stat_file" ]] || return 1
  IFS= read -r line <"$stat_file" || return 1
  [[ -n "$line" && ${#line} -le 4096 ]] || return 1
  has_line_break "$line" && return 1
  [[ "$line" == "$pid ("* && "$line" == *') '* ]] || return 1
  rest="${line##*) }"
  local -a fields=()
  read -r -a fields <<<"$rest"
  ((${#fields[@]} >= 20)) || return 1
  valid_positive_decimal "${fields[19]}" '9223372036854775807' || return 1
  printf '%s\n' "${fields[19]}"
}

owner_uid() {
  local pid="$1"
  local status_file="$PROC_ROOT/$pid/status"
  local line uid='' seen=0
  [[ -f "$status_file" && ! -L "$status_file" ]] || return 1
  while IFS= read -r line || [[ -n "$line" ]]; do
    has_line_break "$line" && return 1
    if [[ "$line" == Uid:$'\t'* ]]; then
      ((seen += 1))
      ((seen == 1)) || return 1
      read -r _ uid _ <<<"$line"
    fi
  done <"$status_file"
  [[ "$uid" =~ ^[0-9]{1,10}$ ]] || return 1
  printf '%s\n' "$uid"
}

current_boot_id() {
  local boot_id_file="$PROC_ROOT/sys/kernel/random/boot_id" boot_id
  [[ -f "$boot_id_file" && ! -L "$boot_id_file" ]] || return 1
  IFS= read -r boot_id <"$boot_id_file" || return 1
  [[ "$boot_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || return 1
  printf '%s\n' "$boot_id"
}

owner_is_live() {
  local pid="$1" expected_start="$2" expected_owner_uid="$3" expected_boot_id="$4"
  local actual_start actual_uid actual_boot_id
  actual_start="$(owner_start_ticks "$pid" 2>/dev/null)" || return 1
  actual_uid="$(owner_uid "$pid" 2>/dev/null)" || return 1
  actual_boot_id="$(current_boot_id 2>/dev/null)" || return 1
  [[ "$actual_start" == "$expected_start" && "$actual_uid" == "$expected_owner_uid" \
     && "$actual_boot_id" == "$expected_boot_id" ]]
}

generate_capability() {
  local capability
  capability="$(od -An -N32 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n')" \
    || deny 'csprng_failed'
  valid_capability "$capability" || deny 'csprng_output_invalid'
  printf '%s\n' "$capability"
}

capability_hash() {
  printf '%s' "$1" | sha256sum | awk '{print $1}'
}

declare -A RECORD=()
readonly RECORD_KEYS='version state run_id capability_hash source_identity owner_pid owner_start_ticks owner_uid boot_id expires_at container_id network_id image_id created_at updated_at'

load_record() {
  local path="$1" line key value count=0
  RECORD=()
  validate_regular_file "$path"
  while IFS= read -r line || [[ -n "$line" ]]; do
    ((count += 1))
    ((count <= 15)) || deny "record_too_many_lines:${path}"
    [[ -n "$line" && ${#line} -le 160 ]] || deny "record_line_invalid:${path}:${count}"
    [[ "$line" != *$'\r'* && "$line" == *=* ]] || deny "record_line_malformed:${path}:${count}"
    key="${line%%=*}"
    value="${line#*=}"
    [[ " $RECORD_KEYS " == *" $key "* ]] || deny "record_unknown_key:${path}:${key}"
    [[ ! -v "RECORD[$key]" ]] || deny "record_duplicate_key:${path}:${key}"
    RECORD["$key"]="$value"
  done <"$path"
  ((count == 15)) || deny "record_line_count_invalid:${path}"
  local required
  for required in $RECORD_KEYS; do
    [[ -v "RECORD[$required]" ]] || deny "record_missing_key:${path}:${required}"
  done
  validate_record "$path"
}

validate_record() {
  local path="$1"
  [[ "${RECORD[version]}" == "$FORMAT_VERSION" ]] || deny "record_version_invalid:${path}"
  [[ "${RECORD[state]}" == 'prepared' || "${RECORD[state]}" == 'committed' || "${RECORD[state]}" == 'consumed' ]] \
    || deny "record_state_invalid:${path}"
  valid_run_id "${RECORD[run_id]}" || deny "record_run_id_invalid:${path}"
  valid_capability "${RECORD[capability_hash]}" || deny "record_capability_hash_invalid:${path}"
  valid_sha256_identity "${RECORD[source_identity]}" || deny "record_source_identity_invalid:${path}"
  valid_positive_decimal "${RECORD[owner_pid]}" "$MAX_PID" || deny "record_owner_pid_invalid:${path}"
  valid_positive_decimal "${RECORD[owner_start_ticks]}" '9223372036854775807' || deny "record_owner_start_invalid:${path}"
  [[ "${RECORD[owner_uid]}" == "$EXPECTED_UID" ]] || deny "record_owner_uid_invalid:${path}"
  [[ "${RECORD[boot_id]}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] \
    || deny "record_boot_id_invalid:${path}"
  valid_positive_decimal "${RECORD[expires_at]}" "$MAX_EPOCH" || deny "record_expiry_invalid:${path}"
  valid_positive_decimal "${RECORD[created_at]}" "$MAX_EPOCH" || deny "record_created_at_invalid:${path}"
  valid_positive_decimal "${RECORD[updated_at]}" "$MAX_EPOCH" || deny "record_updated_at_invalid:${path}"
  if [[ "${RECORD[state]}" == 'prepared' ]]; then
    [[ -z "${RECORD[container_id]}${RECORD[network_id]}${RECORD[image_id]}" ]] \
      || deny "record_prepared_has_resources:${path}"
  else
    valid_object_id "${RECORD[container_id]}" || deny "record_container_id_invalid:${path}"
    valid_object_id "${RECORD[network_id]}" || deny "record_network_id_invalid:${path}"
    valid_sha256_identity "${RECORD[image_id]}" || deny "record_image_id_invalid:${path}"
  fi
}

emit_record_body() {
  printf 'version=%s\n' "${RECORD[version]}"
  printf 'state=%s\n' "${RECORD[state]}"
  printf 'run_id=%s\n' "${RECORD[run_id]}"
  printf 'capability_hash=%s\n' "${RECORD[capability_hash]}"
  printf 'source_identity=%s\n' "${RECORD[source_identity]}"
  printf 'owner_pid=%s\n' "${RECORD[owner_pid]}"
  printf 'owner_start_ticks=%s\n' "${RECORD[owner_start_ticks]}"
  printf 'owner_uid=%s\n' "${RECORD[owner_uid]}"
  printf 'boot_id=%s\n' "${RECORD[boot_id]}"
  printf 'expires_at=%s\n' "${RECORD[expires_at]}"
  printf 'container_id=%s\n' "${RECORD[container_id]}"
  printf 'network_id=%s\n' "${RECORD[network_id]}"
  printf 'image_id=%s\n' "${RECORD[image_id]}"
  printf 'created_at=%s\n' "${RECORD[created_at]}"
  printf 'updated_at=%s\n' "${RECORD[updated_at]}"
}

atomic_write_record() {
  local destination="$1"
  local temp_file
  temp_file="$(mktemp "$ACTIVE_DIR/.lease.XXXXXX")" || deny 'atomic_temp_create_failed'
  trap 'rm -f -- "${temp_file:-}"' RETURN
  emit_record_body >"$temp_file" || deny 'atomic_temp_write_failed'
  chmod 0600 -- "$temp_file" || deny 'atomic_temp_chmod_failed'
  validate_regular_file "$temp_file"
  if [[ "$TEST_MODE" == '1' && "${PANDORA_LEASE_REGISTRY_TEST_FAIL_ATOMIC:-0}" == '1' ]]; then
    deny 'atomic_write_injected_failure'
  fi
  mv -T -- "$temp_file" "$destination" || deny 'atomic_rename_failed'
  trap - RETURN
  validate_regular_file "$destination"
  durable_registry_sync
}

acquire_lock() {
  local run_id="$1"
  LOCK_PATH="$LOCK_DIR/$run_id.lock"
  if [[ ! -e "$LOCK_PATH" && ! -L "$LOCK_PATH" ]]; then
    (set -o noclobber; : >"$LOCK_PATH") 2>/dev/null || true
    chmod 0600 -- "$LOCK_PATH" 2>/dev/null || true
  fi
  validate_regular_file "$LOCK_PATH"
  exec {LOCK_FD}>"$LOCK_PATH" || deny 'lock_open_failed'
  flock -x "$LOCK_FD" || deny 'lock_acquire_failed'
  validate_regular_file "$LOCK_PATH"
}

verify_capability_arg() {
  require_arg capability
  valid_capability "${ARGS[capability]}" || deny 'capability_format_invalid'
  local presented_hash
  presented_hash="$(capability_hash "${ARGS[capability]}")" || deny 'capability_hash_failed'
  [[ "$presented_hash" == "${RECORD[capability_hash]}" ]] || deny 'capability_mismatch'
}

print_record() {
  local live='false'
  owner_is_live "${RECORD[owner_pid]}" "${RECORD[owner_start_ticks]}" "${RECORD[owner_uid]}" "${RECORD[boot_id]}" && live='true'
  printf 'lease_record=version:%s,state:%s,run_id:%s,source_identity:%s,owner_pid:%s,owner_start_ticks:%s,owner_uid:%s,boot_id:%s,owner_live:%s,expires_at:%s,container_id:%s,network_id:%s,image_id:%s,created_at:%s,updated_at:%s\n' \
    "${RECORD[version]}" "${RECORD[state]}" "${RECORD[run_id]}" "${RECORD[source_identity]}" \
    "${RECORD[owner_pid]}" "${RECORD[owner_start_ticks]}" "${RECORD[owner_uid]}" "${RECORD[boot_id]}" "$live" \
    "${RECORD[expires_at]}" "${RECORD[container_id]}" "${RECORD[network_id]}" \
    "${RECORD[image_id]}" "${RECORD[created_at]}" "${RECORD[updated_at]}"
}

command_create() {
  reject_unexpected_args run_id source_identity owner_pid owner_start_ticks expires_at
  require_arg run_id; require_arg source_identity; require_arg owner_pid; require_arg owner_start_ticks; require_arg expires_at
  valid_run_id "${ARGS[run_id]}" || deny 'run_id_invalid'
  valid_sha256_identity "${ARGS[source_identity]}" || deny 'source_identity_invalid'
  valid_positive_decimal "${ARGS[owner_pid]}" "$MAX_PID" || deny 'owner_pid_invalid'
  valid_positive_decimal "${ARGS[owner_start_ticks]}" '9223372036854775807' || deny 'owner_start_ticks_invalid'
  valid_positive_decimal "${ARGS[expires_at]}" "$MAX_EPOCH" || deny 'expires_at_invalid_or_overflow'
  local now capability
  now="$(now_epoch)"
  (( ARGS[expires_at] > now )) || deny 'lease_must_start_in_future'
  (( ARGS[expires_at] <= now + MAX_LEASE_SECONDS )) || deny 'lease_window_too_long'
  [[ "$TEST_MODE" == '1' || "${ARGS[owner_pid]}" == "$PPID" ]] || deny 'owner_pid_must_be_caller_parent'
  local boot_id
  boot_id="$(current_boot_id)" || deny 'boot_id_unavailable'
  owner_is_live "${ARGS[owner_pid]}" "${ARGS[owner_start_ticks]}" "$EXPECTED_UID" "$boot_id" || deny 'owner_not_live_or_identity_mismatch'
  acquire_lock "${ARGS[run_id]}"
  local active="$ACTIVE_DIR/${ARGS[run_id]}.lease"
  local tombstone="$TOMBSTONE_DIR/${ARGS[run_id]}.lease"
  [[ ! -e "$active" && ! -L "$active" ]] || deny 'duplicate_active_lease'
  [[ ! -e "$tombstone" && ! -L "$tombstone" ]] || deny 'replayed_deleted_lease'
  capability="$(generate_capability)"
  RECORD=(
    [version]="$FORMAT_VERSION" [state]='prepared' [run_id]="${ARGS[run_id]}"
    [capability_hash]="$(capability_hash "$capability")" [source_identity]="${ARGS[source_identity]}"
    [owner_pid]="${ARGS[owner_pid]}" [owner_start_ticks]="${ARGS[owner_start_ticks]}" [owner_uid]="$EXPECTED_UID"
    [boot_id]="$boot_id"
    [expires_at]="${ARGS[expires_at]}" [container_id]='' [network_id]='' [image_id]=''
    [created_at]="$now" [updated_at]="$now"
  )
  atomic_write_record "$active"
  printf 'lease_created=run_id:%s,capability:%s\n' "${ARGS[run_id]}" "$capability"
}

command_commit() {
  reject_unexpected_args run_id capability container_id network_id image_id
  require_arg run_id; require_arg capability; require_arg container_id; require_arg network_id; require_arg image_id
  valid_run_id "${ARGS[run_id]}" || deny 'run_id_invalid'
  valid_object_id "${ARGS[container_id]}" || deny 'container_id_invalid'
  valid_object_id "${ARGS[network_id]}" || deny 'network_id_invalid'
  valid_sha256_identity "${ARGS[image_id]}" || deny 'image_id_invalid'
  acquire_lock "${ARGS[run_id]}"
  local active="$ACTIVE_DIR/${ARGS[run_id]}.lease"
  [[ -e "$active" || -L "$active" ]] || deny 'lease_not_found'
  load_record "$active"
  [[ "${RECORD[run_id]}" == "${ARGS[run_id]}" && "${RECORD[state]}" == 'prepared' ]] || deny 'lease_not_prepared'
  verify_capability_arg
  owner_is_live "${RECORD[owner_pid]}" "${RECORD[owner_start_ticks]}" "${RECORD[owner_uid]}" "${RECORD[boot_id]}" || deny 'owner_not_live_or_identity_mismatch'
  local now
  now="$(now_epoch)"
  (( RECORD[expires_at] > now )) || deny 'lease_expired_before_commit'
  RECORD[state]='committed'
  RECORD[container_id]="${ARGS[container_id]}"
  RECORD[network_id]="${ARGS[network_id]}"
  RECORD[image_id]="${ARGS[image_id]}"
  RECORD[updated_at]="$now"
  atomic_write_record "$active"
  printf 'lease_committed=run_id:%s\n' "${ARGS[run_id]}"
}

command_heartbeat() {
  reject_unexpected_args run_id capability expires_at
  require_arg run_id; require_arg capability; require_arg expires_at
  valid_run_id "${ARGS[run_id]}" || deny 'run_id_invalid'
  valid_positive_decimal "${ARGS[expires_at]}" "$MAX_EPOCH" || deny 'expires_at_invalid_or_overflow'
  acquire_lock "${ARGS[run_id]}"
  local active="$ACTIVE_DIR/${ARGS[run_id]}.lease"
  [[ -e "$active" || -L "$active" ]] || deny 'lease_not_found'
  load_record "$active"
  [[ "${RECORD[state]}" == 'prepared' || "${RECORD[state]}" == 'committed' ]] || deny 'lease_not_renewable'
  verify_capability_arg
  owner_is_live "${RECORD[owner_pid]}" "${RECORD[owner_start_ticks]}" "${RECORD[owner_uid]}" "${RECORD[boot_id]}" || deny 'owner_not_live_or_identity_mismatch'
  local now
  now="$(now_epoch)"
  (( RECORD[expires_at] > now )) || deny 'heartbeat_after_expiry_forbidden'
  (( ARGS[expires_at] > now )) || deny 'heartbeat_expiry_not_future'
  (( ARGS[expires_at] <= now + MAX_LEASE_SECONDS )) || deny 'heartbeat_window_too_long'
  (( ARGS[expires_at] > RECORD[expires_at] )) || deny 'heartbeat_must_extend_lease'
  RECORD[expires_at]="${ARGS[expires_at]}"
  RECORD[updated_at]="$now"
  atomic_write_record "$active"
  printf 'lease_heartbeat=run_id:%s,expires_at:%s\n' "${ARGS[run_id]}" "${ARGS[expires_at]}"
}

command_consume() {
  reject_unexpected_args run_id capability orphan
  require_arg run_id
  valid_run_id "${ARGS[run_id]}" || deny 'run_id_invalid'
  [[ -n "${ARGS[capability]:-}" || -n "${ARGS[orphan]:-}" ]] || deny 'consume_authentication_missing'
  [[ -z "${ARGS[capability]:-}" || -z "${ARGS[orphan]:-}" ]] || deny 'consume_authentication_ambiguous'
  acquire_lock "${ARGS[run_id]}"
  local active="$ACTIVE_DIR/${ARGS[run_id]}.lease"
  [[ -e "$active" || -L "$active" ]] || deny 'lease_not_found_or_replayed'
  load_record "$active"
  [[ "${RECORD[state]}" == 'committed' ]] || deny 'lease_not_committed_or_already_consumed'
  if [[ -n "${ARGS[capability]:-}" ]]; then
    verify_capability_arg
    owner_is_live "${RECORD[owner_pid]}" "${RECORD[owner_start_ticks]}" "${RECORD[owner_uid]}" "${RECORD[boot_id]}" || deny 'owner_not_live_or_identity_mismatch'
  else
    local now
    now="$(now_epoch)"
    (( RECORD[expires_at] <= now )) || deny 'orphan_lease_not_expired'
    if owner_is_live "${RECORD[owner_pid]}" "${RECORD[owner_start_ticks]}" "${RECORD[owner_uid]}" "${RECORD[boot_id]}"; then
      deny 'orphan_owner_still_live'
    fi
  fi
  RECORD[state]='consumed'
  RECORD[updated_at]="$(now_epoch)"
  atomic_write_record "$active"
  print_record
}

command_delete() {
  reject_unexpected_args run_id capability orphan
  require_arg run_id
  valid_run_id "${ARGS[run_id]}" || deny 'run_id_invalid'
  [[ -n "${ARGS[capability]:-}" || -n "${ARGS[orphan]:-}" ]] || deny 'delete_authentication_missing'
  [[ -z "${ARGS[capability]:-}" || -z "${ARGS[orphan]:-}" ]] || deny 'delete_authentication_ambiguous'
  acquire_lock "${ARGS[run_id]}"
  local active="$ACTIVE_DIR/${ARGS[run_id]}.lease"
  local tombstone="$TOMBSTONE_DIR/${ARGS[run_id]}.lease"
  [[ -e "$active" || -L "$active" ]] || deny 'lease_not_found_or_replayed'
  [[ ! -e "$tombstone" && ! -L "$tombstone" ]] || deny 'tombstone_already_exists'
  load_record "$active"
  [[ "${RECORD[state]}" == 'consumed' ]] || deny 'lease_not_consumed'
  if [[ -n "${ARGS[capability]:-}" ]]; then
    verify_capability_arg
  else
    local now
    now="$(now_epoch)"
    (( RECORD[expires_at] <= now )) || deny 'orphan_lease_not_expired'
    if owner_is_live "${RECORD[owner_pid]}" "${RECORD[owner_start_ticks]}" "${RECORD[owner_uid]}" "${RECORD[boot_id]}"; then
      deny 'orphan_owner_still_live'
    fi
  fi
  mv -T -- "$active" "$tombstone" || deny 'atomic_delete_to_tombstone_failed'
  validate_regular_file "$tombstone"
  durable_registry_sync
  printf 'lease_deleted=run_id:%s,tombstoned:true\n' "${ARGS[run_id]}"
}

command_inspect() {
  reject_unexpected_args run_id
  require_arg run_id
  valid_run_id "${ARGS[run_id]}" || deny 'run_id_invalid'
  acquire_lock "${ARGS[run_id]}"
  local active="$ACTIVE_DIR/${ARGS[run_id]}.lease"
  [[ -e "$active" || -L "$active" ]] || deny 'lease_not_found'
  load_record "$active"
  print_record
}

(($# >= 1)) || deny 'command_required'
COMMAND="$1"
shift
parse_args "$@"
ensure_registry
case "$COMMAND" in
  create) command_create ;;
  commit) command_commit ;;
  heartbeat) command_heartbeat ;;
  consume) command_consume ;;
  delete) command_delete ;;
  inspect) command_inspect ;;
  *) deny "unknown_command:${COMMAND}" ;;
esac
