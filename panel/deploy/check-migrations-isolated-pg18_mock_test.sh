#!/usr/bin/env bash
# [INPUT]: 依赖同目录 check-migrations-isolated-pg18.sh、../migrations 的 00001–00041 与 frozen-client-auth/00042_client_auth_expand.sql，docker/goose/openssl/install 用桩脚本代替
# [OUTPUT]: 当前主序列 NOT_RUN 的钉子，以及在合成历史序列上的隔离预检全套用例（含空号、同号）
# [POS]: deploy 的桩测试，CI panel-deploy.yml 必跑；不需要数据库或 root
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
# Contract and dynamic mock test for check-migrations-isolated-pg18.sh.
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECK="$ROOT/deploy/check-migrations-isolated-pg18.sh"

FROZEN_00042="$ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql"

# 当前主序列的 00042 已换人，脚本对它以 NOT_RUN（77）拒绝运行；这条先钉住。
set +e
AEGIS_MIGRATIONS_DIR="$ROOT/migrations" "$CHECK" >/dev/null 2>&1
rc=$?
set -e
[ "$rc" -eq 77 ] || { echo "current stream expected NOT_RUN 77, got $rc" >&2; exit 1; }

# 其余用例跑在合成的历史序列上：主序列 00001–00041 加冻结的 00042_client_auth_expand.sql，
# 即这道预检当年面对的形状。
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-isolated-pg18-test.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/migrations"
for migration in "$ROOT"/migrations/000[0-3][0-9]_*.sql "$ROOT"/migrations/0004[01]_*.sql; do
  cp "$migration" "$TMP/migrations/"
done
cp "$FROZEN_00042" "$TMP/migrations/"
[ "$(ls "$TMP/migrations" | wc -l)" -eq 42 ] || { echo 'synthetic historical stream is not 42 files' >&2; exit 1; }
SOURCE_CONTAINER_ID="$(printf '%064d' 2)"
SOURCE_SYSTEM_IDENTIFIER=7777777777777777777
POSTGRES_IMAGE_ID="sha256:$(printf '%064d' 0)"

cat >"$TMP/env" <<'ENV'
POSTGRES_USER=aegis_test
POSTGRES_PASSWORD=source-secret-sentinel
POSTGRES_DB=aegis_live
ENV
cat >"$TMP/bin/openssl" <<'MOCK'
#!/usr/bin/env bash
[ "$*" = 'rand -hex 32' ] || exit 90
printf '%s\n' isolated-secret-sentinel
MOCK
chmod 0755 "$TMP/bin/openssl"

# Git Bash on Windows cannot always apply POSIX modes under /tmp; the contract
# test records install intent while production uses the real Linux install(1).
cat >"$TMP/bin/install" <<'MOCK'
#!/usr/bin/env bash
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s\n' "$*" >>"$mock_root/install.argv"
if [ "${1:-}" = -d ]; then
  shift 3
  mkdir -p -- "$1"
