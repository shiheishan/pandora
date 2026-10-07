#!/usr/bin/env bash
# 起一套只活一次的真实面板，给前端冒烟用。
#
# 为什么不复用 docker-compose.yml：那是开发机的长期数据基座，固定端口、带数据卷、
# 读 deploy/.env；冒烟要的是「每次从空库起、跑完就扔、配置现场生成」。
#
# 配置全部在这里用 openssl 现场生成，只活在 runner 上这一次：
# 仓库是公开的，冒烟里不能出现任何真实部署的值（见根 CLAUDE.md「红线：仓库公开」）。
#
# 用法：
#   run-smoke-stack.sh up   <panel 源码目录> <状态目录>
#   run-smoke-stack.sh down <状态目录>
#
# 状态目录里会有：smoke.env（给后续步骤 source）、bin/（编译出的网关）、
# logs/（网关输出，失败时打印尾部）、pids、containers。

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
# 容器名可覆盖：第 ⑤ 步要让仓库自带的 deploy/psql.sh（写死 docker-compose 的 aegis-postgres）
# 直接连上冒烟库，workflow 在一次性 runner 上把它设成 aegis-postgres
PG_CONTAINER="${PANDORA_SMOKE_PG_CONTAINER:-pandora-smoke-pg-$$}"
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
PG_SUPER=aegis
PG_SUPER_PW="$(rand_hex 24)"
# bootstrap.sh 要求运行角色口令至少 32 位 URL-safe 字符，这里照同一标准
APP_PW="$(rand_hex 24)"
VALKEY_PW="$(rand_hex 24)"
ADMIN_EMAIL="smoke-admin@example.test"
ADMIN_PASS="Smoke-$(rand_hex 16)"

