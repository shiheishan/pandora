#!/bin/bash -p
# Prove the exact production upgrade path on a disposable database clone.
#
# A scratch database is not a faithful release probe once migrations create
# cluster-wide owner roles: those roles legitimately own objects in the live
# database and later scratch replays must reject that pre-existing state. A
# data/ACL-preserving clone instead starts at the live goose version and lets
# goose apply only migrations that are actually pending in this release.
#
# 两种用法：
#   check-migrations.sh
#       完整预检（克隆库上演练待执行迁移）。设了 PANDORA_PRECHECK_ATTESTATION_OUT=<绝对路径>
#       时，通过后在那里写一张预检凭据：源库水位、迁移目录摘要、是否按停写升级演练。
#       发布控制器在停服之前跑这一步，停服时间里不再包含克隆与演练。
#   check-migrations.sh --verify-attestation <凭据>
#       停服之后的轻量核对，只读：重新校验迁移目录、源库水位与续费闸门，并与凭据逐项比对，
#       不克隆、不演练。任何一项对不上即以 78 拒绝，要求重跑完整预检。
#
# 克隆库上的停写闸门：PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes（写入者已停，
# migrate.sh 在停服后内联预检时用）或 PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER=yes
# （停服前的演练：只作用于没有写入者的克隆库，不是「线上写入者已停」的声明）。
set -Eeuo pipefail
umask 077

MODE=full
ATTESTATION_IN=""
if [ "$#" -gt 0 ]; then
  if [ "$#" -eq 2 ] && [ "$1" = --verify-attestation ] && [ -n "$2" ]; then
    MODE=verify
    ATTESTATION_IN="$2"
  else
    echo "migration precheck: usage: check-migrations.sh [--verify-attestation <file>]" >&2
    exit 78
  fi
fi
ATTESTATION_OUT="${PANDORA_PRECHECK_ATTESTATION_OUT:-}"
if [ -n "$ATTESTATION_OUT" ]; then
  [ "$MODE" = full ] || { echo "migration precheck: attestation output only applies to a full precheck" >&2; exit 78; }
  [ "${ATTESTATION_OUT#/}" != "$ATTESTATION_OUT" ] \
    || { echo "migration precheck: attestation path must be absolute" >&2; exit 78; }
  [ ! -e "$ATTESTATION_OUT" ] && [ ! -L "$ATTESTATION_OUT" ] \
    || { echo "migration precheck: attestation path already exists" >&2; exit 78; }
fi
REHEARSE_STOPPED_WRITER=no
if [ "${PANDORA_STOPPED_WRITER_UPGRADE_APPROVED:-}" = yes ] \
    || [ "${PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER:-}" = yes ]; then
  REHEARSE_STOPPED_WRITER=yes
fi

DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="${AEGIS_ENV_FILE:-$DEPLOY_DIR/.env}"
# 迁移文件默认取与 deploy/ 并排的 migrations/：install.sh（/opt/aegispanel）、install-native.sh
# （/opt/pandora）和源码树（make check-migrations）都是这个布局。不写死安装路径——
# 曾写死 /opt/pandora，install.sh 装的机器上会找不到，或者读到另一套安装留下的旧迁移。
MIGRATIONS_DIR="${AEGIS_MIGRATIONS_DIR:-$(dirname -- "$DEPLOY_DIR")/migrations}"

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
# 编号只要求严格递增、不重复，允许空号（主序列有 00073、00091、00092 三个历史
# 空号，不重编）。previous_version 最终是最大版本号，下面拿它和源库水位比。
previous_version=0
for migration in "${migration_files[@]}"; do
  name="${migration##*/}"
  if [[ ! "$name" =~ ^([0-9]{5})_[A-Za-z0-9._-]+\.sql$ ]]; then
    echo "migration precheck: invalid migration filename: $name" >&2
    exit 1
  fi
  version=$((10#${BASH_REMATCH[1]}))
  if [ "$version" -eq "$previous_version" ] && [ "$version" -ne 0 ]; then
    echo "migration precheck: duplicate migration version: $name" >&2
    exit 78
  fi
  if [ "$version" -le "$previous_version" ] || [ "$version" -eq 0 ]; then
    echo "migration precheck: migration versions must be strictly increasing from 00001: $name" >&2
    exit 78
  fi
  previous_version=$version
  printf '%-48s' "$name"
  if awk '
      /^-- \+goose Up([[:space:]]*)$/ { up=1 }
      END { exit !up }
    ' "$migration"; then
    :
  else
    echo "FAIL"
    echo "migration precheck: $name must contain an exact goose Up marker" >&2
    exit 1
  fi
  # 每个迁移都要能回滚，或者明说不能：没有 Down 段的迁移执行 goose down 只删版本行、
  # 什么也不撤，回滚会静默「成功」。完整规则（Down 里 RAISE、forward-fix）由
  # panel/tools/migrationlint 在 CI 里查，这里在发布物上兜底。
  if awk '
      /^-- \+goose Down([[:space:]]*)$/ { down=1 }
      /^-- \+goose Up([[:space:]]*)$/ { header_done=1 }
      !header_done && /^-- irreversible:[[:space:]]*[^[:space:]]/ { irreversible=1 }
      END { exit !(down || irreversible) }
    ' "$migration"; then
    echo "OK"
  else
    echo "FAIL"
    echo "migration precheck: $name must contain a goose Down section or an irreversible header" >&2
    exit 1
  fi
done

sha256_stream() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum | awk '{print $1}'; else shasum -a 256 | awk '{print $1}'; fi
}
# 迁移目录摘要：文件名与内容一起算。停服后的核对拿它确认「演练过的」就是「要跑的」。
MIGRATIONS_DIGEST="$(
  for migration in "${migration_files[@]}"; do
    printf '%s  %s\n' "$(sha256_stream <"$migration")" "${migration##*/}"
  done | sha256_stream
)"
[[ "$MIGRATIONS_DIGEST" =~ ^[0-9a-f]{64}$ ]] \
  || { echo "migration precheck: cannot digest the migration directory" >&2; exit 1; }

# 这里原本钉死了「42 号槽位必须是 00042_client_auth_expand.sql，且 SHA 必须是
# FFAF84B6…」。项目转为只做面板之后，CA42 客户端认证子系统冻结，它的两个迁移
# 移出了主线（从未在任何环境应用过，存档在 git 标签 archive/client-auth），42 号槽位改由
# 00042_seed_registration_mode.sql 占用，这条校验会把每一次发布都拦下来。
#
# 把版本号和具体文件绑定本身就不牢靠 —— 任何一次重排号都会让它失效。真正要防的
# 「迁移文件被篡改」应该对整个 migrations/ 目录做校验，而不是挑一个文件钉死。
# 恢复 CA42（代码与发布门禁脚本在 git 标签 archive/client-auth）时如果还需要
# 这类保护，按目录整体校验重做，不要再钉单个版本号。

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
# settle. This read-only cutover gate emits counts only, never user/order data.
# 发布控制器在停服前的完整预检里跑它一次，停服后 --verify-attestation 再跑一次：
# 后一次才是在全部写入者停止之后、正式迁移之前，结论以它为准。
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
    echo "migration precheck: cancel unpaid ones through the normal order-cancel flow, reconcile processing or paid ones order by order (never edit them with SQL), then rerun this gate" >&2
    exit 78
  fi
fi
echo "renewal cutover active_legacy=0"

