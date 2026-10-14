#!/usr/bin/env bash
# 面板数据库的加密备份（pandora-backup.timer 每天一次，以 root 跑）：
#   pg_dump（custom 格式，带属主与权限）→ 流式让 pg_restore --list 核一遍目录 → age 加密 → 先发布校验文件再发布归档
#   → pandora backup seal-local 写本地封条 → 配了 /etc/pandora/backup-webdav.json 就 pandora backup webdav-upload 传异地
#   → 可选的 PANDORA_BACKUP_REMOTE_HOOK → 按 PANDORA_BACKUP_RETENTION_DAYS 清本地旧备份。
# Redis 不备份（会话丢了只需重新登录，任务丢了靠对账补）。
# 备份目录取 `pandora backup dir`（config 的 backup.dir，与后台系统状态页读的是同一个），不在 deploy.env 里另写一份。
# 读 /etc/pandora/deploy.env（root 所有、0600、单链接）里的 POSTGRES_DB / POSTGRES_PORT / POSTGRES_SUPER_PASSWORD 与 PANDORA_BACKUP_*。
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
  # 逐行读 KEY=VALUE，不 source、不展开：值原样取（去掉一层成对引号）。只收 POSTGRES_* 与 PANDORA_* 两组键，
  # 文件里写了 PATH、IFS 之类也不会改到本脚本的运行环境；赋的是普通变量，不导出给子进程
  local line key value
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    [[ "$line" =~ ^([A-Z][A-Z0-9_]*)=(.*)$ ]] || continue
    key="${BASH_REMATCH[1]}" value="${BASH_REMATCH[2]}"
    [[ "$key" == POSTGRES_* || "$key" == PANDORA_* ]] || continue
    if [[ "$value" =~ ^\"(.*)\"$ ]] || [[ "$value" =~ ^\'(.*)\'$ ]]; then value="${BASH_REMATCH[1]}"; fi
    printf -v "$key" '%s' "$value"
  done <&"$env_fd"
  exec {env_fd}<&-
}
# 以超级用户跑 PostgreSQL 客户端（psql、pg_dump、pg_restore、createdb、dropdb）：本机客户端经
# 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户连（不用 runuser 切到 postgres：备份单元的系统调用过滤
# 不许切换用户）。口令只经环境变量 PGPASSWORD 给这一个客户端，不进命令行参数、不导出给整个脚本。
# 只读归档目录（pg_restore --list）不连库，不要求凭据；要连库的先过 pandora_pg_require_login。
# 备份、校验、恢复三件套各带一份（只信任自己，不 source 共用文件），backup-scripts_mock_test.sh 核对逐字一致。
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
# 部署脚本的变量文件（安装器写的 /etc/pandora/deploy.env，root 所有、0600）；PANDORA_DEPLOY_ENV 只给桩测试改路径
load_trusted_env "${PANDORA_DEPLOY_ENV:-/etc/pandora/deploy.env}"

# 跑 root 所有、单链接、不可他写的程序（发布包的 pandora 二进制），与 verify-backup.sh 同一份
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
: "${PANDORA_BACKUP_RETENTION_DAYS:?PANDORA_BACKUP_RETENTION_DAYS is required}"
: "${PANDORA_BACKUP_AGE_RECIPIENT:?PANDORA_BACKUP_AGE_RECIPIENT is required; plaintext backups are forbidden}"
: "${PANDORA_BACKUP_AGE_IDENTITY:?PANDORA_BACKUP_AGE_IDENTITY is required: every local backup is sealed with it}"
[[ "$PANDORA_BACKUP_AGE_IDENTITY" = /* ]] || die "PANDORA_BACKUP_AGE_IDENTITY must be an absolute path"
# 缺省取安装根目录（本脚本在 <根>/deploy/ 下）的 bin/pandora
pandora_bin="${PANDORA_BACKUP_BIN:-$(dirname -- "$PWD")/bin/pandora}"
[[ "$pandora_bin" = /* ]] || die "PANDORA_BACKUP_BIN must be an absolute path"
# 异地备份：这个文件在就上传（与系统状态页「异地备份已配置」同一个判断）；只给桩测试改路径
webdav_config="${PANDORA_BACKUP_WEBDAV_CONFIG:-/etc/pandora/backup-webdav.json}"
[[ "$webdav_config" = /* ]] || die "PANDORA_BACKUP_WEBDAV_CONFIG must be an absolute path"

[[ "$PANDORA_BACKUP_RETENTION_DAYS" =~ ^[0-9]+$ ]] || die "PANDORA_BACKUP_RETENTION_DAYS must be a non-negative integer"
case "$PANDORA_BACKUP_AGE_RECIPIENT" in
  *CHANGE_ME*|*REPLACE*) die "replace the placeholder PANDORA_BACKUP_AGE_RECIPIENT" ;;
  age1*|age-plugin-*) ;;
  *) die "PANDORA_BACKUP_AGE_RECIPIENT does not look like an age recipient" ;;
esac

remote_hook="${PANDORA_BACKUP_REMOTE_HOOK:-}"
failure_hook="${PANDORA_BACKUP_FAILURE_HOOK:-}"
validate_hook "$remote_hook" PANDORA_BACKUP_REMOTE_HOOK
validate_hook "$failure_hook" PANDORA_BACKUP_FAILURE_HOOK
require_command age
pandora_pg_require pg_dump pg_restore
pandora_pg_require_login
require_command sha256sum
require_command find
require_command flock
require_command ln
require_command install

# 备份目录：config 的 backup.dir（pandora backup dir 已核过是规范的绝对路径、不是 /）
backup_dir="$(run_trusted_executable "$pandora_bin" backup dir)" || die "cannot read backup.dir (pandora backup dir failed)"
[[ "$backup_dir" = /* && "$backup_dir" != / ]] || die "backup.dir must be an absolute path other than /: $backup_dir"

# 维护锁：备份与恢复共用。放在只归备份单元的运行目录里（RuntimeDirectory=pandora-maintenance），
# 不与面板服务的运行目录共用：服务停下时 systemd 会删它们的运行目录，恢复正是先停服务再动库
maintenance_lock_dir=/run/pandora-maintenance
maintenance_lock=/run/pandora-maintenance/database-maintenance.lock
install -d -m 0700 -- "$maintenance_lock_dir"
require_trusted_parent_chain "$maintenance_lock"
[ "$(stat -c %u -- "$maintenance_lock_dir")" = 0 ] \
  || die "database maintenance lock directory must be root-owned"
state_mode=$((8#$(stat -c %a -- "$maintenance_lock_dir")))
# 掩码是 0077 而不是 0177：目录必须保留属主的执行位，否则连自己都进不去
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

# 备份目录由安装器建成 root:pandora 0750：面板（以 pandora 用户跑）的系统状态页要列目录、看最新一份的时间；
# 文件本身都是 0600（umask 077），面板读不到内容。这里只在目录不存在时建成 0700，不改已有目录的属组与权限
#（备份单元的系统调用过滤不许 chown）。目录必须是 root 所有的真目录，组不可写、其他人无权限
[ -e "$backup_dir" ] || install -d -m 0700 -- "$backup_dir"
[ -d "$backup_dir" ] && [ ! -L "$backup_dir" ] || die "backup.dir is not a real directory: $backup_dir"
[ "$(stat -c %u -- "$backup_dir")" = 0 ] || die "backup.dir must be root-owned: $backup_dir"
dir_mode=$((8#$(stat -c %a -- "$backup_dir")))
(( (dir_mode & 0027) == 0 )) || die "backup.dir must not be group-writable or accessible to others (0700 or 0750): $backup_dir"
# 归档名精确到秒：上一份刚好在同一秒写完（连跑两次）时等到下一秒再取，不报「已存在」
for _ in 1 2 3; do
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  archive="$backup_dir/pandora-postgres-${stamp}.dump.age"
  checksum="${archive}.sha256"
  [ -e "$archive" ] || [ -e "$checksum" ] || break
  sleep 1
done
[ ! -e "$archive" ] && [ ! -e "$checksum" ] || die "backup artifact already exists: $archive"
workdir="$(mktemp -d "$backup_dir/.pandora-postgres-${stamp}.XXXXXX")"
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
# 带属主与权限导出：restore-postgres.sh 照原样还原属主与权限
pandora_pg pg_dump -d "$POSTGRES_DB" \
    --format=custom --compress=6 \
  | tee "$fifo" \
  | age --encrypt --recipient "$PANDORA_BACKUP_AGE_RECIPIENT" >"$tmp"
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

# 本地封条（<归档>.seal）：用这台安装的 age 私钥派生的 HMAC 签归档与校验文件，verify-backup.sh 凭它确认
# 这份备份是本安装写出的（见 internal/pkg/dbbackup 的 LocalSealSchema）。封不上就算备份失败，并把刚发布的
# 归档与校验文件撤掉：留在原地的话，系统状态页与留存都会把它当成一份备份，可它恢复不了。
# 封条前还核私钥推出的收件人就是 PANDORA_BACKUP_AGE_RECIPIENT：对不上的私钥封得上、却解不开这份备份
if ! run_trusted_executable "$pandora_bin" backup seal-local "$archive" "$checksum" "$PANDORA_BACKUP_AGE_IDENTITY" "$PANDORA_BACKUP_AGE_RECIPIENT" >/dev/null; then
  rm -f -- "$archive" "$checksum" "${archive}.seal"
  die "sealing the local backup failed (age identity $PANDORA_BACKUP_AGE_IDENTITY readable, root-owned, 0600, one valid AGE-SECRET-KEY-1 line matching PANDORA_BACKUP_AGE_RECIPIENT?); the unsealed archive was removed"
fi

# 异地（WebDAV）：只传已加密、已校验、已封条的文件，明文 dump 从不落盘。传失败时本地这份照常留着（已封条、可恢复），
# 但本次备份记为失败（单元 failed、巡检与失败 hook 都看得见）
if [ -e "$webdav_config" ]; then
  run_trusted_executable "$pandora_bin" backup webdav-upload --webdav-config "$webdav_config" "$archive" "$checksum" \
    || die "offsite WebDAV upload failed; the sealed local backup $archive is kept (details: journalctl -u pandora-backup.service)"
fi

if [ -n "$remote_hook" ]; then
  run_hook "$remote_hook" "$archive" "$checksum"
fi

# 留存只动本脚本在这个目录里写出的文件。0 天表示只留今天的（find -mtime +0）
while IFS= read -r -d '' old; do
  rm -f -- "$old" "${old}.sha256" "${old}.seal"
done < <(find "$backup_dir" -maxdepth 1 -type f \
  -name 'pandora-postgres-*.dump.age' -mtime "+${PANDORA_BACKUP_RETENTION_DAYS}" -print0)

trap - EXIT INT TERM
echo "backup complete: $archive"
