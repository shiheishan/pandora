#!/usr/bin/env bash
# 起一套只活一次的真实面板（单二进制 pandora），给前端冒烟与 Playwright 用。
#
# 不复用 dev/docker-compose.yml：冒烟要「每次从空库起、跑完就扔、配置现场生成」，只活在 runner 上。
# 仓库公开，不能出现任何真实部署的值（见根 CLAUDE.md「红线：仓库公开」）。
#
# 用法：
#   run-smoke-stack.sh up   <panel 源码目录> <状态目录>
#   run-smoke-stack.sh down <状态目录>
#
# 状态目录：stack.env（pandora 进程配置）、smoke.env（后续步骤 source）、
# bin/（pandora、nodesim）、logs/、pids、containers。

set -euo pipefail

usage() {
  echo "用法: $0 up <panel 源码目录> <状态目录> | $0 down <状态目录>" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# down：按状态目录里的记录拆，只拆自己起的东西（含种子后来追加的 nodesim、插件接收端等 pid）
# ---------------------------------------------------------------------------
down() {
  local state="$1"
  [[ -d "$state" ]] || return 0
  if [[ -f "$state/pids" ]]; then
    while read -r pid; do
      [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
    done < "$state/pids"
    rm -f "$state/pids"
  fi
  if [[ -f "$state/containers" ]]; then
    while read -r c; do
      [[ -n "$c" ]] && docker rm -f "$c" >/dev/null 2>&1 || true
    done < "$state/containers"
    rm -f "$state/containers"
  fi
}

case "${1:-}" in
  down) [[ $# -eq 2 ]] || usage; down "$2"; exit 0 ;;
  up)   [[ $# -eq 3 ]] || usage ;;
  *)    usage ;;
esac

PANEL_DIR="$2"
STATE="$3"
[[ -f "$PANEL_DIR/go.mod" ]] || usage
PANEL_DIR="$(cd "$PANEL_DIR" && pwd)"
PDND_DIR="$(cd "$PANEL_DIR/../pdnd" && pwd)"
[[ -f "$PDND_DIR/go.mod" ]] || { echo "找不到 pdnd 模块: $PDND_DIR/go.mod" >&2; exit 2; }
mkdir -p "$STATE"
STATE="$(cd "$STATE" && pwd)"
[[ ! -f "$STATE/pids" && ! -f "$STATE/containers" ]] || {
  echo "状态目录里还有上一次的栈，先 $0 down $STATE" >&2
  exit 2
}
mkdir -p "$STATE/bin" "$STATE/logs"

PG_IMAGE="${PANDORA_SMOKE_PG_IMAGE:-postgres:18-alpine}"
REDIS_IMAGE="${PANDORA_SMOKE_REDIS_IMAGE:-redis:8-alpine}"
PUB_ADDR="${PANDORA_SMOKE_PUBLIC_ADDR:-127.0.0.1:9000}"
ADM_ADDR="${PANDORA_SMOKE_ADMIN_ADDR:-127.0.0.1:9001}"
GRPC_ADDR="${PANDORA_SMOKE_GRPC_ADDR:-127.0.0.1:9443}"
PG_DB=pandora_smoke_test
PG_CONTAINER="pandora-smoke-pg-$$"
REDIS_CONTAINER="pandora-smoke-redis-$$"

on_error() {
  local rc=$?
  echo "起栈失败（退出码 $rc），日志尾部：" >&2
  for f in "$STATE"/logs/*.log; do
    [[ -f "$f" ]] || continue
    echo "---- $(basename "$f") ----" >&2
    tail -40 "$f" >&2
  done
  down "$STATE"
  exit "$rc"
}
trap on_error ERR

rand_b64() { openssl rand -base64 32 | tr -d '\n'; }
rand_hex() { openssl rand -hex "$1"; }

# ---------------------------------------------------------------------------
# 现场生成的一次性配置。口令只进环境变量与状态目录，不进命令行参数
# ---------------------------------------------------------------------------
PG_SUPER=postgres
PG_SUPER_PW="$(rand_hex 24)"
PG_OWNER_PW="$(rand_hex 24)"
REDIS_PW="$(rand_hex 24)"
ADMIN_EMAIL="smoke-admin@example.test"
ADMIN_PASS="Smoke-$(rand_hex 16)"
MASTER_KEY="$(rand_b64)"
PUBLIC_JWT="$(rand_b64)"
ADMIN_JWT="$(rand_b64)"

echo "==> 起 PostgreSQL 18 与 Redis 8"
echo "$PG_CONTAINER" >> "$STATE/containers"
POSTGRES_PASSWORD="$PG_SUPER_PW" docker run -d --name "$PG_CONTAINER" \
  -e POSTGRES_PASSWORD -e POSTGRES_USER="$PG_SUPER" \
  -p 127.0.0.1::5432 "$PG_IMAGE" >/dev/null

echo "$REDIS_CONTAINER" >> "$STATE/containers"
REDIS_PASSWORD="$REDIS_PW" docker run -d --name "$REDIS_CONTAINER" \
  -e REDIS_PASSWORD \
  -p 127.0.0.1::6379 "$REDIS_IMAGE" \
  sh -c 'exec redis-server --requirepass "$REDIS_PASSWORD" --appendonly no' >/dev/null

# 就绪检查走 TCP：镜像初始化时先起只听 unix socket 的临时实例，经 socket 探测会误报就绪
ready=""
for _ in $(seq 1 60); do
  if docker exec "$PG_CONTAINER" pg_isready -h 127.0.0.1 -U "$PG_SUPER" -q 2>/dev/null; then
    ready=1; break
  fi
  sleep 1
done
[[ -n "$ready" ]] || { echo "PostgreSQL 18 60 秒内没有在 TCP 上就绪" >&2; docker logs "$PG_CONTAINER" 2>&1 | tail -20 >&2; false; }

ready=""
for _ in $(seq 1 30); do
  if REDISCLI_AUTH="$REDIS_PW" docker exec -e REDISCLI_AUTH "$REDIS_CONTAINER" redis-cli ping 2>/dev/null | grep -q PONG; then
    ready=1; break
  fi
  sleep 1
done
[[ -n "$ready" ]] || { echo "Redis 30 秒内没有就绪" >&2; false; }

host_port() { docker inspect -f "{{(index (index .NetworkSettings.Ports \"$2/tcp\") 0).HostPort}}" "$1"; }
PG_PORT="$(host_port "$PG_CONTAINER" 5432)"
REDIS_PORT="$(host_port "$REDIS_CONTAINER" 6379)"
[[ "$PG_PORT" =~ ^[0-9]+$ && "$REDIS_PORT" =~ ^[0-9]+$ ]] || { echo "读不出 PG / Redis 发布到宿主的端口" >&2; false; }
echo "    $(docker exec "$PG_CONTAINER" psql -U "$PG_SUPER" -d postgres -tAc 'SELECT version()' | cut -d, -f1)"

echo "==> 建库属主 pandora_owner 与库 $PG_DB（与部署约定同形）"
# 迁移以属主 pandora_owner 跑（非超级用户）；保留期清理要设 session_replication_role，由超级用户授权一次。
# 冒烟暂让 serve/worker 也用 pandora_owner 连库；configure-app-role 合入后再切 pandora_app。
PG_OWNER_PW="$PG_OWNER_PW" docker exec -i -e PG_OWNER_PW "$PG_CONTAINER" \
  psql -X -q -v ON_ERROR_STOP=1 -U "$PG_SUPER" -d postgres >/dev/null <<SQL
\getenv owner_password PG_OWNER_PW
SELECT pg_catalog.format('CREATE ROLE %I LOGIN PASSWORD %L', 'pandora_owner', :'owner_password') \gexec
CREATE DATABASE $PG_DB OWNER pandora_owner TEMPLATE template0 ENCODING 'UTF8';
GRANT SET ON PARAMETER session_replication_role TO pandora_owner;
SQL

OWNER_DSN="postgres://pandora_owner:${PG_OWNER_PW}@127.0.0.1:${PG_PORT}/${PG_DB}?sslmode=disable"
REDIS_URL="redis://:${REDIS_PW}@127.0.0.1:${REDIS_PORT}/0"

# PANDORA_ENV=development：放行支付/插件钩子的回环地址，注册回显 dev_code；
# grpc.node.public_host 让节点 gRPC 监听且证书 SAN 匹配本机模拟器；认证限流放宽供冒烟与浏览器测试高频登录。
( umask 077
  cat > "$STATE/stack.env" <<EOF
PANDORA_ENV=development
PANDORA_DATABASE_URL=$OWNER_DSN
PANDORA_DATABASE_MIGRATION_URL=$OWNER_DSN
PANDORA_REDIS_URL=$REDIS_URL
PANDORA_SECRETS_MASTER_KEY=$MASTER_KEY
PANDORA_AUTH_PUBLIC_JWT_SECRET=$PUBLIC_JWT
PANDORA_AUTH_ADMIN_JWT_SECRET=$ADMIN_JWT
PANDORA_HTTP_PUBLIC_ADDR=$PUB_ADDR
PANDORA_HTTP_ADMIN_ADDR=$ADM_ADDR
PANDORA_PUBLIC_BASE_URL=http://$PUB_ADDR
PANDORA_GRPC_NODE_ADDR=$GRPC_ADDR
PANDORA_GRPC_NODE_PUBLIC_HOST=127.0.0.1
PANDORA_RATELIMIT_AUTH_PER_MIN=1000
EOF
)

echo "==> 编译 pandora 与 nodesim（CGO_ENABLED=0，-mod=readonly）"
( cd "$PANEL_DIR" && CGO_ENABLED=0 go build -mod=readonly -o "$STATE/bin/pandora" ./cmd/pandora )
( cd "$PDND_DIR" && CGO_ENABLED=0 go build -mod=readonly -o "$STATE/bin/nodesim" ./release/nodesim )

echo "==> pandora migrate up"
( set -a; . "$STATE/stack.env"; set +a
  "$STATE/bin/pandora" migrate up
) > "$STATE/logs/migrate.log" 2>&1 || { tail -30 "$STATE/logs/migrate.log" >&2; false; }
tail -1 "$STATE/logs/migrate.log"

echo "==> pandora admin create"
( set -a; . "$STATE/stack.env"; set +a
  printf '%s\n' "$ADMIN_PASS" | "$STATE/bin/pandora" admin create --email "$ADMIN_EMAIL" --password-stdin
  "$STATE/bin/pandora" admin has-admin
) > "$STATE/logs/admin.log" 2>&1 || { cat "$STATE/logs/admin.log" >&2; false; }

echo "==> 启动 pandora serve 与 worker"
start_proc() {
  local name="$1"
  shift
  ( set -a; . "$STATE/stack.env"; set +a
    exec "$STATE/bin/pandora" "$@"
  ) > "$STATE/logs/$name.log" 2>&1 &
  echo $! >> "$STATE/pids"
}
start_proc serve serve
start_proc worker worker
worker_pid="$(tail -1 "$STATE/pids")"

curl() { command curl -q --noproxy '*' "$@"; }
wait_ready() {
  local base="$1" name="$2"
  for _ in $(seq 1 60); do
    if [[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$base/readyz")" == 200 ]]; then
      echo "    $name 就绪：$(curl -s --max-time 3 "$base/healthz")"
      return 0
    fi
    sleep 1
  done
  echo "$name 60 秒内 readyz 没有返回 200" >&2
  return 1
}
wait_ready "http://$PUB_ADDR" public
wait_ready "http://$ADM_ADDR" admin

sleep 3
kill -0 "$worker_pid" 2>/dev/null || { echo "worker 启动后 3 秒内退出" >&2; false; }

login="$(curl -s -X POST "http://$ADM_ADDR/v1/auth/login" -H 'Content-Type: application/json' \
  --data-binary @- <<<"{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}")"
atok="$(python3 -c 'import sys,json; print(json.load(sys.stdin).get("access_token",""))' <<<"$login" 2>/dev/null || true)"
[[ -n "$atok" ]] || { echo "管理员登录没有拿到 access_token（响应已隐藏）" >&2; false; }
me="$(curl -s -o /dev/null -w '%{http_code}' "http://$ADM_ADDR/v1/me" -H "Authorization: Bearer $atok")"
[[ "$me" == 200 ]] || { echo "管理员 /v1/me 返回 $me" >&2; false; }
echo "    管理员登录与 /v1/me 通过"

# SQL 夹具经 docker exec 以容器内 postgres 进库（本地 socket 免口令）；超级用户口令不写文件。
# Redis 口令给 writes.smoke 改会话重认证时间；主密钥只在 stack.env，种子封存提现收款信息时读它。
( umask 077
  cat > "$STATE/smoke.env" <<EOF
SMOKE_PUBLIC_BASE=http://$PUB_ADDR
SMOKE_ADMIN_BASE=http://$ADM_ADDR
SMOKE_ADMIN_EMAIL=$ADMIN_EMAIL
SMOKE_ADMIN_PASSWORD=$ADMIN_PASS
SMOKE_PG_CONTAINER=$PG_CONTAINER
SMOKE_PG_DB=$PG_DB
SMOKE_REDIS_CONTAINER=$REDIS_CONTAINER
SMOKE_REDIS_PASSWORD=$REDIS_PW
SMOKE_NODESIM=$STATE/bin/nodesim
EOF
)

trap - ERR
echo "==> 冒烟栈已就绪：$STATE/smoke.env"
