#!/usr/bin/env bash
# Generate external CLIENT-AUTH-00042 truth only from a live, locally-attested,
# disposable PostgreSQL 18 container. This script is deliberately not wired to
# migration, release, preflight, or production entrypoints.
set -Eeuo pipefail
umask 077

readonly EXIT_DENIED=78
readonly FROZEN_MIGRATION_SHA256='ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5'
readonly CONTRACT_SHA256='4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E'
readonly LOCAL_DOCKER_HOST='unix:///var/run/docker.sock'
readonly EXPECTED_KIND='isolated-pg18-v1'
readonly EXPECTED_SOURCE_KIND='trusted-disposable-client-auth-00042-v1'
readonly EXPECTED_BARRIER='exclusive-shared-and-local-catalog-lock-v1'

deny() { printf 'client_auth_00042_manifest=DENY reason=%s\n' "$1" >&2; exit "$EXIT_DENIED"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
MIGRATION="${AEGIS_CLIENT_AUTH_00042_MIGRATION:-$ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql}"
OUTPUT="${AEGIS_EXPECTED_MANIFEST_OUTPUT:-}"
SOURCE_CONTAINER_ID="${AEGIS_MANIFEST_SOURCE_CONTAINER_ID:-}"
SOURCE_SYSTEM_IDENTIFIER="${AEGIS_MANIFEST_SOURCE_SYSTEM_IDENTIFIER:-}"
SOURCE_DATABASE="${AEGIS_MANIFEST_SOURCE_DATABASE:-}"
SOURCE_DATABASE_OID="${AEGIS_MANIFEST_SOURCE_DATABASE_OID:-}"
SOURCE_DATABASE_USER="${AEGIS_MANIFEST_SOURCE_DATABASE_USER:-}"
SOURCE_IMAGE_ID="${AEGIS_MANIFEST_SOURCE_IMAGE_ID:-}"
SOURCE_RUN_ID="${AEGIS_MANIFEST_SOURCE_RUN_ID:-}"
BARRIER="${AEGIS_MANIFEST_BARRIER_MODE:-}"

for docker_env_name in DOCKER_HOST DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH; do
  [[ -z "${!docker_env_name:-}" ]] || deny "remote_docker_environment:${docker_env_name}"
done
DOCKER_BIN="$(command -v docker 2>/dev/null)" || deny 'docker_not_found'
SHA256_BIN="$(command -v sha256sum 2>/dev/null)" || deny 'sha256sum_not_found'
DATE_BIN="$(command -v date 2>/dev/null)" || deny 'date_not_found'

[[ -f "$MIGRATION" && ! -L "$MIGRATION" ]] || deny 'migration_not_regular'
[[ "$($SHA256_BIN "$MIGRATION" | awk '{print $1}')" == "$FROZEN_MIGRATION_SHA256" ]] \
  || deny 'frozen_migration_sha256_mismatch'
[[ "$OUTPUT" == /* && ! -e "$OUTPUT" ]] || deny 'output_must_be_new_absolute_path'
[[ "$SOURCE_CONTAINER_ID" =~ ^[0-9a-f]{64}$ ]] || deny 'source_container_id_invalid'
[[ "$SOURCE_SYSTEM_IDENTIFIER" =~ ^[1-9][0-9]*$ ]] || deny 'source_system_identifier_invalid'
[[ "$SOURCE_DATABASE" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] || deny 'source_database_invalid'
[[ "$SOURCE_DATABASE_OID" =~ ^[1-9][0-9]*$ ]] || deny 'source_database_oid_invalid'
[[ "$SOURCE_DATABASE_USER" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] || deny 'source_database_user_invalid'
[[ "$SOURCE_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || deny 'source_image_id_invalid'
[[ "$SOURCE_RUN_ID" =~ ^pandoraisolatedpg18[A-Za-z0-9]{6}-[1-9][0-9]*$ ]] \
  || deny 'source_run_id_invalid'
[[ "$BARRIER" == "$EXPECTED_BARRIER" ]] || deny 'barrier_mode_invalid'

docker_local() {
  env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    "$DOCKER_BIN" --host "$LOCAL_DOCKER_HOST" "$@"
}

NOW_EPOCH="$($DATE_BIN -u +%s 2>/dev/null)" || deny 'clock_unavailable'
[[ "$NOW_EPOCH" =~ ^[1-9][0-9]*$ ]] || deny 'clock_invalid'

inspect_source() {
  docker_local inspect --type container --format \
    '{{.Id}}|{{.Name}}|{{.Image}}|{{index .Config.Labels "pandora.preflight"}}|{{index .Config.Labels "pandora.preflight.run_id"}}|{{index .Config.Labels "pandora.preflight.expires_at"}}|{{index .Config.Labels "pandora.manifest.source"}}|{{index .Config.Labels "pandora.manifest.migration_sha256"}}|{{.State.Running}}|{{.HostConfig.AutoRemove}}|{{.HostConfig.NetworkMode}}' \
    "$SOURCE_CONTAINER_ID"
}

SOURCE_NETWORK_NAME="pandora-pg18-preflight-$SOURCE_RUN_ID"
inspect_source_network() {
  docker_local network inspect --format \
    '{{.Id}}|{{.Name}}|{{.Internal}}|{{index .Labels "pandora.preflight"}}|{{index .Labels "pandora.preflight.run_id"}}|{{index .Labels "pandora.preflight.expires_at"}}' \
    "$SOURCE_NETWORK_NAME"
}

FINGERPRINT="$(inspect_source 2>/dev/null)" || deny 'source_container_inspect_failed'
IFS='|' read -r actual_id source_name actual_image kind actual_run expires_at source_kind migration_sha running auto_remove network_mode extra <<<"$FINGERPRINT"
source_name="${source_name#/}"
[[ -z "${extra:-}" ]] || deny 'source_container_fingerprint_invalid'
[[ "$actual_id" == "$SOURCE_CONTAINER_ID" ]] || deny 'source_container_id_mismatch'
[[ "$source_name" == "pandora-pg18-preflight-$SOURCE_RUN_ID" ]] || deny 'source_container_name_mismatch'
[[ "$actual_image" == "$SOURCE_IMAGE_ID" ]] || deny 'source_image_id_mismatch'
[[ "$kind" == "$EXPECTED_KIND" && "$actual_run" == "$SOURCE_RUN_ID" ]] || deny 'source_preflight_labels_invalid'
[[ "$source_kind" == "$EXPECTED_SOURCE_KIND" ]] || deny 'source_not_trusted_disposable'
[[ "$migration_sha" == "$FROZEN_MIGRATION_SHA256" ]] || deny 'source_migration_label_mismatch'
[[ "$expires_at" =~ ^[1-9][0-9]*$ && "$expires_at" -gt "$NOW_EPOCH" ]] || deny 'source_lease_expired'
[[ "$running" == 'true' ]] || deny 'source_container_not_running'
[[ "$auto_remove" == 'true' && "$network_mode" == "$SOURCE_NETWORK_NAME" ]] \
  || deny 'source_container_not_disposable_isolated'
NETWORK_FINGERPRINT="$(inspect_source_network 2>/dev/null)" || deny 'source_network_inspect_failed'
IFS='|' read -r source_network_id network_name network_internal network_kind network_run network_expires network_extra <<<"$NETWORK_FINGERPRINT"
[[ -z "${network_extra:-}" && "$source_network_id" =~ ^[0-9a-f]{64}$ ]] \
  || deny 'source_network_fingerprint_invalid'
[[ "$network_name" == "$SOURCE_NETWORK_NAME" && "$network_internal" == 'true' \
   && "$network_kind" == "$EXPECTED_KIND" && "$network_run" == "$SOURCE_RUN_ID" \
   && "$network_expires" == "$expires_at" ]] || deny 'source_network_not_disposable_isolated'

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-ca42-manifest.XXXXXX")" || deny 'temporary_directory_failed'
TMP_OUTPUT=''
cleanup() {
  [[ -z "$TMP_OUTPUT" ]] || rm -f -- "$TMP_OUTPUT"
  rm -rf -- "$WORK_DIR"
}
trap cleanup EXIT HUP INT TERM

# Strong locks are intentional and are permitted only because the source is an
# attested disposable container. They close the stale-snapshot window for role,
# ACL and DDL provenance; a production endpoint must never reach this script.
if ! OBJECT_MANIFEST="$(docker_local exec -i "$SOURCE_CONTAINER_ID" \
  psql -X -v ON_ERROR_STOP=1 -qAt -U "$SOURCE_DATABASE_USER" -d "$SOURCE_DATABASE" -f - <<SQL
BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE;
SET LOCAL search_path=pg_catalog;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='2min';
LOCK TABLE pg_catalog.pg_authid,pg_catalog.pg_auth_members,
  pg_catalog.pg_db_role_setting,pg_catalog.pg_shdepend,
  pg_catalog.pg_shdescription IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE app.client_auth_00042_meta,public.devices,
  public.device_authorizations,public.config_bundles,public.refresh_tokens,
  public.config_bundle_nodes,public.refresh_families,public.device_proof_nonces,
  public.client_access_token_jtis,public.device_issuance_response_replays,
  public.client_refresh_response_replays,public.device_issuance_replay_uses,
  public.client_refresh_replay_uses IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.device_proof_nonces_id_seq,
  public.client_access_token_jtis_id_seq IN ACCESS EXCLUSIVE MODE;

DO \$guard\$
DECLARE
  v_applied bigint;
  v_role_oids jsonb;
  v_mask smallint;
  v_name text;
  v_oid oid;
  v_comment text;
  v_index integer;
  v_relation text;
  v_names constant text[] := ARRAY[
    'aegis_client_auth_owner','aegis_client_auth_gc_owner',
    'aegis_client_auth_gc','aegis_client_keyring_preflight_owner'];
  v_marker constant text :=
    'pandora:aegispanel:migration=00042;created=v1;contract=$CONTRACT_SHA256';
BEGIN
  IF current_setting('server_version_num')::integer / 10000 <> 18 THEN
    RAISE EXCEPTION 'PostgreSQL major mismatch';
  END IF;
  IF (SELECT system_identifier::text FROM pg_catalog.pg_control_system())
       IS DISTINCT FROM '$SOURCE_SYSTEM_IDENTIFIER'
     OR current_database() IS DISTINCT FROM '$SOURCE_DATABASE'
     OR (SELECT oid::text FROM pg_catalog.pg_database WHERE datname=current_database())
       IS DISTINCT FROM '$SOURCE_DATABASE_OID'
     OR NOT (SELECT usesuper FROM pg_catalog.pg_user WHERE usename=current_user) THEN
    RAISE EXCEPTION 'source identity or privilege mismatch';
  END IF;
  SELECT max(version_id) FILTER (WHERE is_applied) INTO v_applied
    FROM public.goose_db_version;
  IF v_applied IS DISTINCT FROM 42 OR EXISTS (
    SELECT 1 FROM public.goose_db_version WHERE is_applied AND version_id>42
  ) THEN RAISE EXCEPTION 'exact Goose 42 required'; END IF;
  IF (SELECT count(*) FROM app.client_auth_00042_meta)<>1 OR NOT EXISTS (
    SELECT 1 FROM app.client_auth_00042_meta WHERE singleton
      AND contract_sha256='$CONTRACT_SHA256'
      AND catalog_manifest->>'format'='client-auth-00042-catalog-v1'
      AND jsonb_array_length(catalog_manifest->'relations')=15
  ) THEN RAISE EXCEPTION '00042 meta mismatch'; END IF;
  SELECT role_creation_mask,role_oid_manifest INTO STRICT v_mask,v_role_oids
    FROM app.client_auth_00042_meta WHERE singleton;
  IF jsonb_typeof(v_role_oids) IS DISTINCT FROM 'object'
     OR (SELECT count(*) FROM jsonb_object_keys(v_role_oids))<>4
     OR NOT (v_role_oids ?& v_names) THEN
    RAISE EXCEPTION 'role provenance envelope mismatch';
  END IF;
  FOR v_index IN 1..array_length(v_names,1) LOOP
    v_name:=v_names[v_index];
    SELECT oid,pg_catalog.shobj_description(oid,'pg_authid')
      INTO STRICT v_oid,v_comment FROM pg_catalog.pg_authid WHERE rolname=v_name;
    IF v_oid IS DISTINCT FROM (v_role_oids->>v_name)::oid OR NOT EXISTS (
      SELECT 1 FROM pg_catalog.pg_authid WHERE oid=v_oid
        AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb
        AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication
        AND NOT rolbypassrls AND rolconnlimit=-1
        AND rolvaliduntil IS NULL AND rolpassword IS NULL
    ) OR EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members WHERE roleid=v_oid OR member=v_oid)
      OR EXISTS (SELECT 1 FROM pg_catalog.pg_db_role_setting WHERE setrole=v_oid)
      OR EXISTS (SELECT 1 FROM pg_catalog.pg_shdepend
        WHERE refclassid='pg_catalog.pg_authid'::regclass AND refobjid=v_oid)
    THEN RAISE EXCEPTION 'role provenance mismatch: %',v_name; END IF;
    IF (v_mask::integer & (1 << (v_index-1)))<>0
       AND v_comment IS DISTINCT FROM v_marker THEN
      RAISE EXCEPTION 'created role marker mismatch: %',v_name;
    ELSIF (v_mask::integer & (1 << (v_index-1)))=0
       AND v_comment LIKE 'pandora:aegispanel:migration=00042;%' THEN
      RAISE EXCEPTION 'retained role marker mismatch: %',v_name;
    END IF;
  END LOOP;
  FOREACH v_relation IN ARRAY ARRAY[
    'app.client_auth_00042_meta','public.devices',
    'public.device_authorizations','public.config_bundles','public.refresh_tokens',
    'public.config_bundle_nodes','public.refresh_families','public.device_proof_nonces',
    'public.client_access_token_jtis','public.device_issuance_response_replays',
    'public.client_refresh_response_replays','public.device_issuance_replay_uses',
    'public.client_refresh_replay_uses','public.device_proof_nonces_id_seq',
    'public.client_access_token_jtis_id_seq'] LOOP
    IF to_regclass(v_relation) IS NULL THEN
      RAISE EXCEPTION 'target relation missing: %',v_relation;
    END IF;
  END LOOP;
END \$guard\$;

WITH target(schema_name,relation_name) AS (
  VALUES ('app','client_auth_00042_meta'),('public','devices'),
    ('public','device_authorizations'),('public','config_bundles'),
    ('public','refresh_tokens'),('public','config_bundle_nodes'),
    ('public','refresh_families'),('public','device_proof_nonces'),
    ('public','client_access_token_jtis'),
    ('public','device_issuance_response_replays'),
    ('public','client_refresh_response_replays'),
    ('public','device_issuance_replay_uses'),
    ('public','client_refresh_replay_uses'),
    ('public','device_proof_nonces_id_seq'),
    ('public','client_access_token_jtis_id_seq')
), rels AS (
  SELECT jsonb_agg(jsonb_build_array(n.nspname,c.relname,c.oid::bigint,
    c.relfilenode::bigint,c.reltoastrelid::bigint,c.reltype::bigint,
    c.xmin::text,c.relowner::bigint,c.relacl) ORDER BY n.nspname,c.relname) value
  FROM target t JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
), attrs AS (
  SELECT jsonb_agg(jsonb_build_array(a.attrelid::bigint,a.attnum,a.attname,
    a.xmin::text,a.attacl) ORDER BY a.attrelid,a.attnum) value
  FROM target t JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
), constraints AS (
  SELECT coalesce(jsonb_agg(jsonb_build_array(x.oid::bigint,x.xmin::text,
    x.conrelid::bigint,x.conname) ORDER BY x.conrelid,x.conname),'[]'::jsonb) value
  FROM target t JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  JOIN pg_catalog.pg_constraint x ON x.conrelid=c.oid
), indexes AS (
  SELECT coalesce(jsonb_agg(jsonb_build_array(i.indexrelid::bigint,i.xmin::text,
    i.indrelid::bigint,ci.relname) ORDER BY i.indrelid,ci.relname),'[]'::jsonb) value
  FROM target t JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  JOIN pg_catalog.pg_index i ON i.indrelid=c.oid
  JOIN pg_catalog.pg_class ci ON ci.oid=i.indexrelid
), policies AS (
  SELECT coalesce(jsonb_agg(jsonb_build_array(p.oid::bigint,p.xmin::text,
    p.polrelid::bigint,p.polname) ORDER BY p.polrelid,p.polname),'[]'::jsonb) value
  FROM target t JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  JOIN pg_catalog.pg_policy p ON p.polrelid=c.oid
), triggers AS (
  SELECT coalesce(jsonb_agg(jsonb_build_array(g.oid::bigint,g.xmin::text,
    g.tgrelid::bigint,g.tgname) ORDER BY g.tgrelid,g.tgname),'[]'::jsonb) value
  FROM target t JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  JOIN pg_catalog.pg_trigger g ON g.tgrelid=c.oid
), roles AS (
  SELECT jsonb_agg(jsonb_build_array(r.rolname,r.oid::bigint,r.xmin::text,
    r.rolsuper,r.rolinherit,r.rolcreaterole,r.rolcreatedb,r.rolcanlogin,
    r.rolreplication,r.rolbypassrls,r.rolconnlimit,r.rolvaliduntil,
    pg_catalog.shobj_description(r.oid,'pg_authid')) ORDER BY r.rolname) value
  FROM pg_catalog.pg_authid r WHERE r.rolname IN (
    'aegis_client_auth_owner','aegis_client_auth_gc_owner',
    'aegis_client_auth_gc','aegis_client_keyring_preflight_owner')
), role_descriptions AS (
  SELECT coalesce(jsonb_agg(jsonb_build_array(d.objoid::bigint,
    d.classoid::bigint,d.xmin::text,d.description) ORDER BY d.objoid),'[]'::jsonb) value
  FROM pg_catalog.pg_shdescription d JOIN pg_catalog.pg_authid r ON r.oid=d.objoid
  WHERE d.classoid='pg_catalog.pg_authid'::regclass AND r.rolname IN (
    'aegis_client_auth_owner','aegis_client_auth_gc_owner',
    'aegis_client_auth_gc','aegis_client_keyring_preflight_owner')
), namespaces AS (
  SELECT jsonb_agg(jsonb_build_array(n.nspname,n.oid::bigint,n.xmin::text,
    n.nspowner::bigint,n.nspacl) ORDER BY n.nspname) value
  FROM pg_catalog.pg_namespace n WHERE n.nspname IN ('app','public')
)
SELECT jsonb_build_object(
  'format','client-auth-00042-object-manifest-v1',
  'contract_sha256','$CONTRACT_SHA256',
  'role_creation_mask',m.role_creation_mask,
  'role_oid_manifest',m.role_oid_manifest,
  'portable_catalog_manifest',m.catalog_manifest,
  'source_object_provenance',jsonb_build_object(
    'namespaces',ns.value,'relations',r.value,'attributes',a.value,
    'constraints',c.value,'indexes',i.value,'policies',p.value,
    'triggers',t.value,'roles',ro.value,'role_descriptions',rd.value)
)::text
FROM app.client_auth_00042_meta m CROSS JOIN namespaces ns CROSS JOIN rels r
CROSS JOIN attrs a CROSS JOIN constraints c CROSS JOIN indexes i
CROSS JOIN policies p CROSS JOIN triggers t CROSS JOIN roles ro
CROSS JOIN role_descriptions rd
WHERE m.singleton;
ROLLBACK;
SQL
)"; then
  deny 'trusted_disposable_catalog_export_failed'
fi

[[ "$OBJECT_MANIFEST" != *$'\n'* && "$OBJECT_MANIFEST" == \{*\} ]] \
  || deny 'object_manifest_output_not_unique_json'
[[ "$OBJECT_MANIFEST" == *'"format": "client-auth-00042-object-manifest-v1"'* \
   && "$OBJECT_MANIFEST" == *'"source_object_provenance"'* \
   && "$OBJECT_MANIFEST" == *'"portable_catalog_manifest"'* ]] \
  || deny 'object_manifest_contract_missing'

FINGERPRINT_AFTER="$(inspect_source 2>/dev/null)" || deny 'source_container_reinspect_failed'
[[ "$FINGERPRINT_AFTER" == "$FINGERPRINT" ]] || deny 'source_container_changed_during_export'
NETWORK_FINGERPRINT_AFTER="$(inspect_source_network 2>/dev/null)" || deny 'source_network_reinspect_failed'
[[ "$NETWORK_FINGERPRINT_AFTER" == "$NETWORK_FINGERPRINT" ]] || deny 'source_network_changed_during_export'
NOW_AFTER="$($DATE_BIN -u +%s 2>/dev/null)" || deny 'clock_recheck_unavailable'
[[ "$NOW_AFTER" =~ ^[1-9][0-9]*$ && "$expires_at" -gt "$NOW_AFTER" ]] \
  || deny 'source_lease_expired_during_export'

OBJECT_SHA256="$(printf '%s' "$OBJECT_MANIFEST" | "$SHA256_BIN" | awk '{print $1}')" \
  || deny 'object_manifest_hash_failed'
[[ "$OBJECT_SHA256" =~ ^[0-9a-f]{64}$ ]] || deny 'object_manifest_hash_invalid'

OUTPUT_DIR="$(dirname "$OUTPUT")"
[[ -d "$OUTPUT_DIR" ]] || deny 'output_directory_missing'
TMP_OUTPUT="$(mktemp "$OUTPUT_DIR/.client-auth-00042-manifest.XXXXXX")" \
  || deny 'output_stage_create_failed'
printf '{\n' >"$TMP_OUTPUT"
printf '  "format": "client-auth-00042-external-manifest-v1",\n' >>"$TMP_OUTPUT"
printf '  "status": "READY",\n' >>"$TMP_OUTPUT"
printf '  "frozen_migration_sha256": "%s",\n' "$FROZEN_MIGRATION_SHA256" >>"$TMP_OUTPUT"
printf '  "postgresql_major": 18,\n' >>"$TMP_OUTPUT"
printf '  "barrier": "%s",\n' "$EXPECTED_BARRIER" >>"$TMP_OUTPUT"
printf '  "exact_object_manifest_sha256": "%s",\n' "$OBJECT_SHA256" >>"$TMP_OUTPUT"
printf '  "source": {"system_identifier":"%s","container_id":"%s","network_id":"%s","database":"%s","database_oid":"%s","image_id":"%s","run_id":"%s"},\n' \
  "$SOURCE_SYSTEM_IDENTIFIER" "$SOURCE_CONTAINER_ID" "$source_network_id" \
  "$SOURCE_DATABASE" "$SOURCE_DATABASE_OID" "$SOURCE_IMAGE_ID" "$SOURCE_RUN_ID" >>"$TMP_OUTPUT"
printf '  "object_manifest": %s\n' "$OBJECT_MANIFEST" >>"$TMP_OUTPUT"
printf '}\n' >>"$TMP_OUTPUT"
chmod 0600 "$TMP_OUTPUT" || deny 'manifest_stage_chmod_failed'
# Same-directory hard-link publication is atomic and refuses an existing target;
# this closes the initial non-existence check race without overwriting evidence.
ln "$TMP_OUTPUT" "$OUTPUT" || deny 'manifest_publish_no_clobber_failed'
rm -f -- "$TMP_OUTPUT"
TMP_OUTPUT=''
printf 'client_auth_00042_manifest=PASS sha256=%s output=%s\n' "$OBJECT_SHA256" "$OUTPUT"
