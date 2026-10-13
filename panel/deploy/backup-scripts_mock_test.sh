#!/usr/bin/env bash
# 备份三件套（backup-postgres.sh、verify-backup.sh、restore-postgres.sh）与 healthcheck.sh 的桩测试，
# 不需要数据库、root 或 systemd（要 Linux root 的真跑往返在 backup-provenance_mock_test.sh）：
#   - 只有一种连法：本机客户端经 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户（POSTGRES_SUPER_PASSWORD）连，
#     口令只经这一个客户端的环境变量；没有容器分支、不切用户；
#   - 三件套只信任自己、不 source 共用文件：pandora_pg 等函数与变量文件解析器三份逐字一致；
#   - 变量文件（/etc/pandora/deploy.env）逐行读 KEY=VALUE、不 source：只收 POSTGRES_* 与 PANDORA_*，
#     不执行任何内容、不导出给子进程；
#   - restore-postgres.sh 的恢复步骤抽出来换上桩跑真调用：角色只认 pandora_owner / pandora_app / postgres、
#     缺的建成 NOLOGIN、目标库属主 pandora_owner、照原样还原属主与权限、主流程顺序、收尾提示；
#   - 备份单元停下时不删运行目录里的维护锁；同一秒连跑两次不撞名。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'backup-scripts: %s\n' "$*" >&2; exit 1; }
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-backup-scripts.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
TRIO=(backup-postgres.sh verify-backup.sh restore-postgres.sh)

# --- 没有容器分支、不切用户、没有旧名字 ---------------------------------------------------------------
for script in "${TRIO[@]}" healthcheck.sh; do
  # 旧名字的模式拆开写，免得本文件自己被「运维文件里不留旧名字」的检查扫到
  old="ae""gis|AE""GIS_|VAL""KEY_"
  if grep -nE "DB_LAYOUT|docker|runuser -u|$old" "$DEPLOY/$script"; then
    fail "$script still has a container branch or an old name"
  fi
done

# --- 三件套共用的函数逐字一致 -------------------------------------------------------------------------
extract_fn() { awk -v fn="$2" '$0 == fn "() {" { p = 1 } p { print } p && /^}$/ { exit }' "$DEPLOY/$1"; }
for fn in pandora_pg pandora_pg_require pandora_pg_require_login load_trusted_env require_trusted_parent_chain; do
  ref="$(extract_fn "${TRIO[0]}" "$fn")"
  [ -n "$ref" ] || fail "${TRIO[0]} has no $fn"
  for script in "${TRIO[@]}"; do
    [ "$(extract_fn "$script" "$fn")" = "$ref" ] || fail "$script's $fn differs from ${TRIO[0]}'s"
  done
done
ref="$(extract_fn backup-postgres.sh run_trusted_executable)"
[ -n "$ref" ] && [ "$(extract_fn verify-backup.sh run_trusted_executable)" = "$ref" ] \
  || fail "run_trusted_executable differs between backup-postgres.sh and verify-backup.sh"
for script in "${TRIO[@]}"; do
  if grep -Eq 'export PGPASSWORD|^PGPASSWORD=' "$DEPLOY/$script"; then
    fail "$script exports PGPASSWORD for the whole script"
  fi
  grep -Fq 'load_trusted_env "${PANDORA_DEPLOY_ENV:-/etc/pandora/deploy.env}"' "$DEPLOY/$script" \
    || fail "$script does not read /etc/pandora/deploy.env"
done

# --- pandora_pg：本机客户端、回环、postgres 超级用户，口令只在这个客户端的环境里 ----------------------
mkdir -p "$T/bin"
cat >"$T/bin/pg_dump" <<'MOCK'
#!/usr/bin/env bash
printf 'pg_dump %s | PGHOST=%s PGPORT=%s PGUSER=%s PGPASSWORD=%s PGSSLMODE=%s\n' "$*" \
  "${PGHOST:-}" "${PGPORT:-}" "${PGUSER:-}" "${PGPASSWORD:-}" "${PGSSLMODE:-}" >"$(dirname "$0")/../pg.calls"
MOCK
chmod 0755 "$T/bin/pg_dump"
eval "$(extract_fn backup-postgres.sh pandora_pg)"
(
  PATH="$T/bin:$PATH"; POSTGRES_SUPER_PASSWORD=super-pw POSTGRES_PORT=5434
  pandora_pg pg_dump -d pandora --format=custom
)
[ "$(cat "$T/pg.calls")" = 'pg_dump -d pandora --format=custom | PGHOST=127.0.0.1 PGPORT=5434 PGUSER=postgres PGPASSWORD=super-pw PGSSLMODE=disable' ] \
  || fail "pandora_pg: $(cat "$T/pg.calls")"
