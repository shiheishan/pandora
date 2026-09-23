#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BACKUP="$ROOT/deploy/backup-postgres.sh"
VERIFY="$ROOT/deploy/verify-backup.sh"
RESTORE="$ROOT/deploy/restore-postgres.sh"
BUILD="$ROOT/deploy/build-release.sh"
TARGET="$ROOT/internal/domain/dbbackup/target.go"

fail() { printf 'webdav_backup_hardening_mock=FAIL reason=%s\n' "$1" >&2; exit 1; }
need() { grep -Fq -- "$2" "$1" || fail "$3"; }
reject() { if grep -Fq -- "$2" "$1"; then fail "$3"; fi; }
line_of() { grep -nF -- "$2" "$1" | head -n1 | cut -d: -f1; }
exact_line_of() { grep -nE "^[[:space:]]*$2[[:space:]]*$" "$1" | tail -n1 | cut -d: -f1; }
function_bounds() {
  local file="$1" name="$2" start end
  start="$(grep -nE "^${name}\\(\\)[[:space:]]*\\{" "$file" | head -n1 | cut -d: -f1)"
  [ -n "$start" ] || fail "${name}_function_missing"
  end="$(awk -v start="$start" 'NR > start && /^}$/ { print NR; exit }' "$file")"
  [ -n "$end" ] || fail "${name}_function_unterminated"
  printf '%s:%s\n' "$start" "$end"
}
function_need() {
  local file="$1" name="$2" pattern="$3" reason="$4" bounds start end
  bounds="$(function_bounds "$file" "$name")"; start="${bounds%:*}"; end="${bounds#*:}"
  sed -n "${start},${end}p" "$file" | grep -Fq -- "$pattern" || fail "$reason"
}
function_before() {
  local file="$1" name="$2" first_pattern="$3" second_pattern="$4" reason="$5"
  local bounds start end first second
  bounds="$(function_bounds "$file" "$name")"; start="${bounds%:*}"; end="${bounds#*:}"
  first="$(sed -n "${start},${end}p" "$file" | grep -nF -- "$first_pattern" | head -n1 | cut -d: -f1)"
  second="$(sed -n "${start},${end}p" "$file" | grep -nF -- "$second_pattern" | head -n1 | cut -d: -f1)"
  [ -n "$first" ] && [ -n "$second" ] && [ "$first" -lt "$second" ] || fail "$reason"
}
function_before_last() {
  local file="$1" name="$2" first_pattern="$3" second_pattern="$4" reason="$5"
  local bounds start end first second
  bounds="$(function_bounds "$file" "$name")"; start="${bounds%:*}"; end="${bounds#*:}"
  first="$(sed -n "${start},${end}p" "$file" | grep -nF -- "$first_pattern" | head -n1 | cut -d: -f1)"
  second="$(sed -n "${start},${end}p" "$file" | grep -nF -- "$second_pattern" | tail -n1 | cut -d: -f1)"
  [ -n "$first" ] && [ -n "$second" ] && [ "$first" -lt "$second" ] || fail "$reason"
}
before() {
  local first second
  first="$(line_of "$1" "$2")"; second="$(line_of "$1" "$3")"
  [ -n "$first" ] && [ -n "$second" ] && [ "$first" -lt "$second" ] || fail "$4"
}

for script in "$BACKUP" "$VERIFY" "$RESTORE"; do
  reject "$script" '. ./.env' untrusted_direct_env_source
  need "$script" 'require_trusted_parent_chain' trusted_parent_chain_missing
  need "$script" '. "/proc/self/fd/$env_fd"' fd_bound_env_source_missing
  need "$script" 'stat -c %u' root_owner_check_missing
  need "$script" 'stat -c %h' single_link_check_missing
done

need "$BACKUP" 'maintenance_lock=/run/aegispanel/database-maintenance.lock' backup_shared_lock_path_missing
need "$RESTORE" 'maintenance_lock=/run/aegispanel/database-maintenance.lock' restore_shared_lock_path_missing
backup_lock="$(grep -F 'maintenance_lock=/run/aegispanel/' "$BACKUP" | head -n1)"
restore_lock="$(grep -F 'maintenance_lock=/run/aegispanel/' "$RESTORE" | head -n1)"
[ "$backup_lock" = "$restore_lock" ] || fail maintenance_lock_mismatch
need "$BACKUP" 'flock -n 9' backup_exclusion_lock_missing
need "$BACKUP" 'exec 9>"$maintenance_lock"' backup_lock_variable_not_used
before "$BACKUP" 'flock -n 9' 'stamp="$(date -u' backup_lock_after_timestamp
need "$BACKUP" 'ln -- "$tmp_checksum" "$checksum"' checksum_no_clobber_missing
need "$BACKUP" 'ln -- "$tmp" "$archive"' archive_no_clobber_missing
need "$BACKUP" '"/proc/self/fd/$hook_fd"' fd_bound_hook_execution_missing
reject "$BACKUP" 'mv -- "$tmp" "$archive"' clobbering_archive_move_present

need "$RESTORE" 'aegis-backup.service' backup_service_quiescence_missing
need "$RESTORE" 'for property in ActiveState SubState MainPID ControlPID' service_state_check_missing
need "$RESTORE" 'systemctl mask --runtime' restore_runtime_mask_missing
need "$RESTORE" 'createdb -U "$POSTGRES_USER" --template=template0 "${create_args[@]}"' connection_gate_create_missing
need "$RESTORE" 'create_args+=(--connection-limit=0)' connection_gate_missing
before "$RESTORE" 'flock -n 8' 'AEGIS_VERIFY_RESTORE=1' restore_lock_too_late
guard_invocation="$(exact_line_of "$RESTORE" begin_production_guard)"
drop_invocation="$(line_of "$RESTORE" 'dropdb -U')"
[ -n "$guard_invocation" ] && [ -n "$drop_invocation" ] && [ "$guard_invocation" -lt "$drop_invocation" ] \
  || fail production_guard_after_drop
need "$RESTORE" 'pg_stat_activity WHERE datname' database_session_check_missing
need "$RESTORE" 'flock -n 8' restore_exclusion_lock_missing
need "$RESTORE" 'exec 8>"$maintenance_lock"' restore_lock_variable_not_used
need "$RESTORE" 'reinstate_production_guard()' fail_closed_recovery_missing
need "$RESTORE" 'commit_production_guard()' restore_commit_state_machine_missing
need "$RESTORE" 'reinstate_production_guard || recovery_rc=1' exit_trap_recovery_missing
reject "$RESTORE" 'restore_succeeded=' obsolete_restore_success_flag_present
function_need "$RESTORE" reinstate_production_guard \
  'for unit in aegis-public.service aegis-admin.service aegis-node.service aegis-backup.service; do' \
  failure_four_service_loop_missing
function_need "$RESTORE" reinstate_production_guard \
  'systemctl mask --runtime -- "$unit"' failure_service_remask_missing
function_need "$RESTORE" reinstate_production_guard \
  '-c "ALTER DATABASE \"$target_db\" CONNECTION LIMIT 0"' failure_connection_gate_missing
function_before "$RESTORE" reinstate_production_guard \
  'systemctl mask --runtime -- "$unit"' 'CONNECTION LIMIT 0' failure_guard_order
function_need "$RESTORE" commit_production_guard \
  'systemctl unmask --runtime -- "$unit"' commit_service_unmask_missing
function_need "$RESTORE" commit_production_guard \
  'assert_production_quiesced' commit_quiescence_recheck_missing
function_need "$RESTORE" commit_production_guard \
  'CONNECTION LIMIT -1' commit_connection_gate_open_missing
function_need "$RESTORE" commit_production_guard \
  'restore_committed=1' commit_flag_missing
function_before "$RESTORE" commit_production_guard \
  'systemctl unmask --runtime -- "$unit"' 'assert_production_quiesced' commit_unmask_after_recheck
function_before "$RESTORE" commit_production_guard \
  'assert_production_quiesced' 'CONNECTION LIMIT -1' commit_gate_before_recheck
function_before_last "$RESTORE" commit_production_guard \
  'CONNECTION LIMIT -1' 'restore_committed=1' connection_gate_opened_after_commit_flag
pg_restore_line="$(line_of "$RESTORE" 'pg_restore -U')"
commit_invocation="$(exact_line_of "$RESTORE" commit_production_guard)"
completion_line="$(line_of "$RESTORE" 'echo "restore complete:')"
[ -n "$pg_restore_line" ] && [ -n "$commit_invocation" ] && [ -n "$completion_line" ] \
  && [ "$pg_restore_line" -lt "$commit_invocation" ] && [ "$commit_invocation" -lt "$completion_line" ] \
  || fail restore_commit_invocation_order
need "$VERIFY" 'run_trusted_executable "$uploader" verify-manifest' verifier_fd_execution_missing

need "$BUILD" '--owner=0 --group=0 --numeric-owner' root_tar_identity_missing
need "$BUILD" '--mode=0644' data_file_mode_missing
need "$TARGET" '"64:ff9b:1::/48"' local_use_nat64_block_missing

printf 'webdav_backup_hardening_mock=PASS env=fd-bound hooks=fd-bound verifier=fd-bound maintenance_lock=shared ordering=PASS restore_commit=fail-closed restore_mask=present connection_gate=present tar_owner=root nat64=blocked runtime=NOT_RUN\n'
