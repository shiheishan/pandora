#!/usr/bin/env bash
# 连库的运维脚本（check-migrations.sh、migrate.sh、backup-postgres.sh、verify-backup.sh、restore-postgres.sh、
# psql.sh、bootstrap.sh、healthcheck.sh）的桩测试，不需要数据库、root 或 systemd：
#   - 只有一种连法：本机客户端经 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户（POSTGRES_SUPER_PASSWORD）连，
#     口令只经环境变量。没有布局判定、没有容器分支；老 .env 里留着 PANDORA_DB_LAYOUT 也照常走本机；
#   - 备份三件套（只信任自己，不 source 共用文件）的 pandora_pg 等三个函数逐字一致；
#   - restore-postgres.sh 的恢复步骤抽出来换上桩跑真调用：角色只认三个、缺的建成 NOLOGIN、目标库属主、
#     照原样还原属主与权限、主流程顺序；
#   - migrate.sh：经批准回退到本机时以 postgres 连；迁移 DSN 里的口令不进任何命令行参数。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'db-scripts: %s\n' "$*" >&2; exit 1; }
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-db-scripts.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
SCRIPTS=(check-migrations.sh migrate.sh backup-postgres.sh verify-backup.sh restore-postgres.sh psql.sh bootstrap.sh healthcheck.sh)

# --- 没有布局判定、没有容器分支（备份文件名 aegis-postgres-<时间> 不算） ---
for script in "${SCRIPTS[@]}"; do
  if grep -nE 'PANDORA_DB_LAYOUT|pandora_db_layout|DB_LAYOUT|docker|aegis-postgres([^-]|$)|runuser -u' "$DEPLOY/$script"; then
    fail "$script still has a database-layout or container branch"
  fi
done
[ ! -e "$DEPLOY/legacy-privilege-repair.sql" ] || fail 'legacy-privilege-repair.sql is back'

# --- migrate.sh 没有迁移 DSN、经批准回退到本机：以 postgres 超级用户连，口令只在环境里 ---
mkdir -p "$T/bin" "$T/migrations"
printf '%s\n' '-- +goose Up' 'SELECT 1;' >"$T/migrations/00001_a.sql"
cat >"$T/bin/goose" <<'MOCK'
#!/usr/bin/env bash
printf 'DSN=%s PW=%s\n' "$GOOSE_DBSTRING" "${PGPASSWORD:-}" >"$(dirname "$0")/../goose.env"
MOCK
chmod 0755 "$T/bin/goose"
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5432\nPOSTGRES_SUPER_PASSWORD=super-pw\n' >"$T/native.env"
# 老 .env 里留着的布局键不起作用
{ cat "$T/native.env"; printf 'PANDORA_DB_LAYOUT=docker\n'; } >"$T/leftover.env"
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5432\n' >"$T/nosuper.env"
fallback() {
  rm -f "$T/goose.env"
  AEGIS_ENV_FILE="$T/$1.env" AEGIS_MIGRATIONS_DIR="$T/migrations" GOOSE_BIN="$T/bin/goose" \
    PANDORA_LOCAL_MIGRATION_APPROVED=yes bash "$DEPLOY/migrate.sh" version >"$T/fallback.out" 2>&1
}
for env in native leftover; do
  fallback "$env" || fail "migrate.sh version failed for the $env .env: $(cat "$T/fallback.out")"
  [ "$(cat "$T/goose.env")" = 'DSN=host=127.0.0.1 port=5432 user=postgres dbname=aegis sslmode=disable PW=super-pw' ] \
    || fail "$env fallback: $(cat "$T/goose.env")"
done
if fallback nosuper; then fail 'local fallback without POSTGRES_SUPER_PASSWORD was accepted'; fi
grep -Fq 'POSTGRES_SUPER_PASSWORD is required for approved local migration' "$T/fallback.out" \
  || fail "nosuper fallback message: $(cat "$T/fallback.out")"
[ ! -e "$T/goose.env" ] || fail 'goose ran without a superuser password'

# --- 备份、校验、恢复三件套：连库一律经 pandora_pg（三份逐字一致），口令不导出给整个脚本 ---
extract_fn() { awk -v fn="$2" '$0 == fn "() {" { p = 1 } p { print } p && /^}$/ { exit }' "$DEPLOY/$1"; }
TRIO=(backup-postgres.sh verify-backup.sh restore-postgres.sh)
for fn in pandora_pg pandora_pg_require pandora_pg_require_login; do
  ref="$(extract_fn "${TRIO[0]}" "$fn")"
  [ -n "$ref" ] || fail "${TRIO[0]} has no $fn"
  for script in "${TRIO[@]}"; do
    [ "$(extract_fn "$script" "$fn")" = "$ref" ] || fail "$script's $fn differs from ${TRIO[0]}'s"
  done
