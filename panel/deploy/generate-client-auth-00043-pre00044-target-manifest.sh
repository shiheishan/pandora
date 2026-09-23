#!/usr/bin/bash
# Export an OID-free exact catalog for every CLIENT-AUTH-00044 classification
# target. This candidate is deliberately independent from the signed V3
# runner/verifier/migration and is not a release or production entrypoint.
set -Eeuo pipefail
umask 077

readonly EXIT_DENIED=78
readonly LOCAL_DOCKER_HOST='unix:///var/run/docker.sock'
readonly EXPECTED_SOURCE_KIND='trusted-disposable-client-auth-00043-pre00044-v1'
readonly EXPECTED_PREFLIGHT_KIND='isolated-pg18-v1'
readonly EXPECTED_FORMAT='client-auth-00043-pre00044-target-catalog-v1'

# PANDORA_PRE00044_FIXED_TOOLS_BEGIN
readonly BASH_BIN='/usr/bin/bash'
readonly DOCKER_BIN='/usr/bin/docker'
readonly SHA256_BIN='/usr/bin/sha256sum'
readonly DATE_BIN='/usr/bin/date'
readonly STAT_BIN='/usr/bin/stat'
readonly REALPATH_BIN='/usr/bin/realpath'
readonly SYNC_BIN='/usr/bin/sync'
readonly UNAME_BIN='/usr/bin/uname'
readonly ID_BIN='/usr/bin/id'
readonly DIRNAME_BIN='/usr/bin/dirname'
readonly MKTEMP_BIN='/usr/bin/mktemp'
readonly CHMOD_BIN='/usr/bin/chmod'
readonly LN_BIN='/usr/bin/ln'
readonly RM_BIN='/usr/bin/rm'
readonly ENV_BIN='/usr/bin/env'
# PANDORA_PRE00044_FIXED_TOOLS_END