# 网关与生产一样经 unix socket 连 PG 与 Valkey（docker-compose.yml 的 run/ 挂载，install.sh
# 把 .env 换成 socket 连接串），不走 docker-proxy；回环端口照留给迁移与造数据。
# socket 路径不能超过 107 字节，状态目录太深就直接拒绝
SOCK_PG_DIR="$STATE/run/postgresql"
SOCK_VK="$STATE/run/valkey/valkey.sock"
(( ${#SOCK_PG_DIR} + 15 <= 107 && ${#SOCK_VK} <= 107 )) || {
  echo "状态目录路径太长，unix socket 放不下：$STATE" >&2; exit 2; }
mkdir -p "$SOCK_PG_DIR" "$(dirname "$SOCK_VK")"

echo "==> 起 PostgreSQL 18 与 Valkey 8"
echo "$PG_CONTAINER" >> "$STATE/containers"
docker run -d --name "$PG_CONTAINER" \
  -e POSTGRES_PASSWORD="$PG_SUPER_PW" -e POSTGRES_USER="$PG_SUPER" -e POSTGRES_DB="$PG_DB" \
  -v "$SOCK_PG_DIR:/var/run/postgresql" \
  -p 127.0.0.1::5432 "$PG_IMAGE" >/dev/null
echo "$VK_CONTAINER" >> "$STATE/containers"
# 生产是 770（网关以 root 运行）；runner 上网关是普通用户、不在 valkey 组，冒烟放宽到 777
docker run -d --name "$VK_CONTAINER" -p 127.0.0.1::6379 \
  -v "$(dirname "$SOCK_VK"):/data/sock" "$VALKEY_IMAGE" \
  valkey-server --requirepass "$VALKEY_PW" \
  --unixsocket /data/sock/valkey.sock --unixsocketperm 777 >/dev/null

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
ready=""
for _ in $(seq 1 30); do
  if [[ -S "$SOCK_PG_DIR/.s.PGSQL.5432" && -S "$SOCK_VK" ]]; then ready=1; break; fi
  sleep 1
done
[[ -n "$ready" ]] || { echo "宿主机上 30 秒内没有出现 PG / Valkey 的 unix socket" >&2; ls -la "$STATE/run"/* >&2 || true; false; }

PG_PORT="$(docker inspect -f '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' "$PG_CONTAINER")"
MIGRATION_DSN="postgres://${PG_SUPER}:${PG_SUPER_PW}@127.0.0.1:${PG_PORT}/${PG_DB}?sslmode=disable"
# 运行角色走 socket（与 install.sh 换出来的形式一致）：host 是目录，端口决定文件名 .s.PGSQL.5432
APP_DSN="postgres://aegis_app:${APP_PW}@/${PG_DB}?host=${SOCK_PG_DIR}"
echo "    $(docker exec "$PG_CONTAINER" psql -U "$PG_SUPER" -d "$PG_DB" -tAc 'SELECT version()' | cut -d, -f1)"

# ---------------------------------------------------------------------------
# 回环往返对照（只打印、不判，失败也不拦起栈）：同一个库上各 1000 次 SELECT 1，
# 一边是经 docker-proxy 的回环 TCP（宿主端口），一边是挂出来的 unix socket。
# pgbench 用 PG 镜像自带的，跑在宿主网络命名空间里，等同宿主机上的进程；单连接、不重连，
# 测的是纯往返。PANDORA_SMOKE_RTT=0 跳过
# ---------------------------------------------------------------------------
rtt_once() {
  local label="$1" host="$2" port="$3"; shift 3
  ( export PGPASSWORD="$PG_SUPER_PW" PGUSER="$PG_SUPER" PGDATABASE="$PG_DB" PGHOST="$host" PGPORT="$port"
    docker run --rm --network host -e PGPASSWORD -e PGUSER -e PGDATABASE -e PGHOST -e PGPORT "$@" \
      --entrypoint sh "$PG_IMAGE" -c 'echo "SELECT 1;" > /tmp/q.sql && pgbench -n -c 1 -t 1000 -f /tmp/q.sql' 2>&1 ) \
    | awk -v l="$label" '/latency average/ { print "    " l "：1000 次 SELECT 1，" $0 }'
}
if [[ "${PANDORA_SMOKE_RTT:-1}" != 0 ]]; then
  echo "==> 回环往返对照（docker-proxy：$(pgrep -x docker-proxy >/dev/null && echo 在跑 || echo 没有)）"
  {
    for round in 1 2 3; do
      rtt_once "第 $round 轮 TCP 127.0.0.1:$PG_PORT（经 docker-proxy）" 127.0.0.1 "$PG_PORT"
      rtt_once "第 $round 轮 unix socket" /var/run/postgresql 5432 -v "$SOCK_PG_DIR:/var/run/postgresql"
    done
  } | tee "$STATE/rtt.txt" || true
fi

# ---------------------------------------------------------------------------
# 迁移与运行角色：和生产同一条路
# ---------------------------------------------------------------------------
echo "==> goose 迁移到最新"
# 迁移用 aegis 跑，fail-closed 闸门显式满足，两条都照 run-pg18-gates.sh：
# 属主与 ALTER DEFAULT PRIVILEGES 随执行者走，拿 postgres 跑出的库权限拓扑与生产不同
MIGRATE_OPTS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes -c app.order_release_writers_stopped=yes'
( cd "$PANEL_DIR" && PGOPTIONS="$MIGRATE_OPTS" "$GOOSE" -dir migrations postgres "$MIGRATION_DSN" up ) \
  > "$STATE/logs/migrate.log" 2>&1 || { tail -30 "$STATE/logs/migrate.log" >&2; false; }
tail -1 "$STATE/logs/migrate.log"

echo "==> 配置运行角色 aegis_app（configure-app-role.sql，与 bootstrap.sh 同一份）"
docker exec -i -e PGPASSWORD="$PG_SUPER_PW" -e AEGIS_DB_APP_PASSWORD="$APP_PW" "$PG_CONTAINER" \
  psql -X -v ON_ERROR_STOP=1 -U "$PG_SUPER" -d "$PG_DB" -f - \
  < "$PANEL_DIR/deploy/configure-app-role.sql" >/dev/null

# ---------------------------------------------------------------------------
# 网关配置：只有 config.Load 要的键，外加冒烟需要的一个开关（放宽登录限流）
# ---------------------------------------------------------------------------
cat > "$STATE/gateway.env" <<EOF
AEGIS_ENV=test
AEGIS_DATABASE_URL=$APP_DSN
AEGIS_REDIS_URL=unix://:${VALKEY_PW}@${SOCK_VK}?db=0
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

# 给后续步骤的入口。迁移 DSN 只给造数据步骤查库用，网关进程从不拿到它
cat > "$STATE/smoke.env" <<EOF
SMOKE_PUBLIC_BASE=http://$PUB_ADDR
SMOKE_ADMIN_BASE=http://$ADM_ADDR
SMOKE_NODE_BASE=http://$NODE_ADDR
SMOKE_ADMIN_EMAIL=$ADMIN_EMAIL
SMOKE_ADMIN_PASSWORD=$ADMIN_PASS
SMOKE_MIGRATION_DSN=$MIGRATION_DSN
SMOKE_PG_CONTAINER=$PG_CONTAINER
SMOKE_PG_DB=$PG_DB
EOF
chmod 600 "$STATE/smoke.env"

trap - ERR
echo "==> 冒烟栈已就绪：$STATE/smoke.env"
