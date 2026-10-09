#!/usr/bin/env bash
# 数据库布局判定（pandora_db_layout）在每个运维脚本里各带一份：这些脚本有的以 root 在加固的 systemd 单元里跑、
# 只信任自己（备份、校验、恢复），不 source 共用文件。这里核对每一份逐字一致——改判定规则要一起改。
# 规则：.env 的 PANDORA_DB_LAYOUT=native|docker 为准；没有这一键时，有 POSTGRES_SUPER_PASSWORD（只有
# install-native.sh 写它）判为 native，否则 docker；别的值拒绝。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'pg-layout: %s\n' "$*" >&2; exit 1; }
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-pg-layout.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
SCRIPTS=(check-migrations.sh migrate.sh backup-postgres.sh verify-backup.sh restore-postgres.sh psql.sh bootstrap.sh)

extract() { awk '/^pandora_db_layout\(\) \{$/ { p = 1 } p { print } p && /^}$/ { exit }' "$DEPLOY/$1"; }
reference="$(extract "${SCRIPTS[0]}")"
[ -n "$reference" ] || fail "${SCRIPTS[0]} has no pandora_db_layout"
for script in "${SCRIPTS[@]}"; do
  got="$(extract "$script")"
  [ -n "$got" ] || fail "$script has no pandora_db_layout"
  [ "$got" = "$reference" ] || fail "$script's pandora_db_layout differs from ${SCRIPTS[0]}'s"
done

# --- migrate.sh 没有迁移 DSN、经批准回退到本机时：直装以 postgres 超级用户连，docker 布局以 POSTGRES_USER ---
mkdir -p "$T/bin" "$T/migrations"
printf '%s\n' '-- +goose Up' 'SELECT 1;' >"$T/migrations/00001_a.sql"
cat >"$T/bin/goose" <<'MOCK'
#!/usr/bin/env bash
printf 'DSN=%s PW=%s\n' "$GOOSE_DBSTRING" "${PGPASSWORD:-}" >"$(dirname "$0")/../goose.env"
MOCK
chmod 0755 "$T/bin/goose"
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5432\nPOSTGRES_SUPER_PASSWORD=super-pw\n' >"$T/native.env"
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5433\n' >"$T/docker.env"
fallback() {
  AEGIS_ENV_FILE="$T/$1.env" AEGIS_MIGRATIONS_DIR="$T/migrations" GOOSE_BIN="$T/bin/goose" \
    PANDORA_LOCAL_MIGRATION_APPROVED=yes bash "$DEPLOY/migrate.sh" version >/dev/null 2>&1 \
    || fail "migrate.sh version failed for the $1 layout"
  cat "$T/goose.env"
}
[ "$(fallback native)" = 'DSN=host=127.0.0.1 port=5432 user=postgres dbname=aegis sslmode=disable PW=super-pw' ] \
  || fail "native fallback: $(cat "$T/goose.env")"
[ "$(fallback docker)" = 'DSN=host=127.0.0.1 port=5433 user=aegis dbname=aegis sslmode=disable PW=owner-pw' ] \
  || fail "docker fallback: $(cat "$T/goose.env")"

# --- 备份、校验、恢复三件套：连库一律经 pandora_pg（三份逐字一致），别处不许直接 docker exec ---
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
  n="$(grep -c 'docker exec' "$DEPLOY/$script")"
  [ "$n" -eq 1 ] || fail "$script runs docker exec outside pandora_pg ($n occurrences)"
  if grep -Eq 'export PGPASSWORD|PGPASSWORD="\$POSTGRES_PASSWORD" *$' "$DEPLOY/$script"; then
    fail "$script still exports PGPASSWORD for the whole script"
  fi
done

# pandora_pg 的行为：直装走本机客户端、回环、postgres 超级用户；docker 走容器、POSTGRES_USER。口令都只在环境里
cat >"$T/bin/pg_dump" <<'MOCK'
#!/usr/bin/env bash
printf 'pg_dump %s | PGHOST=%s PGPORT=%s PGUSER=%s PGPASSWORD=%s PGSSLMODE=%s\n' "$*" \
  "${PGHOST:-}" "${PGPORT:-}" "${PGUSER:-}" "${PGPASSWORD:-}" "${PGSSLMODE:-}" >"$(dirname "$0")/../pg.calls"
