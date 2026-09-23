#!/usr/bin/env bash
# Isolated PostgreSQL 18 migration preflight. This script is intentionally not
# wired into the production release path yet.
set -Eeuo pipefail
umask 077

deny() { echo "isolated migration preflight: $*" >&2; exit 78; }
log() { echo "isolated migration preflight: $*" >&2; }

DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="${AEGIS_ENV_FILE:-$DEPLOY_DIR/.env}"
# 迁移文件默认取与 deploy/ 并排的 migrations/：install.sh（/opt/aegispanel）、install-native.sh
# （/opt/pandora）和源码树（make check-migrations）都是这个布局。不写死安装路径——
# 曾写死 /opt/pandora，install.sh 装的机器上会找不到，或者读到另一套安装留下的旧迁移。
MIGRATIONS_DIR="${AEGIS_MIGRATIONS_DIR:-$(dirname -- "$DEPLOY_DIR")/migrations}"
GOOSE="${GOOSE_BIN:-/root/go/bin/goose}"
SOURCE_CONTAINER="${PANDORA_SOURCE_POSTGRES_CONTAINER:-aegis-postgres}"
EXPECTED_SOURCE_CONTAINER_ID="${PANDORA_EXPECTED_SOURCE_CONTAINER_ID:-}"
EXPECTED_SOURCE_SYSTEM_IDENTIFIER="${PANDORA_EXPECTED_SOURCE_SYSTEM_IDENTIFIER:-}"
EXPECTED_SOURCE_DATABASE="${PANDORA_EXPECTED_SOURCE_DATABASE:-}"
POSTGRES_IMAGE="${PANDORA_PREFLIGHT_POSTGRES_IMAGE:-postgres:18-alpine}"
EXPECTED_POSTGRES_IMAGE_ID="${PANDORA_EXPECTED_POSTGRES_IMAGE_ID:-}"
LEASE_SECONDS="${PANDORA_PREFLIGHT_LEASE_SECONDS:-900}"
ATTESTATION_OUT="${PANDORA_PREFLIGHT_ATTESTATION_OUT:-}"

# This helper is the historical CLIENT-AUTH-00042 preflight. The active
# product migration stream now owns version 00042 and keeps CA42 under
# migrations/frozen-client-auth. Refuse to run the historical gate against a
# current stream instead of silently applying the wrong waterline contract.
if [[ -f "$MIGRATIONS_DIR/00042_seed_registration_mode.sql" &&
      ! -f "$MIGRATIONS_DIR/00042_client_auth_expand.sql" ]]; then
  printf 'client_auth_00042_isolated_preflight=NOT_RUN reason=frozen_migration_boundary\n'
  exit 77
fi

COMMAND="${1:-up-to}"
if [ "$#" -gt 0 ]; then shift; fi
[ "$COMMAND" = up-to ] || deny "CLIENT-AUTH-00042 preflight requires exact up-to 42"
[ "$#" -le 1 ] || deny "up-to accepts one version"
if [ "$#" -eq 1 ]; then
  [ -n "$1" ] || deny "up-to version must not be empty"
  TARGET_VERSION="$1"
else
  TARGET_VERSION=42
fi
[ "$TARGET_VERSION" = 42 ] || deny "CLIENT-AUTH-00042 preflight requires exact up-to 42"
TARGET_VERSION_DEC=42
COMMAND_CANONICAL='up-to:42'

# A remote or alternate Docker daemon makes local container identity, labels,
# network isolation and cleanup claims meaningless. Reject it before the first
# Docker lookup or source inspection.
for docker_env_name in DOCKER_HOST DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH; do
  [[ -z "${!docker_env_name+x}" ]] || deny "$docker_env_name must be unset"
done

[ -r "$ENV_FILE" ] || deny "missing environment file"
[ -d "$MIGRATIONS_DIR" ] || deny "missing migrations directory"
[ ! -L "$MIGRATIONS_DIR" ] || deny "migrations directory must not be a symlink"
[ -x "$GOOSE" ] || deny "goose executable is missing"
command -v docker >/dev/null 2>&1 || deny "docker is required"
DOCKER_BIN="$(command -v docker)"
readonly LOCAL_DOCKER_HOST='unix:///var/run/docker.sock'
command -v openssl >/dev/null 2>&1 || deny "openssl is required"
command -v sha256sum >/dev/null 2>&1 || deny "sha256sum is required"
for required_command in awk basename date install mktemp rm seq sleep tr; do
  command -v "$required_command" >/dev/null 2>&1 || deny "$required_command is required"