else
  shift 2
  destination="${!#}"
  source_count=$(($# - 1))
  for ((index=1; index<=source_count; index++)); do
    cp -- "${!index}" "$destination"
  done
fi
MOCK
chmod 0755 "$TMP/bin/install"

cat >"$TMP/bin/goose" <<'MOCK'
#!/usr/bin/env bash
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s\n' "$*" >>"$mock_root/goose.argv"
if [ "${*: -1}" = validate ]; then
  [[ "$*" == -dir\ *'/migrations validate' ]] || exit 90
  if [ -e "$mock_root/mutate_source" ]; then
    printf '%s\n' '-- source changed after private snapshot' >>"$mock_root/migrations/00042_client_auth_expand.sql"
  fi
  exit 0
fi
[ "${PGPASSWORD:-}" = isolated-secret-sentinel ] || exit 91
[[ "${GOOSE_DBSTRING:-}" == *'host=127.0.0.1 port=55432 '* ]] || exit 92
[[ "${GOOSE_DBSTRING:-}" == *'dbname=aegis_live '* ]] || exit 93
[ "${GOOSE_MIGRATION_DIR:-}" != "$mock_root/migrations" ] || exit 95
[ -f "${GOOSE_MIGRATION_DIR:-}/00042_client_auth_expand.sql" ] || exit 96
[ "$*" = 'up-to 42' ] || exit 94
exit 0
MOCK
chmod 0755 "$TMP/bin/goose"

cat >"$TMP/bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -u
mock_root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s\n' "$*" >>"$mock_root/docker.argv"
joined=" $* "
if [ "${1:-}" = --host ]; then
  [ "${2:-}" = unix:///var/run/docker.sock ] || exit 87
  shift 2
  joined=" $* "
else
  exit 86
fi

if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then
  if [[ "$joined" == *'{{.Id}}'* ]]; then
    printf 'sha256:%064d\n' 0
  else
    printf 'postgres@sha256:%064d\n' 1
  fi
  exit 0
fi
if [ "${1:-}" = inspect ]; then
  if [[ "$joined" == *'{{.Id}}'* && ( "${*: -1}" = aegis-postgres || "${*: -1}" = "$(printf '%064d' 2)" ) ]]; then
    printf '%064d\n' 2
  elif [[ "$joined" == *'pandora.manifest.source'* ]]; then
    printf '%064d|/%s|sha256:%064d|isolated-pg18-v1|%s|%s|trusted-disposable-client-auth-00042-v1|ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5|true|true|%s\n' \
      5 "$(cat "$mock_root/container.name")" "$([ -e "$mock_root/fail_image_retag" ] && echo 9 || echo 0)" \
      "$(cat "$mock_root/container.run")" \
      "$(cat "$mock_root/container.expires")" "$(cat "$mock_root/container.network")"
  elif [[ "$joined" == *'{{.Image}}'* ]]; then
    if [ -e "$mock_root/fail_image_retag" ]; then
      printf 'sha256:%064d\n' 9
    else
      printf 'sha256:%064d\n' 0
    fi
  else
    exit 89
  fi
  exit 0
fi
if [ "${1:-}" = network ] && [ "${2:-}" = create ]; then
  : >"$mock_root/network.created"
  printf '%s' "${*: -1}" >"$mock_root/network.name"
  for arg in "$@"; do
    case "$arg" in
      pandora.preflight.run_id=*) printf '%s' "${arg#*=}" >"$mock_root/network.run" ;;
      pandora.preflight.expires_at=*) printf '%s' "${arg#*=}" >"$mock_root/network.expires" ;;
    esac
  done
  printf '%064d\n' 4
  exit 0
fi
if [ "${1:-}" = network ] && [ "${2:-}" = inspect ]; then
  printf '%064d|%s|true|isolated-pg18-v1|%s|%s\n' 4 "$(cat "$mock_root/network.name")" \
    "$(cat "$mock_root/network.run")" "$(cat "$mock_root/network.expires")"
  exit 0
fi
if [ "${1:-}" = network ] && [ "${2:-}" = rm ]; then
  : >"$mock_root/network.removed"
  exit 0
fi
if [ "${1:-}" = run ]; then
  [[ "$joined" == *" sha256:$(printf '%064d' 0) "* ]] || exit 88
  : >"$mock_root/container.created"
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --name) printf '%s' "$2" >"$mock_root/container.name"; shift ;;
      --network) printf '%s' "$2" >"$mock_root/container.network"; shift ;;
      pandora.preflight.run_id=*) printf '%s' "${1#*=}" >"$mock_root/container.run" ;;
      pandora.preflight.expires_at=*) printf '%s' "${1#*=}" >"$mock_root/container.expires" ;;
    esac
    shift
  done
  printf '%064d\n' 5
  exit 0
fi
if [ "${1:-}" = rm ] && [ "${2:-}" = -f ]; then
  [ ! -e "$mock_root/fail_cleanup" ] || exit 95
  : >"$mock_root/container.removed"
  exit 0
