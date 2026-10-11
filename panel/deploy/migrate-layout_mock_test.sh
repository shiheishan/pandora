#!/usr/bin/env bash
# migrate.sh 自己挑迁移目录：不设 AEGIS_MIGRATIONS_DIR 时只取与 deploy/ 并排的 migrations/
# （安装目录 /opt/pandora 与源码树都是这个布局），旁边没有就失败、绝不去别处借；显式设置优先。
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-migrate-layout.XXXXXX")"
# 规范化（TMPDIR 常以 / 结尾），与 migrate.sh 里 cd && pwd 得到的路径逐字可比
TMP="$(cd "$TMP" && pwd -P)"
trap 'rm -rf -- "$TMP"' EXIT

mkdir -p "$TMP/bin"
cat >"$TMP/env" <<'ENV'
AEGIS_MIGRATION_DATABASE_URL=host=127.0.0.1 port=5432 user=test dbname=test sslmode=disable
ENV
# 桩 goose 只记下 migrate.sh 交给它的迁移目录
cat >"$TMP/bin/goose" <<'MOCK'
#!/usr/bin/env bash
printf '%s\n' "$GOOSE_MIGRATION_DIR" >"$(dirname "$0")/../goose.dir"
MOCK
chmod 0755 "$TMP/bin/goose"

# 在 prefix 下铺一套安装布局：deploy/ 与 migrations/ 并排
install_layout() {
  mkdir -p "$1/deploy" "$1/migrations"
  cp "$ROOT/deploy/migrate.sh" "$1/deploy/"
  cp "$ROOT"/migrations/*.sql "$1/migrations/"
}

# 不设 AEGIS_MIGRATIONS_DIR，看 migrate.sh 自己挑哪个目录
resolved_dir() {
  rm -f "$TMP/goose.dir"
  env -u AEGIS_MIGRATIONS_DIR AEGIS_ENV_FILE="$TMP/env" GOOSE_BIN="$TMP/bin/goose" \
    bash "$1/deploy/migrate.sh" status >/dev/null 2>&1 || true
  cat "$TMP/goose.dir" 2>/dev/null || true
}

# 安装目录：读到的是自己旁边的迁移
install_layout "$TMP/opt/pandora"
got="$(resolved_dir "$TMP/opt/pandora")"
[ "$got" = "$TMP/opt/pandora/migrations" ] || { echo "install resolved to '$got'" >&2; exit 1; }

# 旁边没有 migrations/ 就失败，绝不去别的安装目录借
install_layout "$TMP/opt/orphan"
rm -rf "$TMP/opt/orphan/migrations"
set +e
env -u AEGIS_MIGRATIONS_DIR AEGIS_ENV_FILE="$TMP/env" GOOSE_BIN="$TMP/bin/goose" \
  bash "$TMP/opt/orphan/deploy/migrate.sh" status >"$TMP/orphan.out" 2>&1
status=$?
set -e
[ "$status" -ne 0 ] || { echo 'missing sibling migrations/ did not fail' >&2; exit 1; }
grep -q 'missing migrations directory' "$TMP/orphan.out" || { cat "$TMP/orphan.out" >&2; exit 1; }

# 显式设置仍然优先
mkdir -p "$TMP/elsewhere"
cp "$ROOT"/migrations/*.sql "$TMP/elsewhere/"
rm -f "$TMP/goose.dir"
AEGIS_MIGRATIONS_DIR="$TMP/elsewhere" AEGIS_ENV_FILE="$TMP/env" GOOSE_BIN="$TMP/bin/goose" \
  bash "$TMP/opt/pandora/deploy/migrate.sh" status >/dev/null 2>&1 || true
[ "$(cat "$TMP/goose.dir")" = "$TMP/elsewhere" ] || { echo 'AEGIS_MIGRATIONS_DIR override ignored' >&2; exit 1; }

echo 'migrate migrations-directory layout: PASS'