eval "$(extract_fn backup-postgres.sh pandora_pg_require_login)"
die() { echo "die: $*" >&2; exit 1; }
if ( unset POSTGRES_SUPER_PASSWORD; POSTGRES_PORT=5432; pandora_pg_require_login ) 2>/dev/null; then
  fail 'pandora_pg_require_login accepted a missing superuser password'
fi
if ( POSTGRES_SUPER_PASSWORD=x POSTGRES_PORT='5432;x'; pandora_pg_require_login ) 2>/dev/null; then
  fail 'pandora_pg_require_login accepted a malformed port'
fi
( POSTGRES_SUPER_PASSWORD=x POSTGRES_PORT=5432; pandora_pg_require_login ) || fail 'pandora_pg_require_login refused a valid login'
unset -f pandora_pg pandora_pg_require_login

# --- 变量文件解析：取 load_trusted_env 里逐行读的那段，对一个打开的 fd 真跑 -----------------------------
# （身份核对那半段要 Linux 的 /proc/self/fd 与 GNU stat，由 backup-provenance_mock_test.sh 在 root 下真跑）
parser="$(extract_fn backup-postgres.sh load_trusted_env | awk '/^  local line key value$/ { p = 1 } p { print } p && /^  done <&"\$env_fd"$/ { exit }')"
[ -n "$parser" ] || fail 'cannot find the KEY=VALUE parser in load_trusted_env'
if grep -Eq '(^|[^[:alnum:]_])(source|eval)[[:space:]]|^[[:space:]]*\.[[:space:]]' <<<"$parser"; then fail "the env parser sources or evals: $parser"; fi
eval "parse_env_fd() { $parser
}"
cat >"$T/deploy.env" <<ENV
# 注释与空行跳过

POSTGRES_DB=pandora
POSTGRES_PORT='5433'
PANDORA_BACKUP_AGE_RECIPIENT="age1fixturerecipient"
PANDORA_BACKUP_REMOTE_HOOK=\$(touch $T/executed-pandora)
PANDORA_WITH_EQUALS=a=b=c
PATH=/nonexistent
IFS=x
UNRELATED=\$(touch $T/executed-other)
lower_case=1
 POSTGRES_USER=leading-space
ENV
printf 'PANDORA_CRLF=value\r\nPANDORA_LAST=no-newline' >>"$T/deploy.env"
(
  exec {env_fd}<"$T/deploy.env"
  path_before="$PATH"
  # 调用方环境里已导出的同名变量先清掉：赋值会沿用导出属性，那就分不清是不是解析器导出的
  while read -r v; do unset "$v"; done < <(compgen -e | grep -E '^(POSTGRES|PANDORA)_' || true)
  exported_before="$(env | grep -E '^(POSTGRES|PANDORA)_' | sort || true)"
  parse_env_fd
  [ "$PATH" = "$path_before" ] || { echo "PATH changed to $PATH" >&2; exit 1; }
  [ "$IFS" = $' \t\n' ] || { echo 'IFS changed' >&2; exit 1; }
  [ "$POSTGRES_DB" = pandora ] && [ "$POSTGRES_PORT" = 5433 ] || { echo "POSTGRES_*: $POSTGRES_DB $POSTGRES_PORT" >&2; exit 1; }
  [ "$PANDORA_BACKUP_AGE_RECIPIENT" = age1fixturerecipient ] || { echo "quotes: $PANDORA_BACKUP_AGE_RECIPIENT" >&2; exit 1; }
  [ "$PANDORA_BACKUP_REMOTE_HOOK" = "\$(touch $T/executed-pandora)" ] || { echo "value expanded: $PANDORA_BACKUP_REMOTE_HOOK" >&2; exit 1; }
  [ "$PANDORA_WITH_EQUALS" = a=b=c ] && [ "$PANDORA_CRLF" = value ] && [ "$PANDORA_LAST" = no-newline ] \
    || { echo "values: $PANDORA_WITH_EQUALS $PANDORA_CRLF $PANDORA_LAST" >&2; exit 1; }
  [ -z "${UNRELATED+x}" ] && [ -z "${lower_case+x}" ] && [ -z "${POSTGRES_USER+x}" ] || { echo 'a non-whitelisted key was taken' >&2; exit 1; }
  # 赋的是普通变量：子进程看不到口令与路径
  [ "$(env | grep -E '^(POSTGRES|PANDORA)_' | sort || true)" = "$exported_before" ] || { echo 'parsed values were exported' >&2; exit 1; }
) || fail 'deploy.env parser'
[ ! -e "$T/executed-pandora" ] && [ ! -e "$T/executed-other" ] || fail 'deploy.env content was executed'

