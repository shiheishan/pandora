#!/bin/bash -p
# Prove the exact production upgrade path on a disposable database clone.
#
# A scratch database is not a faithful release probe once migrations create
# cluster-wide owner roles: those roles legitimately own objects in the live
# database and later scratch replays must reject that pre-existing state. A
# data/ACL-preserving clone instead starts at the live goose version and lets
# goose apply only migrations that are actually pending in this release.
set -Eeuo pipefail
umask 077

DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="${AEGIS_ENV_FILE:-$DEPLOY_DIR/.env}"
MIGRATIONS_DIR="${AEGIS_MIGRATIONS_DIR:-/opt/aegispanel/migrations}"

[ -r "$ENV_FILE" ] || { echo "migration precheck: missing environment file" >&2; exit 1; }
[ -d "$MIGRATIONS_DIR" ] || { echo "migration precheck: missing migrations directory" >&2; exit 1; }
. "$ENV_FILE"
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${POSTGRES_PORT:?POSTGRES_PORT is required}"
GOOSE="${GOOSE_BIN:-/root/go/bin/goose}"

[[ "$POSTGRES_USER" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] \
  || { echo "migration precheck: unsafe PostgreSQL user" >&2; exit 1; }
[[ "$POSTGRES_DB" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] \
  || { echo "migration precheck: unsafe source database" >&2; exit 1; }
[[ "$POSTGRES_PORT" =~ ^[0-9]+$ ]] && [ "$POSTGRES_PORT" -ge 1 ] && [ "$POSTGRES_PORT" -le 65535 ] \
  || { echo "migration precheck: unsafe PostgreSQL port" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "migration precheck: docker is required" >&2; exit 1; }
[ -x "$GOOSE" ] || { echo "migration precheck: goose executable is missing" >&2; exit 1; }

shopt -s nullglob
migration_files=("$MIGRATIONS_DIR"/*.sql)
[ "${#migration_files[@]}" -gt 0 ] || { echo "migration precheck: no SQL migrations found" >&2; exit 1; }

# Validate the complete artifact before touching PostgreSQL. Goose ignores an
# unrelated SQL file without annotations; release packages must fail instead.
previous_version=0
expected_version=1
for migration in "${migration_files[@]}"; do
  name="${migration##*/}"
  if [[ ! "$name" =~ ^([0-9]{5})_[A-Za-z0-9._-]+\.sql$ ]]; then
    echo "migration precheck: invalid migration filename: $name" >&2
    exit 1
  fi
  version=$((10#${BASH_REMATCH[1]}))
  if [ "$version" -ne "$expected_version" ]; then
    echo "migration precheck: migration sequence is incomplete" >&2
    exit 78
  fi
  previous_version=$version
  expected_version=$((expected_version + 1))
  printf '%-48s' "$name"
  if awk '
      /^-- \+goose Up([[:space:]]*)$/ { up=1 }
      END { exit !up }
    ' "$migration"; then
    echo "OK"
  else
    echo "FAIL"
    echo "migration precheck: $name must contain an exact goose Up marker" >&2
    exit 1
  fi
done

# 这里原本钉死了「42 号槽位必须是 00042_client_auth_expand.sql，且 SHA 必须是
# FFAF84B6…」。项目转为只做面板之后，CA42 客户端认证子系统冻结，它的两个迁移
# 被移到 migrations/frozen-client-auth/（从未在任何环境应用过），42 号槽位改由
# 00042_seed_registration_mode.sql 占用，这条校验会把每一次发布都拦下来。
#
# 把版本号和具体文件绑定本身就不牢靠 —— 任何一次重排号都会让它失效。真正要防的
# 「迁移文件被篡改」应该对整个 migrations/ 目录做校验，而不是挑一个文件钉死。
# 恢复 CA42 时如果还需要这类保护，按目录整体校验重做，不要再钉单个版本号。

TEMP_ROOT="${TMPDIR:-/tmp}"
[ -d "$TEMP_ROOT" ] || { echo "migration precheck: temporary root is missing" >&2; exit 1; }
TEMP_ROOT="$(cd "$TEMP_ROOT" && pwd -P)"
TMP_DIR="$(mktemp -d "$TEMP_ROOT/pandora-migration-check.XXXXXX")"
DB="aegis_check_$$"
PASSWORD_FILE="$TMP_DIR/postgres.password"
SCRATCH_DB_CREATED=0

# Cover the secret-file creation window before database helpers and the full
# cleanup trap are available. This trap is replaced before CREATE DATABASE.
cleanup_temp_only() {
  local status=$?
  trap - EXIT INT TERM
  case "$TMP_DIR" in
    "$TEMP_ROOT"/pandora-migration-check.*) rm -rf -- "$TMP_DIR" || status=1 ;;
    *) echo "migration precheck: refusing unsafe temporary directory cleanup" >&2; status=1 ;;
  esac
  exit "$status"
}
trap cleanup_temp_only EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf '%s\n' "$POSTGRES_PASSWORD" >"$PASSWORD_FILE"
chmod 0600 "$PASSWORD_FILE"

# Privileged-mode Bash ignores inherited function exports and SHELLOPTS. Each
# client is then launched through `exec -c`, so it starts from an empty
# environment and receives only the fixed variables below. The password moves
# through a mode-0600 file and is never present in a helper process argv.
run_clean_docker() (
  local cm_path="$PATH" cm_home="${HOME:-/root}" cm_password_file="$PASSWORD_FILE"
  local cm_docker
  cm_docker="$(command -v docker)"
  exec -c /bin/bash --noprofile --norc -p -c '
    password_file=$1; client_path=$2; client_home=$3; shift 3
    IFS= read -r client_password <"$password_file" || exit 91
    export PATH="$client_path" HOME="$client_home" PGPASSWORD="$client_password"
    exec "$@"
  ' pandora-clean-client "$cm_password_file" "$cm_path" "$cm_home" "$cm_docker" "$@"
)
# docker exec inherits the container's baseline environment. Route database
# clients through a second, in-container empty environment as well; the only
# secret crosses that boundary over a dedicated descriptor, never in argv or
# the database client's stdin.
CONTAINER_CLEAN_CLIENT='
  exec 9<<PANDORA_PASSWORD_FD
$PGPASSWORD
PANDORA_PASSWORD_FD
  exec /usr/bin/env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    /bin/sh -c '\''
      IFS= read -r PGPASSWORD <&9 || exit 93
      exec 9<&-
      export PGPASSWORD
      exec "$@"
    '\'' pandora-container-client "$@"
'
psql_run() {
  run_clean_docker exec -i -e PGPASSWORD aegis-postgres \
    /bin/sh -c "$CONTAINER_CLEAN_CLIENT" pandora-container-wrapper \
    psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" "$@"
}
pg_dump_run() {
  run_clean_docker exec -i -e PGPASSWORD aegis-postgres \
    /bin/sh -c "$CONTAINER_CLEAN_CLIENT" pandora-container-wrapper \
    pg_dump -U "$POSTGRES_USER" "$@"
}
cleanup() {
  local status=$? cleanup_failed=0
  trap - EXIT INT TERM
  set +e
  if [ "$SCRATCH_DB_CREATED" -eq 1 ]; then
    if ! psql_run -d postgres -qc "DROP DATABASE IF EXISTS $DB WITH (FORCE);" >/dev/null 2>&1; then
      echo "migration precheck: disposable database cleanup failed" >&2
      cleanup_failed=1
    fi
  fi
  case "$TMP_DIR" in
    "$TEMP_ROOT"/pandora-migration-check.*)
      if ! rm -rf -- "$TMP_DIR"; then
        echo "migration precheck: temporary directory cleanup failed" >&2
        cleanup_failed=1
      fi
      ;;
    *)
      echo "migration precheck: refusing unsafe temporary directory cleanup" >&2
      cleanup_failed=1
      ;;
  esac
  if [ "$status" -eq 0 ] && [ "$cleanup_failed" -ne 0 ]; then status=1; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Bind the host-side goose endpoint to the exact PostgreSQL container used by
# dump/restore. A same-named database on another local cluster is not accepted.
PUBLISHED_ENDPOINT="$(run_clean_docker port aegis-postgres 5432/tcp)"
[ "$PUBLISHED_ENDPOINT" = "127.0.0.1:$POSTGRES_PORT" ] || {
  echo "migration precheck: PostgreSQL published endpoint mismatch" >&2
  exit 1
}

# Establish the exact source waterline using SELECT only. An artifact older
# than the source database is never a complete release input.
if ! GOOSE_TABLE_PRESENT="$(psql_run -d "$POSTGRES_DB" -tAc \
    "SELECT pg_catalog.to_regclass('public.goose_db_version') IS NOT NULL;")"; then
  echo "migration precheck: cannot determine source goose state" >&2
  exit 78
fi
SOURCE_GOOSE_VERSION=0
if [ "$GOOSE_TABLE_PRESENT" = t ]; then
  if ! SOURCE_GOOSE_VERSION="$(psql_run -d "$POSTGRES_DB" -tAc \
      "SELECT coalesce(max(version_id) FILTER (WHERE is_applied),0) FROM public.goose_db_version;")"; then
    echo "migration precheck: cannot determine source goose waterline" >&2
    exit 78
  fi
  [[ "$SOURCE_GOOSE_VERSION" =~ ^[0-9]+$ ]] || {
    echo "migration precheck: invalid source goose waterline" >&2
    exit 78
  }
fi
if [ "$SOURCE_GOOSE_VERSION" -gt "$previous_version" ]; then
  echo "migration precheck: release migration artifact is older than source" >&2
  exit 78
fi

# A renewal created before schema 00036 can remain draft/pending/processing/paid
# without the actor-bound idempotency linkage required by the current
# settlement writer. Applying the new binary in that state would correctly
# fail closed, but a customer could already have paid an order that can never
# settle. This read-only cutover gate runs while every writer is stopped and
# before any production migration. It emits counts only, never user/order data.
ORDERS_PRESENT="$(psql_run -d "$POSTGRES_DB" -tAc \
  "SELECT pg_catalog.to_regclass('public.orders') IS NOT NULL;")" \
  || { echo "migration precheck: cannot determine renewal cutover state" >&2; exit 78; }
case "$ORDERS_PRESENT" in
  t|f) ;;
  *) echo "migration precheck: invalid orders-table presence result" >&2; exit 78 ;;
