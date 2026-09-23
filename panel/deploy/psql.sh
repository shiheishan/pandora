#!/usr/bin/env bash
# AegisPanel psql 封装：从 .env 读凭据，避免命令行泄露密码。
set -euo pipefail
cd "$(dirname "$0")"
set -a; . ./.env; set +a
exec docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" aegis-postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" "$@"
