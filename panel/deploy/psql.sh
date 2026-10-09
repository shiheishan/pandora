#!/usr/bin/env bash
# 以超级用户进面板的库，docker 与直装两种布局同一个入口：
#   ./psql.sh                       交互
#   ./psql.sh -tAc 'SELECT 1'       单条
#   ./psql.sh < 多语句.sql          多语句经标准输入
# 凭据从同目录的 .env 读（不导出给别的进程），口令只经环境变量 PGPASSWORD 给客户端，不进命令行参数。
#   docker：容器 aegis-postgres 里的 psql，以 POSTGRES_USER（容器里的超级用户）；终端里带 -t，有行编辑；
#   native：本机 psql 经 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户（POSTGRES_SUPER_PASSWORD）。
set -euo pipefail
cd "$(dirname "$0")"
die() { echo "psql.sh: $*" >&2; exit 1; }
[ -r ./.env ] || die "cannot read $(pwd)/.env"
# shellcheck disable=SC1091
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

layout="$(pandora_db_layout)" || die "PANDORA_DB_LAYOUT must be native or docker"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
case "$layout" in
  docker)
    : "${POSTGRES_USER:?POSTGRES_USER is required}"
    : "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
    tty=()
    if [ -t 0 ] && [ -t 1 ]; then tty=(-t); fi
    PGPASSWORD="$POSTGRES_PASSWORD" exec docker exec -i ${tty[@]+"${tty[@]}"} -e PGPASSWORD aegis-postgres \
      psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" "$@"
    ;;
  native)
    : "${POSTGRES_SUPER_PASSWORD:?POSTGRES_SUPER_PASSWORD is required}"
    : "${POSTGRES_PORT:?POSTGRES_PORT is required}"
    PGPASSWORD="$POSTGRES_SUPER_PASSWORD" PGSSLMODE=disable exec psql \
      -h 127.0.0.1 -p "$POSTGRES_PORT" -U postgres -d "$POSTGRES_DB" "$@"
    ;;
esac