MOCK
cat >"$T/bin/docker" <<'MOCK'
#!/usr/bin/env bash
printf 'docker %s | PGPASSWORD=%s\n' "$*" "${PGPASSWORD:-}" >"$(dirname "$0")/../pg.calls"
MOCK
chmod 0755 "$T/bin/pg_dump" "$T/bin/docker"
eval "$(extract_fn backup-postgres.sh pandora_pg)"
(
  PATH="$T/bin:$PATH"; DB_LAYOUT=native POSTGRES_SUPER_PASSWORD=super-pw POSTGRES_PORT=5434 POSTGRES_PASSWORD=owner-pw
  pandora_pg pg_dump -d aegis --format=custom
)
[ "$(cat "$T/pg.calls")" = 'pg_dump -d aegis --format=custom | PGHOST=127.0.0.1 PGPORT=5434 PGUSER=postgres PGPASSWORD=super-pw PGSSLMODE=disable' ] \
  || fail "native pandora_pg: $(cat "$T/pg.calls")"
(
  PATH="$T/bin:$PATH"; DB_LAYOUT=docker POSTGRES_USER=aegis POSTGRES_PASSWORD=owner-pw
  pandora_pg pg_dump -d aegis --format=custom
)
[ "$(cat "$T/pg.calls")" = 'docker exec -i -e PGPASSWORD aegis-postgres pg_dump -U aegis -d aegis --format=custom | PGPASSWORD=owner-pw' ] \
  || fail "docker pandora_pg: $(cat "$T/pg.calls")"

# --- restore-postgres.sh 的恢复步骤：抽出函数、换上桩跑真调用 -----------------------------------
# （整个脚本要 Linux root 与 /proc 绑定的 .env，本机跑不了；这几个函数就是它在正式库上做的事）
for fn in archive_role_plan ensure_restore_roles create_target_db restore_into_target reassign_source_migrator \
    legacy_privilege_repair_sql repair_legacy_privileges; do
  eval "$(extract_fn restore-postgres.sh "$fn")"
  declare -F "$fn" >/dev/null || fail "restore-postgres.sh has no $fn"
done
die() { echo "die: $*" >&2; exit 1; }
: >"$T/pg.calls"
# pandora_pg 桩：记下调用；「角色在不在」按 $T/roles 答；current_user 按 $T/me 答
pandora_pg() {
  printf '%s\n' "$*" >>"$T/pg.calls"
  case "$*" in
    *'WHERE datdba ='*) cat "$T/owned-dbs" 2>/dev/null; return 0 ;;
    *"FROM pg_catalog.pg_roles WHERE rolname = '"*)
      local r; r="$(printf '%s' "$*" | sed -n "s/.*rolname = '\([a-z_]*\)'.*/\1/p")"
      if grep -qx "$r" "$T/roles"; then echo 1; fi
      return 0 ;;
    *'SELECT current_user'*) cat "$T/me"; return 0 ;;
    *'-d aegis_legacy -v ON_ERROR_STOP=1') cat >"$T/repair.stdin"; return 0 ;;
    'pg_restore -d '*) cat >"$T/restore.stdin"; return 0 ;;
  esac
  return 0
}
age() { printf 'ARCHIVE\n'; }
# 恢复的目标库：直装给 POSTGRES_USER 当属主、UTF8、正式库关连接闸门
DB_LAYOUT=native POSTGRES_USER=aegis POSTGRES_DB=aegis target_db=aegis
create_target_db
grep -qx 'createdb --template=template0 --encoding=UTF8 --connection-limit=0 --owner=aegis aegis' "$T/pg.calls" \
  || fail "native createdb: $(cat "$T/pg.calls")"