esac
if [ "$ORDERS_PRESENT" = t ]; then
  RENEWAL_LINK_COLUMNS_PRESENT="$(psql_run -d "$POSTGRES_DB" -tAc \
    "SELECT count(*)=2 FROM information_schema.columns WHERE table_schema='public' AND table_name='orders' AND column_name IN ('business_request_id','idempotency_key_id');")" \
    || { echo "migration precheck: cannot inspect renewal linkage columns" >&2; exit 78; }
  case "$RENEWAL_LINK_COLUMNS_PRESENT" in
    t|f) ;;
    *) echo "migration precheck: invalid renewal linkage-column result" >&2; exit 78 ;;
  esac
  if [ "$RENEWAL_LINK_COLUMNS_PRESENT" = t ]; then
    LEGACY_ACTIVE_RENEWALS="$(psql_run -d "$POSTGRES_DB" -tAc \
      "SELECT count(*) FROM public.orders WHERE kind='renewal' AND status IN ('draft','pending_payment','processing','paid') AND (idempotency_key_id IS NULL OR business_request_id IS DISTINCT FROM idempotency_key_id);")" \
      || { echo "migration precheck: cannot count unlinked active renewals" >&2; exit 78; }
  else
    # Before 00036 there is no linkage column, so every active renewal would
    # become an unlinked legacy row when the migration backfills reservations.
    LEGACY_ACTIVE_RENEWALS="$(psql_run -d "$POSTGRES_DB" -tAc \
      "SELECT count(*) FROM public.orders WHERE kind='renewal' AND status IN ('draft','pending_payment','processing','paid');")" \
      || { echo "migration precheck: cannot count pre-linkage active renewals" >&2; exit 78; }
  fi
  [[ "$LEGACY_ACTIVE_RENEWALS" =~ ^[0-9]+$ ]] \
    || { echo "migration precheck: invalid legacy renewal count" >&2; exit 78; }
  if [ "$LEGACY_ACTIVE_RENEWALS" -ne 0 ]; then
    echo "migration precheck: active legacy renewals=$LEGACY_ACTIVE_RENEWALS; release refused" >&2
    echo "migration precheck: follow deploy/renewal-cutover.md, then rerun this gate" >&2
    exit 78
  fi