fi
if [ "${1:-}" = port ]; then
  printf '%s\n' 127.0.0.1:55432
  exit 0
fi

if [ "${1:-}" = exec ]; then
  if [[ "$joined" == *' aegis-postgres '* || "$joined" == *" $(printf '%064d' 2) "* ]]; then
    printf '%s\n' "$*" >>"$mock_root/source.argv"
    if [[ "$joined" =~ (CREATE|DROP|ALTER|ROLE)[[:space:]] ]]; then
      echo 'source mutation attempted' >&2
      exit 96
    fi
    [ ! -e "$mock_root/fail_source_select" ] || exit 97
    if [[ "$joined" == *' pg_dumpall '* ]]; then
      printf '%s\n' '-- mock password-free globals'
    elif [[ "$joined" == *' pg_dump '* ]]; then
      printf '%s\n' 'mock custom database dump'
    elif [[ "$joined" == *'system_identifier'* ]]; then
      printf '%s\n' 7777777777777777777
    elif [[ "$joined" == *"to_regclass('public.goose_db_version')"* ]]; then
      printf '%s\n' t
    elif [[ "$joined" == *'max(version_id)'* ]]; then
      if [ -e "$mock_root/source_waterline_40" ]; then printf '%s\n' 40; else printf '%s\n' 41; fi
    elif [[ "$joined" == *'pg_get_userbyid'* ]]; then
      printf '%s\n' '16384|10|aegis_owner'
    elif [[ "$joined" == *'pg_catalog.pg_roles'* ]]; then
      printf '%s\n' 0
    else
      exit 98
    fi
    exit 0
  fi

  if [[ "$joined" == *' pg_isready '* ]]; then exit 0; fi
  if [[ "$joined" == *"current_setting('server_version_num')"* ]]; then
    printf '%s\n' 180001
    exit 0
  fi
  if [[ "$joined" == *' createdb '* ]]; then
    : >"$mock_root/createdb.called"
    exit 0
  fi
  if [[ "$joined" == *' pg_restore '* ]]; then
    cat >"$mock_root/database.restore"
    exit 0
  fi
  if [[ "$joined" == *'max(version_id)'* ]]; then
    printf '%s\n' 42
    exit 0
  fi
  if [[ "$joined" == *'pg_control_system'* ]]; then
    printf '%s\n' 8888888888888888888
    exit 0
  fi
  if [[ "$joined" == *'SELECT oid::text FROM pg_catalog.pg_database'* ]]; then
    printf '%s\n' 24576
    exit 0
  fi
  if [[ "$joined" == *'version_id=42'* ]]; then
    printf '%s\n' 1
    exit 0
  fi
  if [[ "$joined" == *' psql '* && "$joined" == *' -f - '* ]]; then
    cat >"$mock_root/globals.restore"
    exit 0
  fi
  if [[ "$joined" == *' psql '* ]]; then exit 0; fi
fi
exit 99
MOCK
chmod 0755 "$TMP/bin/docker"

run_check() {
  PATH="$TMP/bin:$PATH" AEGIS_ENV_FILE="$TMP/env" \
    AEGIS_MIGRATIONS_DIR="$TMP/migrations" GOOSE_BIN="$TMP/bin/goose" \
    PANDORA_EXPECTED_SOURCE_CONTAINER_ID="${EXPECTED_SOURCE_ID_OVERRIDE:-$SOURCE_CONTAINER_ID}" \
    PANDORA_EXPECTED_SOURCE_SYSTEM_IDENTIFIER="${EXPECTED_SYSTEM_ID_OVERRIDE:-$SOURCE_SYSTEM_IDENTIFIER}" \
    PANDORA_EXPECTED_SOURCE_DATABASE="${EXPECTED_DATABASE_OVERRIDE:-aegis_live}" \
    PANDORA_EXPECTED_POSTGRES_IMAGE_ID="${EXPECTED_IMAGE_ID_OVERRIDE:-$POSTGRES_IMAGE_ID}" \
    TMPDIR="$TMP" "$CHECK" up-to 42
}

