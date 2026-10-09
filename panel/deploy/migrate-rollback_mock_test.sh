#!/usr/bin/env bash
# migrate.sh 的 rollback-to 与预检凭据接线：不需要数据库，goose、systemctl、check-migrations.sh 都是桩。
#   - rollback-to 的每道前置条件（写入者已停、备份、版本、irreversible 预扫描、确认短语）
#     缺一样就以 78 拒绝，且一个 Down 都不执行；
#   - 成功路径从高到低逐个 down、每步核对版本；某个 Down 失败就停在那里；
#   - up 带凭据时只做停服后的核对（--verify-attestation），不带时照旧完整预检。
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-migrate-rollback.XXXXXX")"
TMP="$(cd "$TMP" && pwd -P)"
trap 'rm -rf -- "$TMP"' EXIT

mkdir -p "$TMP/bin" "$TMP/app/deploy" "$TMP/app/migrations"
cp "$ROOT/deploy/migrate.sh" "$TMP/app/deploy/migrate.sh"
cat >"$TMP/env" <<'ENV'
AEGIS_MIGRATION_DATABASE_URL=host=127.0.0.1 port=5432 user=test dbname=test sslmode=disable
ENV
printf 'backup bytes\n' >"$TMP/backup.dump"

# 迁移 1、2、3、5（4 是空号），3 号的 Down 由桩判为失败时用 MOCK-DOWN-FAIL 标出
write_migration() {
  local file="$1" header="${2:-}"
  {
    [ -z "$header" ] || printf '%s\n' "$header"
    printf '%s\n' '-- +goose Up' 'SELECT 1;' '-- +goose Down' 'SELECT 1;'
  } >"$TMP/app/migrations/$file"
}
reset_migrations() {
  rm -f "$TMP/app/migrations"/*.sql
  write_migration 00001_a.sql
  write_migration 00002_b.sql
  write_migration 00003_c.sql
  write_migration 00005_e.sql
}

# goose 桩：版本存在 $TMP/db.version；down 把版本退到下一个更低的迁移文件
cat >"$TMP/bin/goose" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s PGOPTIONS=%s\n' "$*" "${PGOPTIONS:-}" >>"$root/goose.exec"
current="$(cat "$root/db.version")"
case "${1:-}" in
  version) echo "2026/10/07 12:00:00 goose: version $current" >&2 ;;
  down)
    file="$(ls "$GOOSE_MIGRATION_DIR" | grep -E "^$(printf '%05d' "$current")_" || true)"
    [ -n "$file" ] || { echo "goose: no file for $current" >&2; exit 1; }
    if grep -q 'MOCK-DOWN-FAIL' "$GOOSE_MIGRATION_DIR/$file"; then
      echo "goose: ERROR $file: rollback refused" >&2
      exit 1
    fi
    lower=0
    for f in "$GOOSE_MIGRATION_DIR"/*.sql; do
      v=$((10#$(basename "$f" | cut -c1-5)))
      [ "$v" -lt "$current" ] && [ "$v" -gt "$lower" ] && lower=$v
    done
    printf '%s\n' "$lower" >"$root/db.version"
    echo "goose: OK $file" >&2
    ;;
  up) echo "goose: up" >&2 ;;
  *) exit 0 ;;
esac
MOCK
# systemctl 桩：$TMP/active.<unit> 存在即在运行
cat >"$TMP/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
[ "${1:-}" = is-active ] || exit 0
test -f "$root/active.${!#}"
MOCK
# check-migrations.sh 桩：记下参数，按 $TMP/precheck.status 返回
cat >"$TMP/app/deploy/check-migrations.sh" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/../.." && pwd)"
printf 'args=%s approved=%s\n' "$*" "${PANDORA_STOPPED_WRITER_UPGRADE_APPROVED:-}" >>"$root/precheck.log"
exit "$(cat "$root/precheck.status" 2>/dev/null || echo 0)"
MOCK
# psql 桩：up 前后的无效索引检查（migrate.sh「无效索引护栏」）查到的是干净的库
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$TMP/bin/psql"
chmod 0755 "$TMP/bin/goose" "$TMP/bin/systemctl" "$TMP/app/deploy/check-migrations.sh" "$TMP/bin/psql"

run_migrate() {
  PATH="$TMP/bin:$PATH" AEGIS_ENV_FILE="$TMP/env" GOOSE_BIN="$TMP/bin/goose" \
    bash "$TMP/app/deploy/migrate.sh" "$@" </dev/null
}
# 用法：expect_refusal <标签> <期望报错> <环境变量…> -- <migrate 参数…>
expect_refusal() {
  local label="$1" needle="$2" status
  shift 2
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  : >"$TMP/goose.exec"
  set +e
  env "${envs[@]}" PATH="$TMP/bin:$PATH" AEGIS_ENV_FILE="$TMP/env" GOOSE_BIN="$TMP/bin/goose" \
    bash "$TMP/app/deploy/migrate.sh" "$@" </dev/null >"$TMP/$label.out" 2>&1
  status=$?
  set -e
  [ "$status" -eq 78 ] || { echo "$label: expected 78, got $status" >&2; cat "$TMP/$label.out" >&2; exit 1; }
  grep -Fq -- "$needle" "$TMP/$label.out" \
    || { echo "$label: missing '$needle'" >&2; cat "$TMP/$label.out" >&2; exit 1; }
  if grep -q '^down' "$TMP/goose.exec"; then
    echo "$label: a Down was executed despite the refusal" >&2
    exit 1
  fi
}

STOPPED=PANDORA_ROLLBACK_WRITERS_STOPPED=yes
BACKUP="PANDORA_ROLLBACK_BACKUP=$TMP/backup.dump"
reset_migrations
printf '%s\n' 5 >"$TMP/db.version"

# ---- 前置条件 ----
expect_refusal bad-arg 'requires one canonical non-negative version' "$STOPPED" -- rollback-to 03
expect_refusal no-arg 'requires one canonical non-negative version' "$STOPPED" -- rollback-to
expect_refusal not-stopped 'PANDORA_ROLLBACK_WRITERS_STOPPED=yes' "$BACKUP" -- rollback-to 2
[ ! -s "$TMP/goose.exec" ] || { echo 'goose ran before the writer check' >&2; exit 1; }
: >"$TMP/active.aegis-public.service"
expect_refusal unit-active 'aegis-public.service is still active' "$STOPPED" "$BACKUP" -- rollback-to 2
rm -f "$TMP/active.aegis-public.service"
expect_refusal no-backup 'PANDORA_ROLLBACK_BACKUP must name' "$STOPPED" -- rollback-to 2
: >"$TMP/empty.dump"
expect_refusal empty-backup 'PANDORA_ROLLBACK_BACKUP must name' "$STOPPED" "PANDORA_ROLLBACK_BACKUP=$TMP/empty.dump" -- rollback-to 2
expect_refusal not-below 'must be below the current version 5' "$STOPPED" "$BACKUP" -- rollback-to 5
expect_refusal gap-target 'target 4 is not a migration version' "$STOPPED" "$BACKUP" -- rollback-to 4
expect_refusal no-confirm "confirmation required: set PANDORA_ROLLBACK_CONFIRM='rollback 5 to 2'" "$STOPPED" "$BACKUP" -- rollback-to 2
expect_refusal bad-confirm 'confirmation does not match' "$STOPPED" "$BACKUP" "PANDORA_ROLLBACK_CONFIRM=rollback 5 to 1" -- rollback-to 2

# 库里的版本在发布物里没有文件
printf '%s\n' 7 >"$TMP/db.version"
expect_refusal unknown-current 'no migration file for it' "$STOPPED" "$BACKUP" "PANDORA_ROLLBACK_CONFIRM=rollback 7 to 2" -- rollback-to 2
printf '%s\n' 5 >"$TMP/db.version"

# irreversible 预扫描：范围里有就一个都不执行，并说明最多能回到哪
write_migration 00003_c.sql '-- irreversible: seed only'
expect_refusal irreversible 'the furthest this tool can go is version 3' "$STOPPED" "$BACKUP" "PANDORA_ROLLBACK_CONFIRM=rollback 5 to 1" -- rollback-to 1
grep -Fq 'irreversible migrations in range: 00003_c.sql' "$TMP/irreversible.out"
grep -Fq 'nothing was executed' "$TMP/irreversible.out"
[ "$(cat "$TMP/db.version")" = 5 ]
write_migration 00005_e.sql '-- irreversible: data fix'
expect_refusal irreversible-current 'the current migration itself is irreversible' "$STOPPED" "$BACKUP" "PANDORA_ROLLBACK_CONFIRM=rollback 5 to 3" -- rollback-to 3
reset_migrations

# ---- 成功路径：5 → 3 → 2，跳过空号 4，不下发 PGOPTIONS ----
: >"$TMP/goose.exec"
env "$STOPPED" "$BACKUP" PANDORA_ROLLBACK_CONFIRM='rollback 5 to 2' PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes \
  PATH="$TMP/bin:$PATH" AEGIS_ENV_FILE="$TMP/env" GOOSE_BIN="$TMP/bin/goose" \
  bash "$TMP/app/deploy/migrate.sh" rollback-to 2 </dev/null >"$TMP/success.out" 2>&1 \
  || { echo 'rollback success path failed' >&2; cat "$TMP/success.out" >&2; exit 1; }
[ "$(cat "$TMP/db.version")" = 2 ]
[ "$(grep -c '^down' "$TMP/goose.exec")" -eq 2 ]
grep -Fq 'down 00005_e.sql' "$TMP/success.out"
grep -Fq 'down 00003_c.sql' "$TMP/success.out"
grep -Fq 'migration rollback complete: 5 -> 2' "$TMP/success.out"
if grep -E '^down' "$TMP/goose.exec" | grep -vq 'PGOPTIONS=$'; then
  echo 'rollback passed PGOPTIONS to goose' >&2
  exit 1
fi

# ---- 某个 Down 失败：停在它之前，不再往下 ----
printf '%s\n' 5 >"$TMP/db.version"
printf '%s\n' '-- +goose Up' 'SELECT 1;' '-- +goose Down' '-- MOCK-DOWN-FAIL' >"$TMP/app/migrations/00003_c.sql"
: >"$TMP/goose.exec"
set +e
env "$STOPPED" "$BACKUP" PANDORA_ROLLBACK_CONFIRM='rollback 5 to 1' \
  PATH="$TMP/bin:$PATH" AEGIS_ENV_FILE="$TMP/env" GOOSE_BIN="$TMP/bin/goose" \
  bash "$TMP/app/deploy/migrate.sh" rollback-to 1 </dev/null >"$TMP/stopped.out" 2>&1
stopped_status=$?
set -e
[ "$stopped_status" -eq 1 ] || { echo "failing Down expected exit 1, got $stopped_status" >&2; exit 1; }
grep -Fq 'STOPPED: the Down of 00003_c.sql failed or refused' "$TMP/stopped.out"
grep -Fq 'the database is at version 3' "$TMP/stopped.out"
[ "$(cat "$TMP/db.version")" = 3 ]
[ "$(grep -c '^down' "$TMP/goose.exec")" -eq 2 ]   # 5 成功、3 失败，2 没有尝试
reset_migrations

# 公开入口仍然拒绝 down / redo，并指向 rollback-to
expect_refusal public-down "use the confirmed 'rollback-to <version>'" -- down

# ---- up 与预检凭据 ----
printf '%s\n' 5 >"$TMP/db.version"
: >"$TMP/precheck.log"; : >"$TMP/goose.exec"
run_migrate up >"$TMP/up-full.out" 2>&1
grep -Fxq 'args= approved=' "$TMP/precheck.log"          # 没有凭据：完整预检
grep -q '^up' "$TMP/goose.exec"

: >"$TMP/precheck.log"; : >"$TMP/goose.exec"
PANDORA_PRECHECK_ATTESTATION="$TMP/att" PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes \
  run_migrate up >"$TMP/up-attested.out" 2>&1
grep -Fxq "args=--verify-attestation $TMP/att approved=yes" "$TMP/precheck.log"
[ "$(wc -l <"$TMP/precheck.log")" -eq 1 ]                 # 只核对，不再完整预检
grep -Fq 'migration precheck=attested' "$TMP/up-attested.out"
grep -q '^up' "$TMP/goose.exec"

printf '%s\n' 78 >"$TMP/precheck.status"
: >"$TMP/precheck.log"; : >"$TMP/goose.exec"
set +e
PANDORA_PRECHECK_ATTESTATION="$TMP/att" run_migrate up >"$TMP/up-refused.out" 2>&1
refused_status=$?
set -e
[ "$refused_status" -eq 78 ]
grep -Fq 'precheck attestation was not accepted; production is unchanged' "$TMP/up-refused.out"
[ ! -s "$TMP/goose.exec" ] || { echo 'goose ran after a refused attestation' >&2; exit 1; }
rm -f "$TMP/precheck.status"

: >"$TMP/precheck.log"
PANDORA_SKIP_PRECHECK_FRESH_DB=yes-empty-database PANDORA_PRECHECK_ATTESTATION="$TMP/att" \
  run_migrate up >/dev/null 2>&1
[ ! -s "$TMP/precheck.log" ]                               # 全新库：两种预检都不跑

echo 'migrate rollback-to and precheck attestation wiring: PASS'