done
for script in "${TRIO[@]}"; do
  if grep -Eq 'export PGPASSWORD|^PGPASSWORD=' "$DEPLOY/$script"; then
    fail "$script exports PGPASSWORD for the whole script"
  fi
done

# pandora_pg 的行为：本机客户端、回环、postgres 超级用户，口令只在这个客户端的环境里
cat >"$T/bin/pg_dump" <<'MOCK'
#!/usr/bin/env bash
printf 'pg_dump %s | PGHOST=%s PGPORT=%s PGUSER=%s PGPASSWORD=%s PGSSLMODE=%s\n' "$*" \
  "${PGHOST:-}" "${PGPORT:-}" "${PGUSER:-}" "${PGPASSWORD:-}" "${PGSSLMODE:-}" >"$(dirname "$0")/../pg.calls"
MOCK
chmod 0755 "$T/bin/pg_dump"
eval "$(extract_fn backup-postgres.sh pandora_pg)"
(
  PATH="$T/bin:$PATH"; POSTGRES_SUPER_PASSWORD=super-pw POSTGRES_PORT=5434 POSTGRES_PASSWORD=owner-pw
  pandora_pg pg_dump -d aegis --format=custom
)
[ "$(cat "$T/pg.calls")" = 'pg_dump -d aegis --format=custom | PGHOST=127.0.0.1 PGPORT=5434 PGUSER=postgres PGPASSWORD=super-pw PGSSLMODE=disable' ] \
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

# --- restore-postgres.sh 的恢复步骤：抽出函数、换上桩跑真调用 -----------------------------------
# （整个脚本要 Linux root 与 /proc 绑定的 .env，本机跑不了；这几个函数就是它在正式库上做的事）
for fn in archive_role_plan ensure_restore_roles require_db_owner_role create_target_db restore_into_target; do
  eval "$(extract_fn restore-postgres.sh "$fn")"
  declare -F "$fn" >/dev/null || fail "restore-postgres.sh has no $fn"
done
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
# 目标库：属主 POSTGRES_USER、UTF8；正式库关连接闸门
POSTGRES_USER=aegis POSTGRES_DB=aegis target_db=aegis
create_target_db
grep -qx 'createdb --template=template0 --encoding=UTF8 --connection-limit=0 --owner=aegis aegis' "$T/pg.calls" \
  || fail "production createdb: $(cat "$T/pg.calls")"
: >"$T/pg.calls"; target_db=aegis_recovery
create_target_db
grep -qx 'createdb --template=template0 --encoding=UTF8 --owner=aegis aegis_recovery' "$T/pg.calls" \
  || fail "recovery createdb: $(cat "$T/pg.calls")"
# 照原样恢复属主与权限
: >"$T/pg.calls"; AEGIS_BACKUP_AGE_IDENTITY=/k archive=/a
restore_into_target
grep -qx 'pg_restore -d aegis_recovery --exit-on-error' "$T/pg.calls" || fail "restore drops owners or privileges: $(cat "$T/pg.calls")"
grep -qx 'ARCHIVE' "$T/restore.stdin" || fail 'restore did not receive the decrypted archive'
# 备份里要的角色：属主、默认权限的主人、被授权者；PUBLIC 不算
plan="$(printf '%s\n' \
  'ALTER SCHEMA app OWNER TO postgres;' \
  'ALTER TABLE public.users OWNER TO postgres;' \
  'ALTER FUNCTION app.bind_idem(uuid) OWNER TO aegis_idempotency_owner;' \
  'ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public GRANT SELECT ON TABLES TO aegis_app;' \
  'ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public REVOKE ALL ON FUNCTIONS FROM PUBLIC;' \
  'GRANT SELECT ON TABLE public.users TO aegis_app;' \
  'GRANT USAGE ON SCHEMA app TO aegis_idempotency_owner WITH GRANT OPTION;' \
  'GRANT USAGE ON SCHEMA public TO PUBLIC;' \
  'REVOKE ALL ON FUNCTION app.bind_idem(uuid) FROM PUBLIC;' | archive_role_plan)"
[ "$plan" = "$(printf '%s\n' aegis_app aegis_idempotency_owner postgres)" ] || fail "role plan: $plan"
# 缺的专用角色建成 NOLOGIN、无特权；已有的不重建
: >"$T/pg.calls"; printf 'postgres\naegis_app\n' >"$T/roles"
ensure_restore_roles "$plan" 2>/dev/null || fail "ensure_restore_roles failed: $(cat "$T/pg.calls")"
grep -qx 'psql -X -d postgres -v ON_ERROR_STOP=1 -c CREATE ROLE "aegis_idempotency_owner" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS' "$T/pg.calls" \
  || fail "owner role not created safely: $(cat "$T/pg.calls")"
