#!/usr/bin/env bash
# 数据库布局判定（pandora_db_layout）在每个运维脚本里各带一份：这些脚本有的以 root 在加固的 systemd 单元里跑、
# 只信任自己（备份、校验、恢复），不 source 共用文件。这里核对每一份逐字一致——改判定规则要一起改。
# 规则：.env 的 PANDORA_DB_LAYOUT=native|docker 为准；没有这一键时，有 POSTGRES_SUPER_PASSWORD（只有
# install-native.sh 写它）判为 native，否则 docker；别的值拒绝。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'pg-layout: %s\n' "$*" >&2; exit 1; }
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-pg-layout.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
SCRIPTS=(check-migrations.sh migrate.sh backup-postgres.sh verify-backup.sh restore-postgres.sh psql.sh bootstrap.sh)

extract() { awk '/^pandora_db_layout\(\) \{$/ { p = 1 } p { print } p && /^}$/ { exit }' "$DEPLOY/$1"; }
reference="$(extract "${SCRIPTS[0]}")"
[ -n "$reference" ] || fail "${SCRIPTS[0]} has no pandora_db_layout"
for script in "${SCRIPTS[@]}"; do
  got="$(extract "$script")"
  [ -n "$got" ] || fail "$script has no pandora_db_layout"
  [ "$got" = "$reference" ] || fail "$script's pandora_db_layout differs from ${SCRIPTS[0]}'s"
done

# --- migrate.sh 没有迁移 DSN、经批准回退到本机时：直装以 postgres 超级用户连，docker 布局以 POSTGRES_USER ---
mkdir -p "$T/bin" "$T/migrations"
printf '%s\n' '-- +goose Up' 'SELECT 1;' >"$T/migrations/00001_a.sql"
cat >"$T/bin/goose" <<'MOCK'
#!/usr/bin/env bash
printf 'DSN=%s PW=%s\n' "$GOOSE_DBSTRING" "${PGPASSWORD:-}" >"$(dirname "$0")/../goose.env"
MOCK
chmod 0755 "$T/bin/goose"
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5432\nPOSTGRES_SUPER_PASSWORD=super-pw\n' >"$T/native.env"
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5433\n' >"$T/docker.env"
fallback() {
  AEGIS_ENV_FILE="$T/$1.env" AEGIS_MIGRATIONS_DIR="$T/migrations" GOOSE_BIN="$T/bin/goose" \
    PANDORA_LOCAL_MIGRATION_APPROVED=yes bash "$DEPLOY/migrate.sh" version >/dev/null 2>&1 \
    || fail "migrate.sh version failed for the $1 layout"
  cat "$T/goose.env"
}
[ "$(fallback native)" = 'DSN=host=127.0.0.1 port=5432 user=postgres dbname=aegis sslmode=disable PW=super-pw' ] \
  || fail "native fallback: $(cat "$T/goose.env")"
[ "$(fallback docker)" = 'DSN=host=127.0.0.1 port=5433 user=aegis dbname=aegis sslmode=disable PW=owner-pw' ] \
  || fail "docker fallback: $(cat "$T/goose.env")"

# --- 备份、校验、恢复三件套：连库一律经 pandora_pg（三份逐字一致），别处不许直接 docker exec ---
extract_fn() { awk -v fn="$2" '$0 == fn "() {" { p = 1 } p { print } p && /^}$/ { exit }' "$DEPLOY/$1"; }
TRIO=(backup-postgres.sh verify-backup.sh restore-postgres.sh)
for fn in pandora_pg pandora_pg_require pandora_pg_require_login; do
  ref="$(extract_fn "${TRIO[0]}" "$fn")"
  [ -n "$ref" ] || fail "${TRIO[0]} has no $fn"
  for script in "${TRIO[@]}"; do
    [ "$(extract_fn "$script" "$fn")" = "$ref" ] || fail "$script's $fn differs from ${TRIO[0]}'s"
  done
done
for script in "${TRIO[@]}"; do
  n="$(grep -c 'docker exec' "$DEPLOY/$script")"
  [ "$n" -eq 1 ] || fail "$script runs docker exec outside pandora_pg ($n occurrences)"
  if grep -Eq 'export PGPASSWORD|PGPASSWORD="\$POSTGRES_PASSWORD" *$' "$DEPLOY/$script"; then
    fail "$script still exports PGPASSWORD for the whole script"
  fi
done

# pandora_pg 的行为：直装走本机客户端、回环、postgres 超级用户；docker 走容器、POSTGRES_USER。口令都只在环境里
cat >"$T/bin/pg_dump" <<'MOCK'
#!/usr/bin/env bash
printf 'pg_dump %s | PGHOST=%s PGPORT=%s PGUSER=%s PGPASSWORD=%s PGSSLMODE=%s\n' "$*" \
  "${PGHOST:-}" "${PGPORT:-}" "${PGUSER:-}" "${PGPASSWORD:-}" "${PGSSLMODE:-}" >"$(dirname "$0")/../pg.calls"
MOCK
cat >"$T/bin/docker" <<'MOCK'
#!/usr/bin/env bash
printf 'docker %s | PGPASSWORD=%s\n' "$*" "${PGPASSWORD:-}" >"$(dirname "$0")/../pg.calls"
MOCK
chmod 0755 "$T/bin/pg_dump" "$T/bin/docker"
eval "$(extract_fn backup-postgres.sh pandora_pg)"
(
  PATH="$T/bin:$PATH"; DB_LAYOUT=native POSTGRES_SUPER_PASSWORD=super-pw POSTGRES_PORT=5434 POSTGRES_PASSWORD=owner-pw
  pandora_pg pg_dump -d aegis --format=custom
)
[ "$(cat "$T/pg.calls")" = 'pg_dump -d aegis --format=custom | PGHOST=127.0.0.1 PGPORT=5434 PGUSER=postgres PGPASSWORD=super-pw PGSSLMODE=disable' ] \
  || fail "native pandora_pg: $(cat "$T/pg.calls")"
