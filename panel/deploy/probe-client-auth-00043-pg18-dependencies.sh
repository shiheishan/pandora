#!/usr/bin/env bash
# Narrow, disposable PostgreSQL 18 catalog probe for CLIENT-AUTH-00043.
# It never connects to a caller-supplied database and is not a migration entrypoint.
set -Eeuo pipefail
umask 077

readonly EXIT_DENIED=78
readonly LOCAL_DOCKER_HOST='unix:///var/run/docker.sock'
readonly V3_CONTRACT_SHA256='e8c323762014b5f97d04cf861287026f0d3784d56b2324e1e6b0bc14d833dac7'
readonly MIGRATION_42_SHA256='ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5'
readonly MIGRATION_43_SHA256='5e8d9ca3b18b3d4dba833b02395d94c9a27740e31746df44ade9673874018844'

deny() {
  printf 'client_auth_00043_dependency_probe=DENY reason=%s\n' "$1" >&2
  exit "$EXIT_DENIED"
}

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
V3_CONTRACT="$ROOT/.ai-company/handoffs/client-auth-00043-runner-goose-redesign-v3-20260731.md"
MIGRATION_42="$ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql"
MIGRATION_43="$ROOT/migrations/frozen-client-auth/00043_client_auth_existing_parent_indexes.sql"
BASELINE_SQL="${AEGIS_CA43_DEP_PROBE_BASELINE_SQL:-}"
FIXTURE_SQL="${AEGIS_CA43_DEP_PROBE_FIXTURE_SQL:-}"
EXPECTED_BASELINE_SHA="${AEGIS_CA43_DEP_PROBE_EXPECTED_BASELINE_SHA256:-}"
EXPECTED_FIXTURE_SHA="${AEGIS_CA43_DEP_PROBE_EXPECTED_FIXTURE_SHA256:-}"
POSTGRES_IMAGE="${AEGIS_CA43_DEP_PROBE_POSTGRES_IMAGE:-postgres:18.4}"
EXPECTED_IMAGE_ID="${AEGIS_CA43_DEP_PROBE_EXPECTED_POSTGRES_IMAGE_ID:-}"
EVIDENCE_DIR="${AEGIS_CA43_DEP_PROBE_EVIDENCE_DIR:-}"
APPROVED="${AEGIS_CA43_DEP_PROBE_APPROVED:-}"

readonly -a IDS=(
  U43-01 U43-02 U43-03 U43-04 U43-05 U43-06 U43-07 U43-08 U43-09
  U43-10 U43-11 U43-12 U43-13 U43-14 U43-15 U43-16 U43-17 U43-18
)
readonly -a TABLES=(
  subscriptions devices sessions sessions sessions sessions
  device_authorizations device_authorizations device_authorizations
  device_tokens device_tokens subscription_credentials subscription_credentials
  config_bundles refresh_tokens refresh_tokens refresh_tokens refresh_tokens
)
readonly -a NAMES=(
  subscriptions_tenant_id_id_user_id_key
  devices_tenant_id_id_user_id_key
  sessions_tenant_id_id_key
  sessions_tenant_id_id_user_id_key
  sessions_tenant_id_id_device_id_key
  sessions_tenant_id_id_user_id_device_id_key
  device_authorizations_tenant_id_id_key
  device_authorizations_tenant_id_id_device_id_key
  device_authorizations_tenant_user_code_mac_key
  device_tokens_tenant_id_id_key
  device_tokens_tenant_device_id_id_key
  subscription_credentials_tenant_id_id_key
  subscription_credentials_tenant_id_id_sub_user_key
  config_bundles_tenant_id_id_key
  refresh_tokens_tenant_id_id_key
  refresh_tokens_tenant_family_id_id_key
  refresh_tokens_tenant_family_session_device_id_key
  refresh_tokens_tenant_family_generation_key
)
readonly -a COLUMNS=(
  'tenant_id, id, user_id'
  'tenant_id, id, user_id'
  'tenant_id, id'
  'tenant_id, id, user_id'
  'tenant_id, id, device_id'
  'tenant_id, id, user_id, device_id'
  'tenant_id, id'
  'tenant_id, id, device_id'
  'tenant_id, user_code_mac'
  'tenant_id, id'
  'tenant_id, device_id, id'
  'tenant_id, id'
  'tenant_id, id, subscription_id, user_id'
  'tenant_id, id'
  'tenant_id, id'
  'tenant_id, family_id, id'
  'tenant_id, family_id, session_id, device_id, id'
  'tenant_id, family_id, generation'
)

