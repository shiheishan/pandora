#!/usr/bin/env bash
# 以超级用户进面板的库：
#   ./psql.sh                       交互
#   ./psql.sh -tAc 'SELECT 1'       单条
#   ./psql.sh < 多语句.sql          多语句经标准输入
# 凭据从同目录的 .env 读（不导出给别的进程）：本机 psql 经 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户
# （POSTGRES_SUPER_PASSWORD）连，口令只经环境变量 PGPASSWORD 给客户端，不进命令行参数。
set -euo pipefail
cd "$(dirname "$0")"
die() { echo "psql.sh: $*" >&2; exit 1; }
[ -r ./.env ] || die "cannot read $(pwd)/.env"
# shellcheck disable=SC1091
. ./.env

: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${POSTGRES_SUPER_PASSWORD:?POSTGRES_SUPER_PASSWORD is required}"
: "${POSTGRES_PORT:?POSTGRES_PORT is required}"
PGPASSWORD="$POSTGRES_SUPER_PASSWORD" PGSSLMODE=disable exec psql \
  -h 127.0.0.1 -p "$POSTGRES_PORT" -U postgres -d "$POSTGRES_DB" "$@"