fi
echo "renewal cutover active_legacy=0"

# 这里原本拦的是：CLIENT-AUTH-00042 会创建集群级角色，而本预检查是在同一个
# PostgreSQL 集群里克隆一个库来重放迁移的，重放它会污染生产集群。理由成立，
# 但条件写成了「版本号 ≥42」，于是 42 号槽位换成别的迁移之后照样拦。
#
# 42 号现在是 00042_seed_registration_mode.sql，只有两条 INSERT，不建角色。
# 恢复 CA42 时请按「迁移内容是否含 CREATE ROLE / CREATE DATABASE 等集群级 DDL」
# 来判断，而不是版本号 —— 那才是这道闸门真正要防的东西。

psql_run -d postgres -qc "CREATE DATABASE $DB;"
SCRATCH_DB_CREATED=1
# Preserve owners, ACLs, goose history and representative data. The release
# controller has already stopped writers, while pg_dump itself also provides a
# transactionally consistent snapshot for standalone use.
pg_dump_run -d "$POSTGRES_DB" | psql_run -d "$DB" -q
echo "migration precheck database clone created"

MIGRATION_PGOPTIONS=""
if [ "${PANDORA_STOPPED_WRITER_UPGRADE_APPROVED:-}" = yes ]; then
  MIGRATION_PGOPTIONS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes -c aegis.client_auth_00042_upgrade_approved=approved-v1 -c aegis.client_auth_writers_stopped=stopped-v1'