: >"$T/pg.calls"; DB_LAYOUT=docker target_db=aegis_recovery
create_target_db
grep -qx 'createdb --template=template0 --encoding=UTF8 aegis_recovery' "$T/pg.calls" || fail "docker createdb: $(cat "$T/pg.calls")"
# 照原样恢复属主与权限
: >"$T/pg.calls"; AEGIS_BACKUP_AGE_IDENTITY=/k archive=/a
restore_into_target
grep -qx 'pg_restore -d aegis_recovery --exit-on-error' "$T/pg.calls" || fail "restore still drops owners or privileges: $(cat "$T/pg.calls")"
grep -qx 'ARCHIVE' "$T/restore.stdin" || fail 'restore did not receive the decrypted archive'
# 备份里要的角色
plan="$(printf '%s\n' \
  'ALTER SCHEMA app OWNER TO postgres;' \
  'ALTER TABLE public.users OWNER TO postgres;' \
  'ALTER FUNCTION app.bind_idem(uuid) OWNER TO aegis_idempotency_owner;' \
  'ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public GRANT SELECT ON TABLES TO aegis_app;' \
  'GRANT SELECT ON TABLE public.users TO aegis_app;' \
  'GRANT USAGE ON SCHEMA app TO aegis_idempotency_owner WITH GRANT OPTION;' \
  'GRANT USAGE ON SCHEMA public TO PUBLIC;' | archive_role_plan)"
want_plan="$(printf '%s\n' 'acl yes' 'migrator postgres' 'role aegis_app' 'role aegis_idempotency_owner' 'role postgres' | sort)"
[ "$plan" = "$want_plan" ] || fail "role plan: $plan"
[ "$(printf 'ALTER SCHEMA app OWNER TO aegis;\n' | archive_role_plan | grep '^acl')" = 'acl no' ] || fail 'old backups without GRANTs not detected'
# docker 布局恢复直装的备份：postgres 不存在，临时建；专用角色缺了照样建（NOLOGIN、无特权）
: >"$T/pg.calls"; printf 'aegis\naegis_app\n' >"$T/roles"; POSTGRES_USER=aegis
created="$(ensure_restore_roles "$plan" 2>/dev/null)" || fail "ensure_restore_roles failed: $(cat "$T/pg.calls")"
[ "$created" = postgres ] || fail "temporary migrator not reported: '$created'"
grep -qx 'psql -X -d postgres -v ON_ERROR_STOP=1 -c CREATE ROLE "aegis_idempotency_owner" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS' "$T/pg.calls" \
  || fail "owner role not created safely: $(cat "$T/pg.calls")"
if grep -q 'CREATE ROLE "aegis_app"' "$T/pg.calls"; then fail 'an existing role was recreated'; fi
# 认不出的角色：停下，不建
if ( ensure_restore_roles "$(printf 'role intruder\n')" ) >/dev/null 2>&1; then fail 'an unknown role was accepted'; fi
# 迁移角色换成本机的，临时角色删掉
: >"$T/pg.calls"; echo aegis >"$T/me"; target_db=aegis; : >"$T/owned-dbs"
reassign_source_migrator postgres postgres 2>/dev/null
grep -qx 'psql -X -d aegis -v ON_ERROR_STOP=1 -c REASSIGN OWNED BY "postgres" TO "aegis" -c ALTER DATABASE "aegis" OWNER TO "aegis"' "$T/pg.calls" \
  || fail "reassign: $(cat "$T/pg.calls")"
grep -qx 'psql -X -d postgres -v ON_ERROR_STOP=1 -c DROP ROLE "postgres"' "$T/pg.calls" || fail 'temporary migrator role not dropped'
# 同一种布局：迁移角色就是本机的，什么都不改
: >"$T/pg.calls"; echo postgres >"$T/me"
reassign_source_migrator postgres ''
if grep -q 'REASSIGN' "$T/pg.calls"; then fail 'reassigned although the migrator is the same'; fi
# 直装机上把 docker 的备份恢复到别名库：REASSIGN 会顺带改 aegis 名下别的库（正式库、aegis_stale_*），换完改回
: >"$T/pg.calls"; echo postgres >"$T/me"; target_db=aegis_check; printf 'aegis\naegis_stale_20261009\n' >"$T/owned-dbs"
reassign_source_migrator aegis '' 2>/dev/null
for db in aegis aegis_stale_20261009; do
  grep -qx "psql -X -d postgres -v ON_ERROR_STOP=1 -c ALTER DATABASE \"$db\" OWNER TO \"aegis\"" "$T/pg.calls" \
    || fail "database $db not given back to aegis after REASSIGN: $(cat "$T/pg.calls")"
done
awk '/REASSIGN OWNED/ { r = NR } /ALTER DATABASE "aegis" OWNER TO "aegis"/ { a = NR } END { exit !(r && a && r < a) }' "$T/pg.calls" \
  || fail 'owners given back before the REASSIGN'
