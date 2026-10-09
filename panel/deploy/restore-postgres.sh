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
# 数据库布局：docker（install.sh，容器 aegis-postgres）或 native（install-native.sh，系统 PostgreSQL）。
# 以 .env 的 PANDORA_DB_LAYOUT 为准；老的直装 .env 没有这一键，凭只有直装才写的 POSTGRES_SUPER_PASSWORD
# 认出来。各运维脚本各带一份同样的函数（不 source 共用文件，免得多一个要校验的信任面），
# pg-layout_mock_test.sh 核对逐字一致。
pandora_db_layout() {
  case "${PANDORA_DB_LAYOUT:-}" in
    native|docker) printf '%s\n' "$PANDORA_DB_LAYOUT" ;;
    '') if [ -n "${POSTGRES_SUPER_PASSWORD:-}" ]; then printf 'native\n'; else printf 'docker\n'; fi ;;
    *) return 1 ;;
  esac
}
# 以超级用户跑 PostgreSQL 客户端（psql、pg_dump、pg_restore、createdb、dropdb）：
#   docker：容器 aegis-postgres 里的客户端，以 POSTGRES_USER（容器里的超级用户）连；
#   native：本机客户端经 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户连（不用 runuser 切到 postgres：
#     备份单元的系统调用过滤不许切换用户）。
# 口令只经环境变量 PGPASSWORD 给客户端（docker 用 -e PGPASSWORD 按名字透传），不进命令行参数。
# 只读归档目录（pg_restore --list）不连库，不要求凭据；要连库的先过 pandora_pg_require_login。
pandora_pg() {
  local tool="$1"; shift
  case "$DB_LAYOUT" in
    docker) PGPASSWORD="${POSTGRES_PASSWORD-}" docker exec -i -e PGPASSWORD aegis-postgres \
              "$tool" -U "${POSTGRES_USER:-postgres}" "$@" ;;
    native) PGPASSWORD="${POSTGRES_SUPER_PASSWORD-}" PGHOST=127.0.0.1 PGPORT="${POSTGRES_PORT-}" PGUSER=postgres \
              PGSSLMODE=disable "$tool" "$@" ;;
    *) return 1 ;;
  esac
}
# 认出布局（写 DB_LAYOUT）并核对它要的命令。参数是直装布局要用到的本机客户端
pandora_pg_require() {
  local tool
  DB_LAYOUT="$(pandora_db_layout)" || die "PANDORA_DB_LAYOUT must be native or docker"
  case "$DB_LAYOUT" in
    docker) require_command docker ;;
    native) for tool in "$@"; do require_command "$tool"; done ;;
  esac
}
# 连库要的凭据：docker 布局是 POSTGRES_USER / POSTGRES_PASSWORD，直装是 postgres 的 POSTGRES_SUPER_PASSWORD
pandora_pg_require_login() {
  case "$DB_LAYOUT" in
    docker)
      : "${POSTGRES_USER:?POSTGRES_USER is required}"
      : "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
      ;;
    native)
      : "${POSTGRES_SUPER_PASSWORD:?POSTGRES_SUPER_PASSWORD is required for the native database layout}"
      [[ "${POSTGRES_PORT:-}" =~ ^[0-9]+$ ]] || die "POSTGRES_PORT must be a port number for the native database layout"
      ;;
  esac
}
require_command readlink
require_command stat
invocation_restore_confirm="${AEGIS_RESTORE_CONFIRM-}"
invocation_existing_confirm="${AEGIS_RESTORE_EXISTING_CONFIRM-}"
invocation_production_confirm="${AEGIS_RESTORE_PRODUCTION_CONFIRM-}"
invocation_allow_unsigned="${AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY-}"
load_trusted_env "$PWD/.env"
if [ -n "$invocation_restore_confirm" ]; then AEGIS_RESTORE_CONFIRM="$invocation_restore_confirm"; else unset AEGIS_RESTORE_CONFIRM; fi
if [ -n "$invocation_existing_confirm" ]; then AEGIS_RESTORE_EXISTING_CONFIRM="$invocation_existing_confirm"; else unset AEGIS_RESTORE_EXISTING_CONFIRM; fi
if [ -n "$invocation_production_confirm" ]; then AEGIS_RESTORE_PRODUCTION_CONFIRM="$invocation_production_confirm"; else unset AEGIS_RESTORE_PRODUCTION_CONFIRM; fi
if [ -n "$invocation_allow_unsigned" ]; then AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY="$invocation_allow_unsigned"; else unset AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY; fi

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
: "${AEGIS_BACKUP_AGE_IDENTITY:?AEGIS_BACKUP_AGE_IDENTITY is required}"
require_command age
pandora_pg_require psql pg_restore createdb dropdb
pandora_pg_require_login
[ -z "${AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY:-}" ] \
  || die "unsigned legacy backups cannot be used by restore-postgres.sh"

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
# 保留属主与权限的恢复。以前恢复用 --no-owner --no-privileges：全部对象归恢复者，00038 那些归专用
# 角色 aegis_idempotency_owner 的 SECURITY DEFINER 函数变成以超级用户身份执行，迁移给的授权也丢了。
# 现在照原样恢复属主与权限；只把「跑迁移的那个超级用户」名下的对象换成本机跑迁移的超级用户
# （docker 布局是 POSTGRES_USER，直装是 postgres），与 install-native.sh --from-docker 同一个做法。
# 下面几个函数由 pg-layout_mock_test.sh 抽出来、换上桩跑真调用。
#------------------------------------------------------------------------------
# 读 pg_restore --schema-only 的 SQL（标准输入），打印恢复要用到的角色：
#   migrator <角色>   备份里跑迁移的那个（app 模式的属主）
#   role <角色>       对象属主、默认权限的主人、被授权者（PUBLIC 不算）
#   acl yes|no        备份里有没有权限（2026-10 之前的备份导出时带 --no-acl，没有）
archive_role_plan() {
  awk '
    function last(line,   n, f) { sub(/ WITH GRANT OPTION;$/, ";", line); n = split(line, f, " "); sub(/;$/, "", f[n]); return f[n] }
    /^ALTER SCHEMA app OWNER TO / { print "migrator " last($0) }
    / OWNER TO [^ ]+;$/ { print "role " last($0) }
    /^ALTER DEFAULT PRIVILEGES FOR ROLE / { print "role " $6; r = last($0); if (r != "PUBLIC") print "role " r }
    /^(GRANT|REVOKE) / { acl = 1 }
    /^GRANT .* TO [^ ]+( WITH GRANT OPTION)?;$/ { r = last($0); if (r != "PUBLIC") print "role " r }
    END { print "acl " (acl ? "yes" : "no") }
  ' | sort -u
}

