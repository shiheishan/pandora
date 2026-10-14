#!/usr/bin/env bash
# 真 PG18 上走一遍安装器的数据库三步：bootstrap.sh roles → pandora migrate up（pandora_owner）→ bootstrap.sh grants，
# 整套再跑一遍（幂等）；然后核运行角色 pandora_app 的权限，并以它跑 production 的 pandora admin 与 serve。
# 证明：迁移不需要超级用户；configure-app-role.sql 收敛后运行角色够用（读写普通表、调保留期清理函数、readyz 通过）
# 且不多给（不能建表、建临时表、改删只追加表、读迁移版本表）。
#
# 前提（CI 的 deploy-db-roles-pg18 job 准备好）：本机装了 postgresql-18 且 18/main 在线、有 sudo、有 docker（起 Redis）、
# PANDORA_BIN 指向编好的 pandora（缺省 $RUNNER_TEMP/pandora）。口令现生成、只写进 0600 的临时文件，不打印。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
BIN="${PANDORA_BIN:-${RUNNER_TEMP:-/tmp}/pandora}"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-db-roles.XXXXXX")"
REDIS_NAME="pandora-db-roles-redis-$$"
SERVE_PID=""
cleanup() {
  [ -z "$SERVE_PID" ] || kill "$SERVE_PID" 2>/dev/null || true
  docker rm -f "$REDIS_NAME" >/dev/null 2>&1 || true
  rm -rf -- "$T"
}
trap cleanup EXIT
fail() { printf 'db roles pg18: %s\n' "$*" >&2; exit 1; }

[ -x "$BIN" ] || fail "no pandora binary at $BIN (set PANDORA_BIN)"
command -v docker >/dev/null 2>&1 || fail 'needs docker for Redis'
command -v pg_lsclusters >/dev/null 2>&1 || fail 'needs postgresql-common (pg_lsclusters)'
port="$(pg_lsclusters --no-header | awk '$1 == 18 && $2 == "main" && $4 ~ /^online/ { print $3 }')"
[ -n "$port" ] || fail 'PostgreSQL 18/main is not online'

etc="$T/etc"
mkdir -p "$etc" "$T/pdnd-dist" "$T/backups"
gen() { openssl rand -hex 24; }
app_pw="$(gen)"; owner_pw="$(gen)"; super_pw="$(gen)"; redis_pw="$(gen)"
(
  umask 077
  printf '%s\n' \
    "PANDORA_DATABASE_URL=postgres://pandora_app:${app_pw}@127.0.0.1:${port}/pandora?sslmode=disable" \
    "PANDORA_REDIS_URL=redis://:${redis_pw}@127.0.0.1:6379/0" \
    "PANDORA_SECRETS_MASTER_KEY=$(openssl rand -base64 32)" \
    "PANDORA_AUTH_PUBLIC_JWT_SECRET=$(openssl rand -hex 32)" \
    "PANDORA_AUTH_ADMIN_JWT_SECRET=$(openssl rand -hex 32)" >"$etc/secrets.env"
  printf '%s\n' \
    "POSTGRES_PORT=$port" \
    "POSTGRES_DB=pandora" \
    "POSTGRES_SUPER_PASSWORD=$super_pw" \
    "PANDORA_DATABASE_MIGRATION_URL=postgres://pandora_owner:${owner_pw}@127.0.0.1:${port}/pandora?sslmode=disable" \
    "PANDORA_ADMIN_PATH=ops_0123456789abcdef0123456789abcdef" \
    "PANDORA_SYSTEMD_HARDENING=1" >"$etc/deploy.env"
)
# 与 install.sh 生成的同形，只把路径换到临时目录、gRPC 只绑回环
printf '%s\n' \
  'env: production' \
  'public_base_url: https://panel.example.test' \
  'http:' \
  '  public:' \
  '    addr: 127.0.0.1:9000' \
  '  admin:' \
  '    addr: 127.0.0.1:9001' \
  'grpc:' \
  '  node:' \
  '    addr: "127.0.0.1:9443"' \
  '    public_host: panel.example.test' \
  'pdnd:' \
  "  dist_dir: $T/pdnd-dist" \
  'geoip:' \
  "  ipv4_db: $T/no-geoip.xdb" \
  'backup:' \
  "  dir: $T/backups" \
  'log:' \
  '  level: info' >"$etc/config.yaml"