(
  PATH="$T/bin:$PATH"; DB_LAYOUT=docker POSTGRES_USER=aegis POSTGRES_PASSWORD=owner-pw
  pandora_pg pg_dump -d aegis --format=custom
)
[ "$(cat "$T/pg.calls")" = 'docker exec -i -e PGPASSWORD aegis-postgres pg_dump -U aegis -d aegis --format=custom | PGPASSWORD=owner-pw' ] \
  || fail "docker pandora_pg: $(cat "$T/pg.calls")"

# --- psql.sh 与 bootstrap.sh：在临时 deploy/ 里真跑一遍，客户端换成桩 ---
for layout in native docker; do
  mkdir -p "$T/$layout"
  cp "$DEPLOY/psql.sh" "$DEPLOY/bootstrap.sh" "$DEPLOY/configure-app-role.sql" "$T/$layout/"
done
app_pw="$(printf 'a%.0s' $(seq 1 40))"
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5434\nPOSTGRES_SUPER_PASSWORD=super-pw\nAEGIS_DB_APP_PASSWORD=%s\n' "$app_pw" >"$T/native/.env"
printf 'POSTGRES_USER=aegis\nPOSTGRES_PASSWORD=owner-pw\nPOSTGRES_DB=aegis\nPOSTGRES_PORT=5433\nAEGIS_DB_APP_PASSWORD=%s\n' "$app_pw" >"$T/docker/.env"
cat >"$T/bin/psql" <<'MOCK'
#!/usr/bin/env bash
root="$(dirname "$0")/.."
printf 'psql %s | PGPASSWORD=%s APP=%s OWNER=%s\n' "$*" "${PGPASSWORD:-}" "${AEGIS_DB_APP_PASSWORD:+set}" "${POSTGRES_PASSWORD:+leaked}" >"$root/pg.calls"
[ -t 0 ] || cat >"$root/pg.stdin"
MOCK
cat >"$T/bin/docker" <<'MOCK'
#!/usr/bin/env bash
root="$(dirname "$0")/.."
printf 'docker %s | PGPASSWORD=%s APP=%s\n' "$*" "${PGPASSWORD:-}" "${AEGIS_DB_APP_PASSWORD:+set}" >"$root/pg.calls"
[ -t 0 ] || cat >"$root/pg.stdin"
MOCK
chmod 0755 "$T/bin/psql" "$T/bin/docker"
PATH="$T/bin:$PATH" bash "$T/native/psql.sh" -X -tAc 'SELECT 1' </dev/null
[ "$(cat "$T/pg.calls")" = 'psql -h 127.0.0.1 -p 5434 -U postgres -d aegis -X -tAc SELECT 1 | PGPASSWORD=super-pw APP= OWNER=' ] \
  || fail "native psql.sh: $(cat "$T/pg.calls")"
PATH="$T/bin:$PATH" bash "$T/docker/psql.sh" -X -tAc 'SELECT 1' </dev/null
[ "$(cat "$T/pg.calls")" = 'docker exec -i -e PGPASSWORD aegis-postgres psql -U aegis -d aegis -X -tAc SELECT 1 | PGPASSWORD=owner-pw APP=' ] \
  || fail "docker psql.sh: $(cat "$T/pg.calls")"
PATH="$T/bin:$PATH" bash "$T/native/bootstrap.sh" >/dev/null
[ "$(cat "$T/pg.calls")" = 'psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p 5434 -U postgres -d aegis | PGPASSWORD=super-pw APP=set OWNER=' ] \
  || fail "native bootstrap.sh: $(cat "$T/pg.calls")"
cmp -s "$T/pg.stdin" "$DEPLOY/configure-app-role.sql" || fail 'native bootstrap.sh did not feed configure-app-role.sql'
PATH="$T/bin:$PATH" bash "$T/docker/bootstrap.sh" >/dev/null
[ "$(cat "$T/pg.calls")" = 'docker exec -i -e PGPASSWORD -e AEGIS_DB_APP_PASSWORD aegis-postgres psql -X -v ON_ERROR_STOP=1 -U aegis -d aegis | PGPASSWORD=owner-pw APP=set' ] \
  || fail "docker bootstrap.sh: $(cat "$T/pg.calls")"
cmp -s "$T/pg.stdin" "$DEPLOY/configure-app-role.sql" || fail 'docker bootstrap.sh did not feed configure-app-role.sql'

# 判定本身
eval "$reference"
layout() { ( unset PANDORA_DB_LAYOUT POSTGRES_SUPER_PASSWORD; eval "$1"; pandora_db_layout ); }
[ "$(layout '')" = docker ] || fail 'empty env is not docker'
[ "$(layout 'POSTGRES_SUPER_PASSWORD=x')" = native ] || fail 'a native .env without the key is not native'
[ "$(layout 'PANDORA_DB_LAYOUT=docker POSTGRES_SUPER_PASSWORD=x')" = docker ] || fail 'explicit docker is not honoured'
[ "$(layout 'PANDORA_DB_LAYOUT=native')" = native ] || fail 'explicit native is not honoured'
if layout 'PANDORA_DB_LAYOUT=k8s' >/dev/null; then fail 'an unknown layout was accepted'; fi

printf 'pg-layout mock: PASS\n'