set +e
run_check >"$TMP/attestation" 2>"$TMP/stderr"
rc=$?
set -e
if [ "$rc" -ne 0 ]; then
  echo "happy-path preflight returned $rc" >&2
  while IFS= read -r line; do printf '%s\n' "$line" >&2; done <"$TMP/stderr"
  exit 1
fi
grep -Fxq 'format=isolated-pg18-preflight-v1' "$TMP/attestation"
grep -Fxq 'source_system_identifier=7777777777777777777' "$TMP/attestation"
grep -Fxq "source_container_id=$SOURCE_CONTAINER_ID" "$TMP/attestation"
grep -Fxq 'source_database_oid=16384' "$TMP/attestation"
grep -Fxq 'source_database_owner_oid=10' "$TMP/attestation"
grep -Fxq 'source_database_owner_name=aegis_owner' "$TMP/attestation"
grep -Fxq 'expected_source_database=aegis_live' "$TMP/attestation"
grep -Fxq "expected_postgres_image_id=$POSTGRES_IMAGE_ID" "$TMP/attestation"
grep -Fxq 'source_goose_waterline=41' "$TMP/attestation"
grep -Fxq 'command=up-to:42' "$TMP/attestation"
grep -Fxq 'isolated_server_version_num=180001' "$TMP/attestation"
grep -Fxq "isolated_container_id=$(printf '%064d' 5)" "$TMP/attestation"
grep -Fxq "isolated_network_id=$(printf '%064d' 4)" "$TMP/attestation"
grep -Fxq 'isolated_system_identifier=8888888888888888888' "$TMP/attestation"
grep -Fxq 'isolated_database_oid=24576' "$TMP/attestation"
grep -Fxq 'isolated_goose_waterline=42' "$TMP/attestation"
[ "$(grep -c '^migration_sha256=' "$TMP/attestation")" -eq 42 ]
grep -Fxq 'client_auth_00042_sha256=ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5' "$TMP/attestation"
grep -Eq '^migration_set_sha256=[0-9a-f]{64}$' "$TMP/attestation"
grep -Eq '^attestation_sha256=[0-9a-f]{64}$' "$TMP/attestation"
grep -Fxq 'up-to 42' "$TMP/goose.argv"
grep -Fq 'network create --internal' "$TMP/docker.argv"
grep -Fq -- '-p 127.0.0.1::5432' "$TMP/docker.argv"
grep -Fq -- '--tmpfs /var/lib/postgresql:' "$TMP/docker.argv"
grep -Fq 'pandora.preflight.expires_at=' "$TMP/docker.argv"
grep -Fq 'pandora.manifest.source=trusted-disposable-client-auth-00042-v1' "$TMP/docker.argv"
grep -Fq 'pandora.manifest.migration_sha256=ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5' "$TMP/docker.argv"
if grep -Fv -- '--host unix:///var/run/docker.sock' "$TMP/docker.argv" | grep -q .; then
  echo 'a Docker call escaped the fixed local host' >&2
  exit 1
fi
grep -Eq '^-d -m 0700 .*/migrations$' "$TMP/install.argv"
grep -Eq '^-m 0600 .*00042_client_auth_expand.sql .*/migrations/$' "$TMP/install.argv"
grep -Fq -- '--globals-only --no-role-passwords --no-tablespaces' "$TMP/source.argv"
grep -Fq -- '--format=custom --no-tablespaces -d aegis_live' "$TMP/source.argv"
if grep -Fv 'default_transaction_read_only=on' "$TMP/source.argv" | grep -q .; then
  echo 'source client omitted default_transaction_read_only=on' >&2
  exit 1
