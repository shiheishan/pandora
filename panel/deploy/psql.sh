#!/usr/bin/env bash
# 以超级用户 postgres 进面板的库：
#   ./psql.sh                       交互
#   ./psql.sh -tAc 'SELECT 1'       单条
#   ./psql.sh < 多语句.sql          多语句经标准输入
# 连接参数从 deploy.env 逐键读（不 source、不导出给别的进程）：生产是 /etc/pandora/deploy.env（root 0600，所以要 sudo），
# 本机开发由 Makefile 设 PANDORA_DEPLOY_ENV=dev/.env。经 127.0.0.1:POSTGRES_PORT 连，口令只经 PGPASSWORD 给客户端。
set -euo pipefail
die() { echo "psql.sh: $*" >&2; exit 1; }
env_file="${PANDORA_DEPLOY_ENV:-/etc/pandora/deploy.env}"
[ -r "$env_file" ] || die "读不到 $env_file（生产要 sudo；本机开发设 PANDORA_DEPLOY_ENV=dev/.env）"
val() { awk -F= -v k="$1" '$1 == k { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$env_file"; }
port="$(val POSTGRES_PORT)"
db="$(val POSTGRES_DB)"
pw="$(val POSTGRES_SUPER_PASSWORD)"
[ -n "$port" ] || die "$env_file 缺 POSTGRES_PORT"
[ -n "$pw" ] || die "$env_file 缺 POSTGRES_SUPER_PASSWORD"
# 连不上的库别挂住调用方：缺省 5 秒连不上就失败，调用方可以自己给 PGCONNECT_TIMEOUT
PGPASSWORD="$pw" PGSSLMODE=disable PGCONNECT_TIMEOUT="${PGCONNECT_TIMEOUT:-5}" exec psql \
  -h 127.0.0.1 -p "$port" -U postgres -d "${db:-pandora}" "$@"