# Redis：口令写进挂载的配置文件，不进 docker 的命令行
( umask 022; printf 'requirepass %s\n' "$redis_pw" >"$T/redis.conf" )
docker run -d --name "$REDIS_NAME" -p 127.0.0.1:6379:6379 -v "$T/redis.conf:/etc/redis.conf:ro" \
  redis:7.0.15 redis-server /etc/redis.conf >/dev/null

# 逐行导出 KEY=VALUE，不 source
load_env() {
  local line
  while IFS= read -r line; do
    if [[ "$line" =~ ^([A-Z][A-Z0-9_]*)=(.*)$ ]]; then export "${BASH_REMATCH[1]}=${BASH_REMATCH[2]}"; fi
  done <"$1"
}
pandora() { "$BIN" --config "$etc/config.yaml" "$@"; }

# 1. 角色与库 → 迁移（pandora_owner）→ 收敛；整套再跑一遍（幂等）
for round in 1 2; do
  sudo env PANDORA_ETC_DIR="$etc" bash "$DEPLOY/bootstrap.sh" roles || fail "bootstrap roles (round $round)"
  ( load_env "$etc/secrets.env"; load_env "$etc/deploy.env"; pandora migrate up ) || fail "migrate up as pandora_owner (round $round)"
  sudo env PANDORA_ETC_DIR="$etc" bash "$DEPLOY/bootstrap.sh" grants || fail "bootstrap grants (round $round)"
done

# 2. 运行角色的权限
as_app() { PGPASSWORD="$app_pw" psql -X -q -t -A -v ON_ERROR_STOP=1 -h 127.0.0.1 -p "$port" -U pandora_app -d pandora "$@"; }
must_deny() {
  local what="$1" sql="$2"
  if as_app -c "$sql" >/dev/null 2>"$T/err"; then fail "pandora_app could $what"; fi
  grep -qi 'permission denied' "$T/err" || { cat "$T/err" >&2; fail "unexpected error when checking: $what"; }
}
[ "$(as_app -c 'SHOW search_path')" = 'pg_catalog, public, pg_temp' ] || fail "search_path: $(as_app -c 'SHOW search_path')"
[ "$(as_app -c 'SHOW statement_timeout')" = 15s ] || fail "statement_timeout: $(as_app -c 'SHOW statement_timeout')"
[ "$(as_app -c 'SHOW jit')" = off ] || fail 'jit is not off for pandora_app'
must_deny 'create a table' 'CREATE TABLE ci_probe (i int)'
must_deny 'create a temp table' 'CREATE TEMP TABLE ci_probe (i int)'
must_deny 'update an append-only table' 'UPDATE ledger_entries SET id = id WHERE false'
must_deny 'delete from an append-only table' 'DELETE FROM ledger_entries WHERE false'
must_deny 'read the migration version table' 'SELECT count(*) FROM goose_db_version'
as_app -c 'SELECT count(*) FROM orders' >/dev/null || fail 'pandora_app cannot read orders'
for fn in purge_audit_events purge_unverified_payment_events purge_subscription_fetch_log \
  purge_node_traffic_reports purge_node_metrics; do
  as_app -c "SELECT $fn(now(), 1)" >/dev/null || fail "pandora_app cannot run $fn"
done

# 3. production 的 pandora 以运行角色起来：has-admin 先 3、建号（口令走标准输入）后 0；serve 两个 readyz
load_env "$etc/secrets.env"
rc=0; pandora admin has-admin >/dev/null 2>&1 || rc=$?
[ "$rc" = 3 ] || fail "has-admin before create: exit $rc, want 3"
openssl rand -hex 16 | pandora admin create --email admin@example.test --password-stdin >/dev/null || fail 'admin create'
pandora admin has-admin >/dev/null || fail 'has-admin after create'
pandora serve >"$T/serve.log" 2>&1 &
SERVE_PID=$!
ok=""
for _ in $(seq 1 100); do
  if curl -fsS -o /dev/null http://127.0.0.1:9000/readyz && curl -fsS -o /dev/null http://127.0.0.1:9001/readyz; then ok=1; break; fi
  kill -0 "$SERVE_PID" 2>/dev/null || break
  sleep 0.2
done
if [ -z "$ok" ]; then
  tail -50 "$T/serve.log" >&2
  fail 'pandora serve (production, pandora_app) did not become ready'
fi
kill -TERM "$SERVE_PID"
wait "$SERVE_PID" || true
SERVE_PID=""

printf 'db roles on PG18: PASS\n'