# --- restore-postgres.sh 的恢复步骤：抽出函数、换上桩跑真调用 -----------------------------------------
# （整个脚本要 Linux root 与 /proc 绑定的 deploy.env，本机跑不了；这几个函数就是它在正式库上做的事）
for fn in archive_role_plan check_restore_roles create_missing_restore_roles require_db_owner_role create_target_db restore_into_target; do
  eval "$(extract_fn restore-postgres.sh "$fn")"
  declare -F "$fn" >/dev/null || fail "restore-postgres.sh has no $fn"
done
grep -qx 'db_owner=pandora_owner' "$DEPLOY/restore-postgres.sh" || fail 'restore-postgres.sh does not own the database as pandora_owner'
db_owner=pandora_owner
: >"$T/pg.calls"
# pandora_pg 桩：记下调用；「角色在不在」按 $T/roles 答
pandora_pg() {
  printf '%s\n' "$*" >>"$T/pg.calls"
  case "$*" in
    *"FROM pg_catalog.pg_roles WHERE rolname = '"*)
      local r; r="$(printf '%s' "$*" | sed -n "s/.*rolname = '\([a-z_]*\)'.*/\1/p")"
      if grep -qx "$r" "$T/roles"; then echo 1; fi
      return 0 ;;
    'pg_restore -d '*) cat >"$T/restore.stdin"; return 0 ;;
  esac
  return 0
}
age() { printf 'ARCHIVE\n'; }
# 目标库：属主 pandora_owner、UTF8；正式库关连接闸门
POSTGRES_DB=pandora target_db=pandora
create_target_db
grep -qx 'createdb --template=template0 --encoding=UTF8 --connection-limit=0 --owner=pandora_owner pandora' "$T/pg.calls" \
  || fail "production createdb: $(cat "$T/pg.calls")"
: >"$T/pg.calls"; target_db=pandora_recovery
create_target_db
grep -qx 'createdb --template=template0 --encoding=UTF8 --owner=pandora_owner pandora_recovery' "$T/pg.calls" \
  || fail "recovery createdb: $(cat "$T/pg.calls")"
# 照原样恢复属主与权限
: >"$T/pg.calls"; PANDORA_BACKUP_AGE_IDENTITY=/k archive=/a
restore_into_target
grep -qx 'pg_restore -d pandora_recovery --exit-on-error' "$T/pg.calls" || fail "restore drops owners or privileges: $(cat "$T/pg.calls")"
grep -qx 'ARCHIVE' "$T/restore.stdin" || fail 'restore did not receive the decrypted archive'
# 备份里要的角色：属主、默认权限的主人、被授权者；PUBLIC 不算
plan="$(printf '%s\n' \
  'ALTER SCHEMA public OWNER TO pandora_owner;' \
  'ALTER TABLE public.users OWNER TO pandora_owner;' \
  'ALTER FUNCTION public.purge_audit_events(integer) OWNER TO pandora_owner;' \
  'ALTER EXTENSION placeholder OWNER TO postgres;' \
  'ALTER DEFAULT PRIVILEGES FOR ROLE pandora_owner IN SCHEMA public GRANT SELECT ON TABLES TO pandora_app;' \
  'ALTER DEFAULT PRIVILEGES FOR ROLE pandora_owner IN SCHEMA public REVOKE ALL ON FUNCTIONS FROM PUBLIC;' \
  'GRANT SELECT ON TABLE public.users TO pandora_app;' \
  'GRANT USAGE ON SCHEMA public TO pandora_app WITH GRANT OPTION;' \
  'GRANT USAGE ON SCHEMA public TO PUBLIC;' \
  'REVOKE ALL ON FUNCTION public.purge_audit_events(integer) FROM PUBLIC;' | archive_role_plan)"
[ "$plan" = "$(printf '%s\n' pandora_app pandora_owner postgres)" ] || fail "role plan: $plan"
# 缺的角色建成 NOLOGIN、无特权；已有的不重建
: >"$T/pg.calls"; printf 'postgres\npandora_owner\n' >"$T/roles"
check_restore_roles "$plan" || fail 'a valid role plan was refused'
create_missing_restore_roles "$plan" 2>/dev/null || fail "create_missing_restore_roles failed: $(cat "$T/pg.calls")"
grep -qx 'psql -X -d postgres -v ON_ERROR_STOP=1 -c CREATE ROLE "pandora_app" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS' "$T/pg.calls" \
  || fail "app role not created safely: $(cat "$T/pg.calls")"
