#!/usr/bin/env bash
# Configure the least-privileged runtime login after migrations create it.
# The password is passed through environment/stdin only and is never rendered
# into command arguments or script output.
set -euo pipefail
cd "$(dirname "$0")"
. ./.env

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${AEGIS_DB_APP_PASSWORD:?AEGIS_DB_APP_PASSWORD is required}"

if [ "${AEGIS_DB_APP_PASSWORD}" = "CHANGE_ME" ]; then
  echo "bootstrap refused: replace AEGIS_DB_APP_PASSWORD with a URL-safe random secret" >&2
  exit 1
fi
if [[ ! "${AEGIS_DB_APP_PASSWORD}" =~ ^[A-Za-z0-9_-]{32,}$ ]]; then
  echo "bootstrap refused: AEGIS_DB_APP_PASSWORD must be at least 32 URL-safe characters" >&2
  exit 1
fi

export PGPASSWORD="${POSTGRES_PASSWORD}"
export AEGIS_DB_APP_PASSWORD

docker exec -i \
  -e PGPASSWORD \
  -e AEGIS_DB_APP_PASSWORD \
  aegis-postgres \
  psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" \
  < ./configure-app-role.sql

echo "aegis_app runtime login configured (password not printed)"
