#!/usr/bin/env bash
# Dynamic tests for strict migration extraction and password argv hygiene.
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECK="$ROOT/deploy/check-migrations.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-check-migrations.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/empty" "$TMP/missing-up" "$TMP/valid" "$TMP/pending42"

cat >"$TMP/env" <<'ENV'
POSTGRES_USER=aegis_test
POSTGRES_PASSWORD=argv-secret-sentinel
POSTGRES_DB=aegis_live
POSTGRES_PORT=5432
ENV
cat >"$TMP/bin/docker" <<'MOCK'
#!/usr/bin/env bash
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s\n' "$*" >>"$mock_root/docker.argv"
[ -n "${PGPASSWORD+x}" ] || { echo 'mock docker: missing PGPASSWORD' >&2; exit 80; }
if /usr/bin/env | grep -q '^BASH_FUNC_'; then
  echo 'mock docker: inherited exported function' >&2
  exit 88
fi
args=" $* "
container_script=
if [[ "$args" == *' /bin/sh -c '* ]]; then
  previous=
  for arg in "$@"; do
    if [ "$previous" = -c ]; then container_script=$arg; break; fi
    previous=$arg
  done
  [ -n "$container_script" ] || exit 90
  if [ ! -f "$mock_root/container-probe.done" ]; then
    POSTGRES_PASSWORD=container-secret-sentinel \
      /bin/sh -c "$container_script" pandora-container-wrapper \
      "$mock_root/bin/container-client"
    : >"$mock_root/container-probe.done"
  fi
fi
if [[ "$args" == *' port aegis-postgres 5432/tcp '* ]]; then
  if [ -f "$mock_root/fail_cluster" ]; then
    printf '%s\n' '127.0.0.1:6543'
  else
    printf '%s\n' '127.0.0.1:5432'
  fi
  exit 0
fi
if [[ "$args" == *' pg_dump '* ]]; then
  [[ "$args" == *' -d aegis_live '* ]] || exit 81
  [ ! -f "$mock_root/fail_pg_dump" ] || exit 82
  printf '%s\n' '-- mock dump'
  exit 0
