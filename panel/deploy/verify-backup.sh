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
  # 缺省取安装根目录（本脚本在 <根>/deploy/ 下，已 cd 到这里）的 bin/：docker 布局 /opt/aegispanel、直装 /opt/pandora
  uploader="${AEGIS_BACKUP_WEBDAV_BIN:-$(dirname -- "$PWD")/bin/aegis-backup-webdav}"
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
pandora_pg_require pg_restore psql createdb dropdb

(cd "$(dirname "$archive")" && sha256sum --check --status "$(basename "$checksum")") \
  || die "SHA256 verification failed"
age --decrypt --identity "$AEGIS_BACKUP_AGE_IDENTITY" "$archive" \
  | pandora_pg pg_restore --list >/dev/null \
  || die "age decryption or pg_restore TOC verification failed"

if [ "${AEGIS_VERIFY_RESTORE:-0}" = "1" ]; then
  [ "$legacy_mode" -eq 0 ] \
    || die "unsigned legacy backups cannot run full restore rehearsal"
  : "${POSTGRES_DB:?POSTGRES_DB is required for restore verification}"
  pandora_pg_require_login
  verify_db="aegis_verify_$(date -u +%Y%m%d%H%M%S)_$$"
  [[ "$verify_db" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]] || die "unsafe temporary database name"
  cleanup_db() {
    pandora_pg dropdb --if-exists --force "$verify_db" >/dev/null 2>&1 || true
  }
  trap cleanup_db EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  pandora_pg createdb --template=template0 "$verify_db"
  age --decrypt --identity "$AEGIS_BACKUP_AGE_IDENTITY" "$archive" \
    | pandora_pg pg_restore -d "$verify_db" --no-owner --no-privileges --exit-on-error
  pandora_pg psql -X -d "$verify_db" -v ON_ERROR_STOP=1 -c 'SELECT 1' >/dev/null
  cleanup_db
  trap - EXIT INT TERM
fi

echo "backup verified: $archive"
