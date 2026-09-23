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

# CLIENT-AUTH-00042 is intentionally frozen outside the runtime migration
# directory. The precheck must not retain the old version-number guard, while
# the current 00042 runtime migration remains part of the contiguous sequence.
[ -f "$ROOT/migrations/00042_seed_registration_mode.sql" ]
[ -f "$ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql" ]
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
grep -Fq 'follow deploy/renewal-cutover.md' "$TMP/legacy-renewal.out"
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
grep -Fq -- '-c aegis.client_auth_00042_upgrade_approved=approved-v1' "$TMP/goose.env"
grep -Fq -- '-c aegis.client_auth_writers_stopped=stopped-v1' "$TMP/goose.env"

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

echo "check-migrations dynamic gate: PASS"