# 旧格式备份（没有 GRANT）：把 00038/00039 的函数属主与授权补回迁移建出来的样子
: >"$T/pg.calls"; target_db=aegis_legacy
repair_legacy_privileges 2>/dev/null
grep -qx 'psql -X -q -d aegis_legacy -v ON_ERROR_STOP=1' "$T/pg.calls" || fail "repair not applied to the target: $(cat "$T/pg.calls")"
cmp -s "$T/repair.stdin" <(legacy_privilege_repair_sql) || fail 'repair SQL not fed to psql'
# 修复 SQL 里的函数签名必须逐字对上迁移里 OWNER TO aegis_idempotency_owner 的那几个函数
mig_sigs="$(awk '/^ALTER FUNCTION app\./ { buf = "" } /^ALTER FUNCTION app\./ || buf != "" { buf = buf $0 }
    buf != "" && /;[[:space:]]*$/ { if (buf ~ /OWNER TO aegis_idempotency_owner;/) print buf; buf = "" }' \
  "$DEPLOY"/../migrations/*.sql | sed -E 's/^ALTER FUNCTION //; s/\) OWNER TO.*$/)/; s/[[:space:]]+//g' | sort -u)"
[ "$(printf '%s\n' "$mig_sigs" | grep -c .)" -ge 2 ] || fail "could not read the owner-role functions from the migrations: $mig_sigs"
while IFS= read -r sig; do
  grep -Fq "to_regprocedure('$sig')" <<<"$(legacy_privilege_repair_sql)" || fail "repair SQL misses $sig"
done <<<"$mig_sigs"
for v in v_bind v_done; do
  grep -Fq "format('ALTER FUNCTION %s OWNER TO aegis_idempotency_owner', $v)" <<<"$(legacy_privilege_repair_sql)" \
    || fail "repair SQL does not give $v back to aegis_idempotency_owner"
  grep -Fq "format('REVOKE ALL ON FUNCTION %s FROM PUBLIC, aegis_app', $v)" <<<"$(legacy_privilege_repair_sql)" \
    || fail "repair SQL does not take PUBLIC execute away from $v"
done
grep -Fq 'FROM PUBLIC, aegis_app' <<<"$(legacy_privilege_repair_sql)" || fail 'repair SQL does not take PUBLIC execute away'
unset -f pandora_pg age die
# 主流程：核完整性 → 备好角色 → 与正式恢复同参数的演练 → 进正式库保护 → 恢复 → 换迁移角色 → 旧备份补权限 → 开闸门
awk '/^"\$PWD\/verify-backup.sh" "\$archive"$/ { v = NR } /^created_migrator="\$\(ensure_restore_roles/ { e = NR }
     /^AEGIS_VERIFY_RESTORE=owners "\$PWD\/verify-backup.sh"/ { o = NR } /^begin_production_guard$/ { g = NR } /^restore_into_target$/ { r = NR }
     /^reassign_source_migrator / { m = NR } /^  repair_legacy_privileges$/ { l = NR } /^commit_production_guard$/ { c = NR }
     END { exit !(v && e && o && g && r && m && l && c && v < e && e < o && o < g && g < r && r < m && m < l && l < c) }' "$DEPLOY/restore-postgres.sh" \
  || fail 'restore-postgres.sh steps are out of order'
if grep -q '^AEGIS_VERIFY_RESTORE=1 ' "$DEPLOY/restore-postgres.sh"; then fail 'the restore rehearsal still drops owners'; fi
grep -Fq '[ "$AEGIS_VERIFY_RESTORE" = owners ] || restore_opts+=(--no-owner --no-privileges)' "$DEPLOY/verify-backup.sh" \
  || fail 'verify-backup.sh has no owner-preserving rehearsal'
if grep -Eq 'pg_restore .*--no-owner' "$DEPLOY/restore-postgres.sh"; then fail 'restore-postgres.sh still restores with --no-owner'; fi
if grep -Eq -- '--no-owner|--no-acl' "$DEPLOY/backup-postgres.sh"; then fail 'backup-postgres.sh still drops owners or privileges'; fi

# --- psql.sh 与 bootstrap.sh：在临时 deploy/ 里真跑一遍，客户端换成桩 ---
for layout in native docker; do
  mkdir -p "$T/$layout"
  cp "$DEPLOY/psql.sh" "$DEPLOY/bootstrap.sh" "$DEPLOY/configure-app-role.sql" "$T/$layout/"
