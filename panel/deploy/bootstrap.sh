#!/usr/bin/env bash
# Configure the least-privileged runtime login after migrations create it.
# The password is passed through environment/stdin only and is never rendered
# into command arguments or script output.
# 本机 psql 经 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户跑 configure-app-role.sql；
# 恢复库之后、装完迁移之后都要跑。
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
. ./.env

: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${POSTGRES_SUPER_PASSWORD:?POSTGRES_SUPER_PASSWORD is required}"
: "${POSTGRES_PORT:?POSTGRES_PORT is required}"
: "${AEGIS_DB_APP_PASSWORD:?AEGIS_DB_APP_PASSWORD is required}"

if [ "${AEGIS_DB_APP_PASSWORD}" = "CHANGE_ME" ]; then
  echo "bootstrap refused: replace AEGIS_DB_APP_PASSWORD with a URL-safe random secret" >&2
  exit 1
fi
if [[ ! "${AEGIS_DB_APP_PASSWORD}" =~ ^[A-Za-z0-9_-]{32,}$ ]]; then
  echo "bootstrap refused: AEGIS_DB_APP_PASSWORD must be at least 32 URL-safe characters" >&2
  exit 1
fi

# configure-app-role.sql 以 \getenv 读运行角色口令，只经环境变量给 psql
export AEGIS_DB_APP_PASSWORD
PGPASSWORD="${POSTGRES_SUPER_PASSWORD}" PGSSLMODE=disable \
  psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p "${POSTGRES_PORT}" -U postgres -d "${POSTGRES_DB}" \
  < ./configure-app-role.sql

echo "aegis_app runtime login configured (password not printed)"