# 本机集群里缺的角色先建好（恢复时 ALTER … OWNER TO、GRANT 才不失败）。只认识面板自己的几个角色，
# 一律 NOLOGIN、不带任何特权（运行角色的登录与口令随后由 bootstrap.sh 设）；备份里有别的角色就停下，
# 不替人决定。打印临时建的「备份里跑迁移的角色」名字（恢复完要删掉），没有就空
#   ensure_restore_roles <archive_role_plan 的输出>
ensure_restore_roles() {
  local plan="$1" migrator role exists created_migrator=""
  migrator="$(awk '$1 == "migrator" { print $2; exit }' <<<"$plan")"
  [[ -z "$migrator" || "$migrator" =~ ^[a-z_][a-z0-9_]*$ ]] || die "unsupported migrator role in archive: $migrator"
  while read -r role; do
    [ -n "$role" ] || continue
    case "$role" in
      aegis_app|aegis_idempotency_owner|postgres|"$POSTGRES_USER"|"$migrator") ;;
      *) die "archive references role $role that this restore does not know how to create; create it by hand first" ;;
    esac
    [[ "$role" =~ ^[a-z_][a-z0-9_]*$ ]] || die "unsupported role name in archive: $role"
    exists="$(pandora_pg psql -X -d postgres -tAc "SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = '$role'" | tr -d '[:space:]')" \
      || die "cannot check role $role"
    [ "$exists" = 1 ] && continue
    pandora_pg psql -X -d postgres -v ON_ERROR_STOP=1 \
      -c "CREATE ROLE \"$role\" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS" >/dev/null \
      || die "cannot create role $role for the restore"
    echo "restore-postgres: created role $role (NOLOGIN, no privileges)" >&2
    [ "$role" != "$migrator" ] || created_migrator="$role"
  done < <(awk '$1 == "role" || $1 == "migrator" { print $2 }' <<<"$plan" | sort -u)
  printf '%s\n' "$created_migrator"
}

