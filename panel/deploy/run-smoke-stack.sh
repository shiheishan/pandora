#!/usr/bin/env bash
# 起一套只活一次的真实面板，给前端冒烟用。
#
# 为什么不复用开发数据基座 dev/docker-compose.yml：那是开发机上长期的，固定端口、带数据卷、
# 读 deploy/.env；冒烟要的是「每次从空库起、跑完就扔、配置现场生成」。
#
# 库的形状照直装（deploy/install.sh）：超级用户 postgres 跑迁移与 SQL 夹具，库属主是普通登录角色 aegis，
# 网关只用运行角色 aegis_app；网关经 127.0.0.1 的 TCP 连 PG 与 Valkey，连接串与 install.sh 写的同形。
#
# 配置全部在这里用 openssl 现场生成，只活在 runner 上这一次：
# 仓库是公开的，冒烟里不能出现任何真实部署的值（见根 CLAUDE.md「红线：仓库公开」）。
#
# 用法：
#   run-smoke-stack.sh up   <panel 源码目录> <状态目录>
#   run-smoke-stack.sh down <状态目录>
#
# 状态目录里会有：smoke.env（给后续步骤 source）、gateway.env（网关进程的配置）、
# datastore.env（直装 deploy/.env 里库与缓存的那几项，含超级用户口令，只给 run-smoke-e2e.sh 拼 .env）、
# bin/（编译出的网关）、logs/（网关输出，失败时打印尾部）、pids、containers。

# 不开 errtrace：ERR 陷阱只在顶层触发一次，免得子 shell 里先拆一遍栈
set -euo pipefail

