#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
cd "$(dirname "$0")"
die() { echo "backup: $*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }
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
load_trusted_env "$PWD/.env"

validate_hook() {
  local hook="$1" label="$2" mode
  [ -z "$hook" ] && return 0
  require_trusted_parent_chain "$hook"
  [ -f "$hook" ] && [ ! -L "$hook" ] && [ -x "$hook" ] \
    || die "$label is not a trusted executable file: $hook"
  [ "$(stat -c %u -- "$hook")" = 0 ] && [ "$(stat -c %h -- "$hook")" = 1 ] \
    || die "$label must be root-owned with one link"
  mode=$((8#$(stat -c %a -- "$hook")))
  (( (mode & 06022) == 0 )) || die "$label has an unsafe mode"
}
run_hook() {
  local hook="$1" hook_fd path_id fd_id rc; shift
  [ -z "$hook" ] && return 0
  validate_hook "$hook" hook
  exec {hook_fd}<"$hook"
  path_id="$(stat -Lc %d:%i -- "$hook")"
  fd_id="$(stat -Lc %d:%i -- "/proc/self/fd/$hook_fd")"
  [ "$path_id" = "$fd_id" ] || die "hook changed while opening"
  env -i PATH=/usr/bin:/bin "/proc/self/fd/$hook_fd" "$@" || rc=$?
  exec {hook_fd}<&-
  return "${rc:-0}"
}

: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${AEGIS_BACKUP_DIR:?AEGIS_BACKUP_DIR is required}"
: "${AEGIS_BACKUP_RETENTION_DAYS:?AEGIS_BACKUP_RETENTION_DAYS is required}"
: "${AEGIS_BACKUP_AGE_RECIPIENT:?AEGIS_BACKUP_AGE_RECIPIENT is required; plaintext backups are forbidden}"

[[ "$AEGIS_BACKUP_DIR" = /* ]] || die "AEGIS_BACKUP_DIR must be an absolute path"
[ "$AEGIS_BACKUP_DIR" != "/" ] || die "AEGIS_BACKUP_DIR cannot be /"
[[ "$AEGIS_BACKUP_RETENTION_DAYS" =~ ^[0-9]+$ ]] || die "AEGIS_BACKUP_RETENTION_DAYS must be a non-negative integer"
case "$AEGIS_BACKUP_AGE_RECIPIENT" in
  *CHANGE_ME*|*REPLACE*) die "replace the placeholder AEGIS_BACKUP_AGE_RECIPIENT" ;;
  age1*|age-plugin-*) ;;
  *) die "AEGIS_BACKUP_AGE_RECIPIENT does not look like an age recipient" ;;
esac

remote_hook="${AEGIS_BACKUP_REMOTE_HOOK:-}"
failure_hook="${AEGIS_BACKUP_FAILURE_HOOK:-}"
validate_hook "$remote_hook" AEGIS_BACKUP_REMOTE_HOOK
validate_hook "$failure_hook" AEGIS_BACKUP_FAILURE_HOOK
require_command age
pandora_pg_require pg_dump pg_restore
pandora_pg_require_login
require_command sha256sum
require_command find
require_command flock
require_command ln
require_command install

maintenance_lock_dir=/run/aegispanel
maintenance_lock=/run/aegispanel/database-maintenance.lock
install -d -m 0700 -- "$maintenance_lock_dir"
require_trusted_parent_chain "$maintenance_lock"
[ "$(stat -c %u -- "$maintenance_lock_dir")" = 0 ] \
  || die "database maintenance lock directory must be root-owned"
state_mode=$((8#$(stat -c %a -- "$maintenance_lock_dir")))
# 掩码是 0077 而不是 0177：目录必须保留属主的执行位，否则连自己都
# 进不去。0177 是从上面那条环境文件检查抄过来的 —— 文件不需要执行位，
# 目录需要，结果这条检查对任何能用的目录模式都为假，备份从 8 月 1 日
# 换脚本之后就再没成功过，而 systemd 只把它记成一个 failed 单元，
# 备份目录里的旧文件还在，看起来一切正常。
(( (state_mode & 0077) == 0 )) || die "database maintenance lock directory mode must be 0700"
exec 9>"$maintenance_lock"
flock -n 9 || die "database backup or restore is already running"

workdir=""
tmp=""
tmp_checksum=""
fifo=""
validator_status=""
on_exit() {
  local rc=$?
  trap - EXIT ERR INT TERM
  [ -n "$tmp" ] && [ -f "$tmp" ] && rm -f -- "$tmp"
  [ -n "$tmp_checksum" ] && [ -f "$tmp_checksum" ] && rm -f -- "$tmp_checksum"
  [ -n "$fifo" ] && [ -p "$fifo" ] && rm -f -- "$fifo"
  [ -n "$validator_status" ] && [ -f "$validator_status" ] && rm -f -- "$validator_status"
  [ -n "$workdir" ] && [ -d "$workdir" ] && rmdir -- "$workdir" 2>/dev/null || true
  if [ "$rc" -ne 0 ] && [ -n "$failure_hook" ]; then
    run_hook "$failure_hook" backup_failed "$rc" || true
  fi
  exit "$rc"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

install -d -m 0700 -- "$AEGIS_BACKUP_DIR"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
archive="$AEGIS_BACKUP_DIR/aegis-postgres-${stamp}.dump.age"
checksum="${archive}.sha256"
[ ! -e "$archive" ] && [ ! -e "$checksum" ] || die "backup artifact already exists: $archive"
workdir="$(mktemp -d "$AEGIS_BACKUP_DIR/.aegis-postgres-${stamp}.XXXXXX")"
tmp="$workdir/archive.tmp"
tmp_checksum="$workdir/archive.sha256.tmp"
fifo="$workdir/plain.dump.fifo"
validator_status="$workdir/validator.status"
mkfifo -m 0600 "$fifo"

# pg_restore --list may stop reading after the archive TOC. Keep the same FIFO
# descriptor open and drain any remaining dump bytes so tee never receives
# SIGPIPE. Persist the validator exit code for the parent after the drain.
(
  set +e
  exec 3<"$fifo"
  pandora_pg pg_restore --list <&3 >/dev/null
  validator_rc=$?
  cat <&3 >/dev/null
  printf '%s\n' "$validator_rc" >"$validator_status"
  exec 3<&-
) &
validator_pid=$!
# 带属主与权限导出：恢复时照原样还原（restore-postgres.sh 只把跑迁移的超级用户换成本机的）
pandora_pg pg_dump -d "$POSTGRES_DB" \
    --format=custom --compress=6 \
  | tee "$fifo" \
  | age --encrypt --recipient "$AEGIS_BACKUP_AGE_RECIPIENT" >"$tmp"
wait "$validator_pid" || die "backup validator process failed"
[ -f "$validator_status" ] || die "backup validator did not report a result"
[ "$(cat "$validator_status")" = "0" ] || die "pg_restore rejected the streamed custom-format dump"
rm -f -- "$validator_status"
validator_status=""
rm -f -- "$fifo"
fifo=""

[ -s "$tmp" ] || die "encrypted backup is empty"

hash="$(sha256sum "$tmp" | awk '{print $1}')"
printf '%s  %s\n' "$hash" "$(basename "$archive")" >"$tmp_checksum"
# Publish the checksum first. This may leave a harmless orphan checksum after
# a crash, but never exposes an archive without the checksum required by every
# verifier. Cross-file atomic publication is not available on a normal FS.
ln -- "$tmp_checksum" "$checksum" || die "checksum destination already exists"
rm -f -- "$tmp_checksum"
tmp_checksum=""
ln -- "$tmp" "$archive" || die "backup destination already exists"
rm -f -- "$tmp"
tmp=""
rmdir -- "$workdir"
workdir=""

if [ -n "$remote_hook" ]; then
  run_hook "$remote_hook" "$archive" "$checksum"
fi

# Retention only touches artifacts created by this script in the validated
# dedicated directory. A zero-day setting keeps today's files (find -mtime +0).
while IFS= read -r -d '' old; do
  rm -f -- "$old" "${old}.sha256"
done < <(find "$AEGIS_BACKUP_DIR" -maxdepth 1 -type f \
  -name 'aegis-postgres-*.dump.age' -mtime "+${AEGIS_BACKUP_RETENTION_DAYS}" -print0)

trap - EXIT INT TERM
echo "backup complete: $archive"
