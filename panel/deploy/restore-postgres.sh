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
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${AEGIS_BACKUP_AGE_IDENTITY:?AEGIS_BACKUP_AGE_IDENTITY is required}"
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
  sessions="$(docker exec -i -e PGPASSWORD aegis-postgres \
    psql -X -U "$POSTGRES_USER" -d postgres -tAc \
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

  exists="$(docker exec -i -e PGPASSWORD aegis-postgres \
    psql -X -U "$POSTGRES_USER" -d postgres -tAc \
      "SELECT 1 FROM pg_database WHERE datname = '$target_db'" \
      | tr -d '[:space:]')" || recovery_rc=1
  if [ "$exists" = "1" ]; then
    docker exec -i -e PGPASSWORD aegis-postgres \
      psql -X -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
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
  superuser="$(docker exec -i -e PGPASSWORD aegis-postgres \
    psql -X -U "$POSTGRES_USER" -d postgres -tAc \
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
  if ! docker exec -i -e PGPASSWORD aegis-postgres \
    psql -X -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
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

# A valid checksum/TOC is not enough: corrupted data blocks or restore-time SQL
# can still fail. Complete an isolated temporary-database restore before any
# destructive action against the requested target database.
AEGIS_VERIFY_RESTORE=1 "$PWD/verify-backup.sh" "$archive"
export PGPASSWORD="$POSTGRES_PASSWORD"
begin_production_guard
exists="$(docker exec -i -e PGPASSWORD aegis-postgres \
  psql -X -U "$POSTGRES_USER" -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname = '$target_db'" | tr -d '[:space:]')"
if [ "$exists" = "1" ]; then
  [ "${AEGIS_RESTORE_EXISTING_CONFIRM:-}" = "OVERWRITE_EXISTING:${target_db}" ] \
    || die "existing target needs AEGIS_RESTORE_EXISTING_CONFIRM=OVERWRITE_EXISTING:${target_db}"
  assert_production_quiesced
  docker exec -i -e PGPASSWORD aegis-postgres \
    dropdb -U "$POSTGRES_USER" --force "$target_db"
fi
create_args=()
if [ "$target_db" = "$POSTGRES_DB" ]; then
  create_args+=(--connection-limit=0)
fi
docker exec -i -e PGPASSWORD aegis-postgres \
  createdb -U "$POSTGRES_USER" --template=template0 "${create_args[@]}" "$target_db"
assert_production_quiesced

age --decrypt --identity "$AEGIS_BACKUP_AGE_IDENTITY" "$archive" \
  | docker exec -i -e PGPASSWORD aegis-postgres \
      pg_restore -U "$POSTGRES_USER" -d "$target_db" \
        --no-owner --no-privileges --exit-on-error

commit_production_guard

echo "restore complete: $archive -> database $target_db"
