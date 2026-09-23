#!/usr/bin/env bash
# [INPUT]: 依赖同目录 migrate.sh、../migrations/*.sql，goose 用桩脚本代替
# [OUTPUT]: 迁移目录定位契约：不设 AEGIS_MIGRATIONS_DIR 时只认与 deploy/ 并排的 migrations/
# [POS]: deploy 的桩测试，与 migrate_fail_closed_mock_test.sh 并列；不需要数据库或 root
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
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

# 在 prefix 下铺一套 install.sh / install-native.sh 的布局：deploy/ 与 migrations/ 并排
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

# 同一台机器上两套安装并存：每一套都只能读到自己旁边的迁移
install_layout "$TMP/opt/aegispanel"
install_layout "$TMP/opt/pandora"
for prefix in "$TMP/opt/aegispanel" "$TMP/opt/pandora"; do
  got="$(resolved_dir "$prefix")"
  [ "$got" = "$prefix/migrations" ] || { echo "layout $prefix resolved to '$got'" >&2; exit 1; }
done

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
  bash "$TMP/opt/aegispanel/deploy/migrate.sh" status >/dev/null 2>&1 || true
[ "$(cat "$TMP/goose.dir")" = "$TMP/elsewhere" ] || { echo 'AEGIS_MIGRATIONS_DIR override ignored' >&2; exit 1; }

echo 'migrate migrations-directory layout: PASS'
