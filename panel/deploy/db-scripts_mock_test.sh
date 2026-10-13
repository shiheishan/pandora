#!/usr/bin/env bash
# 连库的运维脚本（check-migrations.sh、migrate.sh、psql.sh、bootstrap.sh）的桩测试，不需要数据库、root 或 systemd：
#   - 只有一种连法：本机客户端经 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户（POSTGRES_SUPER_PASSWORD）连，
#     口令只经环境变量。没有布局判定、没有容器分支；老 .env 里留着 PANDORA_DB_LAYOUT 也照常走本机；
#   （备份三件套与 healthcheck.sh 的同类检查在 backup-scripts_mock_test.sh）
#   - migrate.sh：经批准回退到本机时以 postgres 连；迁移 DSN 里的口令不进任何命令行参数。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'db-scripts: %s\n' "$*" >&2; exit 1; }
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-db-scripts.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
SCRIPTS=(check-migrations.sh migrate.sh psql.sh bootstrap.sh)

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

# --- psql.sh 与 bootstrap.sh：在临时 deploy/ 里真跑一遍，客户端换成桩 ---
mkdir -p "$T/deploy"
cp "$DEPLOY/psql.sh" "$DEPLOY/bootstrap.sh" "$DEPLOY/configure-app-role.sql" "$T/deploy/"
app_pw="$(printf 'a%.0s' $(seq 1 40))"
# .env 里同时有库属主口令与留下的布局键：都不该影响连法，也不该进客户端的环境
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5434\nPOSTGRES_SUPER_PASSWORD=super-pw\nAEGIS_DB_APP_PASSWORD=%s\nPANDORA_DB_LAYOUT=docker\n' "$app_pw" >"$T/deploy/.env"
cat >"$T/bin/psql" <<'MOCK'
#!/usr/bin/env bash
root="$(dirname "$0")/.."
printf 'psql %s | PGPASSWORD=%s PGSSLMODE=%s CT=%s APP=%s OWNER=%s SUPER=%s\n' "$*" "${PGPASSWORD:-}" "${PGSSLMODE:-}" "${PGCONNECT_TIMEOUT:-}" \
  "${AEGIS_DB_APP_PASSWORD:+set}" "${POSTGRES_PASSWORD:+leaked}" "${POSTGRES_SUPER_PASSWORD:+leaked}" >"$root/pg.calls"
[ -t 0 ] || cat >"$root/pg.stdin"
MOCK
chmod 0755 "$T/bin/psql"
PATH="$T/bin:$PATH" bash "$T/deploy/psql.sh" -X -tAc 'SELECT 1' </dev/null
[ "$(cat "$T/pg.calls")" = 'psql -h 127.0.0.1 -p 5434 -U postgres -d aegis -X -tAc SELECT 1 | PGPASSWORD=super-pw PGSSLMODE=disable CT=5 APP= OWNER= SUPER=' ] \
  || fail "psql.sh: $(cat "$T/pg.calls")"
# 调用方给了自己的连接超时就用它的（巡检可以给更短的）
PATH="$T/bin:$PATH" PGCONNECT_TIMEOUT=2 bash "$T/deploy/psql.sh" -X -tAc 'SELECT 1' </dev/null
grep -q ' CT=2 ' "$T/pg.calls" || fail "psql.sh ignored the caller's PGCONNECT_TIMEOUT: $(cat "$T/pg.calls")"
PATH="$T/bin:$PATH" bash "$T/deploy/bootstrap.sh" >/dev/null
[ "$(cat "$T/pg.calls")" = 'psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p 5434 -U postgres -d aegis | PGPASSWORD=super-pw PGSSLMODE=disable CT= APP=set OWNER= SUPER=' ] \
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
