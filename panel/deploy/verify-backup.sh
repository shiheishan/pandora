#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
cd "$(dirname "$0")"
die() { echo "verify-backup: $*" >&2; exit 1; }
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
run_trusted_executable() {
  local executable="$1" executable_fd path_id fd_id mode rc; shift
  require_trusted_parent_chain "$executable"
  [ -f "$executable" ] && [ ! -L "$executable" ] && [ -x "$executable" ] \
    || die "trusted executable is not a regular executable file"
  [ "$(stat -c %u -- "$executable")" = 0 ] && [ "$(stat -c %h -- "$executable")" = 1 ] \
    || die "trusted executable must be root-owned with one link"
  mode=$((8#$(stat -c %a -- "$executable")))
  (( (mode & 06022) == 0 )) || die "trusted executable has an unsafe mode"
  exec {executable_fd}<"$executable"
  path_id="$(stat -Lc %d:%i -- "$executable")"
  fd_id="$(stat -Lc %d:%i -- "/proc/self/fd/$executable_fd")"
  [ "$path_id" = "$fd_id" ] || die "trusted executable changed while opening"
  "/proc/self/fd/$executable_fd" "$@" || rc=$?
  exec {executable_fd}<&-
  return "${rc:-0}"
}
require_command readlink
require_command stat
invocation_allow_unsigned="${AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY-}"
invocation_verify_restore="${AEGIS_VERIFY_RESTORE-}"
load_trusted_env "$PWD/.env"
if [ -n "$invocation_allow_unsigned" ]; then
  AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY="$invocation_allow_unsigned"
else
  unset AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY
fi
AEGIS_VERIFY_RESTORE="${invocation_verify_restore:-0}"

[ "$#" -eq 1 ] || die "usage: $0 /absolute/path/aegis-postgres-*.dump.age"
archive="$1"
[[ "$archive" = /* ]] || die "backup path must be absolute"
[ -f "$archive" ] || die "backup not found: $archive"
checksum="${archive}.sha256"
[ -f "$checksum" ] || die "checksum not found: $checksum"
manifest="${archive%.dump.age}.manifest.json"
legacy_approval="RESTORE_UNSIGNED:$(basename "$archive")"
legacy_mode=0
if [ "${AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY:-}" = "$legacy_approval" ] && [ ! -e "$manifest" ]; then
  legacy_mode=1
  echo "verify-backup: WARNING unsigned legacy recovery explicitly enabled" >&2
else
  : "${AEGIS_BACKUP_MANIFEST_PUBLIC_KEY:?AEGIS_BACKUP_MANIFEST_PUBLIC_KEY is required}"
  : "${AEGIS_BACKUP_TRUSTED_CHECKPOINT:?AEGIS_BACKUP_TRUSTED_CHECKPOINT is required}"
  uploader="${AEGIS_BACKUP_WEBDAV_BIN:-/opt/aegispanel/bin/aegis-backup-webdav}"
  [[ "$uploader" = /* ]] || die "AEGIS_BACKUP_WEBDAV_BIN must be an absolute path"
  [ -f "$manifest" ] || die "signed manifest not found: $manifest"
  run_trusted_executable "$uploader" verify-manifest "$archive" "$checksum" "$manifest" \
    "$AEGIS_BACKUP_MANIFEST_PUBLIC_KEY" "$AEGIS_BACKUP_TRUSTED_CHECKPOINT" \
    || die "signed manifest or trusted checkpoint verification failed"
fi
: "${AEGIS_BACKUP_AGE_IDENTITY:?AEGIS_BACKUP_AGE_IDENTITY is required}"
[[ "$AEGIS_BACKUP_AGE_IDENTITY" = /* ]] || die "AEGIS_BACKUP_AGE_IDENTITY must be an absolute path"
[ -r "$AEGIS_BACKUP_AGE_IDENTITY" ] || die "age identity is not readable"
require_command age
require_command docker

(cd "$(dirname "$archive")" && sha256sum --check --status "$(basename "$checksum")") \
  || die "SHA256 verification failed"
age --decrypt --identity "$AEGIS_BACKUP_AGE_IDENTITY" "$archive" \
  | docker exec -i aegis-postgres pg_restore --list >/dev/null \
  || die "age decryption or pg_restore TOC verification failed"

if [ "${AEGIS_VERIFY_RESTORE:-0}" = "1" ]; then
  [ "$legacy_mode" -eq 0 ] \
    || die "unsigned legacy backups cannot run full restore rehearsal"
  : "${POSTGRES_USER:?POSTGRES_USER is required for restore verification}"
  : "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required for restore verification}"
  : "${POSTGRES_DB:?POSTGRES_DB is required for restore verification}"
  verify_db="aegis_verify_$(date -u +%Y%m%d%H%M%S)_$$"
  [[ "$verify_db" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]] || die "unsafe temporary database name"
  export PGPASSWORD="$POSTGRES_PASSWORD"
  cleanup_db() {
    docker exec -i -e PGPASSWORD aegis-postgres \
      dropdb -U "$POSTGRES_USER" --if-exists --force "$verify_db" >/dev/null 2>&1 || true
  }
  trap cleanup_db EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  docker exec -i -e PGPASSWORD aegis-postgres \
    createdb -U "$POSTGRES_USER" --template=template0 "$verify_db"
  age --decrypt --identity "$AEGIS_BACKUP_AGE_IDENTITY" "$archive" \
    | docker exec -i -e PGPASSWORD aegis-postgres \
        pg_restore -U "$POSTGRES_USER" -d "$verify_db" \
          --no-owner --no-privileges --exit-on-error
  docker exec -i -e PGPASSWORD aegis-postgres \
    psql -X -U "$POSTGRES_USER" -d "$verify_db" -v ON_ERROR_STOP=1 \
      -c 'SELECT 1' >/dev/null
  cleanup_db
  trap - EXIT INT TERM
fi

echo "backup verified: $archive"