[ "$(grep -c 'CREATE ROLE' "$T/pg.calls")" -eq 1 ] || fail "an existing role was recreated: $(cat "$T/pg.calls")"
# 认不出的角色（含库属主 aegis：它不该出现在归档里）：停下，什么都不建
for intruder in intruder aegis '"Quoted"'; do
  : >"$T/pg.calls"
  if ( ensure_restore_roles "$(printf '%s\n' aegis_app "$intruder")" ) >/dev/null 2>&1; then fail "role $intruder was accepted"; fi
  if grep -q 'CREATE ROLE' "$T/pg.calls"; then fail "a role was created before refusing $intruder"; fi
done
# 目标库属主要先在（createdb 在删掉旧库之后才跑）
printf 'aegis\n' >"$T/roles"; POSTGRES_USER=aegis
( require_db_owner_role ) || fail 'an existing database owner role was refused'
: >"$T/roles"
if ( require_db_owner_role ) 2>/dev/null; then fail 'a missing database owner role was accepted'; fi
unset -f pandora_pg age die
# 主流程：核完整性 → 备好角色 → 核库属主 → 与正式恢复同参数的演练 → 进正式库保护 → 恢复 → 开闸门
awk '/^"\$PWD\/verify-backup.sh" "\$archive"$/ { v = NR } /^ensure_restore_roles "\$role_plan"$/ { e = NR }
     /^require_db_owner_role$/ { d = NR } /^AEGIS_VERIFY_RESTORE=owners "\$PWD\/verify-backup.sh"/ { o = NR }
     /^begin_production_guard$/ { g = NR } /^restore_into_target$/ { r = NR } /^commit_production_guard$/ { c = NR }
     END { exit !(v && e && d && o && g && r && c && v < e && e < d && d < o && o < g && g < r && r < c) }' "$DEPLOY/restore-postgres.sh" \
  || fail 'restore-postgres.sh steps are out of order'
if grep -Eq 'REASSIGN|reassign_source_migrator|legacy_repair|repair_legacy_privileges|acl (yes|no)|migrator' "$DEPLOY/restore-postgres.sh"; then
  fail 'restore-postgres.sh still carries the cross-layout owner hand-over or the old-format privilege repair'
fi
# 库级设置不在归档里：收尾提示跑 bootstrap.sh，并且在 commit 之后
awk '/^commit_production_guard$/ { c = NR } /NOTE run \.\/bootstrap\.sh before starting the services/ { b = NR }
     END { exit !(c && b && c < b) }' "$DEPLOY/restore-postgres.sh" || fail 'restore-postgres.sh does not tell the operator to run bootstrap.sh'
if grep -q '^AEGIS_VERIFY_RESTORE=1 ' "$DEPLOY/restore-postgres.sh"; then fail 'the restore rehearsal drops owners'; fi
grep -Fq '[ "$AEGIS_VERIFY_RESTORE" = owners ] || restore_opts+=(--no-owner --no-privileges)' "$DEPLOY/verify-backup.sh" \
  || fail 'verify-backup.sh has no owner-preserving rehearsal'
if grep -Eq 'pg_restore .*--no-owner' "$DEPLOY/restore-postgres.sh"; then fail 'restore-postgres.sh restores with --no-owner'; fi
if grep -Eq -- '--no-owner|--no-acl' "$DEPLOY/backup-postgres.sh"; then fail 'backup-postgres.sh drops owners or privileges'; fi

# --- psql.sh 与 bootstrap.sh：在临时 deploy/ 里真跑一遍，客户端换成桩 ---
mkdir -p "$T/deploy"
cp "$DEPLOY/psql.sh" "$DEPLOY/bootstrap.sh" "$DEPLOY/configure-app-role.sql" "$T/deploy/"
app_pw="$(printf 'a%.0s' $(seq 1 40))"
# .env 里同时有库属主口令与留下的布局键：都不该影响连法，也不该进客户端的环境
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5434\nPOSTGRES_SUPER_PASSWORD=super-pw\nAEGIS_DB_APP_PASSWORD=%s\nPANDORA_DB_LAYOUT=docker\n' "$app_pw" >"$T/deploy/.env"
cat >"$T/bin/psql" <<'MOCK'
#!/usr/bin/env bash
root="$(dirname "$0")/.."
printf 'psql %s | PGPASSWORD=%s PGSSLMODE=%s APP=%s OWNER=%s SUPER=%s\n' "$*" "${PGPASSWORD:-}" "${PGSSLMODE:-}" \
  "${AEGIS_DB_APP_PASSWORD:+set}" "${POSTGRES_PASSWORD:+leaked}" "${POSTGRES_SUPER_PASSWORD:+leaked}" >"$root/pg.calls"
