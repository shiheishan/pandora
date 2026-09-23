#!/usr/bin/env bash
# Read-only exact catalog/provenance verifier for CLIENT-AUTH-00042.
# This verifier is intentionally not wired into migration or release scripts.
set -Eeuo pipefail
umask 077

deny() { echo "CLIENT-AUTH-00042 catalog verification: $*" >&2; exit 78; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MIGRATION="${AEGIS_CLIENT_AUTH_00042_MIGRATION:-$ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql}"
PSQL="${PSQL_BIN:-psql}"
DATABASE_URL="${AEGIS_VERIFY_DATABASE_URL:-}"
PGPASS_FILE="${AEGIS_VERIFY_PGPASS_FILE:-}"
FROZEN_MIGRATION_SHA256=ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5

[ -f "$MIGRATION" ] && [ ! -L "$MIGRATION" ] || deny "migration must be a regular non-symlink file"
if [[ "$PSQL" == */* ]]; then
  [ -x "$PSQL" ] || deny "psql executable is missing"
else
  PSQL="$(command -v "$PSQL" 2>/dev/null)" || deny "psql executable is missing"
  [ -n "$PSQL" ] && [ -x "$PSQL" ] || deny "psql executable is missing"
fi
command -v sha256sum >/dev/null 2>&1 || deny "sha256sum executable is missing"
[ -n "$DATABASE_URL" ] || deny "AEGIS_VERIFY_DATABASE_URL is required"
[[ "$DATABASE_URL" != *$'\n'* && "$DATABASE_URL" != *$'\r'* ]] || deny "database URL contains a newline"
[[ "$DATABASE_URL" != *password=* ]] || deny "database URL must not contain a password"
if [[ "$DATABASE_URL" == *://*:*@* ]]; then deny "database URL must not contain password userinfo"; fi
if [ -n "$PGPASS_FILE" ]; then
  [ -f "$PGPASS_FILE" ] && [ ! -L "$PGPASS_FILE" ] && [ -r "$PGPASS_FILE" ] \
    || deny "PGPASS file must be a readable regular non-symlink file"
fi

MIGRATION_SHA256="$(sha256sum "$MIGRATION" | awk '{print $1}')" \
  || deny "cannot hash migration"
[ "$MIGRATION_SHA256" = "$FROZEN_MIGRATION_SHA256" ] \
  || deny "frozen migration SHA256 mismatch"

PSQL_ENV=(env -i PATH="$PATH" HOME="${HOME:-/root}"
  PGOPTIONS='-c default_transaction_read_only=on -c search_path=pg_catalog')
[ -z "$PGPASS_FILE" ] || PSQL_ENV+=(PGPASSFILE="$PGPASS_FILE")

if ! RESULT="$("${PSQL_ENV[@]}" "$PSQL" -X -v ON_ERROR_STOP=1 -qAt \
    -d "$DATABASE_URL" -f - <<'SQL'
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL search_path=pg_catalog;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='2min';

DO $verify_client_auth_00042$
DECLARE
  v_contract constant text := '4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E';
  v_marker constant text := 'pandora:aegispanel:migration=00042;created=v1;contract=4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E';
  v_reserved_prefix constant text := 'pandora:aegispanel:migration=00042;';
  v_names constant text[] := ARRAY[
    'aegis_client_auth_owner','aegis_client_auth_gc_owner',
    'aegis_client_auth_gc','aegis_client_keyring_preflight_owner'
  ];
  v_applied bigint;
  v_mask smallint;
  v_role_oids jsonb;
  v_saved_catalog jsonb;
  v_current_catalog jsonb;
  v_name text;
  v_oid oid;
  v_comment text;
  v_index integer;
BEGIN
  IF current_setting('server_version_num')::integer / 10000 <> 18 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 verifier requires PostgreSQL 18';
  END IF;
  IF to_regclass('public.goose_db_version') IS NULL THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 goose table missing';
  END IF;
  SELECT max(version_id) FILTER (WHERE is_applied) INTO v_applied
  FROM public.goose_db_version;
  IF v_applied IS DISTINCT FROM 42 OR EXISTS (
    SELECT 1 FROM public.goose_db_version WHERE is_applied AND version_id>42
  ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 exact goose version mismatch';
  END IF;
  IF to_regclass('app.client_auth_00042_meta') IS NULL
     OR (SELECT count(*) FROM app.client_auth_00042_meta)<>1 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 meta relation/row mismatch';
  END IF;

  SELECT role_creation_mask,role_oid_manifest,catalog_manifest
    INTO STRICT v_mask,v_role_oids,v_saved_catalog
  FROM app.client_auth_00042_meta
  WHERE singleton
    AND contract_sha256=v_contract
    AND expand_at IS NOT NULL
    AND legacy_revocation_at IS NULL
    AND first_client_write_at IS NULL
    AND cutover_at IS NULL
    AND constraints_validated_at IS NULL;
  IF v_mask NOT BETWEEN 0 AND 15
     OR jsonb_typeof(v_role_oids) IS DISTINCT FROM 'object'
     OR (SELECT count(*) FROM jsonb_object_keys(v_role_oids))<>4
     OR NOT (v_role_oids ?& v_names)
     OR jsonb_typeof(v_saved_catalog) IS DISTINCT FROM 'object'
     OR v_saved_catalog->>'format' IS DISTINCT FROM 'client-auth-00042-catalog-v1'
     OR jsonb_typeof(v_saved_catalog->'relations') IS DISTINCT FROM 'array'
     OR jsonb_array_length(v_saved_catalog->'relations')<>15 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 meta envelope mismatch';
  END IF;

  FOR v_index IN 1..array_length(v_names,1) LOOP
    v_name:=v_names[v_index];
    SELECT oid,pg_catalog.shobj_description(oid,'pg_authid')
      INTO STRICT v_oid,v_comment FROM pg_catalog.pg_authid WHERE rolname=v_name;
    IF v_oid IS DISTINCT FROM (v_role_oids->>v_name)::oid
       OR NOT EXISTS (
         SELECT 1 FROM pg_catalog.pg_authid WHERE oid=v_oid
           AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb
           AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication
           AND NOT rolbypassrls AND rolconnlimit=-1
           AND rolvaliduntil IS NULL AND rolpassword IS NULL
       ) OR EXISTS (
         SELECT 1 FROM pg_catalog.pg_auth_members WHERE roleid=v_oid OR member=v_oid
       ) OR EXISTS (
         SELECT 1 FROM pg_catalog.pg_db_role_setting WHERE setrole=v_oid
       ) OR EXISTS (
         SELECT 1 FROM pg_catalog.pg_shdepend
         WHERE refclassid='pg_catalog.pg_authid'::regclass AND refobjid=v_oid
       ) THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 role OID/provenance mismatch: %',v_name;
    END IF;
    IF (v_mask::integer & (1 << (v_index-1)))<>0 THEN
      IF v_comment IS DISTINCT FROM v_marker THEN
        RAISE EXCEPTION 'CLIENT-AUTH-00042 created role marker mismatch: %',v_name;
      END IF;
    ELSIF v_comment LIKE v_reserved_prefix || '%' THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 retained role marker mismatch: %',v_name;
    END IF;
  END LOOP;

  WITH target(schema_name,relation_name) AS (
    VALUES
      ('app','client_auth_00042_meta'),('public','devices'),
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
  ), relation_catalog AS (
    SELECT jsonb_agg(jsonb_build_object(
      'schema',n.nspname,'name',c.relname,'kind',c.relkind::text,
      'persistence',c.relpersistence::text,
      'owner',pg_catalog.pg_get_userbyid(c.relowner),
      'rls',c.relrowsecurity,'force_rls',c.relforcerowsecurity,
      'replica_identity',c.relreplident::text,
      'comment',pg_catalog.obj_description(c.oid,'pg_class'),
      'columns',coalesce((SELECT jsonb_agg(jsonb_build_array(
        a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,
        pg_catalog.pg_get_expr(d.adbin,d.adrelid,false),a.attidentity::text,
        a.attgenerated::text,
        CASE WHEN a.attcollation=0 THEN NULL ELSE cn.nspname||'.'||co.collname END,
        a.attstorage::text,a.attcompression::text,
        pg_catalog.col_description(a.attrelid,a.attnum),
        coalesce((SELECT jsonb_agg(jsonb_build_array(
          pg_catalog.pg_get_userbyid(x.grantor),
          CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(x.grantee) END,
          x.privilege_type,x.is_grantable
        ) ORDER BY x.grantee,x.grantor,x.privilege_type)
        FROM pg_catalog.aclexplode(a.attacl) x),'[]'::jsonb)
      ) ORDER BY a.attnum)
      FROM pg_catalog.pg_attribute a
      LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
      LEFT JOIN pg_catalog.pg_collation co ON co.oid=a.attcollation
      LEFT JOIN pg_catalog.pg_namespace cn ON cn.oid=co.collnamespace
      WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped),'[]'::jsonb),
      'constraints',coalesce((SELECT jsonb_agg(jsonb_build_array(
        con.conname,con.contype::text,
        CASE WHEN con.conkey IS NULL THEN NULL ELSE (
          SELECT jsonb_agg(a.attname ORDER BY k.ord)
          FROM unnest(con.conkey) WITH ORDINALITY k(attnum,ord)
          JOIN pg_catalog.pg_attribute a ON a.attrelid=con.conrelid AND a.attnum=k.attnum
        ) END,pg_catalog.pg_get_constraintdef(con.oid,false),con.convalidated,
        con.condeferrable,con.condeferred
      ) ORDER BY con.conname) FROM pg_catalog.pg_constraint con
      WHERE con.conrelid=c.oid),'[]'::jsonb),
      'indexes',coalesce((SELECT jsonb_agg(jsonb_build_array(
        ci.relname,pg_catalog.pg_get_indexdef(i.indexrelid,0,false),
        i.indisunique,i.indisprimary,i.indisvalid,i.indisready,i.indislive,
        i.indisreplident,i.indnullsnotdistinct
      ) ORDER BY ci.relname) FROM pg_catalog.pg_index i
      JOIN pg_catalog.pg_class ci ON ci.oid=i.indexrelid
      WHERE i.indrelid=c.oid),'[]'::jsonb),
      'policies',coalesce((SELECT jsonb_agg(jsonb_build_array(
        p.polname,p.polpermissive,p.polcmd::text,
        (SELECT jsonb_agg(pg_catalog.pg_get_userbyid(rid)
           ORDER BY pg_catalog.pg_get_userbyid(rid)) FROM unnest(p.polroles) rid),
        pg_catalog.pg_get_expr(p.polqual,p.polrelid,false),
        pg_catalog.pg_get_expr(p.polwithcheck,p.polrelid,false)
      ) ORDER BY p.polname) FROM pg_catalog.pg_policy p
      WHERE p.polrelid=c.oid),'[]'::jsonb),
      'triggers',coalesce((SELECT jsonb_agg(jsonb_build_array(
        tg.tgname,tg.tgenabled::text,tg.tgisinternal,
        pg_catalog.pg_get_triggerdef(tg.oid,false)
      ) ORDER BY tg.tgname) FROM pg_catalog.pg_trigger tg
      WHERE tg.tgrelid=c.oid),'[]'::jsonb),
      'acl',coalesce((SELECT jsonb_agg(jsonb_build_array(
        pg_catalog.pg_get_userbyid(x.grantor),
        CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(x.grantee) END,
        x.privilege_type,x.is_grantable
      ) ORDER BY x.grantee,x.grantor,x.privilege_type)
      FROM pg_catalog.aclexplode(c.relacl) x),'[]'::jsonb),
      'sequence',CASE WHEN c.relkind='S' THEN (
        SELECT jsonb_build_array(s.seqstart,s.seqincrement,s.seqmax,s.seqmin,
          s.seqcache,s.seqcycle,coalesce((SELECT jsonb_agg(jsonb_build_array(
            dep.deptype::text,rn.nspname,rc.relname,a.attname
          ) ORDER BY dep.deptype,rn.nspname,rc.relname,a.attname)
          FROM pg_catalog.pg_depend dep
          JOIN pg_catalog.pg_class rc ON rc.oid=dep.refobjid
          JOIN pg_catalog.pg_namespace rn ON rn.oid=rc.relnamespace
          LEFT JOIN pg_catalog.pg_attribute a
            ON a.attrelid=rc.oid AND a.attnum=dep.refobjsubid
          WHERE dep.classid='pg_catalog.pg_class'::regclass AND dep.objid=c.oid
            AND dep.refclassid='pg_catalog.pg_class'::regclass
            AND dep.refobjsubid>0),'[]'::jsonb))
        FROM pg_catalog.pg_sequence s WHERE s.seqrelid=c.oid
      ) ELSE NULL END
    ) ORDER BY n.nspname,c.relname) AS document
    FROM target t
    JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
    JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  )
  SELECT jsonb_build_object('format','client-auth-00042-catalog-v1','relations',document)
    INTO STRICT v_current_catalog FROM relation_catalog;
  IF v_current_catalog IS DISTINCT FROM v_saved_catalog THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 exact catalog manifest mismatch';
  END IF;
END
$verify_client_auth_00042$;

SELECT 'CLIENT-AUTH-00042-CATALOG=PASS';
ROLLBACK;
SQL
)"; then
  deny "database rejected exact catalog/provenance verification"
fi

[ "$RESULT" = 'CLIENT-AUTH-00042-CATALOG=PASS' ] \
  || deny "verifier output was not the unique PASS marker"
printf '%s\n' "$RESULT"
