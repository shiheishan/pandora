#!/usr/bin/env bash
# 本机（没有 Docker 的 Mac）按需起 PostgreSQL 18 与 Redis，给 PG 用例、迁移往返、本机起真面板用。
#
# 用法：local-db.sh start | stop | status | test-env | dev-init
#   start     起 PG（127.0.0.1:5433）与 Redis（127.0.0.1:6380），已在跑就跳过；确保测试库 pandora_test 存在
#   stop      停掉两者（用完就停，不常驻）
#   status    看是否在跑、占多少内存
#   test-env  打印 PG 用例要的环境变量：eval "$(local-db.sh test-env)"
#   dev-init  按 panel/dev/.env 建开发库与 pandora_owner（同 dev/initdb/10-pandora-owner.sql），之后 make migrate / serve
#
# 只听回环；不设开机自启。测试用的取舍：关 fsync / 同步提交 / 整页写（掉电丢数据无所谓，换少写盘），
# 共享缓冲 128MB、连接上限 60、Redis 不落盘且限 128MB，占用压在几百 MB 以内。
# 二进制：PG_BIN（缺省 Homebrew 的 postgresql@18）、redis-server 在 PATH 或 Homebrew；
# 数据目录：PG_DATA（缺省 Homebrew 的 var/postgresql@18）；Redis 配置、日志、pid 在 PANDORA_LOCAL_STATE。
set -euo pipefail

brew_prefix=$(brew --prefix 2>/dev/null || echo /opt/homebrew)
PG_BIN=${PG_BIN:-$brew_prefix/opt/postgresql@18/bin}
PG_DATA=${PG_DATA:-$brew_prefix/var/postgresql@18}
PG_PORT=5433
REDIS_PORT=6380
STATE=${PANDORA_LOCAL_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/pandora-dev}
REDIS_BIN=$(command -v redis-server || echo "$brew_prefix/opt/redis/bin/redis-server")
REDIS_CLI=$(dirname "$REDIS_BIN")/redis-cli
ROOT=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
DEV_ENV=$ROOT/panel/dev/.env
TEST_DB=pandora_test

die() { echo "local-db: $*" >&2; exit 1; }
[ -x "$PG_BIN/pg_ctl" ] || die "找不到 $PG_BIN/pg_ctl（brew install postgresql@18，或设 PG_BIN）"
[ -d "$PG_DATA" ] || die "找不到数据目录 $PG_DATA（设 PG_DATA，或先 initdb）"

# 从 dev/.env 取一个值（只读需要的键，不 source 整个文件）
env_val() { if [ -f "$DEV_ENV" ]; then sed -n "s/^$1=//p" "$DEV_ENV" | tail -1; fi; }

psql_su() { "$PG_BIN/psql" -h 127.0.0.1 -p "$PG_PORT" -U "$USER" -X -q -v ON_ERROR_STOP=1 "$@"; }

pg_running() { "$PG_BIN/pg_ctl" -D "$PG_DATA" status >/dev/null 2>&1; }
redis_running() { [ -f "$STATE/redis.pid" ] && kill -0 "$(cat "$STATE/redis.pid")" 2>/dev/null; }

start_pg() {
  if pg_running; then echo "PG 已在跑"; return; fi
  mkdir -p "$STATE"
  # macOS 上 LC_ALL 不是有效区域设置时，postmaster 启动阶段会报 became multithreaded 退出
  LC_ALL=en_US.UTF-8 "$PG_BIN/pg_ctl" -D "$PG_DATA" -l "$STATE/postgres.log" -w -t 30 start -o "-p $PG_PORT \
    -c listen_addresses=127.0.0.1 -c max_connections=60 -c shared_buffers=128MB -c work_mem=8MB \
    -c fsync=off -c synchronous_commit=off -c full_page_writes=off -c timezone=UTC" >/dev/null
  echo "PG 起在 127.0.0.1:$PG_PORT（日志 $STATE/postgres.log）"
}

start_redis() {
  if redis_running; then echo "Redis 已在跑"; return; fi
  [ -x "$REDIS_BIN" ] || die "找不到 redis-server（brew install redis）"
  mkdir -p "$STATE"
  # 口令只进 0600 配置文件，不上命令行
  (umask 077; {
    echo "port $REDIS_PORT"; echo "bind 127.0.0.1"; echo "daemonize yes"
    echo "pidfile $STATE/redis.pid"; echo "logfile $STATE/redis.log"; echo "dir $STATE"
    echo 'save ""'; echo "appendonly no"; echo "maxmemory 128mb"; echo "maxmemory-policy noeviction"
    pw=$(env_val REDIS_PASSWORD); if [ -n "$pw" ]; then echo "requirepass $pw"; fi
  } > "$STATE/redis.conf")
  "$REDIS_BIN" "$STATE/redis.conf"
  for _ in $(seq 50); do redis_running && break; sleep 0.1; done
  redis_running || die "Redis 没起来，看 $STATE/redis.log"
  echo "Redis 起在 127.0.0.1:$REDIS_PORT"
}

ensure_test_db() {
  psql_su -d postgres -tA -c "SELECT 1 FROM pg_database WHERE datname = '$TEST_DB'" | grep -q 1 \
    || psql_su -d postgres -c "CREATE DATABASE $TEST_DB TEMPLATE template0 ENCODING 'UTF8'"
}

case "${1:-}" in
  start) start_pg; start_redis; ensure_test_db ;;
  stop)
    if pg_running; then "$PG_BIN/pg_ctl" -D "$PG_DATA" -m fast -w stop >/dev/null; echo "PG 已停"; fi
    if redis_running; then kill "$(cat "$STATE/redis.pid")"; echo "Redis 已停"; fi ;;
  status)
    if pg_running; then
      rss=$(ps -Ao rss,command | awk -v d="$PG_DATA" 'index($0, "postgres") && (index($0, d) || /postgres:/) {s+=$1} END {printf "%.0f", s/1024}')
      echo "PG 在跑（127.0.0.1:$PG_PORT，进程合计约 ${rss}MB）"
    else echo "PG 没在跑"; fi
    if redis_running; then
      echo "Redis 在跑（127.0.0.1:$REDIS_PORT，约 $(ps -o rss= -p "$(cat "$STATE/redis.pid")" | awk '{printf "%.0f", $1/1024}')MB）"
    else echo "Redis 没在跑"; fi ;;
  test-env)
    echo "export PANDORA_TEST_DATABASE_URL='postgres://$USER@127.0.0.1:$PG_PORT/$TEST_DB?sslmode=disable'"
    # 与 CI 同为 UTC：有用例按本地时区算「一个月后」，本机时区跨夏令时切换时会差一小时
    echo "export TZ=UTC" ;;
  dev-init)
    [ -f "$DEV_ENV" ] || die "没有 $DEV_ENV（从 panel/dev/.env.example 复制并填值）"
    pg_running || die "PG 没在跑，先 start"
    PANDORA_DB=$(env_val POSTGRES_DB) PANDORA_DB_OWNER_PASSWORD=$(env_val PANDORA_OWNER_PASSWORD) \
      psql_su -d postgres -f "$ROOT/panel/dev/initdb/10-pandora-owner.sql"
    echo "开发库已就绪：cd panel && make migrate && make serve" ;;
  *) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