done
[[ "$LEASE_SECONDS" =~ ^[0-9]+$ ]] || deny "invalid lease"
[ "$LEASE_SECONDS" -ge 60 ] && [ "$LEASE_SECONDS" -le 3600 ] || deny "lease must be 60..3600 seconds"

. "$ENV_FILE"
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
[[ "$POSTGRES_USER" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || deny "unsafe source PostgreSQL user"
[[ "$POSTGRES_DB" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || deny "unsafe source database"
[[ "$SOURCE_CONTAINER" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] || deny "unsafe source container"
[[ "$EXPECTED_SOURCE_CONTAINER_ID" =~ ^[0-9a-f]{64}$ ]] || deny "expected source container id is required"
[[ "$EXPECTED_SOURCE_SYSTEM_IDENTIFIER" =~ ^[0-9]+$ ]] || deny "expected source system identifier is required"
[[ "$EXPECTED_SOURCE_DATABASE" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || deny "expected source database is required"
[ "$POSTGRES_DB" = "$EXPECTED_SOURCE_DATABASE" ] || deny "source database does not match explicit expectation"
[[ "$POSTGRES_IMAGE" =~ ^[A-Za-z0-9][A-Za-z0-9._/@:-]{0,255}$ ]] || deny "unsafe PostgreSQL image reference"
[[ "$EXPECTED_POSTGRES_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || deny "expected PostgreSQL image id is required"

shopt -s nullglob
MIGRATION_FILES=("$MIGRATIONS_DIR"/*.sql)
[ "${#MIGRATION_FILES[@]}" -gt 0 ] || deny "no migrations found"

TEMP_ROOT="${TMPDIR:-/tmp}"
[ -d "$TEMP_ROOT" ] || deny "temporary root is missing"
TEMP_ROOT="$(cd "$TEMP_ROOT" && pwd -P)"
TMP_DIR="$(mktemp -d "$TEMP_ROOT/pandora-isolated-pg18.XXXXXX")"
RUN_TOKEN="$(basename "$TMP_DIR" | tr -cd 'A-Za-z0-9')-$$"
CONTAINER="pandora-pg18-preflight-$RUN_TOKEN"
NETWORK="pandora-pg18-preflight-$RUN_TOKEN"
BOOTSTRAP_USER="pandora_preflight_${RUN_TOKEN//[^A-Za-z0-9_]/_}"
BOOTSTRAP_USER="${BOOTSTRAP_USER:0:63}"
SOURCE_PASSWORD_FILE="$TMP_DIR/source.password"
ISOLATED_PASSWORD_FILE="$TMP_DIR/isolated.password"
GLOBALS_DUMP="$TMP_DIR/globals.sql"
DATABASE_DUMP="$TMP_DIR/database.dump"
MIGRATION_MANIFEST="$TMP_DIR/migrations.sha256"
MIGRATION_SNAPSHOT_DIR="$TMP_DIR/migrations"
ATTESTATION_TMP="$TMP_DIR/attestation.txt"
NETWORK_CREATED=0
CONTAINER_CREATED=0
LEASE_CREATED_AT="$(date -u +%s)"
LEASE_EXPIRES_AT=$((LEASE_CREATED_AT + LEASE_SECONDS))

run_clean() (
  local clean_path="$PATH" clean_home="${HOME:-/root}"
  exec -c /bin/bash --noprofile --norc -p -c '
    export PATH=$1 HOME=$2
    shift 2
    exec "$@"
  ' pandora-isolated-clean "$clean_path" "$clean_home" "$@"
)

docker_local() {
  run_clean "$DOCKER_BIN" --host "$LOCAL_DOCKER_HOST" "$@"
}

run_docker_password() (
  local password_file="$1"; shift
  local clean_path="$PATH" clean_home="${HOME:-/root}" docker_bin="$DOCKER_BIN"
  exec -c /bin/bash --noprofile --norc -p -c '
    password_file=$1; client_path=$2; client_home=$3; shift 3
    IFS= read -r PGPASSWORD <"$password_file" || exit 91
    export PATH="$client_path" HOME="$client_home" PGPASSWORD
    exec "$@"
  ' pandora-isolated-password "$password_file" "$clean_path" "$clean_home" \
    "$docker_bin" --host "$LOCAL_DOCKER_HOST" "$@"
)

SOURCE_DOCKER_TARGET="$SOURCE_CONTAINER"
SOURCE_CONTAINER_CLIENT='exec /usr/bin/env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin PGPASSWORD="$PGPASSWORD" PGOPTIONS="-c default_transaction_read_only=on" "$@"'
ISOLATED_CONTAINER_CLIENT='exec /usr/bin/env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin PGPASSWORD="$PGPASSWORD" "$@"'
source_psql() {
  run_docker_password "$SOURCE_PASSWORD_FILE" exec -i -e PGPASSWORD "$SOURCE_DOCKER_TARGET" \
    /bin/sh -c "$SOURCE_CONTAINER_CLIENT" source-client psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" "$@"
}
source_pg_dumpall() {
  run_docker_password "$SOURCE_PASSWORD_FILE" exec -i -e PGPASSWORD "$SOURCE_DOCKER_TARGET" \
    /bin/sh -c "$SOURCE_CONTAINER_CLIENT" source-client pg_dumpall -U "$POSTGRES_USER" \
    --globals-only --no-role-passwords --no-tablespaces
}
source_pg_dump() {
  run_docker_password "$SOURCE_PASSWORD_FILE" exec -i -e PGPASSWORD "$SOURCE_DOCKER_TARGET" \
    /bin/sh -c "$SOURCE_CONTAINER_CLIENT" source-client pg_dump -U "$POSTGRES_USER" \
    --format=custom --no-tablespaces -d "$POSTGRES_DB"
}
isolated_client() {
  run_docker_password "$ISOLATED_PASSWORD_FILE" exec -i -e PGPASSWORD "$ISOLATED_CONTAINER_ID" \
    /bin/sh -c "$ISOLATED_CONTAINER_CLIENT" isolated-client "$@"
}

cleanup_resources() {
  local failed=0
  set +e
  if [ "$CONTAINER_CREATED" -eq 1 ]; then
    if docker_local rm -f "$ISOLATED_CONTAINER_ID" >/dev/null 2>&1; then
      CONTAINER_CREATED=0
    else
      failed=1
    fi
  fi
  if [ "$NETWORK_CREATED" -eq 1 ]; then
    if docker_local network rm "$NETWORK_ID" >/dev/null 2>&1; then
      NETWORK_CREATED=0
    else
      failed=1
    fi
  fi
  set -e
  [ "$failed" -eq 0 ]
}

cleanup() {
  local status=$? cleanup_failed=0
  trap - EXIT INT TERM
  cleanup_resources || cleanup_failed=1
  case "$TMP_DIR" in
    "$TEMP_ROOT"/pandora-isolated-pg18.*) rm -rf -- "$TMP_DIR" || cleanup_failed=1 ;;
    *) cleanup_failed=1 ;;
  esac
  if [ "$status" -eq 0 ] && [ "$cleanup_failed" -ne 0 ]; then status=78; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Install cleanup before materializing any secret or dump. From this point on,
# every ordinary failure path removes the private workspace and any resources
# whose exact IDs were recorded by this run.
printf '%s\n' "$POSTGRES_PASSWORD" >"$SOURCE_PASSWORD_FILE"
unset POSTGRES_PASSWORD
openssl rand -hex 32 >"$ISOLATED_PASSWORD_FILE"
chmod 0600 "$SOURCE_PASSWORD_FILE" "$ISOLATED_PASSWORD_FILE"
install -d -m 0700 "$MIGRATION_SNAPSHOT_DIR"

# The source cluster boundary is deliberately narrow: SELECT, pg_dumpall and
# pg_dump only. No source helper below can issue CREATE/DROP/ALTER/ROLE.
SOURCE_CONTAINER_ID="$(docker_local inspect --format '{{.Id}}' "$SOURCE_CONTAINER")" \
  || deny "cannot inspect source container identity"
[ "$SOURCE_CONTAINER_ID" = "$EXPECTED_SOURCE_CONTAINER_ID" ] \
  || deny "source container id does not match explicit expectation"
SOURCE_DOCKER_TARGET="$SOURCE_CONTAINER_ID"
SOURCE_SYSTEM_IDENTIFIER="$(source_psql -d postgres -tAc \
  "SELECT system_identifier FROM pg_catalog.pg_control_system();")" \
  || deny "cannot read source system identifier"
[[ "$SOURCE_SYSTEM_IDENTIFIER" =~ ^[0-9]+$ ]] || deny "invalid source system identifier"
[ "$SOURCE_SYSTEM_IDENTIFIER" = "$EXPECTED_SOURCE_SYSTEM_IDENTIFIER" ] \
  || deny "source system identifier does not match explicit expectation"
SOURCE_GOOSE_TABLE="$(source_psql -d "$POSTGRES_DB" -tAc \
  "SELECT pg_catalog.to_regclass('public.goose_db_version') IS NOT NULL;")" \
  || deny "cannot inspect source goose table"
[ "$SOURCE_GOOSE_TABLE" = t ] || deny "source goose table is missing"
SOURCE_GOOSE_WATERLINE="$(source_psql -d "$POSTGRES_DB" -tAc \
  "SELECT coalesce(max(version_id) FILTER (WHERE is_applied),0) FROM public.goose_db_version;")" \
  || deny "cannot read source goose waterline"
[[ "$SOURCE_GOOSE_WATERLINE" =~ ^[0-9]+$ ]] || deny "invalid source goose waterline"
[ "$SOURCE_GOOSE_WATERLINE" = 41 ] || deny "CLIENT-AUTH-00042 requires exact source Goose waterline 41"
SOURCE_DB_FINGERPRINT="$(source_psql -d postgres -tAc \
  "SELECT oid::text || '|' || datdba::text || '|' || pg_catalog.pg_get_userbyid(datdba) FROM pg_catalog.pg_database WHERE datname='$POSTGRES_DB';")" \
  || deny "cannot read source database identity"
IFS='|' read -r SOURCE_DATABASE_OID SOURCE_DB_OWNER_OID SOURCE_DB_OWNER SOURCE_DB_EXTRA <<<"$SOURCE_DB_FINGERPRINT"
[[ -z "$SOURCE_DB_EXTRA" && "$SOURCE_DATABASE_OID" =~ ^[1-9][0-9]*$ \
  && "$SOURCE_DB_OWNER_OID" =~ ^[1-9][0-9]*$ \
  && "$SOURCE_DB_OWNER" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] \
  || deny "unsafe source database identity"
BOOTSTRAP_ROLE_COLLISION="$(source_psql -d postgres -tAc \
  "SELECT count(*) FROM pg_catalog.pg_roles WHERE rolname='$BOOTSTRAP_USER';")" \
  || deny "cannot check isolated bootstrap role provenance"
[ "$BOOTSTRAP_ROLE_COLLISION" = 0 ] || deny "isolated bootstrap role collides with source globals"

expected_version=1
CLIENT_AUTH_00042_FROZEN_SHA256=ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5
for migration in "${MIGRATION_FILES[@]}"; do
  [ -f "$migration" ] && [ ! -L "$migration" ] || deny "migration must be a regular non-symlink file"
  name="${migration##*/}"
  [[ "$name" =~ ^([0-9]{5})_[A-Za-z0-9._-]+\.sql$ ]] || deny "invalid migration filename: $name"
  version=$((10#${BASH_REMATCH[1]}))
  [ "$version" -eq "$expected_version" ] || deny "migration versions must be continuous from 00001"
  expected_version=$((expected_version + 1))
  if [ "$version" -eq 42 ]; then
    [ "$name" = 00042_client_auth_expand.sql ] || deny "unexpected CLIENT-AUTH-00042 filename"
  fi
done
install -m 0600 "${MIGRATION_FILES[@]}" "$MIGRATION_SNAPSHOT_DIR/" \
  || deny "cannot create private migration snapshot"
(cd "$MIGRATION_SNAPSHOT_DIR" && sha256sum -- *.sql \
  | awk '{name=$2; sub(/^\*/,"",name); print $1 "  " name}') >"$MIGRATION_MANIFEST" \
  || deny "cannot hash migration snapshot"
[ "$(wc -l <"$MIGRATION_MANIFEST")" -eq "${#MIGRATION_FILES[@]}" ] \
  || deny "migration snapshot manifest is incomplete"
[ -f "$MIGRATION_SNAPSHOT_DIR/00042_client_auth_expand.sql" ] \
  || deny "CLIENT-AUTH-00042 migration is missing"
CLIENT_AUTH_00042_ACTUAL_SHA256="$(awk '$2=="00042_client_auth_expand.sql" {print $1}' "$MIGRATION_MANIFEST")"
[ "$CLIENT_AUTH_00042_ACTUAL_SHA256" = "$CLIENT_AUTH_00042_FROZEN_SHA256" ] \
  || deny "CLIENT-AUTH-00042 frozen SHA256 mismatch"
MIGRATION_SET_SHA256="$(sha256sum "$MIGRATION_MANIFEST" | awk '{print $1}')"
run_clean "$GOOSE" -dir "$MIGRATION_SNAPSHOT_DIR" validate \
  || deny "goose rejected migration snapshot"

source_pg_dumpall >"$GLOBALS_DUMP" || deny "source globals dump failed"
source_pg_dump >"$DATABASE_DUMP" || deny "source database dump failed"
[ -s "$GLOBALS_DUMP" ] && [ -s "$DATABASE_DUMP" ] || deny "source dump is empty"

SOURCE_SYSTEM_IDENTIFIER_AFTER="$(source_psql -d postgres -tAc \
  "SELECT system_identifier FROM pg_catalog.pg_control_system();")" \
  || deny "cannot re-read source system identifier"
SOURCE_GOOSE_WATERLINE_AFTER="$(source_psql -d "$POSTGRES_DB" -tAc \
  "SELECT coalesce(max(version_id) FILTER (WHERE is_applied),0) FROM public.goose_db_version;")" \
  || deny "cannot re-read source goose waterline"
SOURCE_CONTAINER_ID_AFTER="$(docker_local inspect --format '{{.Id}}' "$SOURCE_DOCKER_TARGET")" \
  || deny "cannot re-read source container identity"
SOURCE_DB_FINGERPRINT_AFTER="$(source_psql -d postgres -tAc \
  "SELECT oid::text || '|' || datdba::text || '|' || pg_catalog.pg_get_userbyid(datdba) FROM pg_catalog.pg_database WHERE datname='$POSTGRES_DB';")" \
  || deny "cannot re-read source database identity"
[ "$SOURCE_CONTAINER_ID_AFTER" = "$SOURCE_CONTAINER_ID" ] \
  && [ "$SOURCE_SYSTEM_IDENTIFIER_AFTER" = "$SOURCE_SYSTEM_IDENTIFIER" ] \
  && [ "$SOURCE_GOOSE_WATERLINE_AFTER" = "$SOURCE_GOOSE_WATERLINE" ] \
  && [ "$SOURCE_DB_FINGERPRINT_AFTER" = "$SOURCE_DB_FINGERPRINT" ] \
  || deny "source identity or goose waterline changed during dump"

IMAGE_ID="$(docker_local image inspect --format '{{.Id}}' "$POSTGRES_IMAGE")" \
  || deny "PostgreSQL 18 image is unavailable"
IMAGE_REPODIGEST="$(docker_local image inspect --format '{{index .RepoDigests 0}}' "$POSTGRES_IMAGE")" \
  || deny "PostgreSQL image digest is unavailable"
[[ "$IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || deny "invalid PostgreSQL image id"
[ "$IMAGE_ID" = "$EXPECTED_POSTGRES_IMAGE_ID" ] \
  || deny "resolved PostgreSQL image id is not release-allowlisted"
[[ "$IMAGE_REPODIGEST" =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*@sha256:[0-9a-f]{64}$ ]] \
  || deny "invalid PostgreSQL image digest"

NETWORK_ID="$(docker_local network create --internal \
  --label pandora.preflight=isolated-pg18-v1 \
  --label "pandora.preflight.run_id=$RUN_TOKEN" \
  --label "pandora.preflight.expires_at=$LEASE_EXPIRES_AT" \
  --label pandora.manifest.source=trusted-disposable-client-auth-00042-v1 \
  --label "pandora.manifest.migration_sha256=$CLIENT_AUTH_00042_FROZEN_SHA256" \
  "$NETWORK")" || deny "cannot create isolated network"
[[ "$NETWORK_ID" =~ ^[0-9a-f]{64}$ ]] || deny "isolated network id invalid"
NETWORK_CREATED=1

NETWORK_FINGERPRINT="$(docker_local network inspect --format \
  '{{.Id}}|{{.Name}}|{{.Internal}}|{{index .Labels "pandora.preflight"}}|{{index .Labels "pandora.preflight.run_id"}}|{{index .Labels "pandora.preflight.expires_at"}}' "$NETWORK_ID")" \
  || deny "cannot inspect isolated network"
IFS='|' read -r NETWORK_ACTUAL_ID NETWORK_ACTUAL_NAME NETWORK_INTERNAL NETWORK_KIND NETWORK_RUN NETWORK_EXPIRES NETWORK_EXTRA <<<"$NETWORK_FINGERPRINT"
[[ -z "$NETWORK_EXTRA" && "$NETWORK_ACTUAL_ID" = "$NETWORK_ID" && "$NETWORK_ACTUAL_NAME" = "$NETWORK" \
  && "$NETWORK_INTERNAL" = true && "$NETWORK_KIND" = isolated-pg18-v1 \
  && "$NETWORK_RUN" = "$RUN_TOKEN" && "$NETWORK_EXPIRES" = "$LEASE_EXPIRES_AT" ]] \
  || deny "isolated network fingerprint mismatch"

ISOLATED_CONTAINER_ID="$(docker_local run -d --rm --name "$CONTAINER" --network "$NETWORK" \
  -p 127.0.0.1::5432 \
  --label pandora.preflight=isolated-pg18-v1 \
  --label "pandora.preflight.run_id=$RUN_TOKEN" \
  --label "pandora.preflight.expires_at=$LEASE_EXPIRES_AT" \
  --label pandora.manifest.source=trusted-disposable-client-auth-00042-v1 \
  --label "pandora.manifest.migration_sha256=$CLIENT_AUTH_00042_FROZEN_SHA256" \
  --tmpfs /var/lib/postgresql:rw,nosuid,nodev,noexec,mode=0700 \
  --mount "type=bind,src=$ISOLATED_PASSWORD_FILE,dst=/run/secrets/postgres_password,readonly" \
  -e "POSTGRES_USER=$BOOTSTRAP_USER" -e POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password \
  -e POSTGRES_DB=postgres "$IMAGE_ID")" || deny "cannot start isolated PostgreSQL 18"
[[ "$ISOLATED_CONTAINER_ID" =~ ^[0-9a-f]{64}$ ]] || deny "isolated container id invalid"
CONTAINER_CREATED=1
CONTAINER_FINGERPRINT="$(docker_local inspect --format \
  '{{.Id}}|{{.Name}}|{{.Image}}|{{index .Config.Labels "pandora.preflight"}}|{{index .Config.Labels "pandora.preflight.run_id"}}|{{index .Config.Labels "pandora.preflight.expires_at"}}|{{index .Config.Labels "pandora.manifest.source"}}|{{index .Config.Labels "pandora.manifest.migration_sha256"}}|{{.State.Running}}|{{.HostConfig.AutoRemove}}|{{.HostConfig.NetworkMode}}' \
  "$ISOLATED_CONTAINER_ID")" || deny "cannot inspect isolated container"
IFS='|' read -r CONTAINER_ACTUAL_ID CONTAINER_ACTUAL_NAME CONTAINER_IMAGE_ID CONTAINER_KIND CONTAINER_RUN CONTAINER_EXPIRES CONTAINER_SOURCE_KIND CONTAINER_MIGRATION_SHA CONTAINER_RUNNING CONTAINER_AUTOREMOVE CONTAINER_NETWORK_MODE CONTAINER_EXTRA <<<"$CONTAINER_FINGERPRINT"
CONTAINER_ACTUAL_NAME="${CONTAINER_ACTUAL_NAME#/}"
[[ -z "$CONTAINER_EXTRA" && "$CONTAINER_ACTUAL_ID" = "$ISOLATED_CONTAINER_ID" \
  && "$CONTAINER_ACTUAL_NAME" = "$CONTAINER" && "$CONTAINER_IMAGE_ID" = "$IMAGE_ID" \
  && "$CONTAINER_KIND" = isolated-pg18-v1 && "$CONTAINER_RUN" = "$RUN_TOKEN" \
  && "$CONTAINER_EXPIRES" = "$LEASE_EXPIRES_AT" \
  && "$CONTAINER_SOURCE_KIND" = trusted-disposable-client-auth-00042-v1 \
  && "$CONTAINER_MIGRATION_SHA" = "$CLIENT_AUTH_00042_FROZEN_SHA256" \
  && "$CONTAINER_RUNNING" = true && "$CONTAINER_AUTOREMOVE" = true \
  && "$CONTAINER_NETWORK_MODE" = "$NETWORK" ]] || deny "isolated container fingerprint mismatch"

ready=0
for _ in $(seq 1 60); do
  if isolated_client pg_isready -U "$BOOTSTRAP_USER" -d postgres >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
[ "$ready" -eq 1 ] || deny "isolated PostgreSQL did not become ready"

PUBLISHED_ENDPOINT="$(docker_local port "$ISOLATED_CONTAINER_ID" 5432/tcp)" \
  || deny "cannot resolve isolated PostgreSQL port"
[[ "$PUBLISHED_ENDPOINT" =~ ^127\.0\.0\.1:([0-9]+)$ ]] || deny "isolated port is not random loopback"
ISOLATED_PORT="${BASH_REMATCH[1]}"
[ "$ISOLATED_PORT" -ge 1 ] && [ "$ISOLATED_PORT" -le 65535 ] || deny "invalid isolated port"

ISOLATED_VERSION_NUM="$(isolated_client psql -X -v ON_ERROR_STOP=1 -U "$BOOTSTRAP_USER" \
  -d postgres -tAc "SELECT current_setting('server_version_num');")" \
  || deny "cannot inspect isolated PostgreSQL version"
[[ "$ISOLATED_VERSION_NUM" =~ ^18[0-9]{4}$ ]] || deny "isolated database is not PostgreSQL 18"

isolated_client createdb -U "$BOOTSTRAP_USER" -O "$BOOTSTRAP_USER" "$POSTGRES_DB" \
  || deny "cannot create isolated target database"
isolated_client psql -X -v ON_ERROR_STOP=1 -U "$BOOTSTRAP_USER" -d postgres -f - \
  <"$GLOBALS_DUMP" >/dev/null || deny "cannot restore password-free globals"
isolated_client psql -X -v ON_ERROR_STOP=1 -U "$BOOTSTRAP_USER" -d postgres -c \
  "ALTER DATABASE \"$POSTGRES_DB\" OWNER TO \"$SOURCE_DB_OWNER\";" >/dev/null \
  || deny "cannot restore source database owner"
isolated_client pg_restore --exit-on-error --no-password -U "$BOOTSTRAP_USER" \
  -d "$POSTGRES_DB" <"$DATABASE_DUMP" >/dev/null || deny "cannot restore source database dump"

GOOSE_DBSTRING="host=127.0.0.1 port=$ISOLATED_PORT user=$BOOTSTRAP_USER dbname=$POSTGRES_DB sslmode=disable"
GOOSE_PGOPTIONS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes -c aegis.client_auth_00042_upgrade_approved=approved-v1 -c aegis.client_auth_writers_stopped=stopped-v1'
run_isolated_goose() (
  local clean_path="$PATH" clean_home="${HOME:-/root}"
  exec -c /bin/bash --noprofile --norc -p -c '
    password_file=$1; client_path=$2; client_home=$3; goose=$4
    dbstring=$5; migrations=$6; pgoptions=$7; shift 7
    IFS= read -r PGPASSWORD <"$password_file" || exit 92
    export PATH="$client_path" HOME="$client_home" PGPASSWORD PGOPTIONS="$pgoptions"
    export GOOSE_DRIVER=postgres GOOSE_DBSTRING="$dbstring" GOOSE_MIGRATION_DIR="$migrations"
    exec "$goose" "$@"
  ' pandora-isolated-goose "$ISOLATED_PASSWORD_FILE" "$clean_path" "$clean_home" \
    "$GOOSE" "$GOOSE_DBSTRING" "$MIGRATION_SNAPSHOT_DIR" "$GOOSE_PGOPTIONS" "$@"
)
run_isolated_goose up-to "$TARGET_VERSION_DEC" || deny "isolated goose up-to failed"

ISOLATED_42_APPLIED="$(isolated_client psql -X -v ON_ERROR_STOP=1 -U "$BOOTSTRAP_USER" \
  -d "$POSTGRES_DB" -tAc \
  "SELECT CASE WHEN EXISTS(SELECT 1 FROM public.goose_db_version WHERE version_id=42 AND is_applied) THEN 1 ELSE 0 END;")" \
  || deny "cannot verify isolated CLIENT-AUTH-00042 state"
[ "$ISOLATED_42_APPLIED" = 1 ] || deny "isolated preflight did not apply CLIENT-AUTH-00042"
ISOLATED_GOOSE_WATERLINE="$(isolated_client psql -X -v ON_ERROR_STOP=1 -U "$BOOTSTRAP_USER" \
  -d "$POSTGRES_DB" -tAc \
  "SELECT coalesce(max(version_id) FILTER (WHERE is_applied),0) FROM public.goose_db_version;")" \
  || deny "cannot read isolated Goose waterline"
[ "$ISOLATED_GOOSE_WATERLINE" = 42 ] || deny "isolated Goose waterline is not exact 42"
ISOLATED_SYSTEM_IDENTIFIER="$(isolated_client psql -X -v ON_ERROR_STOP=1 -U "$BOOTSTRAP_USER" \
  -d postgres -tAc "SELECT system_identifier FROM pg_catalog.pg_control_system();")" \
  || deny "cannot read isolated system identifier"
[[ "$ISOLATED_SYSTEM_IDENTIFIER" =~ ^[1-9][0-9]*$ \
  && "$ISOLATED_SYSTEM_IDENTIFIER" != "$SOURCE_SYSTEM_IDENTIFIER" ]] \
  || deny "isolated system identifier invalid or conflated"
ISOLATED_DATABASE_OID="$(isolated_client psql -X -v ON_ERROR_STOP=1 -U "$BOOTSTRAP_USER" \
  -d postgres -tAc "SELECT oid::text FROM pg_catalog.pg_database WHERE datname='$POSTGRES_DB';")" \
  || deny "cannot read isolated database oid"
[[ "$ISOLATED_DATABASE_OID" =~ ^[1-9][0-9]*$ ]] || deny "isolated database oid invalid"
(cd "$MIGRATION_SNAPSHOT_DIR" && sha256sum --check --status "$MIGRATION_MANIFEST") \
  || deny "migration snapshot changed during preflight"

CONTAINER_FINGERPRINT_AFTER="$(docker_local inspect --format \
  '{{.Id}}|{{.Name}}|{{.Image}}|{{index .Config.Labels "pandora.preflight"}}|{{index .Config.Labels "pandora.preflight.run_id"}}|{{index .Config.Labels "pandora.preflight.expires_at"}}|{{index .Config.Labels "pandora.manifest.source"}}|{{index .Config.Labels "pandora.manifest.migration_sha256"}}|{{.State.Running}}|{{.HostConfig.AutoRemove}}|{{.HostConfig.NetworkMode}}' \
  "$ISOLATED_CONTAINER_ID")" || deny "cannot re-inspect isolated container"
[ "$CONTAINER_FINGERPRINT_AFTER" = "$CONTAINER_FINGERPRINT" ] \
  || deny "isolated container fingerprint changed during preflight"
NETWORK_FINGERPRINT_AFTER="$(docker_local network inspect --format \
  '{{.Id}}|{{.Name}}|{{.Internal}}|{{index .Labels "pandora.preflight"}}|{{index .Labels "pandora.preflight.run_id"}}|{{index .Labels "pandora.preflight.expires_at"}}' "$NETWORK_ID")" \
  || deny "cannot re-inspect isolated network"
[ "$NETWORK_FINGERPRINT_AFTER" = "$NETWORK_FINGERPRINT" ] \
  || deny "isolated network fingerprint changed during preflight"

COMPLETED_AT="$(date -u +%s)"
{
  printf 'format=isolated-pg18-preflight-v1\n'
  printf 'source_system_identifier=%s\n' "$SOURCE_SYSTEM_IDENTIFIER"
  printf 'source_container_id=%s\n' "$SOURCE_CONTAINER_ID"
  printf 'source_database=%s\n' "$POSTGRES_DB"
  printf 'source_database_oid=%s\n' "$SOURCE_DATABASE_OID"
  printf 'source_database_owner_oid=%s\n' "$SOURCE_DB_OWNER_OID"
  printf 'source_database_owner_name=%s\n' "$SOURCE_DB_OWNER"
  printf 'expected_source_database=%s\n' "$EXPECTED_SOURCE_DATABASE"
  printf 'source_goose_waterline=%s\n' "$SOURCE_GOOSE_WATERLINE"
  printf 'command=%s\n' "$COMMAND_CANONICAL"
  printf 'postgres_image_ref=%s\n' "$POSTGRES_IMAGE"
  printf 'postgres_image_id=%s\n' "$IMAGE_ID"
  printf 'expected_postgres_image_id=%s\n' "$EXPECTED_POSTGRES_IMAGE_ID"
  printf 'postgres_image_digest=%s\n' "$IMAGE_REPODIGEST"
  printf 'isolated_server_version_num=%s\n' "$ISOLATED_VERSION_NUM"
  printf 'isolated_container_id=%s\n' "$ISOLATED_CONTAINER_ID"
  printf 'isolated_network_id=%s\n' "$NETWORK_ID"
  printf 'isolated_system_identifier=%s\n' "$ISOLATED_SYSTEM_IDENTIFIER"
  printf 'isolated_database_oid=%s\n' "$ISOLATED_DATABASE_OID"
  printf 'isolated_goose_waterline=%s\n' "$ISOLATED_GOOSE_WATERLINE"
  printf 'migration_set_sha256=%s\n' "$MIGRATION_SET_SHA256"
  printf 'client_auth_00042_sha256=%s\n' "$CLIENT_AUTH_00042_FROZEN_SHA256"
  while IFS= read -r line; do printf 'migration_sha256=%s\n' "$line"; done <"$MIGRATION_MANIFEST"
  printf 'lease_created_at=%s\n' "$LEASE_CREATED_AT"
  printf 'lease_expires_at=%s\n' "$LEASE_EXPIRES_AT"
  printf 'completed_at=%s\n' "$COMPLETED_AT"
} >"$ATTESTATION_TMP"
ATTESTATION_SHA256="$(sha256sum "$ATTESTATION_TMP" | awk '{print $1}')"
printf 'attestation_sha256=%s\n' "$ATTESTATION_SHA256" >>"$ATTESTATION_TMP"

cleanup_resources || deny "isolated resource cleanup failed"

if [ -n "$ATTESTATION_OUT" ]; then
  [[ "$ATTESTATION_OUT" = /* ]] || deny "attestation output path must be absolute"
  [ ! -e "$ATTESTATION_OUT" ] || deny "attestation output already exists"
  install -m 0600 "$ATTESTATION_TMP" "$ATTESTATION_OUT" || deny "cannot publish attestation"
  log "attestation written: $ATTESTATION_OUT"
else
  while IFS= read -r line; do printf '%s\n' "$line"; done <"$ATTESTATION_TMP"
fi
log "complete"
