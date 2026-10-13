#!/usr/bin/env bash
# 备份三件套（backup-postgres.sh、verify-backup.sh、restore-postgres.sh）加固点的静态检查：变量文件与可执行文件
# 按属主与 /proc/self/fd 绑定、维护锁、不覆盖发布、恢复的生产保护状态机；WebDAV 目标拒绝内网地址。
# 真跑的往返在 backup-provenance_mock_test.sh（要 Linux root），三件套共用代码逐字一致在 backup-scripts_mock_test.sh。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BACKUP="$ROOT/deploy/backup-postgres.sh"
VERIFY="$ROOT/deploy/verify-backup.sh"
RESTORE="$ROOT/deploy/restore-postgres.sh"
TARGET="$ROOT/internal/pkg/dbbackup/target.go"

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
  # 变量文件只按 KEY=VALUE 逐行读（不 source），读的是打开后核过身份的那个 fd
  need "$script" 'done <&"$env_fd"' fd_bound_env_parse_missing
  reject "$script" '. "/proc/self/fd/$env_fd"' env_file_sourced
  need "$script" '[[ "$key" == POSTGRES_* || "$key" == PANDORA_* ]] || continue' env_key_whitelist_missing
  need "$script" 'stat -c %u' root_owner_check_missing
  need "$script" 'stat -c %h' single_link_check_missing
done

# 锁放在只归备份单元的运行目录（RuntimeDirectory=pandora-maintenance），不与面板服务的运行目录共用：
# 恢复先停服务，systemd 会删停掉的服务的运行目录
need "$BACKUP" 'maintenance_lock=/run/pandora-maintenance/database-maintenance.lock' backup_shared_lock_path_missing
need "$RESTORE" 'maintenance_lock=/run/pandora-maintenance/database-maintenance.lock' restore_shared_lock_path_missing
backup_lock="$(grep -F 'maintenance_lock=/run/pandora-maintenance/' "$BACKUP" | head -n1)"
restore_lock="$(grep -F 'maintenance_lock=/run/pandora-maintenance/' "$RESTORE" | head -n1)"
[ "$backup_lock" = "$restore_lock" ] || fail maintenance_lock_mismatch
need "$BACKUP" 'flock -n 9' backup_exclusion_lock_missing
need "$BACKUP" 'exec 9>"$maintenance_lock"' backup_lock_variable_not_used
before "$BACKUP" 'flock -n 9' 'stamp="$(date -u' backup_lock_after_timestamp
need "$BACKUP" 'ln -- "$tmp_checksum" "$checksum"' checksum_no_clobber_missing
need "$BACKUP" 'ln -- "$tmp" "$archive"' archive_no_clobber_missing
need "$BACKUP" '"/proc/self/fd/$hook_fd"' fd_bound_hook_execution_missing
reject "$BACKUP" 'mv -- "$tmp" "$archive"' clobbering_archive_move_present

need "$RESTORE" 'quiesce_units=(pandora-serve.service pandora-worker.service pandora-backup.service pandora-health.service)' service_quiescence_list_missing
need "$RESTORE" 'for property in ActiveState SubState MainPID ControlPID' service_state_check_missing
need "$RESTORE" 'systemctl mask --runtime' restore_runtime_mask_missing
need "$RESTORE" 'pandora_pg createdb "${args[@]}" "$target_db"' connection_gate_create_missing
need "$RESTORE" 'args+=(--connection-limit=0)' connection_gate_missing
before "$RESTORE" 'flock -n 8' '"$PWD/verify-backup.sh" "$archive"' restore_lock_too_late
guard_invocation="$(exact_line_of "$RESTORE" begin_production_guard)"
drop_invocation="$(line_of "$RESTORE" 'pandora_pg dropdb --force')"
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
  'for unit in "${quiesce_units[@]}"; do' \
  failure_service_loop_missing
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
pg_restore_line="$(exact_line_of "$RESTORE" restore_into_target)"
commit_invocation="$(exact_line_of "$RESTORE" commit_production_guard)"
completion_line="$(line_of "$RESTORE" 'echo "restore complete:')"
[ -n "$pg_restore_line" ] && [ -n "$commit_invocation" ] && [ -n "$completion_line" ] \
  && [ "$pg_restore_line" -lt "$commit_invocation" ] && [ "$commit_invocation" -lt "$completion_line" ] \
  || fail restore_commit_invocation_order
need "$VERIFY" 'run_trusted_executable "$pandora_bin" backup verify-manifest' verifier_fd_execution_missing
need "$VERIFY" 'run_trusted_executable "$pandora_bin" backup verify-local-seal' seal_verifier_fd_execution_missing
need "$BACKUP" 'run_trusted_executable "$pandora_bin" backup webdav-upload' uploader_fd_execution_missing

need "$TARGET" '"64:ff9b:1::/48"' local_use_nat64_block_missing

printf 'webdav_backup_hardening_mock=PASS env=fd-bound-parsed hooks=fd-bound verifier=fd-bound maintenance_lock=shared ordering=PASS restore_commit=fail-closed restore_mask=present connection_gate=present nat64=blocked runtime=NOT_RUN\n'
