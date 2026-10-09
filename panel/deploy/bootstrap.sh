#!/usr/bin/env bash
# Configure the least-privileged runtime login after migrations create it.
# The password is passed through environment/stdin only and is never rendered
# into command arguments or script output.
# 两种布局同一个入口（布局判定见 pandora_db_layout）：docker 在容器 aegis-postgres 里以 POSTGRES_USER 跑，
# 直装用本机 psql 经 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户跑。恢复库之后、装完迁移之后都要跑。
set -euo pipefail
cd "$(dirname "$0")"
. ./.env

# 数据库布局：docker（install.sh，容器 aegis-postgres）或 native（install-native.sh，系统 PostgreSQL）。
# 以 .env 的 PANDORA_DB_LAYOUT 为准；老的直装 .env 没有这一键，凭只有直装才写的 POSTGRES_SUPER_PASSWORD
# 认出来。各运维脚本各带一份同样的函数（不 source 共用文件，免得多一个要校验的信任面），
# pg-layout_mock_test.sh 核对逐字一致。
pandora_db_layout() {
  case "${PANDORA_DB_LAYOUT:-}" in
    native|docker) printf '%s\n' "$PANDORA_DB_LAYOUT" ;;
    '') if [ -n "${POSTGRES_SUPER_PASSWORD:-}" ]; then printf 'native\n'; else printf 'docker\n'; fi ;;
    *) return 1 ;;
  esac
}
layout="$(pandora_db_layout)" || { echo "bootstrap refused: PANDORA_DB_LAYOUT must be native or docker" >&2; exit 1; }
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

export AEGIS_DB_APP_PASSWORD
case "$layout" in
  docker)
    : "${POSTGRES_USER:?POSTGRES_USER is required}"
    : "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
    PGPASSWORD="${POSTGRES_PASSWORD}" docker exec -i \
      -e PGPASSWORD \
      -e AEGIS_DB_APP_PASSWORD \
      aegis-postgres \
      psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" \
      < ./configure-app-role.sql
    ;;
  native)
    : "${POSTGRES_SUPER_PASSWORD:?POSTGRES_SUPER_PASSWORD is required}"
    : "${POSTGRES_PORT:?POSTGRES_PORT is required}"
    PGPASSWORD="${POSTGRES_SUPER_PASSWORD}" PGSSLMODE=disable \
      psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p "${POSTGRES_PORT}" -U postgres -d "${POSTGRES_DB}" \
      < ./configure-app-role.sql
    ;;
esac

echo "aegis_app runtime login configured (password not printed)"
