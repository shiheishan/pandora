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
SCRIPTS=(check-migrations.sh migrate.sh)

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

# 判定本身
eval "$reference"
layout() { ( unset PANDORA_DB_LAYOUT POSTGRES_SUPER_PASSWORD; eval "$1"; pandora_db_layout ); }
[ "$(layout '')" = docker ] || fail 'empty env is not docker'
[ "$(layout 'POSTGRES_SUPER_PASSWORD=x')" = native ] || fail 'a native .env without the key is not native'
[ "$(layout 'PANDORA_DB_LAYOUT=docker POSTGRES_SUPER_PASSWORD=x')" = docker ] || fail 'explicit docker is not honoured'
[ "$(layout 'PANDORA_DB_LAYOUT=native')" = native ] || fail 'explicit native is not honoured'
if layout 'PANDORA_DB_LAYOUT=k8s' >/dev/null; then fail 'an unknown layout was accepted'; fi

printf 'pg-layout mock: PASS\n'