fi
[ -s "$TMP/globals.restore" ] && [ -s "$TMP/database.restore" ]
[ -e "$TMP/container.removed" ] && [ -e "$TMP/network.removed" ]
if grep -Eq '(CREATE|DROP|ALTER|ROLE)[[:space:]]' "$TMP/source.argv"; then
  echo 'source mutation escaped the read-only contract' >&2
  exit 1
fi
if grep -Fq source-secret-sentinel "$TMP/docker.argv"; then
  echo 'source password leaked into docker argv' >&2
  exit 1
fi
grep -Fq 'unset POSTGRES_PASSWORD' "$CHECK"

# Mutating the source migration after validate must not affect the private
# snapshot used by Goose or the final snapshot checksum.
before_source_42="$(sha256sum "$TMP/migrations/00042_client_auth_expand.sql" | awk '{print $1}')"
touch "$TMP/mutate_source"
run_check >"$TMP/toctou.attestation" 2>"$TMP/toctou.stderr"
after_source_42="$(sha256sum "$TMP/migrations/00042_client_auth_expand.sql" | awk '{print $1}')"
[ "$before_source_42" != "$after_source_42" ]
grep -Fxq 'client_auth_00042_sha256=ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5' \
  "$TMP/toctou.attestation"
rm -f "$TMP/mutate_source"
cp "$FROZEN_00042" "$TMP/migrations/00042_client_auth_expand.sql"

# 编号规则：严格递增、不重复，允许空号。空号照常签出证明，同号以 78 拒绝。
mv "$TMP/migrations/00010_"*.sql "$TMP/"
run_check >"$TMP/gap.attestation" 2>"$TMP/gap.stderr" \
  || { echo 'gapped historical stream was rejected' >&2; cat "$TMP/gap.stderr" >&2; exit 1; }
[ "$(grep -c '^migration_sha256=' "$TMP/gap.attestation")" -eq 41 ]
mv "$TMP/00010_"*.sql "$TMP/migrations/"
printf '%s\n' '-- +goose Up' 'SELECT 1;' >"$TMP/migrations/00041_duplicate.sql"
set +e
run_check >"$TMP/duplicate.out" 2>"$TMP/duplicate.err"
rc=$?
set -e
[ "$rc" -eq 78 ] || { echo "duplicate version returned $rc, want 78" >&2; exit 1; }
grep -Fq 'duplicate migration version: 00041_duplicate.sql' "$TMP/duplicate.err"
[ ! -s "$TMP/duplicate.out" ]
rm -f "$TMP/migrations/00041_duplicate.sql"

# Explicit source identity mismatches fail before any source SELECT or
# isolated network/container creation.
rm -f "$TMP/docker.argv" "$TMP/source.argv" "$TMP/network.created" "$TMP/container.created"
set +e
EXPECTED_SOURCE_ID_OVERRIDE="$(printf '%064d' 3)" run_check >"$TMP/bad-source.out" 2>"$TMP/bad-source.err"
rc=$?
set -e
[ "$rc" -eq 78 ]
[ ! -e "$TMP/source.argv" ] && [ ! -e "$TMP/network.created" ] && [ ! -e "$TMP/container.created" ]

rm -f "$TMP/docker.argv" "$TMP/source.argv" "$TMP/network.created" "$TMP/container.created"
set +e
EXPECTED_SYSTEM_ID_OVERRIDE=8888888888888888888 run_check >"$TMP/bad-system.out" 2>"$TMP/bad-system.err"
rc=$?
set -e
[ "$rc" -eq 78 ]
[ -e "$TMP/source.argv" ] && [ ! -e "$TMP/network.created" ] && [ ! -e "$TMP/container.created" ]

# Explicit database and immutable image expectations fail before creating any
# isolated resource. A database mismatch fails before the first source query.
rm -f "$TMP/docker.argv" "$TMP/source.argv" "$TMP/network.created" "$TMP/container.created"
set +e
EXPECTED_DATABASE_OVERRIDE=wrong_db run_check >"$TMP/bad-database.out" 2>"$TMP/bad-database.err"
rc=$?
set -e
[ "$rc" -eq 78 ]
[ ! -e "$TMP/source.argv" ] && [ ! -e "$TMP/network.created" ] && [ ! -e "$TMP/container.created" ]