[ "$(grep -c 'CREATE ROLE' "$T/pg.calls")" -eq 1 ] || fail "an existing role was recreated: $(cat "$T/pg.calls")"
# 认不出的角色（含旧安装的角色名）：停下，什么都不建、也不连库查；提示说清楚不是本安装方式导出的
for intruder in intruder legacy_app '"Quoted"'; do
  : >"$T/pg.calls"
  if ( check_restore_roles "$(printf '%s\n' pandora_app "$intruder")" ) >"$T/out" 2>&1; then fail "role $intruder was accepted"; fi
  [ ! -s "$T/pg.calls" ] || fail "the role check touched the database before refusing $intruder: $(cat "$T/pg.calls")"
  grep -Fq 'not exported by this kind of installation' "$T/out" && grep -Fq 'nothing was changed' "$T/out" \
    && grep -Fq 'RUNBOOK.md, restoring a backup of unknown origin' "$T/out" \
    || fail "unhelpful refusal for $intruder: $(cat "$T/out")"
  if grep -Fq 'create it by hand' "$T/out"; then fail 'the refusal still suggests creating the role by hand'; fi
done
# 缺白名单角色、同时有陌生角色：先核全部再建，一条 CREATE ROLE 都不跑
: >"$T/pg.calls"; printf 'postgres\n' >"$T/roles"
if ( check_restore_roles "$(printf '%s\n' pandora_app intruder)" && \
     create_missing_restore_roles "$(printf '%s\n' pandora_app intruder)" ) >/dev/null 2>&1; then
  fail 'a plan with an unknown role went through'
fi
if grep -q 'CREATE ROLE' "$T/pg.calls"; then fail "a whitelisted role was created before refusing the unknown one: $(cat "$T/pg.calls")"; fi
# 目标库属主要先在（createdb 在删掉旧库之后才跑）
printf 'pandora_owner\n' >"$T/roles"
( require_db_owner_role ) || fail 'an existing database owner role was refused'
: >"$T/roles"
if ( require_db_owner_role ) 2>/dev/null; then fail 'a missing database owner role was accepted'; fi
unset -f pandora_pg age die

# --- restore-postgres.sh 主流程：核完整性 → 备好角色 → 核库属主 → 同参数演练 → 正式库保护 → 恢复 → 开闸门 ---
awk '/^"\$PWD\/verify-backup.sh" "\$archive"$/ { v = NR } /^check_restore_roles "\$role_plan"$/ { e = NR }
     /^require_db_owner_role$/ { d = NR } /^create_missing_restore_roles "\$role_plan"$/ { m = NR }
     /^PANDORA_BACKUP_VERIFY_RESTORE=owners "\$PWD\/verify-backup.sh"/ { o = NR }
     /^begin_production_guard$/ { g = NR } /^restore_into_target$/ { r = NR } /^commit_production_guard$/ { c = NR }
     END { exit !(v && e && d && m && o && g && r && c && v < e && e < d && d < m && m < o && o < g && g < r && r < c) }' "$DEPLOY/restore-postgres.sh" \
  || fail 'restore-postgres.sh steps are out of order'
if grep -Eq 'REASSIGN|reassign_source_migrator|legacy_repair|repair_legacy_privileges|migrator' "$DEPLOY/restore-postgres.sh"; then
  fail 'restore-postgres.sh still carries the cross-layout owner hand-over or the old-format privilege repair'
fi
# 库级设置不在归档里：收尾提示重配角色并迁移，并且在 commit 之后
awk '/^commit_production_guard$/ { c = NR } /NOTE before starting the services: re-apply the database roles .*pandora migrate up/ { b = NR }
     END { exit !(c && b && c < b) }' "$DEPLOY/restore-postgres.sh" || fail 'restore-postgres.sh does not tell the operator to re-apply roles and migrate'
if grep -q '^PANDORA_BACKUP_VERIFY_RESTORE=1 ' "$DEPLOY/restore-postgres.sh"; then fail 'the restore rehearsal drops owners'; fi
grep -Fq 'createdb_opts+=(--owner pandora_owner)' "$DEPLOY/verify-backup.sh" && grep -Fq 'restore_opts+=(--no-owner --no-privileges)' "$DEPLOY/verify-backup.sh" \
  || fail 'verify-backup.sh has no owner-preserving rehearsal with the same database owner'