done
app_pw="$(printf 'a%.0s' $(seq 1 40))"
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5434\nPOSTGRES_SUPER_PASSWORD=super-pw\nAEGIS_DB_APP_PASSWORD=%s\n' "$app_pw" >"$T/native/.env"
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5433\nAEGIS_DB_APP_PASSWORD=%s\n' "$app_pw" >"$T/docker/.env"
cat >"$T/bin/psql" <<'MOCK'
#!/usr/bin/env bash
root="$(dirname "$0")/.."
printf 'psql %s | PGPASSWORD=%s APP=%s OWNER=%s\n' "$*" "${PGPASSWORD:-}" "${AEGIS_DB_APP_PASSWORD:+set}" "${POSTGRES_PASSWORD:+leaked}" >"$root/pg.calls"
[ -t 0 ] || cat >"$root/pg.stdin"
MOCK
cat >"$T/bin/docker" <<'MOCK'
#!/usr/bin/env bash
root="$(dirname "$0")/.."
printf 'docker %s | PGPASSWORD=%s APP=%s\n' "$*" "${PGPASSWORD:-}" "${AEGIS_DB_APP_PASSWORD:+set}" >"$root/pg.calls"
[ -t 0 ] || cat >"$root/pg.stdin"
MOCK
chmod 0755 "$T/bin/psql" "$T/bin/docker"
PATH="$T/bin:$PATH" bash "$T/native/psql.sh" -X -tAc 'SELECT 1' </dev/null
[ "$(cat "$T/pg.calls")" = 'psql -h 127.0.0.1 -p 5434 -U postgres -d aegis -X -tAc SELECT 1 | PGPASSWORD=super-pw APP= OWNER=' ] \
  || fail "native psql.sh: $(cat "$T/pg.calls")"
PATH="$T/bin:$PATH" bash "$T/docker/psql.sh" -X -tAc 'SELECT 1' </dev/null
[ "$(cat "$T/pg.calls")" = 'docker exec -i -e PGPASSWORD aegis-postgres psql -U aegis -d aegis -X -tAc SELECT 1 | PGPASSWORD=owner-pw APP=' ] \
  || fail "docker psql.sh: $(cat "$T/pg.calls")"
PATH="$T/bin:$PATH" bash "$T/native/bootstrap.sh" >/dev/null
[ "$(cat "$T/pg.calls")" = 'psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p 5434 -U postgres -d aegis | PGPASSWORD=super-pw APP=set OWNER=' ] \
  || fail "native bootstrap.sh: $(cat "$T/pg.calls")"
cmp -s "$T/pg.stdin" "$DEPLOY/configure-app-role.sql" || fail 'native bootstrap.sh did not feed configure-app-role.sql'
PATH="$T/bin:$PATH" bash "$T/docker/bootstrap.sh" >/dev/null
[ "$(cat "$T/pg.calls")" = 'docker exec -i -e PGPASSWORD -e AEGIS_DB_APP_PASSWORD aegis-postgres psql -X -v ON_ERROR_STOP=1 -U aegis -d aegis | PGPASSWORD=owner-pw APP=set' ] \
  || fail "docker bootstrap.sh: $(cat "$T/pg.calls")"
cmp -s "$T/pg.stdin" "$DEPLOY/configure-app-role.sql" || fail 'docker bootstrap.sh did not feed configure-app-role.sql'

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
rm -f "$T/bin/env" "$T/bin/goose"

# 判定本身
eval "$reference"
layout() { ( unset PANDORA_DB_LAYOUT POSTGRES_SUPER_PASSWORD; eval "$1"; pandora_db_layout ); }
[ "$(layout '')" = docker ] || fail 'empty env is not docker'
[ "$(layout 'POSTGRES_SUPER_PASSWORD=x')" = native ] || fail 'a native .env without the key is not native'
[ "$(layout 'PANDORA_DB_LAYOUT=docker POSTGRES_SUPER_PASSWORD=x')" = docker ] || fail 'explicit docker is not honoured'
[ "$(layout 'PANDORA_DB_LAYOUT=native')" = native ] || fail 'explicit native is not honoured'
if layout 'PANDORA_DB_LAYOUT=k8s' >/dev/null; then fail 'an unknown layout was accepted'; fi

printf 'pg-layout mock: PASS\n'