[[ "$APPROVED" == 'approved-local-disposable-pg18-dependency-probe-v1' ]] \
  || deny approval_missing
[[ "$(uname -s 2>/dev/null)" == Linux ]] || deny native_linux_required
[[ "$(uname -r 2>/dev/null)" != *[Mm]icrosoft* ]] || deny wsl_forbidden
[[ -r /proc/version && "$(cat /proc/version)" != *[Mm]icrosoft* ]] \
  || deny native_proc_required
[[ "$EUID" -eq 0 ]] || deny root_required
for docker_env_name in DOCKER_HOST DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH; do
  [[ -z "${!docker_env_name:-}" ]] || deny "remote_docker_environment:$docker_env_name"
done

DOCKER_BIN="$(command -v docker 2>/dev/null)" || deny docker_not_found
SHA256_BIN="$(command -v sha256sum 2>/dev/null)" || deny sha256sum_not_found

docker_local() {
  env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    "$DOCKER_BIN" --host "$LOCAL_DOCKER_HOST" "$@"
}

regular_artifact() {
  [[ -f "$1" && ! -L "$1" ]] || deny "artifact_missing_or_unsafe:$2"
}

exact_sha() {
  [[ "$2" =~ ^[0-9a-f]{64}$ ]] || deny "expected_sha_invalid:$3"
  [[ "$("$SHA256_BIN" "$1" | awk '{print $1}')" == "$2" ]] \
    || deny "artifact_sha_mismatch:$3"
}

