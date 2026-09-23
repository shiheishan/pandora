-- CLIENT-AUTH-00042 OID-free protected-surface recomputation.
-- Read-only, deterministic under PostgreSQL 18 and search_path=pg_catalog.
-- The current document intentionally reproduces catalog_manifest v1 exactly.
\set ON_ERROR_STOP on
\pset tuples_only on
\pset format unaligned
\set VERBOSITY terse

BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL search_path = pg_catalog;
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '5s';

SELECT 1 / CASE
  WHEN current_setting('server_version_num')::integer BETWEEN 180000 AND 189999
  THEN 1 ELSE 0
END;

WITH target(ordinal,schema_name,relation_name) AS (
  VALUES
    (1,'app','client_auth_00042_meta'),
    (2,'public','devices'),
    (3,'public','device_authorizations'),
    (4,'public','config_bundles'),
    (5,'public','refresh_tokens'),
    (6,'public','config_bundle_nodes'),
    (7,'public','refresh_families'),
    (8,'public','device_proof_nonces'),
    (9,'public','client_access_token_jtis'),
    (10,'public','device_issuance_response_replays'),
    (11,'public','client_refresh_response_replays'),
    (12,'public','device_issuance_replay_uses'),
    (13,'public','client_refresh_replay_uses'),
    (14,'public','device_proof_nonces_id_seq'),
    (15,'public','client_access_token_jtis_id_seq')
), presence AS (
  SELECT t.ordinal,t.schema_name,t.relation_name,count(c.oid) AS catalog_rows
  FROM target t
  LEFT JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  LEFT JOIN pg_catalog.pg_class c
    ON c.relnamespace=n.oid AND c.relname=t.relation_name
  GROUP BY t.ordinal,t.schema_name,t.relation_name
), target_guard AS (
  SELECT 1 / CASE
    WHEN count(*)=15
     AND count(DISTINCT (schema_name,relation_name))=15
     AND bool_and(catalog_rows=1)
    THEN 1 ELSE 0
  END AS exact_targets
  FROM presence
), relation_catalog AS (
  SELECT jsonb_agg(
    jsonb_build_object(
      'schema', n.nspname,
      'name', c.relname,
      'kind', c.relkind::text,
      'persistence', c.relpersistence::text,
      'owner', pg_catalog.pg_get_userbyid(c.relowner),
      'rls', c.relrowsecurity,
      'force_rls', c.relforcerowsecurity,
      'replica_identity', c.relreplident::text,
      'comment', pg_catalog.obj_description(c.oid,'pg_class'),
      'columns', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          a.attname,
          pg_catalog.format_type(a.atttypid,a.atttypmod),
          a.attnotnull,
          pg_catalog.pg_get_expr(d.adbin,d.adrelid,false),
          a.attidentity::text,
          a.attgenerated::text,
          CASE WHEN a.attcollation=0 THEN NULL
               ELSE cn.nspname||'.'||co.collname END,
          a.attstorage::text,
          a.attcompression::text,
          pg_catalog.col_description(a.attrelid,a.attnum),
          coalesce((
            SELECT jsonb_agg(jsonb_build_array(
              pg_catalog.pg_get_userbyid(x.grantor),
              CASE WHEN x.grantee=0 THEN 'PUBLIC'
                   ELSE pg_catalog.pg_get_userbyid(x.grantee) END,
              x.privilege_type,
              x.is_grantable
            ) ORDER BY x.grantee,x.grantor,x.privilege_type)
            FROM pg_catalog.aclexplode(a.attacl) x
          ),'[]'::jsonb)
        ) ORDER BY a.attnum)
        FROM pg_catalog.pg_attribute a
        LEFT JOIN pg_catalog.pg_attrdef d
          ON d.adrelid=a.attrelid AND d.adnum=a.attnum
        LEFT JOIN pg_catalog.pg_collation co ON co.oid=a.attcollation
        LEFT JOIN pg_catalog.pg_namespace cn ON cn.oid=co.collnamespace
        WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
      ),'[]'::jsonb),
      'constraints', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          con.conname,
          con.contype::text,
          CASE WHEN con.conkey IS NULL THEN NULL ELSE (
            SELECT jsonb_agg(a.attname ORDER BY k.ord)
            FROM unnest(con.conkey) WITH ORDINALITY k(attnum,ord)
            JOIN pg_catalog.pg_attribute a
              ON a.attrelid=con.conrelid AND a.attnum=k.attnum
          ) END,
          pg_catalog.pg_get_constraintdef(con.oid,false),
          con.convalidated,
          con.condeferrable,
          con.condeferred
        ) ORDER BY con.conname)
        FROM pg_catalog.pg_constraint con
        WHERE con.conrelid=c.oid
      ),'[]'::jsonb),
      'indexes', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          ci.relname,
          pg_catalog.pg_get_indexdef(i.indexrelid,0,false),
          i.indisunique,
          i.indisprimary,
          i.indisvalid,
          i.indisready,
          i.indislive,
          i.indisreplident,
          i.indnullsnotdistinct
        ) ORDER BY ci.relname)
        FROM pg_catalog.pg_index i
        JOIN pg_catalog.pg_class ci ON ci.oid=i.indexrelid
        WHERE i.indrelid=c.oid
      ),'[]'::jsonb),
      'policies', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          p.polname,
          p.polpermissive,
          p.polcmd::text,
          (SELECT jsonb_agg(pg_catalog.pg_get_userbyid(rid)
                    ORDER BY pg_catalog.pg_get_userbyid(rid))
             FROM unnest(p.polroles) rid),
          pg_catalog.pg_get_expr(p.polqual,p.polrelid,false),
          pg_catalog.pg_get_expr(p.polwithcheck,p.polrelid,false)
        ) ORDER BY p.polname)
        FROM pg_catalog.pg_policy p
        WHERE p.polrelid=c.oid
      ),'[]'::jsonb),
      'triggers', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          tg.tgname,
          tg.tgenabled::text,
          tg.tgisinternal,
          pg_catalog.pg_get_triggerdef(tg.oid,false)
        ) ORDER BY tg.tgname)
        FROM pg_catalog.pg_trigger tg
        WHERE tg.tgrelid=c.oid
      ),'[]'::jsonb),
      'acl', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          pg_catalog.pg_get_userbyid(x.grantor),
          CASE WHEN x.grantee=0 THEN 'PUBLIC'
               ELSE pg_catalog.pg_get_userbyid(x.grantee) END,
          x.privilege_type,
          x.is_grantable
        ) ORDER BY x.grantee,x.grantor,x.privilege_type)
        FROM pg_catalog.aclexplode(c.relacl) x
      ),'[]'::jsonb),
      'sequence', CASE WHEN c.relkind='S' THEN (
        SELECT jsonb_build_array(
          s.seqstart,s.seqincrement,s.seqmax,s.seqmin,s.seqcache,s.seqcycle,
          coalesce((
            SELECT jsonb_agg(jsonb_build_array(
              d.deptype::text,rn.nspname,rc.relname,a.attname
            ) ORDER BY d.deptype,rn.nspname,rc.relname,a.attname)
            FROM pg_catalog.pg_depend d
            JOIN pg_catalog.pg_class rc ON rc.oid=d.refobjid
            JOIN pg_catalog.pg_namespace rn ON rn.oid=rc.relnamespace
            LEFT JOIN pg_catalog.pg_attribute a
              ON a.attrelid=rc.oid AND a.attnum=d.refobjsubid
            WHERE d.classid='pg_catalog.pg_class'::regclass
              AND d.objid=c.oid
              AND d.refclassid='pg_catalog.pg_class'::regclass
              AND d.refobjsubid>0
          ),'[]'::jsonb)
        )
        FROM pg_catalog.pg_sequence s
        WHERE s.seqrelid=c.oid
      ) ELSE NULL END
    ) ORDER BY n.nspname,c.relname
  ) AS document
  FROM target t
  JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c
    ON c.relnamespace=n.oid AND c.relname=t.relation_name
), current_surface AS (
  SELECT jsonb_build_object(
    'format','client-auth-00042-catalog-v1',
    'relations',relation_catalog.document
  ) AS manifest
  FROM relation_catalog
), stored_surface AS (
  SELECT count(*) AS meta_rows,
         max(m.catalog_manifest::text)::jsonb AS manifest
  FROM app.client_auth_00042_meta m
  WHERE m.singleton
), verified AS (
  SELECT c.manifest AS current_manifest,
         s.manifest AS stored_manifest,
         encode(pg_catalog.sha256(
           pg_catalog.convert_to(c.manifest::text,'UTF8')),'hex') AS current_sha256,
         encode(pg_catalog.sha256(
           pg_catalog.convert_to(s.manifest::text,'UTF8')),'hex') AS stored_sha256,
         1 / CASE
           WHEN g.exact_targets=1
            AND s.meta_rows=1
            AND jsonb_typeof(s.manifest)='object'
            AND s.manifest->>'format'='client-auth-00042-catalog-v1'
            AND jsonb_typeof(s.manifest->'relations')='array'
            AND jsonb_array_length(s.manifest->'relations')=15
            AND c.manifest=s.manifest
           THEN 1 ELSE 0
         END AS exact_match
  FROM current_surface c
  CROSS JOIN stored_surface s
  CROSS JOIN target_guard g
), output(ordinal,line) AS (
  SELECT v.ordinal,v.line
  FROM verified x
  CROSS JOIN LATERAL (VALUES
    (1,'protected_surface_format=client-auth-00042-catalog-v1'),
    (2,'protected_surface_relation_count=15'),
    (3,'protected_surface_current_sha256='||x.current_sha256),
    (4,'protected_surface_stored_sha256='||x.stored_sha256),
    (5,'protected_surface_match='||(x.exact_match=1)::text),
    (6,'protected_surface_manifest='||x.current_manifest::text)
  ) v(ordinal,line)
)
SELECT line FROM output ORDER BY ordinal;

ROLLBACK;