[ -t 0 ] || cat >"$root/pg.stdin"
MOCK
chmod 0755 "$T/bin/psql"
PATH="$T/bin:$PATH" bash "$T/deploy/psql.sh" -X -tAc 'SELECT 1' </dev/null
[ "$(cat "$T/pg.calls")" = 'psql -h 127.0.0.1 -p 5434 -U postgres -d aegis -X -tAc SELECT 1 | PGPASSWORD=super-pw PGSSLMODE=disable APP= OWNER= SUPER=' ] \
  || fail "psql.sh: $(cat "$T/pg.calls")"
PATH="$T/bin:$PATH" bash "$T/deploy/bootstrap.sh" >/dev/null
[ "$(cat "$T/pg.calls")" = 'psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p 5434 -U postgres -d aegis | PGPASSWORD=super-pw PGSSLMODE=disable APP=set OWNER= SUPER=' ] \
  || fail "bootstrap.sh: $(cat "$T/pg.calls")"
cmp -s "$T/pg.stdin" "$DEPLOY/configure-app-role.sql" || fail 'bootstrap.sh did not feed configure-app-role.sql'
# 没有超级用户口令：两个都拒绝，不调客户端
sed -i.bak '/^POSTGRES_SUPER_PASSWORD=/d' "$T/deploy/.env"
for script in psql.sh bootstrap.sh; do
  rm -f "$T/pg.calls"
  if PATH="$T/bin:$PATH" bash "$T/deploy/$script" </dev/null >/dev/null 2>&1; then fail "$script ran without POSTGRES_SUPER_PASSWORD"; fi
  [ ! -e "$T/pg.calls" ] || fail "$script called psql without POSTGRES_SUPER_PASSWORD"
done

# --- migrate.sh：迁移 DSN 里的超级用户口令不进任何命令行参数（psql、goose、env） ---------------------
cat >"$T/bin/psql-argv" <<'MOCK'
#!/usr/bin/env bash
printf 'psql %s | PGPASSWORD=%s\n' "$*" "${PGPASSWORD:-}" >>"$(dirname "$0")/../argv.log"
MOCK
cat >"$T/bin/goose" <<'MOCK'
#!/usr/bin/env bash
printf 'goose %s | DSN=%s\n' "$*" "${GOOSE_DBSTRING:-}" >>"$(dirname "$0")/../argv.log"
MOCK
real_env="$(command -v env)"
printf '#!/usr/bin/env bash\nprintf "env %%s\\n" "$*" >>"%s/argv.log"\nexec "%s" "$@"\n' "$T" "$real_env" >"$T/bin/env"
chmod 0755 "$T/bin/psql-argv" "$T/bin/goose" "$T/bin/env"
printf 'AEGIS_MIGRATION_DATABASE_URL=postgres://postgres:dsn%%2Dsuper%%2Dfixture@127.0.0.1:5432/aegis?sslmode=disable\n' >"$T/dsn.env"
: >"$T/argv.log"
PATH="$T/bin:$PATH" AEGIS_ENV_FILE="$T/dsn.env" AEGIS_MIGRATIONS_DIR="$T/migrations" GOOSE_BIN="$T/bin/goose" \
  PANDORA_PSQL_BIN="$T/bin/psql-argv" bash "$DEPLOY/migrate.sh" check-indexes >/dev/null 2>&1 \
  || fail "migrate.sh check-indexes failed: $(cat "$T/argv.log")"
PATH="$T/bin:$PATH" AEGIS_ENV_FILE="$T/dsn.env" AEGIS_MIGRATIONS_DIR="$T/migrations" GOOSE_BIN="$T/bin/goose" \
  bash "$DEPLOY/migrate.sh" version >/dev/null 2>&1 || fail "migrate.sh version failed: $(cat "$T/argv.log")"
grep -Fq -- '-d postgres://postgres@127.0.0.1:5432/aegis?sslmode=disable -c' "$T/argv.log" && grep -q '| PGPASSWORD=dsn-super-fixture$' "$T/argv.log" \
  || fail "psql did not get the password through PGPASSWORD: $(cat "$T/argv.log")"
grep -q '^goose version | DSN=postgres://postgres:dsn%2Dsuper%2Dfixture@' "$T/argv.log" || fail "goose lost its DSN: $(cat "$T/argv.log")"
if sed 's/ | .*//' "$T/argv.log" | grep -Eq 'dsn(%2D|-)super'; then
  fail "the migration password reached a command line: $(sed 's/ | .*//' "$T/argv.log" | grep -E 'dsn(%2D|-)super')"
fi

printf 'db-scripts mock: PASS\n'