usage() {
  echo "用法: $0 up <panel 源码目录> <状态目录> | $0 down <状态目录>" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# down：按状态目录里的记录拆，只拆自己起的东西
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
mkdir -p "$STATE"
STATE="$(cd "$STATE" && pwd)"
[[ ! -f "$STATE/pids" && ! -f "$STATE/containers" ]] || {
  echo "状态目录里还有上一次的栈，先 $0 down $STATE" >&2
  exit 2
}
mkdir -p "$STATE/bin" "$STATE/logs"

GOOSE="${GOOSE_BIN:-goose}"
PG_IMAGE="${PANDORA_SMOKE_PG_IMAGE:-postgres:18-alpine}"
VALKEY_IMAGE="${PANDORA_SMOKE_VALKEY_IMAGE:-valkey/valkey:8-alpine}"
PUB_ADDR="${PANDORA_SMOKE_PUBLIC_ADDR:-127.0.0.1:9000}"
ADM_ADDR="${PANDORA_SMOKE_ADMIN_ADDR:-127.0.0.1:9001}"
NODE_ADDR="${PANDORA_SMOKE_NODE_ADDR:-127.0.0.1:9003}"
# 库名带独立的 test 段：tests/admin_e2e.sh 与 uniproxy_e2e.sh 只肯在名字看得出是一次性库的
# 库上跑（第 ⑤ 步在冒烟栈上跑这些脚本），冒烟库本来就是一次性的
PG_DB=aegis_smoke_test
# 容器名只给本栈自己与 SQL 夹具（seed.ts、tests/browser 经 docker exec 进去）用；
# deploy/psql.sh 走主机 psql 与 TCP，不认容器名
PG_CONTAINER="pandora-smoke-pg-$$"
VK_CONTAINER="pandora-smoke-valkey-$$"

# 失败时把网关日志尾部打出来再拆栈；成功时栈留着给后续步骤用
on_error() {
  local rc=$?
  echo "起栈失败（退出码 $rc），网关日志尾部：" >&2
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
PG_OWNER=aegis
PG_OWNER_PW="$(rand_hex 24)"
# bootstrap.sh 要求运行角色口令至少 32 位 URL-safe 字符，这里照同一标准
APP_PW="$(rand_hex 24)"
VALKEY_PW="$(rand_hex 24)"
ADMIN_EMAIL="smoke-admin@example.test"
ADMIN_PASS="Smoke-$(rand_hex 16)"

echo "==> 起 PostgreSQL 18 与 Valkey 8"
echo "$PG_CONTAINER" >> "$STATE/containers"
# 超级用户口令经 -e 只给变量名、值取自本进程环境，不进 docker 的命令行参数。
# 不设 POSTGRES_DB：库由下面照 install.sh 建，属主才是 aegis
POSTGRES_PASSWORD="$PG_SUPER_PW" docker run -d --name "$PG_CONTAINER" \
  -e POSTGRES_PASSWORD -e POSTGRES_USER="$PG_SUPER" \
  -p 127.0.0.1::5432 "$PG_IMAGE" >/dev/null
echo "$VK_CONTAINER" >> "$STATE/containers"
docker run -d --name "$VK_CONTAINER" -p 127.0.0.1::6379 "$VALKEY_IMAGE" \
  valkey-server --requirepass "$VALKEY_PW" >/dev/null

# 就绪检查走 TCP，理由同 run-pg18-gates.sh：镜像初始化时先起一个只听 unix socket
# 的临时实例，经 socket 探测会在它身上报「就绪」，紧接着的迁移正好撞上它关停。
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
  if docker exec "$VK_CONTAINER" valkey-cli -a "$VALKEY_PW" --no-auth-warning ping 2>/dev/null | grep -q PONG; then
    ready=1; break
  fi
  sleep 1
done
[[ -n "$ready" ]] || { echo "Valkey 30 秒内没有就绪" >&2; false; }

host_port() { docker inspect -f "{{(index (index .NetworkSettings.Ports \"$2/tcp\") 0).HostPort}}" "$1"; }
PG_PORT="$(host_port "$PG_CONTAINER" 5432)"
VK_PORT="$(host_port "$VK_CONTAINER" 6379)"
[[ "$PG_PORT" =~ ^[0-9]+$ && "$VK_PORT" =~ ^[0-9]+$ ]] || { echo "读不出 PG / Valkey 发布到宿主的端口" >&2; false; }
MIGRATION_DSN="postgres://${PG_SUPER}:${PG_SUPER_PW}@127.0.0.1:${PG_PORT}/${PG_DB}?sslmode=disable"
APP_DSN="postgres://aegis_app:${APP_PW}@127.0.0.1:${PG_PORT}/${PG_DB}?sslmode=disable"
echo "    $(docker exec "$PG_CONTAINER" psql -U "$PG_SUPER" -d postgres -tAc 'SELECT version()' | cut -d, -f1)"

# ---------------------------------------------------------------------------
# 建库、迁移与运行角色：和生产同一条路
# ---------------------------------------------------------------------------
echo "==> 建库属主 $PG_OWNER 与库 $PG_DB（与 install.sh 同一组语句）"
# 容器里经 unix socket 连（镜像的本地连接是 trust）；属主口令经 \getenv 从环境取，不进 SQL 文本与参数
PG_OWNER_PW="$PG_OWNER_PW" docker exec -i -e PG_OWNER_PW "$PG_CONTAINER" \
  psql -X -q -v ON_ERROR_STOP=1 -U "$PG_SUPER" -d postgres >/dev/null <<SQL
\getenv owner_password PG_OWNER_PW
SELECT pg_catalog.format('CREATE ROLE %I LOGIN PASSWORD %L', '$PG_OWNER', :'owner_password') \gexec
CREATE DATABASE $PG_DB OWNER $PG_OWNER TEMPLATE template0 ENCODING 'UTF8';
SQL

echo "==> goose 迁移到最新"
# 迁移以超级用户 postgres 跑（与直装 .env 的 AEGIS_MIGRATION_DATABASE_URL 同一身份：属主与
# ALTER DEFAULT PRIVILEGES 随执行者走），全新库的 fail-closed 闸门照 install.sh 首装显式满足
MIGRATE_OPTS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes -c app.order_release_writers_stopped=yes'
( cd "$PANEL_DIR" && PGOPTIONS="$MIGRATE_OPTS" "$GOOSE" -dir migrations postgres "$MIGRATION_DSN" up ) \
  > "$STATE/logs/migrate.log" 2>&1 || { tail -30 "$STATE/logs/migrate.log" >&2; false; }
tail -1 "$STATE/logs/migrate.log"

echo "==> 配置运行角色 aegis_app（configure-app-role.sql，与 bootstrap.sh 同一份）"
AEGIS_DB_APP_PASSWORD="$APP_PW" docker exec -i -e AEGIS_DB_APP_PASSWORD "$PG_CONTAINER" \
  psql -X -v ON_ERROR_STOP=1 -U "$PG_SUPER" -d "$PG_DB" -f - \
  < "$PANEL_DIR/deploy/configure-app-role.sql" >/dev/null

# ---------------------------------------------------------------------------
# 网关配置：只有 config.Load 要的键，外加冒烟需要的一个开关（放宽登录限流）
# ---------------------------------------------------------------------------
cat > "$STATE/gateway.env" <<EOF
AEGIS_ENV=test
AEGIS_DATABASE_URL=$APP_DSN
AEGIS_REDIS_URL=redis://:${VALKEY_PW}@127.0.0.1:${VK_PORT}/0
AEGIS_PUBLIC_ADDR=$PUB_ADDR
AEGIS_ADMIN_ADDR=$ADM_ADDR
AEGIS_NODE_ADDR=$NODE_ADDR
AEGIS_PUBLIC_BASE_URL=http://$PUB_ADDR
AEGIS_MASTER_KEY=$(rand_b64)
AEGIS_JWT_PUBLIC_SECRET=$(rand_b64)
AEGIS_JWT_ADMIN_SECRET=$(rand_b64)
AEGIS_CONFIG_SIGNING_SEED=$(rand_b64)
AEGIS_RL_AUTH_PER_MIN=1000
EOF
chmod 600 "$STATE/gateway.env"

echo "==> 编译网关（CGO_ENABLED=0，与发布包一致）"
( cd "$PANEL_DIR" && CGO_ENABLED=0 go build -mod=readonly -o "$STATE/bin/" \
    ./cmd/aegis-public ./cmd/aegis-admin ./cmd/aegis-node ./cmd/aegis-adminctl )

echo "==> aegis-adminctl 建平台管理员"
( set -a; . "$STATE/gateway.env"; set +a
  printf '%s\n' "$ADMIN_PASS" | "$STATE/bin/aegis-adminctl" create \
    --email "$ADMIN_EMAIL" --password-stdin --role platform_admin
) > "$STATE/logs/adminctl.log" 2>&1 || { cat "$STATE/logs/adminctl.log" >&2; false; }

echo "==> 启动 aegis-public、aegis-admin 与 aegis-node"
start_gateway() {
  local name="$1"
  ( set -a; . "$STATE/gateway.env"; set +a
    exec "$STATE/bin/$name" ) > "$STATE/logs/$name.log" 2>&1 &
  echo $! >> "$STATE/pids"
}
start_gateway aegis-public
start_gateway aegis-admin
# 节点控制面：造数据时节点经它上报心跳（UniProxy）
start_gateway aegis-node

# ---------------------------------------------------------------------------
# 健康检查：readyz 连库连缓存；再用管理员真登录一次，证明 adminctl 建的号能用
# ---------------------------------------------------------------------------
curl() { command curl -q --noproxy '*' "$@"; }
# 节点控制面没有 readyz，只探 healthz（第三个参数）
wait_ready() {
  local base="$1" name="$2" probe="${3:-readyz}"
  for _ in $(seq 1 60); do
    if [[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$base/$probe")" == 200 ]]; then
      echo "    $name 就绪：$(curl -s --max-time 3 "$base/healthz")"
      return 0
    fi
    sleep 1
  done
  echo "$name 60 秒内 $probe 没有返回 200" >&2
  return 1
}
wait_ready "http://$PUB_ADDR" aegis-public
wait_ready "http://$ADM_ADDR" aegis-admin
wait_ready "http://$NODE_ADDR" aegis-node healthz

login="$(curl -s -X POST "http://$ADM_ADDR/v1/auth/login" -H 'Content-Type: application/json' \
  --data-binary @- <<<"{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}")"
atok="$(python3 -c 'import sys,json; print(json.load(sys.stdin).get("access_token",""))' <<<"$login" 2>/dev/null || true)"
[[ -n "$atok" ]] || { echo "管理员登录没有拿到 access_token（响应已隐藏）" >&2; false; }
me="$(curl -s -o /dev/null -w '%{http_code}' "http://$ADM_ADDR/v1/me" -H "Authorization: Bearer $atok")"
[[ "$me" == 200 ]] || { echo "管理员 /v1/me 返回 $me" >&2; false; }
echo "    管理员登录与 /v1/me 通过"

# 给后续步骤的入口。超级用户口令不进 smoke.env（各步骤都 source 它）：SQL 夹具经 docker exec 进容器，
# 只有 run-smoke-e2e.sh 读 datastore.env 拼直装形状的 deploy/.env；网关进程两份都拿不到
cat > "$STATE/smoke.env" <<EOF
SMOKE_PUBLIC_BASE=http://$PUB_ADDR
SMOKE_ADMIN_BASE=http://$ADM_ADDR
SMOKE_NODE_BASE=http://$NODE_ADDR
SMOKE_ADMIN_EMAIL=$ADMIN_EMAIL
SMOKE_ADMIN_PASSWORD=$ADMIN_PASS
SMOKE_PG_CONTAINER=$PG_CONTAINER
SMOKE_PG_DB=$PG_DB
EOF
chmod 600 "$STATE/smoke.env"
# 与 install.sh 写的 deploy/.env 同名同义的库与缓存几项
( umask 077
  cat > "$STATE/datastore.env" <<EOF
POSTGRES_USER=$PG_OWNER
POSTGRES_PASSWORD=$PG_OWNER_PW
POSTGRES_DB=$PG_DB
POSTGRES_PORT=$PG_PORT
POSTGRES_SUPER_PASSWORD=$PG_SUPER_PW
AEGIS_MIGRATION_DATABASE_URL=$MIGRATION_DSN
AEGIS_DB_APP_PASSWORD=$APP_PW
VALKEY_PASSWORD=$VALKEY_PW
VALKEY_PORT=$VK_PORT
EOF
)

trap - ERR
echo "==> 冒烟栈已就绪：$STATE/smoke.env"