if [ "$MODE" = verify ]; then
  # 停服后的轻量核对：上面已经只读地重做了文件校验、水位与续费闸门，这里与停服前那次
  # 完整预检留下的凭据逐项比对。对不上说明演练的不是这次要跑的东西（有人在中间迁移过、
  # 迁移目录换过、换了库、演练与正式运行的停写闸门不一致、或凭据太旧），一律拒绝。
  attest_refuse() {
    echo "migration precheck: attestation $1; production is unchanged, rerun the full precheck" >&2
    exit 78
  }
  [ -f "$ATTESTATION_IN" ] && [ ! -L "$ATTESTATION_IN" ] || attest_refuse "is missing or not a regular file"
  [ -O "$ATTESTATION_IN" ] || attest_refuse "is not owned by the current user"
  [ -z "$(find "$ATTESTATION_IN" -prune \( -perm -0020 -o -perm -0002 \) -print)" ] \
    || attest_refuse "is group- or world-writable"
  attestation_value() {
    awk -v key="$1" 'index($0, key "=") == 1 { print substr($0, length(key) + 2); found=1; exit } END { exit !found }' \
      "$ATTESTATION_IN"
  }
  [ "$(attestation_value format || true)" = pandora-precheck-v1 ] || attest_refuse "has an unknown format"
  [ "$(attestation_value database || true)" = "$POSTGRES_DB" ] || attest_refuse "was made for another database"
  [ "$(attestation_value migrations_sha256 || true)" = "$MIGRATIONS_DIGEST" ] \
    || attest_refuse "was made for a different migration directory"
  [ "$(attestation_value release_max_version || true)" = "$previous_version" ] \
    || attest_refuse "was made for a different release"
  [ "$(attestation_value source_goose_version || true)" = "$SOURCE_GOOSE_VERSION" ] \
    || attest_refuse "waterline does not match the database (now $SOURCE_GOOSE_VERSION)"
  [ "$(attestation_value rehearsed_stopped_writer || true)" = "$REHEARSE_STOPPED_WRITER" ] \
    || attest_refuse "rehearsed a different stopped-writer approval than this run"
  ATTESTED_EPOCH="$(attestation_value created_epoch || true)"
  [[ "$ATTESTED_EPOCH" =~ ^[0-9]{1,12}$ ]] || attest_refuse "has an invalid timestamp"
  ATTESTATION_AGE=$(( $(date +%s) - 10#$ATTESTED_EPOCH ))
  # 预检演练的是克隆那一刻的数据；隔得越久越不代表现在。发布控制器从预检到迁移通常是
  # 分钟级，但中间夹着整库加密备份，库大时备份本身可能要一两个小时，上限给到六小时。
  [ "$ATTESTATION_AGE" -ge 0 ] && [ "$ATTESTATION_AGE" -le 21600 ] \
    || attest_refuse "is older than six hours (or from the future)"
  echo "migration precheck attestation verified: source=$SOURCE_GOOSE_VERSION release=$previous_version age=${ATTESTATION_AGE}s"
  exit 0
fi

# 这里原本拦的是：CLIENT-AUTH-00042 会创建集群级角色，而本预检查是在同一个
# PostgreSQL 集群里克隆一个库来重放迁移的，重放它会污染生产集群。理由成立，
# 但条件写成了「版本号 ≥42」，于是 42 号槽位换成别的迁移之后照样拦。
#
# 42 号现在是 00042_seed_registration_mode.sql，只有两条 INSERT，不建角色。
# 恢复 CA42 时请按「迁移内容是否含 CREATE ROLE / CREATE DATABASE 等集群级 DDL」
# 来判断，而不是版本号 —— 那才是这道闸门真正要防的东西。

PRECHECK_STARTED_EPOCH="$(date +%s)"
psql_run -d postgres -qc "CREATE DATABASE $DB;"
SCRATCH_DB_CREATED=1
# Preserve owners, ACLs, goose history and representative data. pg_dump takes a
# transactionally consistent snapshot, so the clone is sound even while writers
# are still running (the release controller now prechecks before stopping them).
pg_dump_run -d "$POSTGRES_DB" | psql_run -d "$DB" -q
echo "migration precheck database clone created"

MIGRATION_PGOPTIONS=""
if [ "$REHEARSE_STOPPED_WRITER" = yes ]; then
  # 冻结迁移要的两个 aegis.client_auth_* 开关随它移出主序列，不再下发。
  MIGRATION_PGOPTIONS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes'
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

if [ -n "$ATTESTATION_OUT" ]; then
  # 先写同目录临时文件再改名：控制器只会看到完整的凭据。
  ATTESTATION_TMP="$(mktemp "$(dirname -- "$ATTESTATION_OUT")/.pandora-precheck-attestation.XXXXXX")"
  {
    printf 'format=pandora-precheck-v1\n'
    printf 'created_epoch=%s\n' "$PRECHECK_STARTED_EPOCH"
    printf 'database=%s\n' "$POSTGRES_DB"
    printf 'source_goose_version=%s\n' "$SOURCE_GOOSE_VERSION"
    printf 'release_max_version=%s\n' "$previous_version"
    printf 'migrations_sha256=%s\n' "$MIGRATIONS_DIGEST"
    printf 'rehearsed_stopped_writer=%s\n' "$REHEARSE_STOPPED_WRITER"
  } >"$ATTESTATION_TMP"
  mv -f -- "$ATTESTATION_TMP" "$ATTESTATION_OUT"
  echo "migration precheck attestation written"
fi

echo "migration precheck complete"