deny() {
  printf 'client_auth_pre00044_target_manifest=DENY reason=%s\n' "$1" >&2
  exit "$EXIT_DENIED"
}
safe_sha() { [[ "$1" =~ ^[0-9a-f]{64}$ ]]; }
safe_upper_sha() { [[ "$1" =~ ^[0-9A-F]{64}$ ]]; }
safe_token() { [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; }

OUTPUT="${AEGIS_PRE00044_TARGET_MANIFEST_OUTPUT:-}"
SOURCE_CONTAINER_ID="${AEGIS_PRE00044_SOURCE_CONTAINER_ID:-}"
SOURCE_SYSTEM_IDENTIFIER="${AEGIS_PRE00044_SOURCE_SYSTEM_IDENTIFIER:-}"
SOURCE_DATABASE="${AEGIS_PRE00044_SOURCE_DATABASE:-}"
SOURCE_DATABASE_OID="${AEGIS_PRE00044_SOURCE_DATABASE_OID:-}"
SOURCE_DATABASE_USER="${AEGIS_PRE00044_SOURCE_DATABASE_USER:-}"
SOURCE_IMAGE_ID="${AEGIS_PRE00044_SOURCE_IMAGE_ID:-}"
SOURCE_RUN_ID="${AEGIS_PRE00044_SOURCE_RUN_ID:-}"
PROOF_00042_CONTRACT="${AEGIS_PRE00044_00042_CONTRACT_SHA256:-}"
PROOF_00042_CATALOG="${AEGIS_PRE00044_00042_CATALOG_SHA256:-}"
PROOF_00043_CONTRACT="${AEGIS_PRE00044_00043_CONTRACT_SHA256:-}"
PROOF_00043_RELEASE="${AEGIS_PRE00044_00043_RELEASE_ID:-}"
PROOF_00043_RUNNER="${AEGIS_PRE00044_00043_RUNNER_SHA256:-}"
PROOF_00043_MIGRATION="${AEGIS_PRE00044_00043_MIGRATION_SHA256:-}"
PROOF_00043_RUN_ID="${AEGIS_PRE00044_00043_RUN_ID:-}"
PROOF_00043_EVIDENCE="${AEGIS_PRE00044_00043_EVIDENCE_SHA256:-}"
PROOF_00043_CATALOG="${AEGIS_PRE00044_00043_CATALOG_SHA256:-}"
PROTECTED_EXACT_PROOF="${AEGIS_PRE00044_PROTECTED_EXACT_PROOF_SHA256:-}"
EXPECTED_APP_ROUTINE_CATALOG="${AEGIS_PRE00044_APP_ROUTINE_CATALOG_SHA256:-}"
EXPECTED_ROUTINE_DEPENDENCY_CLOSURE="${AEGIS_PRE00044_ROUTINE_DEPENDENCY_CLOSURE_SHA256:-}"
EXPECTED_TRIGGER_CLOSURE="${AEGIS_PRE00044_TRIGGER_CLOSURE_SHA256:-}"
EXPECTED_REWRITE_RULE_CATALOG="${AEGIS_PRE00044_REWRITE_RULE_CATALOG_SHA256:-}"
EXPECTED_INHERITANCE_PARTITION_CATALOG="${AEGIS_PRE00044_INHERITANCE_PARTITION_CATALOG_SHA256:-}"

for docker_env_name in DOCKER_HOST DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH; do
  [[ -z "${!docker_env_name:-}" ]] || deny "remote_docker_environment:${docker_env_name}"
done
[[ -x "$STAT_BIN" && -f "$STAT_BIN" && ! -L "$STAT_BIN" ]] || deny fixed_stat_untrusted
validate_fixed_tool() {
  local tool="$1" row uid kind mode dev inode links size mtime cursor
  [[ "$tool" == /* && -x "$tool" && -f "$tool" && ! -L "$tool" ]] || return 1
  row="$("$STAT_BIN" -c '%u|%F|%a|%d|%i|%h|%s|%Y' -- "$tool" 2>/dev/null)" ||
    return 1
  IFS='|' read -r uid kind mode dev inode links size mtime <<<"$row"
  [[ "$uid" == 0 && "$kind" == 'regular file' && "$mode" =~ ^[0-7]{3,4}$ &&
     "$dev" =~ ^[0-9]+$ && "$inode" =~ ^[1-9][0-9]*$ &&
     "$links" =~ ^[1-9][0-9]*$ && "$size" =~ ^[1-9][0-9]*$ &&
     "$mtime" =~ ^-?[0-9]+$ ]] || return 1
  (( (8#$mode & 8#022) == 0 )) || return 1
  cursor="${tool%/*}"
  [[ -n "$cursor" ]] || cursor=/
  while :; do
    [[ ! -L "$cursor" ]] || return 1
    row="$("$STAT_BIN" -c '%u|%F|%a' -- "$cursor" 2>/dev/null)" || return 1
    IFS='|' read -r uid kind mode <<<"$row"
    [[ "$uid" == 0 && "$kind" == directory && "$mode" =~ ^[0-7]{3,4}$ ]] ||
      return 1
    (( (8#$mode & 8#022) == 0 )) || return 1
    [[ "$cursor" == / ]] && break
    cursor="${cursor%/*}"
    [[ -n "$cursor" ]] || cursor=/
  done
  printf '%s|%s\n' "$tool" \
    "$("$STAT_BIN" -c '%u|%F|%a|%d|%i|%h|%s|%Y' -- "$tool")"
}
fixed_tool_identity_set() {
  local fixed_tool
  for fixed_tool in "$BASH_BIN" "$DOCKER_BIN" "$SHA256_BIN" "$DATE_BIN" "$STAT_BIN" \
    "$REALPATH_BIN" "$SYNC_BIN" "$UNAME_BIN" "$ID_BIN" "$DIRNAME_BIN" \
    "$MKTEMP_BIN" "$CHMOD_BIN" "$LN_BIN" "$RM_BIN" "$ENV_BIN"; do
    validate_fixed_tool "$fixed_tool" || return 1
  done
}
FIXED_TOOL_IDENTITIES="$(fixed_tool_identity_set)" || deny fixed_tool_identity_untrusted
[[ "$("$UNAME_BIN" -s 2>/dev/null)" == Linux ]] || deny linux_required
[[ "$("$ID_BIN" -u 2>/dev/null)" == 0 ]] || deny root_required

[[ "$OUTPUT" == /* && ! -e "$OUTPUT" && ! -L "$OUTPUT" ]] ||
  deny output_must_be_new_absolute_path
[[ "$SOURCE_CONTAINER_ID" =~ ^[0-9a-f]{64}$ ]] || deny source_container_id_invalid
[[ "$SOURCE_SYSTEM_IDENTIFIER" =~ ^[1-9][0-9]{0,19}$ ]] ||
  deny source_system_identifier_invalid
[[ "$SOURCE_DATABASE" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] || deny source_database_invalid
[[ "$SOURCE_DATABASE_OID" =~ ^[1-9][0-9]*$ ]] || deny source_database_oid_invalid
[[ "$SOURCE_DATABASE_USER" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] ||
  deny source_database_user_invalid
[[ "$SOURCE_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || deny source_image_id_invalid
[[ "$SOURCE_RUN_ID" =~ ^pandoraisolatedpg18[A-Za-z0-9]{6}-[1-9][0-9]*$ ]] ||
  deny source_run_id_invalid
safe_upper_sha "$PROOF_00042_CONTRACT" || deny proof_00042_contract_invalid
safe_sha "$PROOF_00042_CATALOG" || deny proof_00042_catalog_invalid
safe_upper_sha "$PROOF_00043_CONTRACT" || deny proof_00043_contract_invalid
[[ "$PROOF_00043_RELEASE" == client-auth-00043-v3 ]] || deny proof_00043_release_invalid
safe_sha "$PROOF_00043_RUNNER" || deny proof_00043_runner_invalid
safe_sha "$PROOF_00043_MIGRATION" || deny proof_00043_migration_invalid
safe_token "$PROOF_00043_RUN_ID" || deny proof_00043_run_id_invalid
safe_sha "$PROOF_00043_EVIDENCE" || deny proof_00043_evidence_invalid
safe_sha "$PROOF_00043_CATALOG" || deny proof_00043_catalog_invalid
safe_sha "$PROTECTED_EXACT_PROOF" || deny protected_exact_proof_invalid
safe_sha "$EXPECTED_APP_ROUTINE_CATALOG" || deny app_routine_catalog_proof_invalid
safe_sha "$EXPECTED_ROUTINE_DEPENDENCY_CLOSURE" ||
  deny routine_dependency_closure_proof_invalid
safe_sha "$EXPECTED_TRIGGER_CLOSURE" || deny trigger_closure_proof_invalid
safe_sha "$EXPECTED_REWRITE_RULE_CATALOG" || deny rewrite_rule_catalog_proof_invalid
safe_sha "$EXPECTED_INHERITANCE_PARTITION_CATALOG" ||
  deny inheritance_partition_catalog_proof_invalid

trusted_directory_chain() {
  local cursor="$1" row uid kind mode dev inode parent
  while :; do
    [[ "$cursor" == /* && ! -L "$cursor" ]] || return 1
    row="$("$STAT_BIN" -c '%u|%F|%a|%d|%i' -- "$cursor" 2>/dev/null)" || return 1
    IFS='|' read -r uid kind mode dev inode <<<"$row"
    [[ "$uid" == 0 && "$kind" == directory && "$mode" =~ ^[0-7]{3,4}$ &&
       "$dev" =~ ^[0-9]+$ && "$inode" =~ ^[0-9]+$ ]] || return 1
    (( (8#$mode & 8#022) == 0 )) || return 1
    printf '%s|%s|%s\n' "$cursor" "$dev" "$inode"
    [[ "$cursor" == / ]] && break
    parent="$("$DIRNAME_BIN" -- "$cursor")"
    [[ "$parent" != "$cursor" ]] || return 1
    cursor="$parent"
  done
}

trusted_file_identity() {
  local path="$1"
  [[ "$path" == /* && -f "$path" && ! -L "$path" ]] || return 1
  "$STAT_BIN" -c '%u|%F|%a|%d|%i|%h|%s' -- "$path" 2>/dev/null
}

OUTPUT_DIR="$("$DIRNAME_BIN" -- "$OUTPUT")"
OUTPUT_DIR_REAL="$("$REALPATH_BIN" -e -- "$OUTPUT_DIR" 2>/dev/null)" ||
  deny output_directory_realpath_failed
[[ "$OUTPUT_DIR_REAL" == "$OUTPUT_DIR" && -d "$OUTPUT_DIR_REAL" && ! -L "$OUTPUT_DIR_REAL" ]] ||
  deny output_directory_not_canonical
OUTPUT_CHAIN_BEFORE="$(trusted_directory_chain "$OUTPUT_DIR_REAL")" ||
  deny output_ancestor_chain_untrusted
OUTPUT_DIR_ID="$("$STAT_BIN" -c '%d|%i' -- "$OUTPUT_DIR_REAL")" ||
  deny output_directory_identity_failed

docker_local() {
  "$ENV_BIN" -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    "$DOCKER_BIN" --host "$LOCAL_DOCKER_HOST" "$@"
}

NOW_EPOCH="$("$DATE_BIN" -u +%s 2>/dev/null)" || deny clock_unavailable
[[ "$NOW_EPOCH" =~ ^[1-9][0-9]*$ ]] || deny clock_invalid
NETWORK_NAME="pandora-pg18-preflight-$SOURCE_RUN_ID"

inspect_source() {
  docker_local inspect --type container --format \
    '{{.Id}}|{{.Name}}|{{.Image}}|{{index .Config.Labels "pandora.preflight"}}|{{index .Config.Labels "pandora.preflight.run_id"}}|{{index .Config.Labels "pandora.preflight.expires_at"}}|{{index .Config.Labels "pandora.manifest.source"}}|{{index .Config.Labels "pandora.manifest.goose"}}|{{.State.Running}}|{{.HostConfig.AutoRemove}}|{{.HostConfig.NetworkMode}}' \
    "$SOURCE_CONTAINER_ID"
}
inspect_network() {
  docker_local network inspect --format \
    '{{.Id}}|{{.Name}}|{{.Internal}}|{{index .Labels "pandora.preflight"}}|{{index .Labels "pandora.preflight.run_id"}}|{{index .Labels "pandora.preflight.expires_at"}}' \
    "$NETWORK_NAME"
}
inspect_network_members() {
  docker_local network inspect --format \
    '{{range $id,$c := .Containers}}{{$id}}|{{$c.Name}}{{"\n"}}{{end}}' \
    "$NETWORK_NAME"
}

FINGERPRINT="$(inspect_source 2>/dev/null)" || deny source_container_inspect_failed
IFS='|' read -r actual_id source_name actual_image preflight_kind actual_run expires_at \
  source_kind source_goose running auto_remove network_mode extra <<<"$FINGERPRINT"
source_name="${source_name#/}"
[[ -z "${extra:-}" && "$actual_id" == "$SOURCE_CONTAINER_ID" ]] ||
  deny source_container_fingerprint_invalid
[[ "$source_name" == "pandora-pg18-preflight-$SOURCE_RUN_ID" ]] ||
  deny source_container_name_mismatch
[[ "$actual_image" == "$SOURCE_IMAGE_ID" ]] || deny source_image_id_mismatch
[[ "$preflight_kind" == "$EXPECTED_PREFLIGHT_KIND" && "$actual_run" == "$SOURCE_RUN_ID" ]] ||
  deny source_preflight_labels_invalid
[[ "$source_kind" == "$EXPECTED_SOURCE_KIND" && "$source_goose" == 43 ]] ||
  deny source_not_trusted_disposable_goose43
[[ "$expires_at" =~ ^[1-9][0-9]*$ && "$expires_at" -gt "$NOW_EPOCH" ]] ||
  deny source_lease_expired
[[ "$running" == true && "$auto_remove" == true && "$network_mode" == "$NETWORK_NAME" ]] ||
  deny source_container_not_disposable_isolated

NETWORK_FINGERPRINT="$(inspect_network 2>/dev/null)" || deny source_network_inspect_failed
IFS='|' read -r network_id network_name network_internal network_kind network_run \
  network_expires network_extra <<<"$NETWORK_FINGERPRINT"
[[ -z "${network_extra:-}" && "$network_id" =~ ^[0-9a-f]{64}$ ]] ||
  deny source_network_fingerprint_invalid
[[ "$network_name" == "$NETWORK_NAME" && "$network_internal" == true &&
   "$network_kind" == "$EXPECTED_PREFLIGHT_KIND" && "$network_run" == "$SOURCE_RUN_ID" &&
   "$network_expires" == "$expires_at" ]] || deny source_network_not_disposable_isolated
NETWORK_MEMBERS="$(inspect_network_members 2>/dev/null)" ||
  deny source_network_membership_inspect_failed
[[ "$NETWORK_MEMBERS" == "$SOURCE_CONTAINER_ID|$source_name" ]] ||
  deny source_network_membership_not_source_only

# The disposable-container attestation is the concurrency barrier: no other
# application or migration process is attached to this internal network.
# The database transaction itself is explicitly read only.
export_exact_catalog() {
  docker_local exec -i "$SOURCE_CONTAINER_ID" \
  psql -X -v ON_ERROR_STOP=1 -qAt -U "$SOURCE_DATABASE_USER" -d "$SOURCE_DATABASE" -f - <<SQL
BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE READ ONLY DEFERRABLE;
SET LOCAL search_path=pg_catalog;
SET LOCAL statement_timeout='2min';
SET LOCAL lock_timeout='5s';
LOCK TABLE public.devices,public.device_authorizations,public.sessions,
  public.refresh_tokens,public.api_tokens,public.device_tokens,
  public.subscription_credentials,public.config_bundles,
  public.credential_access_log,public.config_bundle_nodes,
  app.client_auth_00042_meta,app.client_auth_00043_meta
  IN ACCESS SHARE MODE;

DO \$guard\$
DECLARE
  v_applied bigint;
BEGIN
  IF current_setting('server_version_num')::integer NOT BETWEEN 180000 AND 189999
     OR (SELECT system_identifier::text FROM pg_catalog.pg_control_system())
        IS DISTINCT FROM '$SOURCE_SYSTEM_IDENTIFIER'
     OR current_database() IS DISTINCT FROM '$SOURCE_DATABASE'
     OR (SELECT oid::text FROM pg_catalog.pg_database WHERE datname=current_database())
        IS DISTINCT FROM '$SOURCE_DATABASE_OID'
     OR NOT (SELECT usesuper FROM pg_catalog.pg_user WHERE usename=current_user) THEN
    RAISE EXCEPTION 'source identity, major, or privilege mismatch';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_stat_activity
    WHERE backend_type='client backend' AND pid<>pg_catalog.pg_backend_pid()
  ) THEN
    RAISE EXCEPTION 'other database client session present';
  END IF;
  SELECT max(version_id) FILTER (WHERE is_applied) INTO v_applied
  FROM public.goose_db_version;
  IF v_applied IS DISTINCT FROM 43 OR EXISTS (
    SELECT 1 FROM public.goose_db_version WHERE is_applied AND version_id>43
  ) THEN RAISE EXCEPTION 'exact Goose 43 required'; END IF;
  IF (SELECT count(*) FROM app.client_auth_00042_meta)<>1 OR NOT EXISTS (
    SELECT 1 FROM app.client_auth_00042_meta WHERE singleton
      AND contract_sha256='$PROOF_00042_CONTRACT'
      AND encode(pg_catalog.sha256(pg_catalog.convert_to(catalog_manifest::text,'UTF8')),'hex')
          ='$PROOF_00042_CATALOG'
      AND legacy_revocation_at IS NULL AND first_client_write_at IS NULL
      AND cutover_at IS NULL AND constraints_validated_at IS NULL
  ) THEN RAISE EXCEPTION '00042 exact proof binding mismatch'; END IF;
  IF (SELECT count(*) FROM app.client_auth_00043_meta)<>1 OR NOT EXISTS (
    SELECT 1 FROM app.client_auth_00043_meta WHERE singleton
      AND v3_contract_sha256='$PROOF_00043_CONTRACT'
      AND release_id='$PROOF_00043_RELEASE'
      AND runner_sha256='$PROOF_00043_RUNNER'
      AND migration_sha256='$PROOF_00043_MIGRATION'
      AND run_id='$PROOF_00043_RUN_ID'
      AND evidence_sha256='$PROOF_00043_EVIDENCE'
      AND source_system_identifier='$SOURCE_SYSTEM_IDENTIFIER'
      AND source_database_name='$SOURCE_DATABASE'
      AND source_database_oid::text='$SOURCE_DATABASE_OID'
      AND encode(pg_catalog.sha256(pg_catalog.convert_to(catalog_manifest::text,'UTF8')),'hex')
          ='$PROOF_00043_CATALOG'
      AND legacy_revocation_at IS NULL AND first_client_write_at IS NULL
      AND cutover_at IS NULL AND constraints_validated_at IS NULL
  ) THEN RAISE EXCEPTION '00043 exact proof binding mismatch'; END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='app' AND c.relname ~ '^client_auth_'
      AND c.relname NOT IN ('client_auth_00042_meta','client_auth_00043_meta')
  ) OR EXISTS (
    SELECT 1 FROM pg_catalog.pg_proc p
    JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname='app' AND p.proname ~ '^client_auth_'
  ) THEN
    RAISE EXCEPTION 'unknown app.client_auth_* complement object';
  END IF;
END
\$guard\$;

WITH RECURSIVE target(ordinal,schema_name,relation_name) AS (
  VALUES
    (1,'public','devices'),
    (2,'public','device_authorizations'),
    (3,'public','sessions'),
    (4,'public','refresh_tokens'),
    (5,'public','api_tokens'),
    (6,'public','device_tokens'),
    (7,'public','subscription_credentials'),
    (8,'public','config_bundles'),
    (9,'public','credential_access_log'),
    (10,'public','config_bundle_nodes'),
    (11,'app','client_auth_00042_meta'),
    (12,'app','client_auth_00043_meta')
), presence AS (
  SELECT t.ordinal,t.schema_name,t.relation_name,count(c.oid) AS catalog_rows
  FROM target t
  LEFT JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  LEFT JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  GROUP BY t.ordinal,t.schema_name,t.relation_name
), relations AS (
  SELECT jsonb_agg(jsonb_build_object(
    'schema',n.nspname,'name',c.relname,'kind',c.relkind::text,
    'persistence',c.relpersistence::text,'owner',pg_catalog.pg_get_userbyid(c.relowner),
    'access_method',am.amname,'tablespace',ts.spcname,
    'rls',c.relrowsecurity,'force_rls',c.relforcerowsecurity,
    'replica_identity',c.relreplident::text,'reloptions',c.reloptions,
    'partition_bound',pg_catalog.pg_get_expr(c.relpartbound,c.oid,false),
    'comment',pg_catalog.obj_description(c.oid,'pg_class'),
    'acl',coalesce((
      SELECT jsonb_agg(jsonb_build_array(
        pg_catalog.pg_get_userbyid(x.grantor),
        CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(x.grantee) END,
        x.privilege_type,x.is_grantable
      ) ORDER BY x.grantee,x.grantor,x.privilege_type)
      FROM pg_catalog.aclexplode(c.relacl) x
    ),'[]'::jsonb),
    'columns',coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'attnum',a.attnum,'name',a.attname,
        'type',pg_catalog.format_type(a.atttypid,a.atttypmod),
        'not_null',a.attnotnull,
        'default',pg_catalog.pg_get_expr(d.adbin,d.adrelid,false),
        'identity',a.attidentity::text,'generated',a.attgenerated::text,
        'collation',CASE WHEN a.attcollation=0 THEN NULL
          ELSE cn.nspname||'.'||co.collname END,
        'storage',a.attstorage::text,'compression',a.attcompression::text,
        'statistics',a.attstattarget,
        'comment',pg_catalog.col_description(a.attrelid,a.attnum),
        'acl',coalesce((
          SELECT jsonb_agg(jsonb_build_array(
            pg_catalog.pg_get_userbyid(ax.grantor),
            CASE WHEN ax.grantee=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(ax.grantee) END,
            ax.privilege_type,ax.is_grantable
          ) ORDER BY ax.grantee,ax.grantor,ax.privilege_type)
          FROM pg_catalog.aclexplode(a.attacl) ax
        ),'[]'::jsonb)
      ) ORDER BY a.attnum)
      FROM pg_catalog.pg_attribute a
      LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
      LEFT JOIN pg_catalog.pg_collation co ON co.oid=a.attcollation
      LEFT JOIN pg_catalog.pg_namespace cn ON cn.oid=co.collnamespace
      WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
    ),'[]'::jsonb),
    'constraints',coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'name',con.conname,'type',con.contype::text,
        'definition',pg_catalog.pg_get_constraintdef(con.oid,false),
        'validated',con.convalidated,'deferrable',con.condeferrable,
        'initially_deferred',con.condeferred,'no_inherit',con.connoinherit,
        'dependencies',coalesce((
          SELECT jsonb_agg(z.item ORDER BY z.item::text)
          FROM (
            SELECT jsonb_build_object('direction','out','deptype',dep.deptype::text,
              'type',i.type,'identity',i.object_identity,
              'names',i.address_names,'args',i.address_args) item
            FROM pg_catalog.pg_depend dep
            CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
              dep.refclassid,dep.refobjid,dep.refobjsubid) i
            WHERE dep.classid='pg_catalog.pg_constraint'::regclass AND dep.objid=con.oid
            UNION ALL
            SELECT jsonb_build_object('direction','in','deptype',dep.deptype::text,
              'type',i.type,'identity',i.object_identity,
              'names',i.address_names,'args',i.address_args)
            FROM pg_catalog.pg_depend dep
            CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
              dep.classid,dep.objid,dep.objsubid) i
            WHERE dep.refclassid='pg_catalog.pg_constraint'::regclass AND dep.refobjid=con.oid
          ) z
        ),'[]'::jsonb)
      ) ORDER BY con.conname)
      FROM pg_catalog.pg_constraint con WHERE con.conrelid=c.oid
    ),'[]'::jsonb),
    'indexes',coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'name',ci.relname,'owner',pg_catalog.pg_get_userbyid(ci.relowner),
        'access_method',iam.amname,'tablespace',its.spcname,
        'definition',pg_catalog.pg_get_indexdef(i.indexrelid,0,false),
        'unique',i.indisunique,'primary',i.indisprimary,'exclusion',i.indisexclusion,
        'immediate',i.indimmediate,'clustered',i.indisclustered,
        'valid',i.indisvalid,'ready',i.indisready,'live',i.indislive,
        'replica_identity',i.indisreplident,
        'nulls_not_distinct',i.indnullsnotdistinct,
        'reloptions',ci.reloptions,'comment',pg_catalog.obj_description(ci.oid,'pg_class'),
        'dependencies',coalesce((
          SELECT jsonb_agg(jsonb_build_object(
            'direction','out','deptype',dep.deptype::text,'type',di.type,
            'identity',di.object_identity,'names',di.address_names,'args',di.address_args
          ) ORDER BY dep.deptype,di.type,di.object_identity)
          FROM pg_catalog.pg_depend dep
          CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
            dep.refclassid,dep.refobjid,dep.refobjsubid) di
          WHERE dep.classid='pg_catalog.pg_class'::regclass AND dep.objid=i.indexrelid
        ),'[]'::jsonb)
      ) ORDER BY ci.relname)
      FROM pg_catalog.pg_index i
      JOIN pg_catalog.pg_class ci ON ci.oid=i.indexrelid
      LEFT JOIN pg_catalog.pg_am iam ON iam.oid=ci.relam
      LEFT JOIN pg_catalog.pg_tablespace its ON its.oid=ci.reltablespace
      WHERE i.indrelid=c.oid
    ),'[]'::jsonb),
    'policies',coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'name',p.polname,'permissive',p.polpermissive,'command',p.polcmd::text,
        'roles',coalesce((SELECT jsonb_agg(CASE WHEN rid=0 THEN 'PUBLIC'
          ELSE pg_catalog.pg_get_userbyid(rid) END ORDER BY
          CASE WHEN rid=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(rid) END)
          FROM unnest(p.polroles) rid),'[]'::jsonb),
        'using',pg_catalog.pg_get_expr(p.polqual,p.polrelid,false),
        'with_check',pg_catalog.pg_get_expr(p.polwithcheck,p.polrelid,false)
      ) ORDER BY p.polname)
      FROM pg_catalog.pg_policy p WHERE p.polrelid=c.oid
    ),'[]'::jsonb),
    'triggers',coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'name',tg.tgname,'enabled',tg.tgenabled::text,
        'definition',pg_catalog.pg_get_triggerdef(tg.oid,false),
        'function',jsonb_build_object(
          'schema',pn.nspname,'name',pr.proname,
          'identity_arguments',pg_catalog.pg_get_function_identity_arguments(pr.oid),
          'owner',pg_catalog.pg_get_userbyid(pr.proowner),
          'language',lan.lanname,'security_definer',pr.prosecdef,
          'volatility',pr.provolatile::text,
          'definition',pg_catalog.pg_get_functiondef(pr.oid),'prosrc',pr.prosrc),
        'dependencies',coalesce((
          SELECT jsonb_agg(jsonb_build_object(
            'direction','out','deptype',dep.deptype::text,'type',di.type,
            'identity',di.object_identity,'names',di.address_names,'args',di.address_args
          ) ORDER BY dep.deptype,di.type,di.object_identity)
          FROM pg_catalog.pg_depend dep
          CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
            dep.refclassid,dep.refobjid,dep.refobjsubid) di
          WHERE dep.classid='pg_catalog.pg_trigger'::regclass AND dep.objid=tg.oid
        ),'[]'::jsonb)
      ) ORDER BY tg.tgname)
      FROM pg_catalog.pg_trigger tg
      JOIN pg_catalog.pg_proc pr ON pr.oid=tg.tgfoid
      JOIN pg_catalog.pg_namespace pn ON pn.oid=pr.pronamespace
      JOIN pg_catalog.pg_language lan ON lan.oid=pr.prolang
      WHERE tg.tgrelid=c.oid AND NOT tg.tgisinternal
    ),'[]'::jsonb)
  ) ORDER BY t.ordinal) AS value
  FROM target t
  JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  LEFT JOIN pg_catalog.pg_am am ON am.oid=c.relam
  LEFT JOIN pg_catalog.pg_tablespace ts ON ts.oid=c.reltablespace
), app_routine_catalog AS (
  SELECT coalesce(jsonb_agg(jsonb_build_object(
    'schema',n.nspname,'name',p.proname,
    'identity_arguments',pg_catalog.pg_get_function_identity_arguments(p.oid),
    'result',pg_catalog.pg_get_function_result(p.oid),
    'kind',p.prokind::text,'owner',pg_catalog.pg_get_userbyid(p.proowner),
    'language',l.lanname,'security_definer',p.prosecdef,
    'leakproof',p.proleakproof,'strict',p.proisstrict,
    'volatility',p.provolatile::text,'parallel',p.proparallel::text,
    'config',p.proconfig,'acl',coalesce((
      SELECT jsonb_agg(jsonb_build_array(
        pg_catalog.pg_get_userbyid(ax.grantor),
        CASE WHEN ax.grantee=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(ax.grantee) END,
        ax.privilege_type,ax.is_grantable
      ) ORDER BY ax.grantee,ax.grantor,ax.privilege_type)
      FROM pg_catalog.aclexplode(p.proacl) ax
    ),'[]'::jsonb),
    'definition',CASE WHEN p.prokind IN ('f','p')
      THEN pg_catalog.pg_get_functiondef(p.oid) ELSE NULL END,
    'prosrc',p.prosrc,
    'dependencies',coalesce((
      SELECT jsonb_agg(x.item ORDER BY x.item::text)
      FROM (
        SELECT jsonb_build_object(
          'direction','out','deptype',dep.deptype::text,'type',di.type,
          'identity',di.object_identity,'names',di.address_names,'args',di.address_args
        ) item
        FROM pg_catalog.pg_depend dep
        CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
          dep.refclassid,dep.refobjid,dep.refobjsubid) di
        WHERE dep.classid='pg_catalog.pg_proc'::regclass AND dep.objid=p.oid
        UNION ALL
        SELECT jsonb_build_object(
          'direction','in','deptype',dep.deptype::text,'type',di.type,
          'identity',di.object_identity,'names',di.address_names,'args',di.address_args)
        FROM pg_catalog.pg_depend dep
        CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
          dep.classid,dep.objid,dep.objsubid) di
        WHERE dep.refclassid='pg_catalog.pg_proc'::regclass AND dep.refobjid=p.oid
      ) x
    ),'[]'::jsonb)
  ) ORDER BY n.nspname,p.proname,pg_catalog.pg_get_function_identity_arguments(p.oid)),
  '[]'::jsonb) AS value
  FROM pg_catalog.pg_proc p
  JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
  JOIN pg_catalog.pg_language l ON l.oid=p.prolang
  WHERE n.nspname='app'
), seed_objects(classid,objid,objsubid) AS (
  SELECT 'pg_catalog.pg_class'::regclass::oid,c.oid,0
  FROM target t
  JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  UNION
  SELECT 'pg_catalog.pg_class'::regclass::oid,c.oid,a.attnum
  FROM target t
  JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  JOIN pg_catalog.pg_attribute a
    ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
), dependency_walk(classid,objid,objsubid) AS (
  SELECT classid,objid,objsubid FROM seed_objects
  UNION
  SELECT d.classid,d.objid,d.objsubid
  FROM pg_catalog.pg_depend d
  JOIN dependency_walk w
    ON d.refclassid=w.classid AND d.refobjid=w.objid
   AND d.refobjsubid=w.objsubid
), routine_dependency_closure AS (
  SELECT coalesce(jsonb_agg(jsonb_build_object(
    'type',i.type,'identity',i.object_identity,
    'names',i.address_names,'args',i.address_args,
    'reason',CASE WHEN EXISTS (
      SELECT 1 FROM pg_catalog.pg_trigger tg
      JOIN dependency_walk tw ON tw.classid='pg_catalog.pg_trigger'::regclass
        AND tw.objid=tg.oid
      WHERE tg.tgfoid=p.oid AND NOT tg.tgisinternal
    ) THEN 'target-trigger-function' ELSE 'catalog-reverse-dependency' END,
    'dependencies',coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'deptype',d.deptype::text,'type',di.type,'identity',di.object_identity,
        'names',di.address_names,'args',di.address_args
      ) ORDER BY d.deptype,di.type,di.object_identity)
      FROM pg_catalog.pg_depend d
      CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
        d.refclassid,d.refobjid,d.refobjsubid) di
      WHERE d.classid='pg_catalog.pg_proc'::regclass AND d.objid=p.oid
    ),'[]'::jsonb)
  ) ORDER BY i.type,i.object_identity),'[]'::jsonb) AS value
  FROM pg_catalog.pg_proc p
  JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
  CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
    'pg_catalog.pg_proc'::regclass,p.oid,0) i
  WHERE n.nspname='app' AND (
    EXISTS (
      SELECT 1 FROM dependency_walk w
      WHERE w.classid='pg_catalog.pg_proc'::regclass AND w.objid=p.oid
    ) OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_trigger tg
      JOIN dependency_walk tw ON tw.classid='pg_catalog.pg_trigger'::regclass
        AND tw.objid=tg.oid
      WHERE tg.tgfoid=p.oid AND NOT tg.tgisinternal
    )
  )
), target_trigger_closure AS (
  SELECT coalesce(jsonb_agg(jsonb_build_object(
    'table',t.schema_name||'.'||t.relation_name,
    'trigger',tg.tgname,'enabled',tg.tgenabled::text,
    'definition',pg_catalog.pg_get_triggerdef(tg.oid,false),
    'function_type',fi.type,'function_identity',fi.object_identity,
    'function_names',fi.address_names,'function_args',fi.address_args,
    'dependencies',coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'deptype',d.deptype::text,'type',di.type,'identity',di.object_identity,
        'names',di.address_names,'args',di.address_args
      ) ORDER BY d.deptype,di.type,di.object_identity)
      FROM pg_catalog.pg_depend d
      CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
        d.refclassid,d.refobjid,d.refobjsubid) di
      WHERE d.classid='pg_catalog.pg_trigger'::regclass AND d.objid=tg.oid
    ),'[]'::jsonb)
  ) ORDER BY t.ordinal,tg.tgname),'[]'::jsonb) AS value
  FROM target t
  JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  JOIN pg_catalog.pg_trigger tg ON tg.tgrelid=c.oid AND NOT tg.tgisinternal
  CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
    'pg_catalog.pg_proc'::regclass,tg.tgfoid,0) fi
), rewrite_rule_catalog AS (
  SELECT coalesce(jsonb_agg(jsonb_build_object(
    'type',ri.type,'identity',ri.object_identity,
    'names',ri.address_names,'args',ri.address_args,
    'event_relation',en.nspname||'.'||ec.relname,
    'name',rw.rulename,'event',rw.ev_type::text,
    'enabled',rw.ev_enabled::text,'instead',rw.is_instead,
    'definition',pg_catalog.pg_get_ruledef(rw.oid,false),
    'dependencies',coalesce((
      SELECT jsonb_agg(x.item ORDER BY x.item::text)
      FROM (
        SELECT jsonb_build_object(
          'direction','out','deptype',dep.deptype::text,'type',di.type,
          'identity',di.object_identity,'names',di.address_names,'args',di.address_args
        ) item
        FROM pg_catalog.pg_depend dep
        CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
          dep.refclassid,dep.refobjid,dep.refobjsubid) di
        WHERE dep.classid='pg_catalog.pg_rewrite'::regclass AND dep.objid=rw.oid
        UNION ALL
        SELECT jsonb_build_object(
          'direction','in','deptype',dep.deptype::text,'type',di.type,
          'identity',di.object_identity,'names',di.address_names,'args',di.address_args)
        FROM pg_catalog.pg_depend dep
        CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
          dep.classid,dep.objid,dep.objsubid) di
        WHERE dep.refclassid='pg_catalog.pg_rewrite'::regclass AND dep.refobjid=rw.oid
      ) x
    ),'[]'::jsonb)
  ) ORDER BY ri.type,ri.object_identity),'[]'::jsonb) AS value
  FROM pg_catalog.pg_rewrite rw
  JOIN dependency_walk w
    ON w.classid='pg_catalog.pg_rewrite'::regclass AND w.objid=rw.oid
  JOIN pg_catalog.pg_class ec ON ec.oid=rw.ev_class
  JOIN pg_catalog.pg_namespace en ON en.oid=ec.relnamespace
  CROSS JOIN LATERAL pg_catalog.pg_identify_object_as_address(
    'pg_catalog.pg_rewrite'::regclass,rw.oid,0) ri
), behavior_relids(oid) AS (
  SELECT c.oid
  FROM target t
  JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  UNION
  SELECT w.objid
  FROM dependency_walk w
  JOIN pg_catalog.pg_class c ON c.oid=w.objid
  WHERE w.classid='pg_catalog.pg_class'::regclass
    AND c.relkind IN ('r','p','v','m','f')
), inheritance_partition_relids(oid) AS (
  SELECT oid FROM behavior_relids
  UNION
  SELECT CASE
           WHEN inh.inhrelid=walk.oid THEN inh.inhparent
           ELSE inh.inhrelid
         END
  FROM inheritance_partition_relids walk
  JOIN pg_catalog.pg_inherits inh
    ON inh.inhrelid=walk.oid OR inh.inhparent=walk.oid
), inheritance_partition_catalog AS (
  SELECT jsonb_build_object(
    'edges',coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'child',cn.nspname||'.'||cc.relname,
        'parent',pn.nspname||'.'||pc.relname,
        'sequence',inh.inhseqno,'detach_pending',inh.inhdetachpending
      ) ORDER BY cn.nspname,cc.relname,inh.inhseqno,pn.nspname,pc.relname)
      FROM pg_catalog.pg_inherits inh
      JOIN pg_catalog.pg_class cc ON cc.oid=inh.inhrelid
      JOIN pg_catalog.pg_namespace cn ON cn.oid=cc.relnamespace
      JOIN pg_catalog.pg_class pc ON pc.oid=inh.inhparent
      JOIN pg_catalog.pg_namespace pn ON pn.oid=pc.relnamespace
      WHERE inh.inhrelid IN (SELECT oid FROM inheritance_partition_relids)
        AND inh.inhparent IN (SELECT oid FROM inheritance_partition_relids)
    ),'[]'::jsonb),
    'relations',coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'relation',n.nspname||'.'||c.relname,'kind',c.relkind::text,
        'is_partition',c.relispartition,
        'partition_bound',pg_catalog.pg_get_expr(c.relpartbound,c.oid,false),
        'partition_key',CASE WHEN c.relkind='p'
          THEN pg_catalog.pg_get_partkeydef(c.oid) ELSE NULL END,
        'partition_strategy',pt.partstrat::text,
        'default_partition',CASE WHEN pt.partdefid=0 THEN NULL
          ELSE (SELECT dn.nspname||'.'||dc.relname
                FROM pg_catalog.pg_class dc
                JOIN pg_catalog.pg_namespace dn ON dn.oid=dc.relnamespace
                WHERE dc.oid=pt.partdefid) END,
        'parents',coalesce((
          SELECT jsonb_agg(pn.nspname||'.'||pc.relname ORDER BY
            inh.inhseqno,pn.nspname,pc.relname)
          FROM pg_catalog.pg_inherits inh
          JOIN pg_catalog.pg_class pc ON pc.oid=inh.inhparent
          JOIN pg_catalog.pg_namespace pn ON pn.oid=pc.relnamespace
          WHERE inh.inhrelid=c.oid
        ),'[]'::jsonb),
        'children',coalesce((
          SELECT jsonb_agg(cn.nspname||'.'||cc.relname ORDER BY
            cn.nspname,cc.relname,inh.inhseqno)
          FROM pg_catalog.pg_inherits inh
          JOIN pg_catalog.pg_class cc ON cc.oid=inh.inhrelid
          JOIN pg_catalog.pg_namespace cn ON cn.oid=cc.relnamespace
          WHERE inh.inhparent=c.oid
        ),'[]'::jsonb)
      ) ORDER BY n.nspname,c.relname)
      FROM inheritance_partition_relids b
      JOIN pg_catalog.pg_class c ON c.oid=b.oid
      JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
      LEFT JOIN pg_catalog.pg_partitioned_table pt ON pt.partrelid=c.oid
    ),'[]'::jsonb)
  ) AS value
)
SELECT jsonb_build_object(
  'format','$EXPECTED_FORMAT',
  'status','CANDIDATE_NOT_RELEASE_APPROVAL',
  'target_relation_count',12,
  'app_client_auth_relation_allowlist',
    jsonb_build_array('app.client_auth_00042_meta','app.client_auth_00043_meta'),
  'app_client_auth_routine_allowlist',jsonb_build_array(),
  'app_client_auth_complement','DENY',
  'source_proof',jsonb_build_object(
    'postgresql_major',18,'system_identifier','$SOURCE_SYSTEM_IDENTIFIER',
    'database','$SOURCE_DATABASE','database_oid','$SOURCE_DATABASE_OID',
    'goose_applied',43,
    'meta_00042_contract_sha256','$PROOF_00042_CONTRACT',
    'meta_00042_catalog_sha256','$PROOF_00042_CATALOG',
    'meta_00043_contract_sha256','$PROOF_00043_CONTRACT',
    'meta_00043_release_id','$PROOF_00043_RELEASE',
    'meta_00043_runner_sha256','$PROOF_00043_RUNNER',
    'meta_00043_migration_sha256','$PROOF_00043_MIGRATION',
    'meta_00043_run_id','$PROOF_00043_RUN_ID',
    'meta_00043_evidence_sha256','$PROOF_00043_EVIDENCE',
    'meta_00043_catalog_sha256','$PROOF_00043_CATALOG',
    'protected_exact_proof_sha256','$PROTECTED_EXACT_PROOF',
    'app_routine_catalog_sha256','$EXPECTED_APP_ROUTINE_CATALOG',
    'routine_dependency_closure_sha256','$EXPECTED_ROUTINE_DEPENDENCY_CLOSURE',
    'target_trigger_closure_sha256','$EXPECTED_TRIGGER_CLOSURE',
    'rewrite_rule_catalog_sha256','$EXPECTED_REWRITE_RULE_CATALOG',
    'inheritance_partition_catalog_sha256','$EXPECTED_INHERITANCE_PARTITION_CATALOG'),
  'relations',r.value,
  'app_routine_catalog',a.value,
  'routine_dependency_closure',d.value,
  'target_trigger_closure',tr.value,
  'rewrite_rule_catalog',rw.value,
  'inheritance_partition_catalog',ip.value,
  'routine_surface_contract',
    'CATALOG_DEPENDENCY_EQUALITY_ONLY_NO_SEMANTIC_SQL_PARSING'
)::text
FROM relations r CROSS JOIN app_routine_catalog a
CROSS JOIN routine_dependency_closure d CROSS JOIN target_trigger_closure tr
CROSS JOIN rewrite_rule_catalog rw CROSS JOIN inheritance_partition_catalog ip
WHERE NOT EXISTS (SELECT 1 FROM presence WHERE catalog_rows<>1)
  AND NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_stat_activity
    WHERE backend_type='client backend' AND pid<>pg_catalog.pg_backend_pid()
  )
  AND encode(pg_catalog.sha256(pg_catalog.convert_to(a.value::text,'UTF8')),'hex')
      ='$EXPECTED_APP_ROUTINE_CATALOG'
  AND encode(pg_catalog.sha256(pg_catalog.convert_to(d.value::text,'UTF8')),'hex')
      ='$EXPECTED_ROUTINE_DEPENDENCY_CLOSURE'
  AND encode(pg_catalog.sha256(pg_catalog.convert_to(tr.value::text,'UTF8')),'hex')
      ='$EXPECTED_TRIGGER_CLOSURE'
  AND encode(pg_catalog.sha256(pg_catalog.convert_to(rw.value::text,'UTF8')),'hex')
      ='$EXPECTED_REWRITE_RULE_CATALOG'
  AND encode(pg_catalog.sha256(pg_catalog.convert_to(ip.value::text,'UTF8')),'hex')
      ='$EXPECTED_INHERITANCE_PARTITION_CATALOG';
ROLLBACK;
SQL
}

if ! EXACT_CATALOG="$(export_exact_catalog)"; then
  deny trusted_disposable_catalog_export_failed
fi

[[ "$EXACT_CATALOG" != *$'\n'* && "$EXACT_CATALOG" == \{*\} ]] ||
  deny exact_catalog_output_not_unique_json
for required in '"format": "client-auth-00043-pre00044-target-catalog-v1"' \
  '"relations"' '"columns"' '"constraints"' '"indexes"' '"policies"' '"triggers"' \
  '"acl"' '"owner"' '"rls"' '"force_rls"' '"comment"' '"identity"' '"generated"' \
  '"collation"' '"definition"' '"validated"' '"deferrable"' '"initially_deferred"' \
  '"dependencies"' '"enabled"' '"function"' '"prosrc"' '"app_routine_catalog"' \
  '"routine_dependency_closure"' '"target_trigger_closure"' \
  '"rewrite_rule_catalog"' '"inheritance_partition_catalog"' \
  '"routine_surface_contract": "CATALOG_DEPENDENCY_EQUALITY_ONLY_NO_SEMANTIC_SQL_PARSING"' \
  '"app_client_auth_complement": "DENY"' '"protected_exact_proof_sha256"'; do
  [[ "$EXACT_CATALOG" == *"$required"* ]] || deny exact_catalog_dimension_missing
done

FINGERPRINT_AFTER="$(inspect_source 2>/dev/null)" || deny source_container_reinspect_failed
[[ "$FINGERPRINT_AFTER" == "$FINGERPRINT" ]] || deny source_container_changed_during_export
NETWORK_FINGERPRINT_AFTER="$(inspect_network 2>/dev/null)" || deny source_network_reinspect_failed
[[ "$NETWORK_FINGERPRINT_AFTER" == "$NETWORK_FINGERPRINT" ]] ||
  deny source_network_changed_during_export
NETWORK_MEMBERS_AFTER="$(inspect_network_members 2>/dev/null)" ||
  deny source_network_membership_reinspect_failed
[[ "$NETWORK_MEMBERS_AFTER" == "$NETWORK_MEMBERS" ]] ||
  deny source_network_membership_changed_during_export

# A second isolated, lock-protected transaction recomputes the entire exact
# catalog after the first export. Publication is refused unless the canonical
# bytes are identical. This is intentionally not a substitute for trusting the
# isolated root runner; it closes ordinary concurrent-client/catalog drift.
if ! EXACT_CATALOG_FRESH="$(export_exact_catalog)"; then
  deny fresh_catalog_recomputation_failed
fi
[[ "$EXACT_CATALOG_FRESH" == "$EXACT_CATALOG" ]] ||
  deny fresh_catalog_recomputation_mismatch
FINGERPRINT_FRESH="$(inspect_source 2>/dev/null)" || deny source_container_fresh_reinspect_failed
[[ "$FINGERPRINT_FRESH" == "$FINGERPRINT" ]] ||
  deny source_container_changed_after_fresh_export
NETWORK_FINGERPRINT_FRESH="$(inspect_network 2>/dev/null)" ||
  deny source_network_fresh_reinspect_failed
[[ "$NETWORK_FINGERPRINT_FRESH" == "$NETWORK_FINGERPRINT" ]] ||
  deny source_network_changed_after_fresh_export
NETWORK_MEMBERS_FRESH="$(inspect_network_members 2>/dev/null)" ||
  deny source_network_membership_fresh_reinspect_failed
[[ "$NETWORK_MEMBERS_FRESH" == "$NETWORK_MEMBERS" ]] ||
  deny source_network_membership_changed_after_fresh_export
NOW_AFTER="$("$DATE_BIN" -u +%s 2>/dev/null)" || deny clock_recheck_unavailable
[[ "$NOW_AFTER" =~ ^[1-9][0-9]*$ && "$expires_at" -gt "$NOW_AFTER" ]] ||
  deny source_lease_expired_during_export

EXACT_SHA_LINE="$(printf '%s' "$EXACT_CATALOG" | "$SHA256_BIN" --text)" ||
  deny exact_catalog_hash_failed
read -r EXACT_SHA exact_sha_label exact_sha_extra <<<"$EXACT_SHA_LINE"
[[ "$exact_sha_label" == - && -z "${exact_sha_extra:-}" ]] ||
  deny exact_catalog_hash_output_invalid
safe_sha "$EXACT_SHA" || deny exact_catalog_hash_invalid

[[ -d "$OUTPUT_DIR_REAL" && ! -L "$OUTPUT_DIR_REAL" ]] || deny output_directory_invalid
[[ "$(fixed_tool_identity_set)" == "$FIXED_TOOL_IDENTITIES" ]] ||
  deny fixed_tool_identity_changed_before_publish
STAGE="$("$MKTEMP_BIN" "$OUTPUT_DIR_REAL/.client-auth-pre00044-target.XXXXXX")" ||
  deny output_stage_create_failed
trap '"$RM_BIN" -f -- "${STAGE:-}"' EXIT HUP INT TERM
{
  printf '{\n'
  printf '  "format": "%s",\n' "$EXPECTED_FORMAT"
  printf '  "status": "CANDIDATE_NOT_RELEASE_APPROVAL",\n'
  printf '  "postgresql_major": 18,\n'
  printf '  "source": {"system_identifier":"%s","container_id":"%s","network_id":"%s","database":"%s","database_oid":"%s","image_id":"%s","run_id":"%s"},\n' \
    "$SOURCE_SYSTEM_IDENTIFIER" "$SOURCE_CONTAINER_ID" "$network_id" "$SOURCE_DATABASE" \
    "$SOURCE_DATABASE_OID" "$SOURCE_IMAGE_ID" "$SOURCE_RUN_ID"
  printf '  "proof_bindings": {"protected_exact_proof_sha256":"%s","app_routine_catalog_sha256":"%s","routine_dependency_closure_sha256":"%s","target_trigger_closure_sha256":"%s","rewrite_rule_catalog_sha256":"%s","inheritance_partition_catalog_sha256":"%s"},\n' \
    "$PROTECTED_EXACT_PROOF" "$EXPECTED_APP_ROUTINE_CATALOG" \
    "$EXPECTED_ROUTINE_DEPENDENCY_CLOSURE" "$EXPECTED_TRIGGER_CLOSURE" \
    "$EXPECTED_REWRITE_RULE_CATALOG" "$EXPECTED_INHERITANCE_PARTITION_CATALOG"
  printf '  "exact_catalog_sha256": "%s",\n' "$EXACT_SHA"
  printf '  "exact_catalog": %s\n' "$EXACT_CATALOG"
  printf '}\n'
} >"$STAGE"
"$CHMOD_BIN" 0600 "$STAGE" || deny output_stage_chmod_failed
STAGE_ID_BEFORE="$(trusted_file_identity "$STAGE")" || deny output_stage_identity_failed
IFS='|' read -r stage_uid stage_kind stage_mode stage_dev stage_inode stage_links stage_size \
  <<<"$STAGE_ID_BEFORE"
IFS='|' read -r output_dir_dev output_dir_inode <<<"$OUTPUT_DIR_ID"
[[ "$stage_uid" == 0 && "$stage_kind" == 'regular file' && "$stage_mode" == 600 &&
   "$stage_dev" == "$output_dir_dev" && "$stage_links" == 1 &&
   "$stage_inode" =~ ^[1-9][0-9]*$ && "$stage_size" =~ ^[1-9][0-9]*$ ]] ||
  deny output_stage_policy_failed
STAGE_SHA_LINE="$("$SHA256_BIN" --text <"$STAGE")" ||
  deny output_stage_hash_failed
read -r STAGE_SHA stage_sha_label stage_sha_extra <<<"$STAGE_SHA_LINE"
[[ "$stage_sha_label" == - && -z "${stage_sha_extra:-}" ]] ||
  deny output_stage_hash_output_invalid
safe_sha "$STAGE_SHA" || deny output_stage_hash_invalid
"$SYNC_BIN" -f "$STAGE" || deny output_stage_fdatasync_failed
[[ "$(trusted_file_identity "$STAGE")" == "$STAGE_ID_BEFORE" ]] ||
  deny output_stage_changed_after_fsync
[[ "$(trusted_directory_chain "$OUTPUT_DIR_REAL")" == "$OUTPUT_CHAIN_BEFORE" &&
   "$("$STAT_BIN" -c '%d|%i' -- "$OUTPUT_DIR_REAL")" == "$OUTPUT_DIR_ID" ]] ||
  deny output_ancestor_chain_changed_before_publish
[[ ! -e "$OUTPUT" && ! -L "$OUTPUT" ]] || deny output_appeared_before_publish
"$LN_BIN" -- "$STAGE" "$OUTPUT" || deny manifest_publish_no_clobber_failed
STAGE_ID_LINKED="$(trusted_file_identity "$STAGE")" || deny linked_stage_identity_failed
OUTPUT_ID_LINKED="$(trusted_file_identity "$OUTPUT")" || deny linked_output_identity_failed
IFS='|' read -r _ _ linked_stage_mode linked_stage_dev linked_stage_inode \
  linked_stage_links linked_stage_size <<<"$STAGE_ID_LINKED"
IFS='|' read -r linked_output_uid linked_output_kind linked_output_mode linked_output_dev \
  linked_output_inode linked_output_links linked_output_size <<<"$OUTPUT_ID_LINKED"
[[ "$linked_output_uid" == 0 && "$linked_output_kind" == 'regular file' &&
   "$linked_stage_mode" == 600 && "$linked_output_mode" == 600 &&
   "$linked_stage_dev" == "$stage_dev" && "$linked_output_dev" == "$stage_dev" &&
   "$linked_stage_inode" == "$stage_inode" && "$linked_output_inode" == "$stage_inode" &&
   "$linked_stage_links" == 2 && "$linked_output_links" == 2 &&
   "$linked_stage_size" == "$stage_size" && "$linked_output_size" == "$stage_size" ]] ||
  deny hardlink_publish_identity_mismatch
"$SYNC_BIN" -f "$OUTPUT_DIR_REAL" || deny output_directory_fsync_after_link_failed
"$RM_BIN" -f -- "$STAGE"
STAGE=''
OUTPUT_ID_FINAL="$(trusted_file_identity "$OUTPUT")" || deny final_output_identity_failed
IFS='|' read -r final_uid final_kind final_mode final_dev final_inode final_links final_size \
  <<<"$OUTPUT_ID_FINAL"
[[ "$final_uid" == 0 && "$final_kind" == 'regular file' && "$final_mode" == 600 &&
   "$final_dev" == "$stage_dev" && "$final_inode" == "$stage_inode" &&
   "$final_links" == 1 && "$final_size" == "$stage_size" ]] ||
  deny final_output_policy_failed
FINAL_SHA_LINE="$("$SHA256_BIN" --text <"$OUTPUT")" || deny final_output_hash_failed
read -r FINAL_SHA final_sha_label final_sha_extra <<<"$FINAL_SHA_LINE"
[[ "$final_sha_label" == - && -z "${final_sha_extra:-}" &&
   "$FINAL_SHA" == "$STAGE_SHA" ]] ||
  deny final_output_hash_mismatch
"$SYNC_BIN" -f "$OUTPUT_DIR_REAL" || deny output_directory_fsync_after_unlink_failed
OUTPUT_CHAIN_AFTER="$(trusted_directory_chain "$OUTPUT_DIR_REAL")" ||
  deny final_output_ancestor_chain_untrusted
[[ "$OUTPUT_CHAIN_AFTER" == "$OUTPUT_CHAIN_BEFORE" &&
   "$("$STAT_BIN" -c '%d|%i' -- "$OUTPUT_DIR_REAL")" == "$OUTPUT_DIR_ID" ]] ||
  deny output_ancestor_chain_changed_after_publish
[[ "$(fixed_tool_identity_set)" == "$FIXED_TOOL_IDENTITIES" ]] ||
  deny fixed_tool_identity_changed_after_publish
trap - EXIT HUP INT TERM
printf 'client_auth_pre00044_target_manifest=PASS sha256=%s file_sha256=%s status=CANDIDATE_NOT_RELEASE_APPROVAL\n' \
  "$EXACT_SHA" "$STAGE_SHA"
