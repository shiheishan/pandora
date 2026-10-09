#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
cd "$(dirname "$0")"
die() { echo "restore-postgres: $*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
require_trusted_parent_chain() {
  local path="$1" parent mode
  [[ "$path" = /* && "$(readlink -m -- "$path")" = "$path" ]] \
    || die "trusted path must be absolute and normalized: $path"
  parent="$(dirname "$path")"
  while :; do
    [ -d "$parent" ] && [ ! -L "$parent" ] \
      || die "trusted path parent is not a real directory: $parent"
    [ "$(stat -c %u -- "$parent")" = 0 ] \
      || die "trusted path parent must be root-owned: $parent"
    mode=$((8#$(stat -c %a -- "$parent")))
    (( (mode & 0022) == 0 )) \
      || die "trusted path parent must not be group/world writable: $parent"
    [ "$parent" = / ] && break
    parent="$(dirname "$parent")"
  done
}
load_trusted_env() {
  local env_file="$1" env_fd mode path_id fd_id
  require_trusted_parent_chain "$env_file"
  [ -f "$env_file" ] && [ ! -L "$env_file" ] \
    || die "environment file must be a regular non-symlink: $env_file"
  [ "$(stat -c %u -- "$env_file")" = 0 ] && [ "$(stat -c %h -- "$env_file")" = 1 ] \
    || die "environment file must be root-owned with one link"
  mode=$((8#$(stat -c %a -- "$env_file")))
  (( (mode & 0177) == 0 )) || die "environment file mode must be 0400 or 0600"
  exec {env_fd}<"$env_file"
  path_id="$(stat -Lc %d:%i -- "$env_file")"
  fd_id="$(stat -Lc %d:%i -- "/proc/self/fd/$env_fd")"
  [ "$path_id" = "$fd_id" ] || die "environment file changed while opening"
  # shellcheck disable=SC1090
  . "/proc/self/fd/$env_fd"
  exec {env_fd}<&-
}
# 以超级用户跑 PostgreSQL 客户端（psql、pg_dump、pg_restore、createdb、dropdb）：本机客户端经
# 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户连（不用 runuser 切到 postgres：备份单元的系统调用过滤
# 不许切换用户）。口令只经环境变量 PGPASSWORD 给这一个客户端，不进命令行参数、不导出给整个脚本。
# 只读归档目录（pg_restore --list）不连库，不要求凭据；要连库的先过 pandora_pg_require_login。
# 备份、校验、恢复三件套各带一份（只信任自己，不 source 共用文件），db-scripts_mock_test.sh 核对逐字一致。
pandora_pg() {
  local tool="$1"; shift
  PGPASSWORD="${POSTGRES_SUPER_PASSWORD-}" PGHOST=127.0.0.1 PGPORT="${POSTGRES_PORT-}" PGUSER=postgres \
    PGSSLMODE=disable "$tool" "$@"
}
# 核对要用到的本机客户端
pandora_pg_require() {
  local tool
  for tool in "$@"; do require_command "$tool"; done
}
# 连库要的凭据：postgres 超级用户的口令与本机端口
pandora_pg_require_login() {
  : "${POSTGRES_SUPER_PASSWORD:?POSTGRES_SUPER_PASSWORD is required}"
  [[ "${POSTGRES_PORT:-}" =~ ^[0-9]+$ ]] || die "POSTGRES_PORT must be a port number"
}
require_command readlink
require_command stat
invocation_restore_confirm="${AEGIS_RESTORE_CONFIRM-}"
invocation_existing_confirm="${AEGIS_RESTORE_EXISTING_CONFIRM-}"
invocation_production_confirm="${AEGIS_RESTORE_PRODUCTION_CONFIRM-}"
load_trusted_env "$PWD/.env"
if [ -n "$invocation_restore_confirm" ]; then AEGIS_RESTORE_CONFIRM="$invocation_restore_confirm"; else unset AEGIS_RESTORE_CONFIRM; fi
if [ -n "$invocation_existing_confirm" ]; then AEGIS_RESTORE_EXISTING_CONFIRM="$invocation_existing_confirm"; else unset AEGIS_RESTORE_EXISTING_CONFIRM; fi
if [ -n "$invocation_production_confirm" ]; then AEGIS_RESTORE_PRODUCTION_CONFIRM="$invocation_production_confirm"; else unset AEGIS_RESTORE_PRODUCTION_CONFIRM; fi

archive=""
target_db=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --archive) [ "$#" -ge 2 ] || die "--archive needs a value"; archive="$2"; shift 2 ;;
    --target-db) [ "$#" -ge 2 ] || die "--target-db needs a value"; target_db="$2"; shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[ -n "$archive" ] || die "--archive is required"
[ -n "$target_db" ] || die "--target-db is required"
[[ "$archive" = /* ]] || die "--archive must be an absolute path"
[[ "$target_db" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]] || die "unsafe target database name"
[ "${AEGIS_RESTORE_CONFIRM:-}" = "RESTORE:${target_db}" ] \
  || die "set AEGIS_RESTORE_CONFIRM=RESTORE:${target_db} for this invocation"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${POSTGRES_USER:?POSTGRES_USER is required}"
[[ "$POSTGRES_USER" =~ ^[a-z_][a-z0-9_]*$ ]] || die "unsafe POSTGRES_USER"
: "${AEGIS_BACKUP_AGE_IDENTITY:?AEGIS_BACKUP_AGE_IDENTITY is required}"
require_command age
pandora_pg_require psql pg_restore createdb dropdb
pandora_pg_require_login

require_command flock
require_command install
maintenance_lock_dir=/run/aegispanel
maintenance_lock=/run/aegispanel/database-maintenance.lock
install -d -o root -g root -m 0700 -- "$maintenance_lock_dir"
exec 8>"$maintenance_lock"
flock -n 8 || die "database backup or restore is already running"

assert_production_quiesced() {
  local unit property value sessions
  [ "$target_db" = "$POSTGRES_DB" ] || return 0
  require_command systemctl
  for unit in aegis-public.service aegis-admin.service aegis-node.service aegis-backup.service; do
    for property in ActiveState SubState MainPID ControlPID; do
      value="$(systemctl show "$unit" --property "$property" --value 2>/dev/null)" \
        || die "cannot verify $unit $property"
      case "$property:$value" in
        ActiveState:inactive|SubState:dead|MainPID:0|ControlPID:0) ;;
        *) die "$unit is not fully inactive ($property)" ;;
      esac
    done
  done
  sessions="$(pandora_pg psql -X -d postgres -tAc \
      "SELECT count(*) FROM pg_stat_activity WHERE datname = '$target_db'" \
      | tr -d '[:space:]')"
  [[ "$sessions" =~ ^[0-9]+$ ]] && [ "$sessions" = 0 ] \
    || die "configured database still has active sessions"
}

masked_by_restore=()
production_guard_started=0
restore_committed=0
target_dropped=0
reinstate_production_guard() {
  local unit exists recovery_rc=0
  [ "$production_guard_started" -eq 1 ] || return 0

  # Fail closed after any interrupted or partial commit. Mask every service,
  # including units that were already unmasked during a failed commit attempt.
  for unit in aegis-public.service aegis-admin.service aegis-node.service aegis-backup.service; do
    systemctl mask --runtime -- "$unit" >/dev/null 2>&1 || recovery_rc=1
  done

  exists="$(pandora_pg psql -X -d postgres -tAc \
      "SELECT 1 FROM pg_database WHERE datname = '$target_db'" \
      | tr -d '[:space:]')" || recovery_rc=1
  if [ "$exists" = "1" ]; then
    pandora_pg psql -X -d postgres -v ON_ERROR_STOP=1 \
        -c "ALTER DATABASE \"$target_db\" CONNECTION LIMIT 0" >/dev/null 2>&1 \
      || recovery_rc=1
  fi
  return "$recovery_rc"
}
finish_restore_guard() {
  local rc=$? recovery_rc=0
  trap - EXIT INT TERM
  if [ "$production_guard_started" -eq 1 ] && [ "$restore_committed" -ne 1 ]; then
    reinstate_production_guard || recovery_rc=1
    echo "restore-postgres: FAIL-CLOSED; runtime service masks and database connection gate remain after restore failure" >&2
  fi
  if [ "$rc" -ne 0 ] && [ "$target_dropped" -eq 1 ]; then
    echo "restore-postgres: the previous database $target_db was already dropped before the failure; it is gone. Fix the cause and run the restore again from a backup (the archive is unchanged)" >&2
  fi
  if [ "$recovery_rc" -ne 0 ]; then
    echo "restore-postgres: FAIL-CLOSED recovery was incomplete; operator intervention is required" >&2
    [ "$rc" -ne 0 ] || rc=1
  fi
  exit "$rc"
}
trap finish_restore_guard EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

begin_production_guard() {
  local unit state superuser
  [ "$target_db" = "$POSTGRES_DB" ] || return 0
  assert_production_quiesced
  production_guard_started=1
  for unit in aegis-public.service aegis-admin.service aegis-node.service aegis-backup.service; do
    state="$(systemctl is-enabled "$unit" 2>/dev/null || true)"
    case "$state" in
      masked|masked-runtime) ;;
      *)
        systemctl mask --runtime -- "$unit" >/dev/null \
          || die "cannot runtime-mask $unit for restore"
        masked_by_restore+=("$unit")
        ;;
    esac
  done
  assert_production_quiesced
  superuser="$(pandora_pg psql -X -d postgres -tAc \
      'SELECT rolsuper FROM pg_roles WHERE rolname = current_user' \
      | tr -d '[:space:]')"
  [ "$superuser" = t ] \
    || die "configured database restore requires a PostgreSQL superuser for the connection gate"
}

commit_production_guard() {
  local unit
  if [ "$target_db" != "$POSTGRES_DB" ]; then
    restore_committed=1
    return 0
  fi

  # Keep the database connection gate closed while restoring the service unit
  # state. Pre-existing masks are not removed because they are absent here.
  for unit in "${masked_by_restore[@]}"; do
    if ! systemctl unmask --runtime -- "$unit" >/dev/null; then
      reinstate_production_guard || true
      die "cannot remove restore runtime mask from $unit; production guard reinstated"
    fi
  done
  if ! assert_production_quiesced; then
    reinstate_production_guard || true
    die "service state changed during restore commit; production guard reinstated"
  fi

  # Opening the connection gate is the final irreversible commit step. If a
  # signal lands before restore_committed is set, the EXIT trap closes it again.
  if ! pandora_pg psql -X -d postgres -v ON_ERROR_STOP=1 \
      -c "ALTER DATABASE \"$target_db\" CONNECTION LIMIT -1" >/dev/null; then
    reinstate_production_guard || true
    die "cannot open restored database connection gate; production guard reinstated"
  fi
  restore_committed=1
}

if [ "$target_db" = "$POSTGRES_DB" ] && \
   [ "${AEGIS_RESTORE_PRODUCTION_CONFIRM:-}" != "OVERWRITE_CONFIGURED_DATABASE:${target_db}" ]; then
  die "configured database restore needs AEGIS_RESTORE_PRODUCTION_CONFIRM=OVERWRITE_CONFIGURED_DATABASE:${target_db}"
fi

#------------------------------------------------------------------------------
# 保留属主与权限的恢复（不带 --no-owner / --no-privileges）：备份由同一种安装导出，对象属主是跑迁移的
# postgres，00038/00039 的 SECURITY DEFINER 函数属专用角色 aegis_idempotency_owner，授权照迁移原样。
# 下面几个函数由 db-scripts_mock_test.sh 抽出来、换上桩跑真调用。
#------------------------------------------------------------------------------
# 读 pg_restore --schema-only 的 SQL（标准输入），打印恢复要用到的角色（一行一个，去重）：
# 对象属主、默认权限的主人、被授权者（PUBLIC 不算）
archive_role_plan() {
  awk '
    function last(line,   n, f) { sub(/ WITH GRANT OPTION;$/, ";", line); n = split(line, f, " "); sub(/;$/, "", f[n]); return f[n] }
    / OWNER TO [^ ]+;$/ { print last($0) }
    /^ALTER DEFAULT PRIVILEGES FOR ROLE / { print $6; r = last($0); if (r != "PUBLIC") print r }
    /^GRANT .* TO [^ ]+( WITH GRANT OPTION)?;$/ { r = last($0); if (r != "PUBLIC") print r }
  ' | sort -u
}

# 本机集群里缺的角色先建好（恢复时 ALTER … OWNER TO、GRANT 才不失败）。备份里合法的角色只有三个：
# postgres（跑迁移、对象属主、默认权限的主人）、aegis_idempotency_owner（两个 SECURITY DEFINER 函数的属主）、
# aegis_app（运行角色，被授权者）。库的属主 POSTGRES_USER 不在归档里（不带 --create 的导出不含库本身）。
# 分两遍：check_restore_roles 只核、不动手（有一个认不出就停，什么都没建）；全部输入核完之后
# create_missing_restore_roles 才建缺的，一律 NOLOGIN、不带任何特权（运行角色的登录与口令随后由 bootstrap.sh 设）。
#   check_restore_roles <archive_role_plan 的输出>
check_restore_roles() {
  local plan="$1" role
  while read -r role; do
    [ -n "$role" ] || continue
    case "$role" in
      aegis_app|aegis_idempotency_owner|postgres) ;;
      *) die "archive references role $role: this backup was not exported by this kind of installation and cannot be restored with this script (restore it by hand into a new database, see MIGRATION-RUNBOOK.md section 3, then run ./bootstrap.sh); nothing was changed" ;;
    esac
  done <<<"$plan"
}
#   create_missing_restore_roles <archive_role_plan 的输出>（先过 check_restore_roles）
create_missing_restore_roles() {
  local plan="$1" role exists
  while read -r role; do
    [ -n "$role" ] || continue
    exists="$(pandora_pg psql -X -d postgres -tAc "SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = '$role'" | tr -d '[:space:]')" \
      || die "cannot check role $role"
    [ "$exists" = 1 ] && continue
    pandora_pg psql -X -d postgres -v ON_ERROR_STOP=1 \
      -c "CREATE ROLE \"$role\" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS" >/dev/null \
      || die "cannot create role $role for the restore"
    echo "restore-postgres: created role $role (NOLOGIN, no privileges)" >&2
  done <<<"$plan"
}

# 目标库的属主 POSTGRES_USER（安装器建的）要先在：createdb 在删掉旧库之后才跑，到那时再发现就晚了
require_db_owner_role() {
  local exists
  exists="$(pandora_pg psql -X -d postgres -tAc "SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = '$POSTGRES_USER'" | tr -d '[:space:]')" \
    || die "cannot check the database owner role $POSTGRES_USER"
  [ "$exists" = 1 ] || die "database owner role $POSTGRES_USER does not exist; run the installer first"
}

# 目标库：template0、UTF8，属主 POSTGRES_USER（与全新安装一致）；正式库先关连接闸门
create_target_db() {
  local args=(--template=template0 --encoding=UTF8)
  [ "$target_db" != "$POSTGRES_DB" ] || args+=(--connection-limit=0)
  args+=(--owner="$POSTGRES_USER")
  pandora_pg createdb "${args[@]}" "$target_db"
}

# 照原样恢复属主与权限（不带 --no-owner / --no-privileges）
restore_into_target() {
  age --decrypt --identity "$AEGIS_BACKUP_AGE_IDENTITY" "$archive" \
    | pandora_pg pg_restore -d "$target_db" --exit-on-error
}

# 先只核完整性（sha256、签名清单、归档目录），再读归档要的角色、缺的建好（只增不删，认不出的停下），
# 然后在临时库里用与正式恢复同样的参数（照原样还原属主与权限）完整恢复一遍：角色或权限上的问题在删正式库
# 之前就暴露。A valid checksum/TOC is not enough: corrupted data blocks or
# restore-time SQL can still fail.
"$PWD/verify-backup.sh" "$archive"
role_plan="$(age --decrypt --identity "$AEGIS_BACKUP_AGE_IDENTITY" "$archive" | pandora_pg pg_restore --schema-only -f - | archive_role_plan)" \
  || die "cannot read the roles this archive needs"
check_restore_roles "$role_plan"
require_db_owner_role
create_missing_restore_roles "$role_plan"
AEGIS_VERIFY_RESTORE=owners "$PWD/verify-backup.sh" "$archive"
begin_production_guard
exists="$(pandora_pg psql -X -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname = '$target_db'" | tr -d '[:space:]')"
if [ "$exists" = "1" ]; then
  [ "${AEGIS_RESTORE_EXISTING_CONFIRM:-}" = "OVERWRITE_EXISTING:${target_db}" ] \
    || die "existing target needs AEGIS_RESTORE_EXISTING_CONFIRM=OVERWRITE_EXISTING:${target_db}"
  assert_production_quiesced
  pandora_pg dropdb --force "$target_db"
  target_dropped=1
fi
create_target_db
assert_production_quiesced

restore_into_target

commit_production_guard

echo "restore complete: $archive -> database $target_db"
# 库级的东西不在归档里（不带 --create 的导出）：运行角色在这个库上的设置（search_path、jit、statement_timeout）
# 与收回 PUBLIC 的 TEMPORARY，新建的库上都没有；新建的运行角色还没有登录口令。都由 bootstrap.sh 补上
echo "restore-postgres: NOTE run ./bootstrap.sh before starting the services (runtime role login and its per-database settings)" >&2