fi
if [[ "$args" == *' psql '* ]]; then
  if [[ "$args" =~ CREATE[[:space:]]DATABASE[[:space:]](aegis_check_[0-9]+) ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}" >"$mock_root/create.id"
  elif [[ "$args" =~ DROP[[:space:]]DATABASE[[:space:]]IF[[:space:]]EXISTS[[:space:]](aegis_check_[0-9]+)[[:space:]]WITH[[:space:]]\(FORCE\) ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}" >"$mock_root/drop.id"
    [ ! -f "$mock_root/fail_drop" ] || exit 83
  elif [[ "$args" == *' -d aegis_check_'*' -q '* ]]; then
    previous=
    for arg in "$@"; do
      if [ "$previous" = -d ]; then
        printf '%s\n' "$arg" >"$mock_root/restore.id"
        break
      fi
      previous=$arg
    done
    POSTGRES_PASSWORD=container-secret-sentinel \
      /bin/sh -c "$container_script" pandora-container-wrapper \
      "$mock_root/bin/restore-client"
    [ ! -f "$mock_root/fail_restore" ] || exit 84
  elif [[ "$args" == *' -tAc '* ]]; then
    if [[ "$args" == *"to_regclass('public.goose_db_version')"* ]] \
        && [ -f "$mock_root/source-goose.exists" ]; then
      printf '%s\n' t
    elif [[ "$args" == *"to_regclass('public.orders')"* ]]; then
      if [ -f "$mock_root/orders.exists" ]; then printf '%s\n' t; else printf '%s\n' f; fi
    elif [[ "$args" == *"column_name IN ('business_request_id','idempotency_key_id')"* ]]; then
      if [ -f "$mock_root/renewal-link-columns.exists" ]; then printf '%s\n' t; else printf '%s\n' f; fi
    elif [[ "$args" == *"kind='renewal'"* && "$args" == *"idempotency_key_id IS NULL"* ]]; then
      if [ -f "$mock_root/legacy-renewal.count" ]; then cat "$mock_root/legacy-renewal.count"; else printf '%s\n' 0; fi
    elif [[ "$args" == *"kind='renewal'"* && "$args" == *"status IN ('draft','pending_payment','processing','paid')"* ]]; then
      if [ -f "$mock_root/legacy-renewal.count" ]; then cat "$mock_root/legacy-renewal.count"; else printf '%s\n' 0; fi
    elif [[ "$args" == *'max(version_id)'* ]] \
        && [ -f "$mock_root/source-goose.version" ]; then
      cat "$mock_root/source-goose.version"
    else
      printf '%s\n' 1
    fi
  fi
fi
exit 0
MOCK
chmod 0755 "$TMP/bin/docker"
cat >"$TMP/bin/container-client" <<'MOCK'
#!/usr/bin/env sh
[ "${PGPASSWORD:-}" = argv-secret-sentinel ] || exit 94
[ -z "${POSTGRES_PASSWORD+x}" ] || exit 95
if /usr/bin/env | grep -q '^BASH_FUNC_'; then exit 96; fi
exit 0
MOCK
chmod 0755 "$TMP/bin/container-client"
cat >"$TMP/bin/restore-client" <<'MOCK'
#!/usr/bin/env sh
[ "${PGPASSWORD:-}" = argv-secret-sentinel ] || exit 97
[ -z "${POSTGRES_PASSWORD+x}" ] || exit 98
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
cat >"$mock_root/restore.payload"
MOCK
chmod 0755 "$TMP/bin/restore-client"
cat >"$TMP/bin/goose" <<'MOCK'
#!/usr/bin/env bash
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s\n' "$*" >>"$mock_root/goose.argv"
[ -n "${PGPASSWORD+x}" ] || { echo 'mock goose: missing PGPASSWORD' >&2; exit 85; }
if /usr/bin/env | grep -q '^BASH_FUNC_'; then
  echo 'mock goose: inherited exported function' >&2
  exit 89
fi
printf 'GOOSE_DBSTRING=%s\n' "${GOOSE_DBSTRING:-}" >>"$mock_root/goose.env"
printf 'PGOPTIONS=%s\n' "${PGOPTIONS:-}" >>"$mock_root/goose.env"
if [[ "${GOOSE_DBSTRING:-}" =~ dbname=(aegis_check_[0-9]+) ]]; then
  printf '%s\n' "${BASH_REMATCH[1]}" >"$mock_root/goose.id"
else
  exit 86
fi
[ ! -f "$mock_root/fail_goose" ] || exit 87
[ "${1:-}" = up ]
MOCK
chmod 0755 "$TMP/bin/goose"
cat >"$TMP/bin/env" <<'MOCK'
#!/usr/bin/env bash
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s\n' "$*" >>"$mock_root/env.argv"
exec /usr/bin/env "$@"
MOCK
chmod 0755 "$TMP/bin/env"

run_check() {
  PATH="$TMP/bin:$PATH" GOOSE_BIN="$TMP/bin/goose" \
    AEGIS_ENV_FILE="$TMP/env" AEGIS_MIGRATIONS_DIR="$1" \
    "$CHECK"
}

if run_check "$TMP/empty" >"$TMP/empty.out" 2>&1; then
  echo "empty migration set was accepted" >&2
  exit 1
fi
grep -Fq 'no SQL migrations found' "$TMP/empty.out"

printf '%s\n' 'SELECT 1;' >"$TMP/missing-up/00001_missing.sql"
if run_check "$TMP/missing-up" >"$TMP/missing.out" 2>&1; then
  echo "migration without goose Up marker was accepted" >&2
  exit 1
fi
grep -Fq 'FAIL' "$TMP/missing.out"

printf '%s\n' '-- +goose Up' 'SELECT 1;' '-- +goose Down' 'SELECT 1;' \
  >"$TMP/valid/00001_valid.sql"

# 编号规则：严格递增、不重复，允许空号。同号在碰数据库之前就以 78 拒绝。
mkdir -p "$TMP/gapped" "$TMP/duplicate"
for file in 00001_a.sql 00003_c.sql; do
  printf '%s\n' '-- +goose Up' 'SELECT 1;' '-- +goose Down' 'SELECT 1;' >"$TMP/gapped/$file"
done
for file in 00001_a.sql 00002_b.sql 00002_c.sql; do
  printf '%s\n' '-- +goose Up' 'SELECT 1;' '-- +goose Down' 'SELECT 1;' >"$TMP/duplicate/$file"
done
run_check "$TMP/gapped" >"$TMP/gapped.out" 2>&1 \
  || { echo 'gapped migration sequence was rejected' >&2; cat "$TMP/gapped.out" >&2; exit 1; }
grep -Fq 'migration precheck complete' "$TMP/gapped.out"
rm -f "$TMP/create.id" "$TMP/docker.argv"
set +e
run_check "$TMP/duplicate" >"$TMP/duplicate.out" 2>&1
duplicate_status=$?
set -e
[ "$duplicate_status" -eq 78 ]
grep -Fq 'duplicate migration version: 00002_c.sql' "$TMP/duplicate.out"
[ ! -e "$TMP/create.id" ] && [ ! -s "$TMP/docker.argv" ]

# 仓库里真实的 migrations/ 必须能过文件名、编号与 Up 标记这一层校验。
run_check "$ROOT/migrations" >"$TMP/real.out" 2>&1 \
  || { echo 'the real migrations directory was rejected' >&2; cat "$TMP/real.out" >&2; exit 1; }
grep -Fq 'migration precheck complete' "$TMP/real.out"

# CLIENT-AUTH-00042 lives only in the archive/client-auth tag, outside the
# runtime migration directory. The precheck must not retain the old
# version-number guard, while the current 00042 runtime migration remains part
# of the ordinary sequence.
[ -f "$ROOT/migrations/00042_seed_registration_mode.sql" ]
[ ! -e "$ROOT/migrations/00042_client_auth_expand.sql" ]
if grep -Fq 'CLIENT-AUTH-00042 is pending' "$CHECK"; then
  echo 'stale CLIENT-AUTH-00042 version guard remains in check-migrations.sh' >&2
  exit 1
fi

# A post-00036 active renewal without exact idempotency linkage refuses before
# clone DDL or any migration process. The gate exposes only a count.
rm -f "$TMP/create.id" "$TMP/drop.id"
touch "$TMP/orders.exists" "$TMP/renewal-link-columns.exists"
printf '%s\n' 2 >"$TMP/legacy-renewal.count"
set +e
run_check "$TMP/valid" >"$TMP/legacy-renewal.out" 2>&1
legacy_status=$?
set -e
[ "$legacy_status" -eq 78 ]
grep -Fq 'active legacy renewals=2; release refused' "$TMP/legacy-renewal.out"
grep -Fq 'cancel unpaid ones through the normal order-cancel flow' "$TMP/legacy-renewal.out"
[ ! -e "$TMP/create.id" ] && [ ! -e "$TMP/drop.id" ]
rm -f "$TMP/legacy-renewal.count"

# A pre-00036 database has no linkage columns; every active renewal must be
# treated as legacy and block the upgrade.
rm -f "$TMP/renewal-link-columns.exists" "$TMP/create.id" "$TMP/drop.id"
printf '%s\n' 1 >"$TMP/legacy-renewal.count"
set +e
run_check "$TMP/valid" >"$TMP/pre36-renewal.out" 2>&1
pre36_status=$?
set -e
[ "$pre36_status" -eq 78 ]
grep -Fq 'active legacy renewals=1; release refused' "$TMP/pre36-renewal.out"
[ ! -e "$TMP/create.id" ] && [ ! -e "$TMP/drop.id" ]

# The same schema with zero unlinked active renewals proceeds normally.
touch "$TMP/renewal-link-columns.exists"
printf '%s\n' 0 >"$TMP/legacy-renewal.count"

run_check "$TMP/valid" >"$TMP/valid.out" 2>&1
grep -Fq 'migration precheck complete' "$TMP/valid.out"
grep -Fq 'renewal cutover active_legacy=0' "$TMP/valid.out"
grep -Fxq -- '-- mock dump' "$TMP/restore.payload"

# The executable gate must ignore hostile exported functions before any
# password-bearing client is launched, and clean clients must inherit none.
compgen() {
  if [ "${PGPASSWORD:-}" = argv-secret-sentinel ]; then
    : >"$TMP/hostile-compgen-ran"
  fi
  builtin compgen "$@"
}
benign_exported_function() { :; }
export -f compgen benign_exported_function
run_check "$TMP/valid" >"$TMP/hostile-env.out" 2>&1
unset -f compgen benign_exported_function
[ ! -e "$TMP/hostile-compgen-ran" ] || {
  echo 'inherited compgen function executed in password context' >&2
  exit 1
}
grep -Fq 'migration precheck complete' "$TMP/hostile-env.out"

cmp -s "$TMP/create.id" "$TMP/restore.id"
cmp -s "$TMP/create.id" "$TMP/goose.id"
cmp -s "$TMP/create.id" "$TMP/drop.id"
grep -Fq 'pg_dump -U aegis_test -d aegis_live' "$TMP/docker.argv"
grep -Eq 'DROP DATABASE IF EXISTS aegis_check_[0-9]+ WITH \(FORCE\)' "$TMP/docker.argv"
grep -Fq '/usr/bin/env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' "$TMP/docker.argv"

if grep -Fq 'argv-secret-sentinel' "$TMP/docker.argv"; then
  echo "PostgreSQL password leaked into docker argv" >&2
  exit 1
fi
grep -Fq -- '-e PGPASSWORD' "$TMP/docker.argv"
if grep -Fq 'argv-secret-sentinel' "$TMP/goose.argv"; then
  echo "PostgreSQL password leaked into goose argv" >&2
  exit 1
fi
grep -Fq 'GOOSE_DBSTRING=host=127.0.0.1 port=5432 user=aegis_test dbname=aegis_check_' "$TMP/goose.env"
if [ -s "$TMP/env.argv" ] && grep -Fq 'argv-secret-sentinel' "$TMP/env.argv"; then
  echo "PostgreSQL password leaked into env argv" >&2
  exit 1
fi

: >"$TMP/docker.argv"
: >"$TMP/goose.env"
PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes run_check "$TMP/valid" \
  >"$TMP/approved.out" 2>&1
grep -Fq 'PGOPTIONS=-c app.idempotency_writers_stopped=yes' "$TMP/goose.env"
grep -Fq -- '-c app.allow_idempotency_schema37_up=yes' "$TMP/goose.env"
grep -Fq -- '-c app.allow_idempotency_schema38_up=yes' "$TMP/goose.env"
grep -Fq -- '-c app.allow_idempotency_schema39_up=yes' "$TMP/goose.env"

for fault in pg_dump restore goose; do
  touch "$TMP/fail_$fault"
  if run_check "$TMP/valid" >"$TMP/fail-$fault.out" 2>&1; then
    echo "$fault failure was accepted" >&2
    exit 1
  fi
  rm -f "$TMP/fail_$fault"
done

touch "$TMP/fail_cluster"
if run_check "$TMP/valid" >"$TMP/fail-cluster.out" 2>&1; then
  echo "cluster endpoint mismatch was accepted" >&2
  exit 1
fi
rm -f "$TMP/fail_cluster"
grep -Fq 'PostgreSQL published endpoint mismatch' "$TMP/fail-cluster.out"

touch "$TMP/fail_drop"
if run_check "$TMP/valid" >"$TMP/fail-drop.out" 2>&1; then
  echo "clone cleanup failure was accepted" >&2
  exit 1
fi
rm -f "$TMP/fail_drop"
grep -Fq 'disposable database cleanup failed' "$TMP/fail-drop.out"

# 每个迁移要有 Down 段或文件头的 irreversible 标记，缺了在碰数据库之前就拒绝。
mkdir -p "$TMP/no-down" "$TMP/irreversible"
printf '%s\n' '-- +goose Up' 'SELECT 1;' '-- +goose Down' 'SELECT 1;' >"$TMP/no-down/00001_a.sql"
printf '%s\n' '-- +goose Up' 'SELECT 1;' >"$TMP/no-down/00002_b.sql"
: >"$TMP/docker.argv"
if run_check "$TMP/no-down" >"$TMP/no-down.out" 2>&1; then
  echo 'migration without Down section was accepted' >&2
  exit 1
fi
grep -Fq '00002_b.sql must contain a goose Down section or an irreversible header' "$TMP/no-down.out"
[ ! -s "$TMP/docker.argv" ] || { echo 'missing-Down rejection touched docker' >&2; exit 1; }
# 不在文件头（Up 之后）的 irreversible 字样不算标记
printf '%s\n' '-- +goose Up' '-- irreversible: too late' 'SELECT 1;' >"$TMP/no-down/00002_b.sql"
if run_check "$TMP/no-down" >"$TMP/no-down2.out" 2>&1; then
  echo 'irreversible marker after the Up marker was accepted' >&2
  exit 1
fi
printf '%s\n' '-- irreversible: seed only' '-- forward-fix: edit settings' '-- +goose Up' 'SELECT 1;' \
  >"$TMP/irreversible/00001_a.sql"
run_check "$TMP/irreversible" >"$TMP/irreversible.out" 2>&1 \
  || { echo 'irreversible header was rejected' >&2; cat "$TMP/irreversible.out" >&2; exit 1; }

# ---- 预检凭据：停服前完整预检写凭据，停服后只读核对 ----
ATT="$TMP/attest/precheck.attestation"
mkdir -p "$TMP/attest"
mkdir -p "$TMP/attest-migrations"
cp "$TMP/valid/00001_valid.sql" "$TMP/attest-migrations/"
touch "$TMP/source-goose.exists"
printf '%s\n' 1 >"$TMP/source-goose.version"
: >"$TMP/goose.env"
PANDORA_PRECHECK_ATTESTATION_OUT="$ATT" PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER=yes \
  run_check "$TMP/attest-migrations" >"$TMP/attest-write.out" 2>&1 \
  || { echo 'attested precheck failed' >&2; cat "$TMP/attest-write.out" >&2; exit 1; }
grep -Fq 'migration precheck attestation written' "$TMP/attest-write.out"
grep -Fxq 'format=pandora-precheck-v1' "$ATT"
grep -Fxq 'database=aegis_live' "$ATT"
grep -Fxq 'source_goose_version=1' "$ATT"
grep -Fxq 'release_max_version=1' "$ATT"
grep -Fxq 'rehearsed_stopped_writer=yes' "$ATT"
grep -Eq '^migrations_sha256=[0-9a-f]{64}$' "$ATT"
# 停服前的演练给克隆库带上停写闸门，但凭据不是「线上写入者已停」的声明
grep -Fq 'PGOPTIONS=-c app.idempotency_writers_stopped=yes' "$TMP/goose.env"
if grep -Fq 'argv-secret-sentinel' "$ATT"; then echo 'password leaked into attestation' >&2; exit 1; fi

# 凭据路径已存在、或不是绝对路径：拒绝，不覆盖
for bad in "$ATT" "relative.attestation"; do
  set +e
  PANDORA_PRECHECK_ATTESTATION_OUT="$bad" run_check "$TMP/attest-migrations" >"$TMP/attest-bad.out" 2>&1
  bad_status=$?
  set -e
  [ "$bad_status" -eq 78 ] || { echo "bad attestation path $bad expected 78, got $bad_status" >&2; exit 1; }
done

verify_check() {
  PATH="$TMP/bin:$PATH" GOOSE_BIN="$TMP/bin/goose" \
    AEGIS_ENV_FILE="$TMP/env" AEGIS_MIGRATIONS_DIR="$TMP/attest-migrations" \
    "$CHECK" --verify-attestation "$1"
}
# 用法：expect_verify_refusal <标签> <期望报错> <停写批准 yes|no> <凭据>
expect_verify_refusal() {
  local label="$1" needle="$2" approved="$3" file="$4" status
  rm -f "$TMP/create.id" "$TMP/goose.id"
  set +e
  if [ "$approved" = yes ]; then
    PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes verify_check "$file" >"$TMP/verify-$label.out" 2>&1
  else
    verify_check "$file" >"$TMP/verify-$label.out" 2>&1
  fi
  status=$?
  set -e
  [ "$status" -eq 78 ] || { echo "verify $label expected 78, got $status" >&2; cat "$TMP/verify-$label.out" >&2; exit 1; }
  grep -Fq "$needle" "$TMP/verify-$label.out" \
    || { echo "verify $label: missing '$needle'" >&2; cat "$TMP/verify-$label.out" >&2; exit 1; }
  [ ! -e "$TMP/create.id" ] && [ ! -e "$TMP/goose.id" ] \
    || { echo "verify $label cloned or migrated" >&2; exit 1; }
}

# 匹配的凭据：只读核对通过，不建克隆库、不跑 goose
rm -f "$TMP/create.id" "$TMP/goose.id"
PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes verify_check "$ATT" >"$TMP/verify-ok.out" 2>&1 \
  || { echo 'matching attestation was refused' >&2; cat "$TMP/verify-ok.out" >&2; exit 1; }
grep -Fq 'migration precheck attestation verified: source=1 release=1' "$TMP/verify-ok.out"
grep -Fq 'renewal cutover active_legacy=0' "$TMP/verify-ok.out"
[ ! -e "$TMP/create.id" ] && [ ! -e "$TMP/goose.id" ]

# 停服后续费闸门照样生效
printf '%s\n' 3 >"$TMP/legacy-renewal.count"
expect_verify_refusal renewal 'active legacy renewals=3' yes "$ATT"
printf '%s\n' 0 >"$TMP/legacy-renewal.count"
# 中间有人迁移过：水位对不上
printf '%s\n' 0 >"$TMP/source-goose.version"
expect_verify_refusal waterline 'waterline does not match the database' yes "$ATT"
printf '%s\n' 1 >"$TMP/source-goose.version"
# 演练与正式运行的停写闸门不一致
expect_verify_refusal rehearsal 'rehearsed a different stopped-writer approval' no "$ATT"
# 迁移目录换过
printf '%s\n' '-- changed' >>"$TMP/attest-migrations/00001_valid.sql"
expect_verify_refusal digest 'different migration directory' yes "$ATT"
cp "$TMP/valid/00001_valid.sql" "$TMP/attest-migrations/00001_valid.sql"
# 换了库、凭据太旧、凭据被篡改成组可写、符号链接、缺失
sed 's/^database=.*/database=other_db/' "$ATT" >"$TMP/attest/other-db"
expect_verify_refusal database 'made for another database' yes "$TMP/attest/other-db"
sed 's/^created_epoch=.*/created_epoch=1000/' "$ATT" >"$TMP/attest/stale"
expect_verify_refusal stale 'older than six hours' yes "$TMP/attest/stale"
cp "$ATT" "$TMP/attest/writable"
chmod 0664 "$TMP/attest/writable"
expect_verify_refusal writable 'group- or world-writable' yes "$TMP/attest/writable"
ln -s "$ATT" "$TMP/attest/link"
expect_verify_refusal symlink 'missing or not a regular file' yes "$TMP/attest/link"
expect_verify_refusal missing 'missing or not a regular file' yes "$TMP/attest/none"
set +e
"$CHECK" --verify-attestation >"$TMP/usage.out" 2>&1
usage_status=$?
"$CHECK" --bogus x >>"$TMP/usage.out" 2>&1
bogus_status=$?
set -e
[ "$usage_status" -eq 78 ] && [ "$bogus_status" -eq 78 ]
rm -f "$TMP/source-goose.exists" "$TMP/source-goose.version"

echo "check-migrations dynamic gate: PASS"