awk '/if \[ "\$PANDORA_BACKUP_VERIFY_RESTORE" = owners \]; then/ { o = NR } /createdb_opts\+=\(--owner/ { w = NR } /^  else$/ && o && !e { e = NR }
     /restore_opts\+=\(--no-owner --no-privileges\)/ { n = NR } END { exit !(o && w && e && n && o < w && w < e && e < n) }' "$DEPLOY/verify-backup.sh" \
  || fail 'verify-backup.sh: the owner-preserving rehearsal is not the owners branch'
# 演练开关只认本次调用给的值，deploy.env 里写了也不算
grep -Fq 'PANDORA_BACKUP_VERIFY_RESTORE="${invocation_verify_restore:-0}"' "$DEPLOY/verify-backup.sh" \
  || fail 'verify-backup.sh takes the rehearsal switch from deploy.env'
if grep -Eq 'pg_restore .*--no-owner' "$DEPLOY/restore-postgres.sh"; then fail 'restore-postgres.sh restores with --no-owner'; fi
# 恢复中途失败、正式库已删：提示照实说旧库没了
grep -Fq 'target_dropped=1' "$DEPLOY/restore-postgres.sh" && grep -Fq 'was already dropped before the failure; it is gone' "$DEPLOY/restore-postgres.sh" \
  || fail 'a failed restore does not say the previous database is gone'
awk '/pandora_pg dropdb --force "\$target_db"/ { d = NR } /^  target_dropped=1$/ { t = NR } END { exit !(d && t && t == d + 1) }' "$DEPLOY/restore-postgres.sh" \
  || fail 'target_dropped is not set right after dropping the database'
# 恢复停的是新栈的全部单元（面板两个服务、备份、巡检）
grep -qx 'quiesce_units=(pandora-serve.service pandora-worker.service pandora-backup.service pandora-health.service)' "$DEPLOY/restore-postgres.sh" \
  || fail 'restore-postgres.sh does not quiesce the pandora units'

# --- 备份单元与 backup-postgres.sh ---------------------------------------------------------------------
unit="$DEPLOY/systemd/pandora-backup.service"
grep -qx 'RuntimeDirectory=pandora-maintenance' "$unit" && grep -qx 'RuntimeDirectoryPreserve=yes' "$unit" \
  || fail 'pandora-backup.service removes the maintenance lock directory when it stops'
grep -qx 'ExecStart=/opt/pandora/deploy/backup-postgres.sh' "$unit" || fail 'pandora-backup.service runs a different script'
grep -qx 'User=root' "$unit" || fail 'pandora-backup.service must run as root'
# 可写的正好是备份目录与 WebDAV 检查点目录；系统调用过滤不许 chown，脚本也就不能 chown
grep -qx 'ReadWritePaths=/var/backups/pandora -/var/lib/pandora/backup-webdav' "$unit" || fail "pandora-backup.service ReadWritePaths drifted"
if grep -vE '^[[:space:]]*#' "$DEPLOY/backup-postgres.sh" | grep -E '(^|[[:space:]])(chown|install [^#]*-o )'; then fail 'backup-postgres.sh changes ownership (blocked by the unit seccomp filter)'; fi
grep -qx 'Unit=pandora-backup.service' "$DEPLOY/systemd/pandora-backup.timer" || fail 'pandora-backup.timer does not start the backup service'
grep -qx 'Persistent=true' "$DEPLOY/systemd/pandora-backup.timer" || fail 'pandora-backup.timer does not catch up after downtime'
if grep -Eq -- '--no-owner|--no-acl' "$DEPLOY/backup-postgres.sh"; then fail 'backup-postgres.sh drops owners or privileges'; fi
# 归档名精确到秒：同一秒已有一份时等到下一秒再取（连跑两次不报「已存在」）
awk '/^for _ in 1 2 3; do$/ { f = NR } f && !s && /^  stamp="\$\(date -u \+%Y%m%dT%H%M%SZ\)"$/ { s = NR }
     f && /^  sleep 1$/ { z = NR } f && /^done$/ && !d { d = NR }
     END { exit !(f && s && z && d && f < s && s < z && z < d) }' "$DEPLOY/backup-postgres.sh" \
  || fail 'backup-postgres.sh does not retry the archive name on a same-second collision'
# 备份目录取 config 的 backup.dir；只备份 PostgreSQL，不碰 Redis
grep -Fq 'backup_dir="$(run_trusted_executable "$pandora_bin" backup dir)"' "$DEPLOY/backup-postgres.sh" || fail 'backup dir is not read from backup.dir'
if grep -Eiq 'redis-cli|BGSAVE|dump\.rdb' "$DEPLOY/backup-postgres.sh"; then fail 'backup-postgres.sh backs up Redis'; fi

printf 'backup-scripts mock: PASS\n'