rm -f "$TMP/docker.argv" "$TMP/source.argv" "$TMP/network.created" "$TMP/container.created"
set +e
EXPECTED_IMAGE_ID_OVERRIDE="sha256:$(printf '%064d' 8)" run_check >"$TMP/bad-image.out" 2>"$TMP/bad-image.err"
rc=$?
set -e
[ "$rc" -eq 78 ]
[ -e "$TMP/source.argv" ] && [ ! -e "$TMP/network.created" ] && [ ! -e "$TMP/container.created" ]

# A retag between tag inspection and container identity verification fails
# closed and cleans the newly created resources.
rm -f "$TMP/docker.argv" "$TMP/network.removed" "$TMP/container.removed"
touch "$TMP/fail_image_retag"
set +e
run_check >"$TMP/retag.out" 2>"$TMP/retag.err"
rc=$?
set -e
[ "$rc" -eq 78 ]
[ ! -s "$TMP/retag.out" ]
[ -e "$TMP/container.removed" ] && [ -e "$TMP/network.removed" ]
rm -f "$TMP/fail_image_retag"

# An uncertain source SELECT must return EX_CONFIG before network/container DDL.
rm -f "$TMP/docker.argv" "$TMP/network.created" "$TMP/container.created"
touch "$TMP/fail_source_select"
set +e
run_check >"$TMP/fail-source.out" 2>"$TMP/fail-source.err"
rc=$?
set -e
[ "$rc" -eq 78 ] || { echo "uncertain source state returned $rc, want 78" >&2; exit 1; }
[ ! -e "$TMP/network.created" ] && [ ! -e "$TMP/container.created" ]
rm -f "$TMP/fail_source_select"

# Source waterline must be exactly 41; an older but otherwise valid source may
# not mint an authorization for the 41-to-42 boundary.
rm -f "$TMP/docker.argv" "$TMP/network.created" "$TMP/container.created"
touch "$TMP/source_waterline_40"
set +e
run_check >"$TMP/source-40.out" 2>"$TMP/source-40.err"
rc=$?
set -e
[ "$rc" -eq 78 ] || { echo "source waterline 40 returned $rc, want 78" >&2; exit 1; }
[ ! -e "$TMP/network.created" ] && [ ! -e "$TMP/container.created" ]
rm -f "$TMP/source_waterline_40"

# Remote Docker contexts are rejected before any Docker lookup or source query.
rm -f "$TMP/docker.argv" "$TMP/source.argv" "$TMP/network.created" "$TMP/container.created"
set +e
DOCKER_HOST=tcp://127.0.0.1:2375 run_check >"$TMP/remote-docker.out" 2>"$TMP/remote-docker.err"
rc=$?
set -e
[ "$rc" -eq 78 ] || { echo "remote Docker context returned $rc, want 78" >&2; exit 1; }
[ ! -e "$TMP/docker.argv" ] && [ ! -e "$TMP/source.argv" ]
[ ! -e "$TMP/network.created" ] && [ ! -e "$TMP/container.created" ]

# Cleanup failure cannot publish a successful attestation.
rm -f "$TMP/docker.argv" "$TMP/network.removed" "$TMP/container.removed"
touch "$TMP/fail_cleanup"
set +e
run_check >"$TMP/fail-cleanup.out" 2>"$TMP/fail-cleanup.err"
rc=$?
set -e
[ "$rc" -eq 78 ] || { echo "cleanup failure returned $rc, want 78" >&2; exit 1; }
[ ! -s "$TMP/fail-cleanup.out" ] || { echo 'cleanup failure published attestation' >&2; exit 1; }

echo 'isolated PG18 migration preflight mock: PASS'
