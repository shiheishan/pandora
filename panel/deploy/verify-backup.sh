#!/usr/bin/env bash
# 核一份备份能不能恢复：来源（本地封条或签名清单 + 可信检查点）→ SHA256 → age 解密 + pg_restore 读目录；
# PANDORA_BACKUP_VERIFY_RESTORE=1 再在临时库里完整恢复一遍（不还原属主与权限），=owners 照原样还原属主与权限
#（restore-postgres.sh 备好角色后这样演练）。封条与清单的核验由 pandora backup 子命令做。
#   verify-backup.sh /absolute/path/pandora-postgres-<时间>.dump.age
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
invocation_verify_restore="${PANDORA_BACKUP_VERIFY_RESTORE-}"
# 部署脚本的变量文件（安装器写的 /etc/pandora/deploy.env，root 所有、0600）；PANDORA_DEPLOY_ENV 只给桩测试改路径
load_trusted_env "${PANDORA_DEPLOY_ENV:-/etc/pandora/deploy.env}"
PANDORA_BACKUP_VERIFY_RESTORE="${invocation_verify_restore:-0}"

[ "$#" -eq 1 ] || die "usage: $0 /absolute/path/pandora-postgres-*.dump.age"
archive="$1"
[[ "$archive" = /* ]] || die "backup path must be absolute"
[ -f "$archive" ] || die "backup not found: $archive"
checksum="${archive}.sha256"
[ -f "$checksum" ] || die "checksum not found: $checksum"
manifest="${archive%.dump.age}.manifest.json"
seal="${archive}.seal"
: "${PANDORA_BACKUP_AGE_IDENTITY:?PANDORA_BACKUP_AGE_IDENTITY is required}"
[[ "$PANDORA_BACKUP_AGE_IDENTITY" = /* ]] || die "PANDORA_BACKUP_AGE_IDENTITY must be an absolute path"
# 缺省取安装根目录（本脚本在 <根>/deploy/ 下，已 cd 到这里）的 bin/pandora，即 /opt/pandora/bin/pandora
pandora_bin="${PANDORA_BACKUP_BIN:-$(dirname -- "$PWD")/bin/pandora}"
[[ "$pandora_bin" = /* ]] || die "PANDORA_BACKUP_BIN must be an absolute path"
# 来源两种，都要核过才往下走：
#   - 本机写的备份：backup-postgres.sh 写的封条 <归档>.seal（这台安装的 age 私钥派生的 HMAC），本机留存期内哪一份都能核；
#   - 从 WebDAV 取回的备份：签名清单 + WebDAV 之外的可信检查点（只认最新一份，防替换、防回滚）。
# 两样都没有就拒绝：来路不明的归档不进库
if [ -e "$seal" ]; then
  run_trusted_executable "$pandora_bin" backup verify-local-seal "$archive" "$checksum" "$seal" "$PANDORA_BACKUP_AGE_IDENTITY" >/dev/null \
    || die "local backup seal verification failed: this archive was not written by this installation, or it was changed (if you are sure of its origin, restore it by hand: RUNBOOK.md, restoring a backup of unknown origin)"
elif [ -e "$manifest" ]; then
  : "${PANDORA_BACKUP_MANIFEST_PUBLIC_KEY:?PANDORA_BACKUP_MANIFEST_PUBLIC_KEY is required}"
  : "${PANDORA_BACKUP_TRUSTED_CHECKPOINT:?PANDORA_BACKUP_TRUSTED_CHECKPOINT is required}"
  run_trusted_executable "$pandora_bin" backup verify-manifest "$archive" "$checksum" "$manifest" \
    "$PANDORA_BACKUP_MANIFEST_PUBLIC_KEY" "$PANDORA_BACKUP_TRUSTED_CHECKPOINT" \
    || die "signed manifest or trusted checkpoint verification failed"
else
  die "neither a local seal ($seal) nor a signed manifest ($manifest): refusing an archive of unknown origin"
fi
[ -r "$PANDORA_BACKUP_AGE_IDENTITY" ] || die "age identity is not readable"
require_command age
pandora_pg_require pg_restore psql createdb dropdb

(cd "$(dirname "$archive")" && sha256sum --check --status "$(basename "$checksum")") \
  || die "SHA256 verification failed"
# pg_restore --list 读完目录就退出，不读后面的数据块；不把剩下的读掉，age 会因 SIGPIPE 失败（库稍大就必现，
# panel2 演练发现），pipefail 把它当成解密失败
age --decrypt --identity "$PANDORA_BACKUP_AGE_IDENTITY" "$archive" \
  | { pandora_pg pg_restore --list >/dev/null; list_rc=$?; cat >/dev/null; exit "$list_rc"; } \
  || die "age decryption or pg_restore TOC verification failed"

# PANDORA_BACKUP_VERIFY_RESTORE=1：在临时库里完整恢复一遍（不还原属主与权限，单独校验时用，角色不必齐）；
# =owners：与正式恢复同样的参数照原样还原属主与权限，restore-postgres.sh 先备好角色再这样演练
case "${PANDORA_BACKUP_VERIFY_RESTORE:-0}" in
  0|1|owners) ;;
  *) die "PANDORA_BACKUP_VERIFY_RESTORE must be 0, 1 or owners" ;;
esac
if [ "${PANDORA_BACKUP_VERIFY_RESTORE:-0}" != 0 ]; then
  : "${POSTGRES_DB:?POSTGRES_DB is required for restore verification}"
  pandora_pg_require_login
  verify_db="pandora_verify_$(date -u +%Y%m%d%H%M%S)_$$"
  [[ "$verify_db" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]] || die "unsafe temporary database name"
  cleanup_db() {
    pandora_pg dropdb --if-exists --force "$verify_db" >/dev/null 2>&1 || true
  }
  trap cleanup_db EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  restore_opts=(--exit-on-error)
  createdb_opts=(--template=template0 --encoding=UTF8)
  if [ "$PANDORA_BACKUP_VERIFY_RESTORE" = owners ]; then
    # 与正式恢复同参数：库属主与 restore-postgres.sh 建正式库时一样（迁移属主角色 pandora_owner）
    createdb_opts+=(--owner pandora_owner)
  else
    restore_opts+=(--no-owner --no-privileges)
  fi
  pandora_pg createdb "${createdb_opts[@]}" "$verify_db"
  age --decrypt --identity "$PANDORA_BACKUP_AGE_IDENTITY" "$archive" \
    | pandora_pg pg_restore -d "$verify_db" "${restore_opts[@]}"
  pandora_pg psql -X -d "$verify_db" -v ON_ERROR_STOP=1 -c 'SELECT 1' >/dev/null
  cleanup_db
  trap - EXIT INT TERM
fi

echo "backup verified: $archive"
