#!/usr/bin/env bash
# [INPUT]: 依赖 docker（postgres:18-alpine、valkey/valkey:8-alpine）、goose、go、openssl、curl、python3，同目录 configure-app-role.sql，../migrations，../cmd 下的网关源码
# [OUTPUT]: up 起一套一次性的真实面板栈（PG18 + Valkey + aegis-public + aegis-admin + 一个平台管理员），把地址与账号写进 <状态目录>/smoke.env；down 拆掉
# [POS]: 第 4 阶段联调冒烟的底座，被 .github/workflows/panel-smoke.yml 调用，之后的造数据与 frontend/tests/smoke 都读 smoke.env；起库做法照 run-pg18-gates.sh，运行角色照 bootstrap.sh
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
#
# 起一套只活一次的真实面板，给前端冒烟用。
#
# 为什么不复用 docker-compose.yml：那是开发机的长期数据基座，固定端口、带数据卷、
# 读 deploy/.env；冒烟要的是「每次从空库起、跑完就扔、配置现场生成」。
#
# 配置全部在这里用 openssl 现场生成，只活在 runner 上这一次：
# 仓库是公开的，冒烟里不能出现任何真实部署的值（docs/CONSTRAINTS.md 第 11 条）。
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
PG_SUPER=aegis
PG_SUPER_PW="$(rand_hex 24)"
# bootstrap.sh 要求运行角色口令至少 32 位 URL-safe 字符，这里照同一标准
APP_PW="$(rand_hex 24)"
VALKEY_PW="$(rand_hex 24)"
ADMIN_EMAIL="smoke-admin@example.test"
ADMIN_PASS="Smoke-$(rand_hex 16)"

echo "==> 起 PostgreSQL 18 与 Valkey 8"
echo "$PG_CONTAINER" >> "$STATE/containers"
docker run -d --name "$PG_CONTAINER" \
  -e POSTGRES_PASSWORD="$PG_SUPER_PW" -e POSTGRES_USER="$PG_SUPER" -e POSTGRES_DB=aegis \
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

PG_PORT="$(docker inspect -f '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' "$PG_CONTAINER")"
VK_PORT="$(docker inspect -f '{{(index (index .NetworkSettings.Ports "6379/tcp") 0).HostPort}}' "$VK_CONTAINER")"
MIGRATION_DSN="postgres://${PG_SUPER}:${PG_SUPER_PW}@127.0.0.1:${PG_PORT}/aegis?sslmode=disable"
APP_DSN="postgres://aegis_app:${APP_PW}@127.0.0.1:${PG_PORT}/aegis?sslmode=disable"
echo "    $(docker exec "$PG_CONTAINER" psql -U "$PG_SUPER" -d aegis -tAc 'SELECT version()' | cut -d, -f1)"

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
  psql -X -v ON_ERROR_STOP=1 -U "$PG_SUPER" -d aegis -f - \
  < "$PANEL_DIR/deploy/configure-app-role.sql" >/dev/null

# ---------------------------------------------------------------------------
# 网关配置：只有 config.Load 要的键，外加冒烟需要的两个开关
# ---------------------------------------------------------------------------
cat > "$STATE/gateway.env" <<EOF
AEGIS_ENV=test
AEGIS_DATABASE_URL=$APP_DSN
AEGIS_REDIS_URL=redis://:${VALKEY_PW}@127.0.0.1:${VK_PORT}/0
AEGIS_PUBLIC_ADDR=$PUB_ADDR
AEGIS_ADMIN_ADDR=$ADM_ADDR
AEGIS_PUBLIC_BASE_URL=http://$PUB_ADDR
AEGIS_MASTER_KEY=$(rand_b64)
AEGIS_JWT_PUBLIC_SECRET=$(rand_b64)
AEGIS_JWT_ADMIN_SECRET=$(rand_b64)
AEGIS_JWT_CLIENT_SECRET=$(rand_b64)
AEGIS_CONFIG_SIGNING_SEED=$(rand_b64)
AEGIS_SALES_ENABLED=1
AEGIS_RL_AUTH_PER_MIN=1000
EOF
chmod 600 "$STATE/gateway.env"

echo "==> 编译网关（CGO_ENABLED=0，与发布包一致）"
( cd "$PANEL_DIR" && CGO_ENABLED=0 go build -mod=readonly -o "$STATE/bin/" \
    ./cmd/aegis-public ./cmd/aegis-admin ./cmd/aegis-adminctl )

echo "==> aegis-adminctl 建平台管理员"
( set -a; . "$STATE/gateway.env"; set +a
  printf '%s\n' "$ADMIN_PASS" | "$STATE/bin/aegis-adminctl" create \
    --email "$ADMIN_EMAIL" --password-stdin --role platform_admin
) > "$STATE/logs/adminctl.log" 2>&1 || { cat "$STATE/logs/adminctl.log" >&2; false; }

echo "==> 启动 aegis-public 与 aegis-admin"
start_gateway() {
  local name="$1"
  ( set -a; . "$STATE/gateway.env"; set +a
    exec "$STATE/bin/$name" ) > "$STATE/logs/$name.log" 2>&1 &
  echo $! >> "$STATE/pids"
}
start_gateway aegis-public
start_gateway aegis-admin

# ---------------------------------------------------------------------------
# 健康检查：readyz 连库连缓存；再用管理员真登录一次，证明 adminctl 建的号能用
# ---------------------------------------------------------------------------
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
wait_ready "http://$PUB_ADDR" aegis-public
wait_ready "http://$ADM_ADDR" aegis-admin

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
SMOKE_ADMIN_EMAIL=$ADMIN_EMAIL
SMOKE_ADMIN_PASSWORD=$ADMIN_PASS
SMOKE_MIGRATION_DSN=$MIGRATION_DSN
SMOKE_PG_CONTAINER=$PG_CONTAINER
EOF
chmod 600 "$STATE/smoke.env"

trap - ERR
echo "==> 冒烟栈已就绪：$STATE/smoke.env"