regular_artifact "$V3_CONTRACT" v3_contract
regular_artifact "$MIGRATION_42" migration42
regular_artifact "$MIGRATION_43" migration43
regular_artifact "$BASELINE_SQL" baseline
regular_artifact "$FIXTURE_SQL" fixture
exact_sha "$V3_CONTRACT" "$V3_CONTRACT_SHA256" v3_contract
exact_sha "$MIGRATION_42" "$MIGRATION_42_SHA256" migration42
exact_sha "$MIGRATION_43" "$MIGRATION_43_SHA256" migration43
exact_sha "$BASELINE_SQL" "$EXPECTED_BASELINE_SHA" baseline
exact_sha "$FIXTURE_SQL" "$EXPECTED_FIXTURE_SHA" fixture
[[ "$EXPECTED_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || deny expected_image_id_invalid
[[ "$EVIDENCE_DIR" == /* && ! -e "$EVIDENCE_DIR" && ! -L "$EVIDENCE_DIR" ]] \
  || deny evidence_dir_must_be_new_absolute_path
EVIDENCE_PARENT="$(dirname "$EVIDENCE_DIR")"
[[ -d "$EVIDENCE_PARENT" && ! -L "$EVIDENCE_PARENT" ]] || deny evidence_parent_invalid
parent_mode="$(stat -c '%a' "$EVIDENCE_PARENT")"
[[ "$(stat -c '%u' "$EVIDENCE_PARENT")" == 0 ]] || deny evidence_parent_not_root_owned
(( (8#$parent_mode & 8#022) == 0 )) || deny evidence_parent_group_world_writable

[[ "$(docker_local info --format '{{.OSType}}' 2>/dev/null)" == linux ]] \
  || deny linux_docker_engine_required
ACTUAL_IMAGE_ID="$(docker_local image inspect --format '{{.Id}}' "$POSTGRES_IMAGE" 2>/dev/null)" \
  || deny postgres18_image_unavailable
[[ "$ACTUAL_IMAGE_ID" == "$EXPECTED_IMAGE_ID" ]] || deny postgres_image_id_mismatch

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-ca43-dep-probe.XXXXXX")" \
  || deny temporary_directory_failed
chmod 0700 "$WORK_DIR"
STAGE_DIR="$(mktemp -d "$EVIDENCE_PARENT/.pandora-ca43-dep-evidence.XXXXXX")" \
  || deny evidence_stage_failed
chmod 0700 "$STAGE_DIR"
RUN_TOKEN="$(basename "$WORK_DIR" | tr -cd 'A-Za-z0-9')-$$"
NETWORK="pandora-ca43-dep-$RUN_TOKEN"
CONTAINER="pandora-ca43-dep-$RUN_TOKEN"
PASSWORD_FILE="$WORK_DIR/postgres.password"
DDL_FILE="$WORK_DIR/attach.sql"
NETWORK_ID=''
CONTAINER_ID=''
PUBLISHED=0

cleanup() {
  local rc=$?
  if [[ -n "$CONTAINER_ID" ]]; then
    docker_local rm -f "$CONTAINER_ID" >/dev/null 2>&1 || true
  fi
  if [[ -n "$NETWORK_ID" ]]; then
    docker_local network rm "$NETWORK_ID" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$WORK_DIR"
  if [[ "$PUBLISHED" -eq 0 ]]; then rm -rf -- "$STAGE_DIR"; fi
  exit "$rc"
}
trap cleanup EXIT HUP INT TERM

od -An -N32 -tx1 /dev/urandom | tr -d ' \n' >"$PASSWORD_FILE" \
  || deny password_generation_failed
[[ "$(wc -c <"$PASSWORD_FILE")" -eq 64 ]] || deny password_generation_invalid
chmod 0600 "$PASSWORD_FILE"

NETWORK_ID="$(docker_local network create --internal \
  --label pandora.probe=client-auth-00043-pg18-dependency-v1 "$NETWORK")" \
  || deny network_create_failed
[[ "$NETWORK_ID" =~ ^[0-9a-f]{64}$ ]] || deny network_id_invalid
[[ "$(docker_local network inspect --format '{{.Internal}}' "$NETWORK_ID")" == true ]] \
  || deny network_not_internal

CONTAINER_ID="$(docker_local run -d --rm --name "$CONTAINER" --network "$NETWORK" \
  --tmpfs /var/lib/postgresql:rw,nosuid,nodev,noexec,mode=0700 \
  --mount "type=bind,src=$PASSWORD_FILE,dst=/run/secrets/postgres_password,readonly" \
  --label pandora.probe=client-auth-00043-pg18-dependency-v1 \
  -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password \
  -e POSTGRES_DB=postgres "$ACTUAL_IMAGE_ID")" || deny container_start_failed
[[ "$CONTAINER_ID" =~ ^[0-9a-f]{64}$ ]] || deny container_id_invalid

ready=0
for _ in $(seq 1 120); do
  if docker_local exec "$CONTAINER_ID" pg_isready -U postgres -d postgres \
    >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.25
done
[[ "$ready" -eq 1 ]] || deny postgres18_not_ready
PG_VERSION_NUM="$(docker_local exec "$CONTAINER_ID" psql -X -U postgres -d postgres \
  -qtAc "SELECT current_setting('server_version_num');")" \
  || deny postgres_version_query_failed
[[ "$PG_VERSION_NUM" =~ ^18[0-9]{4}$ ]] || deny postgres_major_not_18

docker_local exec -i "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 \
  -U postgres -d postgres -f - <"$BASELINE_SQL" \
  >"$WORK_DIR/baseline.out" 2>"$WORK_DIR/baseline.err" \
  || deny baseline_restore_failed
BASE_STATE="$(docker_local exec "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 \
  -U postgres -d aegis_ca43_base -qtAc "
    SELECT current_setting('server_version_num')::integer/10000 || '|' ||
      coalesce((SELECT max(version_id) FILTER (WHERE is_applied)
        FROM public.goose_db_version),-1) || '|' ||
      (SELECT count(*) FROM app.client_auth_00042_meta);")" \
  || deny baseline_contract_query_failed
[[ "$BASE_STATE" == '18|42|1' ]] || deny "baseline_contract_mismatch:$BASE_STATE"

docker_local exec "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 \
  -U postgres -d postgres -c \
  "CREATE DATABASE ca43_dependency_probe TEMPLATE aegis_ca43_base;" \
  >/dev/null || deny probe_clone_failed
docker_local exec -i "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 \
  --set=scenario=clean -U postgres -d ca43_dependency_probe -f - <"$FIXTURE_SQL" \
  >"$WORK_DIR/fixture.out" 2>"$WORK_DIR/fixture.err" \
  || deny clean_fixture_failed

{
  printf '\\set ON_ERROR_STOP on\n'
  printf 'SET lock_timeout=%s;\n' "'5s'"
  printf 'SET statement_timeout=%s;\n' "'120s'"
  for i in "${!IDS[@]}"; do
    printf 'CREATE UNIQUE INDEX CONCURRENTLY %s ON public.%s USING btree (%s);\n' \
      "${NAMES[$i]}" "${TABLES[$i]}" "${COLUMNS[$i]}"
    printf 'ALTER TABLE public.%s ADD CONSTRAINT %s UNIQUE USING INDEX %s;\n' \
      "${TABLES[$i]}" "${NAMES[$i]}" "${NAMES[$i]}"
  done
} >"$DDL_FILE"
chmod 0600 "$DDL_FILE"
docker_local exec -i "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 \
  -U postgres -d ca43_dependency_probe -f - <"$DDL_FILE" \
  >"$WORK_DIR/ddl.out" 2>"$WORK_DIR/ddl.err" \
  || deny known_good_attach_failed

VALUES_SQL=''
for i in "${!IDS[@]}"; do
  [[ -z "$VALUES_SQL" ]] || VALUES_SQL+=','
  VALUES_SQL+="('${IDS[$i]}','${TABLES[$i]}','${NAMES[$i]}')"
done

INTERNAL_CHECK="$(docker_local exec "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 \
  -U postgres -d ca43_dependency_probe -qtAc "
WITH candidates(id,table_name,index_name) AS (VALUES $VALUES_SQL),
objects AS (
 SELECT c.id,tc.oid table_oid,ic.oid index_oid,con.oid constraint_oid
 FROM candidates c
 JOIN pg_namespace n ON n.nspname='public'
 JOIN pg_class tc ON tc.relnamespace=n.oid AND tc.relname=c.table_name
 JOIN pg_class ic ON ic.relnamespace=n.oid AND ic.relname=c.index_name
 JOIN pg_constraint con ON con.connamespace=n.oid AND con.conname=c.index_name
  AND con.conrelid=tc.oid AND con.conindid=ic.oid
)
SELECT count(*)||'|'||coalesce(sum(internal_count),0)||'|'||
       count(*) FILTER (WHERE internal_count=1)
FROM (
 SELECT o.id,count(d.*) internal_count
 FROM objects o LEFT JOIN pg_depend d
  ON d.classid='pg_class'::regclass AND d.objid=o.index_oid AND d.objsubid=0
  AND d.refclassid='pg_constraint'::regclass
  AND d.refobjid=o.constraint_oid AND d.refobjsubid=0 AND d.deptype='i'
 GROUP BY o.id
) checked;")" || deny internal_dependency_query_failed
[[ "$INTERNAL_CHECK" == '18|18|18' ]] \
  || deny "internal_dependency_not_exact:$INTERNAL_CHECK"

docker_local exec "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 \
  -U postgres -d ca43_dependency_probe -c "
COPY (
 WITH candidates(id,table_name,index_name) AS (VALUES $VALUES_SQL)
 SELECT c.id,c.table_name,c.index_name,con.conname constraint_name,
   d.classid::regclass::text class_name,d.objid,d.objsubid,
   d.refclassid::regclass::text refclass_name,d.refobjid,d.refobjsubid,d.deptype
 FROM candidates c
 JOIN pg_namespace n ON n.nspname='public'
 JOIN pg_class tc ON tc.relnamespace=n.oid AND tc.relname=c.table_name
 JOIN pg_class ic ON ic.relnamespace=n.oid AND ic.relname=c.index_name
 JOIN pg_constraint con ON con.connamespace=n.oid AND con.conname=c.index_name
  AND con.conrelid=tc.oid AND con.conindid=ic.oid
 JOIN pg_depend d ON d.classid='pg_class'::regclass AND d.objid=ic.oid
 ORDER BY c.id,d.classid,d.objid,d.objsubid,d.refclassid,d.refobjid,d.refobjsubid,d.deptype
) TO STDOUT WITH (FORMAT csv,DELIMITER E'\t',HEADER true);" \
  >"$STAGE_DIR/dependency-raw.tsv" || deny raw_dependency_export_failed

docker_local exec "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 \
  -U postgres -d ca43_dependency_probe -c "
COPY (
 WITH candidates(id,table_name,index_name) AS (VALUES $VALUES_SQL)
 SELECT c.id,c.table_name,c.index_name,con.conname constraint_name,
   d.classid::regclass::text class_name,d.objsubid,
   address.type ref_type,to_json(address.object_names)::text ref_names,
   to_json(address.object_args)::text ref_args,d.deptype
 FROM candidates c
 JOIN pg_namespace n ON n.nspname='public'
 JOIN pg_class tc ON tc.relnamespace=n.oid AND tc.relname=c.table_name
 JOIN pg_class ic ON ic.relnamespace=n.oid AND ic.relname=c.index_name
 JOIN pg_constraint con ON con.connamespace=n.oid AND con.conname=c.index_name
  AND con.conrelid=tc.oid AND con.conindid=ic.oid
 JOIN pg_depend d ON d.classid='pg_class'::regclass AND d.objid=ic.oid
 CROSS JOIN LATERAL pg_identify_object_as_address(
   d.refclassid,d.refobjid,d.refobjsubid) address
 ORDER BY c.id,class_name,d.objsubid,address.type,
   to_json(address.object_names)::text,to_json(address.object_args)::text,d.deptype
) TO STDOUT WITH (FORMAT csv,DELIMITER E'\t',HEADER true);" \
  >"$STAGE_DIR/dependency-portable.tsv" || deny portable_dependency_export_failed

RAW_ROWS="$(( $(wc -l <"$STAGE_DIR/dependency-raw.tsv") - 1 ))"
PORTABLE_ROWS="$(( $(wc -l <"$STAGE_DIR/dependency-portable.tsv") - 1 ))"
[[ "$RAW_ROWS" -gt 0 && "$RAW_ROWS" -eq "$PORTABLE_ROWS" ]] \
  || deny dependency_row_count_mismatch
RAW_SHA="$("$SHA256_BIN" "$STAGE_DIR/dependency-raw.tsv" | awk '{print $1}')"
PORTABLE_SHA="$("$SHA256_BIN" "$STAGE_DIR/dependency-portable.tsv" | awk '{print $1}')"

{
  printf 'format=client-auth-00043-pg18-dependency-probe-v1\n'
  printf 'status=COMPLETE\n'
  printf 'scope=local-disposable-postgresql18-clone\n'
  printf 'postgres_image_id=%s\n' "$ACTUAL_IMAGE_ID"
  printf 'postgres_version_num=%s\n' "$PG_VERSION_NUM"
  printf 'v3_contract_sha256=%s\n' "$V3_CONTRACT_SHA256"
  printf 'migration_00042_sha256=%s\n' "$MIGRATION_42_SHA256"
  printf 'migration_00043_sha256=%s\n' "$MIGRATION_43_SHA256"
  printf 'baseline_sha256=%s\n' "$EXPECTED_BASELINE_SHA"
  printf 'fixture_sha256=%s\n' "$EXPECTED_FIXTURE_SHA"
  printf 'candidate_count=18\n'
  printf 'internal_dependency_count=18\n'
  printf 'dependency_row_count=%s\n' "$PORTABLE_ROWS"
  printf 'raw_rows_sha256=%s\n' "$RAW_SHA"
  printf 'portable_signature_sha256=%s\n' "$PORTABLE_SHA"
  printf 'production_or_remote_target_used=false\n'
} >"$STAGE_DIR/manifest.txt"
chmod 0600 "$STAGE_DIR"/*
sync -f "$STAGE_DIR/dependency-raw.tsv" || deny raw_evidence_fsync_failed
sync -f "$STAGE_DIR/dependency-portable.tsv" || deny portable_evidence_fsync_failed
sync -f "$STAGE_DIR/manifest.txt" || deny manifest_fsync_failed
sync -f "$STAGE_DIR" || deny stage_directory_fsync_failed
mv -T -- "$STAGE_DIR" "$EVIDENCE_DIR" || deny evidence_publish_failed
STAGE_DIR="$EVIDENCE_DIR"
sync -f "$EVIDENCE_PARENT" || deny evidence_parent_fsync_failed
PUBLISHED=1

printf 'client_auth_00043_dependency_probe=PASS status=COMPLETE evidence=%s portable_signature_sha256=%s real_pg18=RUN\n' \
  "$EVIDENCE_DIR" "$PORTABLE_SHA"