fi
GOOSE_DBSTRING="host=127.0.0.1 port=$POSTGRES_PORT user=$POSTGRES_USER dbname=$DB sslmode=disable"
run_clean_goose() (
  local cm_path="$PATH" cm_home="${HOME:-/root}" cm_password_file="$PASSWORD_FILE"
  local cm_goose="$GOOSE" cm_dbstring="$GOOSE_DBSTRING"
  local cm_migrations="$MIGRATIONS_DIR" cm_pgoptions="$MIGRATION_PGOPTIONS"
  exec -c /bin/bash --noprofile --norc -p -c '
    password_file=$1; client_path=$2; client_home=$3; goose=$4
    dbstring=$5; migrations=$6; pgoptions=$7
    IFS= read -r client_password <"$password_file" || exit 92
    export PATH="$client_path" HOME="$client_home" PGPASSWORD="$client_password"
    export GOOSE_DRIVER=postgres GOOSE_DBSTRING="$dbstring"
    export GOOSE_MIGRATION_DIR="$migrations"
    [ -z "$pgoptions" ] || export PGOPTIONS="$pgoptions"
    exec "$goose" up
  ' pandora-clean-client "$cm_password_file" "$cm_path" "$cm_home" \
    "$cm_goose" "$cm_dbstring" "$cm_migrations" "$cm_pgoptions"
)
run_clean_goose
echo "migration clone upgrade=ok"

echo "schema diagnostics"
psql_run -d "$DB" -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE';"
psql_run -d "$DB" -tAc \
  "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relrowsecurity;"
psql_run -d "$DB" -tAc \
  "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relforcerowsecurity;"
psql_run -d "$DB" -tAc \
  "SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%transition%' AND NOT tgisinternal;"
psql_run -d "$DB" -tAc "SELECT count(*) FROM permissions;"
psql_run -d "$DB" -tAc "SELECT count(*) FROM roles WHERE is_system;"
psql_run -d "$DB" -tAc "SELECT count(*) FROM role_permissions;"

echo "migration precheck complete"