# 目标库：template0、UTF8；直装以 postgres 连库，库的属主给 POSTGRES_USER（aegis），与全新直装一致
create_target_db() {
  local args=(--template=template0 --encoding=UTF8)
  [ "$target_db" != "$POSTGRES_DB" ] || args+=(--connection-limit=0)
  [ "$DB_LAYOUT" != native ] || args+=(--owner="${POSTGRES_USER:-aegis}")
  pandora_pg createdb "${args[@]}" "$target_db"
}

# 照原样恢复属主与权限（不带 --no-owner / --no-privileges）
restore_into_target() {
  age --decrypt --identity "$AEGIS_BACKUP_AGE_IDENTITY" "$archive" \
    | pandora_pg pg_restore -d "$target_db" --exit-on-error
}

# 备份里跑迁移的角色换成本机跑迁移的超级用户；为恢复临时建的那个角色删掉
#   reassign_source_migrator <备份里的迁移角色> <临时建的，或空>
reassign_source_migrator() {
  local source="$1" created="$2" local_migrator
  [ -n "$source" ] || return 0
  local_migrator="$(pandora_pg psql -X -d postgres -tAc 'SELECT current_user' | tr -d '[:space:]')" \
    || die "cannot read the local migrator role"
  [ "$source" != "$local_migrator" ] || return 0
  pandora_pg psql -X -d "$target_db" -v ON_ERROR_STOP=1 \
    -c "REASSIGN OWNED BY \"$source\" TO \"$local_migrator\"" \
    -c "ALTER DATABASE \"$target_db\" OWNER TO \"${POSTGRES_USER:-$local_migrator}\"" >/dev/null \
    || die "cannot hand objects of $source to $local_migrator"
  echo "restore-postgres: objects owned by the archive's migrator $source now belong to $local_migrator" >&2
  if [ -n "$created" ]; then
    pandora_pg psql -X -d "$target_db" -v ON_ERROR_STOP=1 -c "DROP OWNED BY \"$created\"" >/dev/null \
      && pandora_pg psql -X -d postgres -v ON_ERROR_STOP=1 -c "DROP ROLE \"$created\"" >/dev/null \
      || echo "restore-postgres: WARNING could not drop the temporary role $created" >&2
  fi
}

# A valid checksum/TOC is not enough: corrupted data blocks or restore-time SQL
# can still fail. Complete an isolated temporary-database restore before any
# destructive action against the requested target database.
AEGIS_VERIFY_RESTORE=1 "$PWD/verify-backup.sh" "$archive"
# 动正式库之前：读出备份要的角色，缺的建好（只增不删），认不出的停下
role_plan="$(age --decrypt --identity "$AEGIS_BACKUP_AGE_IDENTITY" "$archive" | pandora_pg pg_restore --schema-only -f - | archive_role_plan)" \
  || die "cannot read the roles this archive needs"
source_migrator="$(awk '$1 == "migrator" { print $2; exit }' <<<"$role_plan")"
created_migrator="$(ensure_restore_roles "$role_plan")"
begin_production_guard
exists="$(pandora_pg psql -X -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname = '$target_db'" | tr -d '[:space:]')"
if [ "$exists" = "1" ]; then
  [ "${AEGIS_RESTORE_EXISTING_CONFIRM:-}" = "OVERWRITE_EXISTING:${target_db}" ] \
    || die "existing target needs AEGIS_RESTORE_EXISTING_CONFIRM=OVERWRITE_EXISTING:${target_db}"
  assert_production_quiesced
  pandora_pg dropdb --force "$target_db"
fi
create_target_db
assert_production_quiesced

restore_into_target
reassign_source_migrator "$source_migrator" "$created_migrator"

commit_production_guard

echo "restore complete: $archive -> database $target_db"
if grep -qx 'acl no' <<<"$role_plan"; then
  echo "restore-postgres: NOTE this archive predates owner/privilege-preserving backups (no GRANTs inside)." >&2
  echo "restore-postgres: run ./bootstrap.sh for the runtime role, then re-apply the GRANTs migrations give to aegis_idempotency_owner (deploy/MIGRATION-RUNBOOK.md section 3)" >&2
fi
