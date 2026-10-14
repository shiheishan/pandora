#!/usr/bin/env bash
# 迁移前建库与角色（roles），迁移后收敛运行角色权限（grants）。
# 口令只经环境变量交给 psql（\getenv），不进命令行参数，也不打印。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

ETC="${PANDORA_ETC_DIR:-/etc/pandora}"
DEPLOY_ENV="$ETC/deploy.env"
SECRETS_ENV="$ETC/secrets.env"
CONFIGURE_SQL="$SCRIPT_DIR/configure-app-role.sql"

usage() {
  echo "用法: $(basename "$0") roles|grants" >&2
  exit 2
}

die() {
  echo "bootstrap: $*" >&2
  exit 1
}

# 从 KEY=VALUE 文件读单个键（最后一次出现为准），不 source
env_file_value() {
  local file="$1" key="$2"
  awk -F= -v k="$key" '$1 == k { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$file"
}

validate_password() {
  local label="$1" value="$2"
  if [[ ! "$value" =~ ^[A-Za-z0-9_-]{32,}$ ]]; then
    die "${label} 不合规：须至少 32 个 URL 安全字符（字母、数字、_、-），且不会打印其值"
  fi
}

parse_dsn_password() {
  local label="$1" url="$2" expected_role="$3"
  local role password
  if [[ ! "$url" =~ ^postgres://([^:@/]+):([^@]+)@127\.0\.0\.1:([0-9]+)/([^?]+)\?sslmode=disable$ ]]; then
    die "${label} 格式须为 postgres://<角色>:<口令>@127.0.0.1:<端口>/<库>?sslmode=disable"
  fi
  role="${BASH_REMATCH[1]}"
  password="${BASH_REMATCH[2]}"
  if [[ "$role" != "$expected_role" ]]; then
    die "${label} 里的数据库角色必须是 ${expected_role}"
  fi
  validate_password "$label 口令" "$password"
  printf '%s' "$password"
}

run_psql() {
  local db="$1"
  (
    cd /
    runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -p "$POSTGRES_PORT" -d "$db"
  )
}

[[ "${1:-}" == roles || "${1:-}" == grants ]] || usage
phase="$1"

if [[ "$(id -u)" -ne 0 ]]; then
  die "须以 root 运行"
fi

[[ -r "$DEPLOY_ENV" ]] || die "读不了 $DEPLOY_ENV（用 sudo 或检查 PANDORA_ETC_DIR）"
[[ -r "$SECRETS_ENV" ]] || die "读不了 $SECRETS_ENV（用 sudo 或检查 PANDORA_ETC_DIR）"

POSTGRES_PORT="$(env_file_value "$DEPLOY_ENV" POSTGRES_PORT)"
POSTGRES_DB="$(env_file_value "$DEPLOY_ENV" POSTGRES_DB)"
POSTGRES_SUPER_PASSWORD="$(env_file_value "$DEPLOY_ENV" POSTGRES_SUPER_PASSWORD)"
MIGRATION_URL="$(env_file_value "$DEPLOY_ENV" PANDORA_DATABASE_MIGRATION_URL)"
APP_URL="$(env_file_value "$SECRETS_ENV" PANDORA_DATABASE_URL)"

[[ -n "$POSTGRES_PORT" && "$POSTGRES_PORT" =~ ^[0-9]+$ ]] || die "POSTGRES_PORT 必须是数字"
[[ "$POSTGRES_DB" == pandora ]] || die "POSTGRES_DB 必须是 pandora"
validate_password "POSTGRES_SUPER_PASSWORD" "$POSTGRES_SUPER_PASSWORD"

OWNER_PASSWORD="$(parse_dsn_password "PANDORA_DATABASE_MIGRATION_URL" "$MIGRATION_URL" pandora_owner)"
APP_PASSWORD="$(parse_dsn_password "PANDORA_DATABASE_URL" "$APP_URL" pandora_app)"

if [[ "$phase" == roles ]]; then
  if ! PANDORA_OWNER_PASSWORD="$OWNER_PASSWORD" \
       PANDORA_APP_PASSWORD="$APP_PASSWORD" \
       PANDORA_SUPER_PASSWORD="$POSTGRES_SUPER_PASSWORD" \
       run_psql postgres <<'SQL'
\getenv owner_password PANDORA_OWNER_PASSWORD
\getenv app_password PANDORA_APP_PASSWORD
\getenv super_password PANDORA_SUPER_PASSWORD
SELECT 'CREATE ROLE pandora_owner LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'pandora_owner') \gexec
SELECT 'CREATE ROLE pandora_app LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'pandora_app') \gexec
ALTER ROLE pandora_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD :'owner_password';
ALTER ROLE pandora_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT PASSWORD :'app_password';
ALTER ROLE postgres PASSWORD :'super_password';
SELECT 'CREATE DATABASE pandora OWNER pandora_owner TEMPLATE template0 ENCODING ''UTF8''' WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_database WHERE datname = 'pandora') \gexec
ALTER DATABASE pandora OWNER TO pandora_owner;
GRANT SET ON PARAMETER session_replication_role TO pandora_owner;
SQL
  then
    die "roles 阶段：psql 执行失败"
  fi

  if ! run_psql pandora <<'SQL'
ALTER SCHEMA public OWNER TO pandora_owner;
SQL
  then
    die "roles 阶段：设置 schema public 属主失败"
  fi

  echo "数据库角色 pandora_owner / pandora_app 与库 pandora 已就绪（口令没有打印）"
  exit 0
fi

[[ -f "$CONFIGURE_SQL" ]] || die "grants 阶段：找不到 $CONFIGURE_SQL"
if ! run_psql pandora < "$CONFIGURE_SQL"; then
  die "grants 阶段：configure-app-role.sql 执行失败"
fi
echo "运行角色 pandora_app 的权限已收敛"
