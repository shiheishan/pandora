-- CLIENT-AUTH-00043 Goose V3 finalizer/control plane only.
-- V3 contract SHA-256:
--   E8C323762014B5F97D04CF861287026F0D3784D56B2324E1E6B0BC14D833DAC7
-- The companion runner owns every concurrent index/constraint data-plane action.

-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
SET LOCAL search_path = pg_catalog;

-- +goose StatementBegin
DO $client_auth_00043_finalize_up$
DECLARE
  v_tables constant text[] := ARRAY[
    'subscriptions','devices','sessions','sessions','sessions','sessions',
    'device_authorizations','device_authorizations','device_authorizations',
    'device_tokens','device_tokens','subscription_credentials','subscription_credentials',
    'config_bundles','refresh_tokens','refresh_tokens','refresh_tokens','refresh_tokens','refresh_tokens'
  ];
  v_names constant text[] := ARRAY[
    'subscriptions_tenant_id_id_user_id_key',
    'devices_tenant_id_id_user_id_key',
    'sessions_tenant_id_id_key',
    'sessions_tenant_id_id_user_id_key',
    'sessions_tenant_id_id_device_id_key',
    'sessions_tenant_id_id_user_id_device_id_key',
    'device_authorizations_tenant_id_id_key',
    'device_authorizations_tenant_id_id_device_id_key',
    'device_authorizations_tenant_user_code_mac_key',
    'device_tokens_tenant_id_id_key',
    'device_tokens_tenant_device_id_id_key',
    'subscription_credentials_tenant_id_id_key',
    'subscription_credentials_tenant_id_id_sub_user_key',
    'config_bundles_tenant_id_id_key',
    'refresh_tokens_tenant_id_id_key',
    'refresh_tokens_tenant_family_id_id_key',
    'refresh_tokens_tenant_family_session_device_id_key',
    'refresh_tokens_tenant_family_generation_key',
    'refresh_tokens_one_active_family'
  ];
  v_defs constant text[] := ARRAY[
    'CREATE UNIQUE INDEX subscriptions_tenant_id_id_user_id_key ON public.subscriptions USING btree (tenant_id, id, user_id)',
    'CREATE UNIQUE INDEX devices_tenant_id_id_user_id_key ON public.devices USING btree (tenant_id, id, user_id)',
    'CREATE UNIQUE INDEX sessions_tenant_id_id_key ON public.sessions USING btree (tenant_id, id)',
    'CREATE UNIQUE INDEX sessions_tenant_id_id_user_id_key ON public.sessions USING btree (tenant_id, id, user_id)',
    'CREATE UNIQUE INDEX sessions_tenant_id_id_device_id_key ON public.sessions USING btree (tenant_id, id, device_id)',
    'CREATE UNIQUE INDEX sessions_tenant_id_id_user_id_device_id_key ON public.sessions USING btree (tenant_id, id, user_id, device_id)',
    'CREATE UNIQUE INDEX device_authorizations_tenant_id_id_key ON public.device_authorizations USING btree (tenant_id, id)',
    'CREATE UNIQUE INDEX device_authorizations_tenant_id_id_device_id_key ON public.device_authorizations USING btree (tenant_id, id, device_id)',
    'CREATE UNIQUE INDEX device_authorizations_tenant_user_code_mac_key ON public.device_authorizations USING btree (tenant_id, user_code_mac)',
    'CREATE UNIQUE INDEX device_tokens_tenant_id_id_key ON public.device_tokens USING btree (tenant_id, id)',
    'CREATE UNIQUE INDEX device_tokens_tenant_device_id_id_key ON public.device_tokens USING btree (tenant_id, device_id, id)',
    'CREATE UNIQUE INDEX subscription_credentials_tenant_id_id_key ON public.subscription_credentials USING btree (tenant_id, id)',
    'CREATE UNIQUE INDEX subscription_credentials_tenant_id_id_sub_user_key ON public.subscription_credentials USING btree (tenant_id, id, subscription_id, user_id)',
    'CREATE UNIQUE INDEX config_bundles_tenant_id_id_key ON public.config_bundles USING btree (tenant_id, id)',
    'CREATE UNIQUE INDEX refresh_tokens_tenant_id_id_key ON public.refresh_tokens USING btree (tenant_id, id)',
    'CREATE UNIQUE INDEX refresh_tokens_tenant_family_id_id_key ON public.refresh_tokens USING btree (tenant_id, family_id, id)',
    'CREATE UNIQUE INDEX refresh_tokens_tenant_family_session_device_id_key ON public.refresh_tokens USING btree (tenant_id, family_id, session_id, device_id, id)',
    'CREATE UNIQUE INDEX refresh_tokens_tenant_family_generation_key ON public.refresh_tokens USING btree (tenant_id, family_id, generation)',
    'CREATE UNIQUE INDEX refresh_tokens_one_active_family ON public.refresh_tokens USING btree (tenant_id, family_id) WHERE (status = ''active''::text)'
  ];
  v_contract constant text := 'E8C323762014B5F97D04CF861287026F0D3784D56B2324E1E6B0BC14D833DAC7';
  v_expected_runner constant text := '509935a161d2716edd4da6feefe87775de36d5dc1b3850bff4db94a942776df7';
  v_expected_release constant text := 'client-auth-00043-v3';
  v_run_id text := current_setting('aegis.client_auth_00043_run_id', true);
  v_evidence text := current_setting('aegis.client_auth_00043_evidence_sha256', true);
  v_runner text := current_setting('aegis.client_auth_00043_runner_sha256', true);
  v_migration text := current_setting('aegis.client_auth_00043_migration_sha256', true);
  v_release text := current_setting('aegis.client_auth_00043_release_id', true);
  v_source_system text := current_setting('aegis.client_auth_00043_source_system_identifier', true);
  v_source_database text := current_setting('aegis.client_auth_00043_source_database_name', true);
  v_source_oid text := current_setting('aegis.client_auth_00043_source_database_oid', true);
  v_applied bigint;
  v_count integer;
  v_index integer;
  v_manifest jsonb := '[]'::jsonb;
  v_index_oid oid;
  v_constraint_oid oid;
  v_stored_catalog_manifest jsonb;
  v_current_catalog_manifest jsonb;
  v_stored_catalog_sha256 text;
  v_current_catalog_sha256 text;
  v_excluded_index_count integer;
  v_excluded_constraint_count integer;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock(420042,1);
  IF current_setting('server_version_num')::integer NOT BETWEEN 180000 AND 189999 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 requires PostgreSQL 18';
  END IF;
  IF current_setting('aegis.client_auth_00043_finalize_approved', true) IS DISTINCT FROM 'approved-v1'
     OR current_setting('aegis.client_auth_00043_writers_stopped', true) IS DISTINCT FROM 'stopped-v1'
     OR v_run_id !~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'
     OR v_evidence !~ '^[0-9a-f]{64}$'
     OR v_runner IS DISTINCT FROM v_expected_runner
     OR v_migration !~ '^[0-9a-f]{64}$'
     OR v_release IS DISTINCT FROM v_expected_release
     OR v_source_system !~ '^[1-9][0-9]{0,19}$'
     OR v_source_database !~ '^[A-Za-z_][A-Za-z0-9_-]{0,62}$'
     OR v_source_oid !~ '^[1-9][0-9]{0,9}$' THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 finalize approval/evidence identity missing';
  END IF;
  IF (SELECT system_identifier::text FROM pg_catalog.pg_control_system()) IS DISTINCT FROM v_source_system
     OR current_database() IS DISTINCT FROM v_source_database
     OR (SELECT oid::text FROM pg_catalog.pg_database WHERE datname=current_database()) IS DISTINCT FROM v_source_oid THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 source database identity mismatch';
  END IF;
  IF to_regclass('public.goose_db_version') IS NULL THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 requires goose_db_version';
  END IF;
  SELECT max(version_id) FILTER (WHERE is_applied) INTO v_applied FROM public.goose_db_version;
  IF v_applied IS DISTINCT FROM 42
     OR EXISTS (SELECT 1 FROM public.goose_db_version WHERE is_applied AND version_id > 42) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 Up requires exact applied version 42';
  END IF;
  IF to_regclass('app.client_auth_00042_meta') IS NULL
     OR (SELECT count(*) FROM app.client_auth_00042_meta) <> 1
     OR NOT EXISTS (
       SELECT 1 FROM app.client_auth_00042_meta
       WHERE singleton
         AND contract_sha256='4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E'
         AND legacy_revocation_at IS NULL AND first_client_write_at IS NULL
         AND cutover_at IS NULL AND constraints_validated_at IS NULL
         AND catalog_manifest->>'format'='client-auth-00042-catalog-v1'
     ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 00042 meta/watermark boundary mismatch';
  END IF;
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='subscription_credentials'
      AND is_generated='ALWAYS'
  ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 refuses 00044 generated evidence';
  END IF;
  IF (SELECT count(*) FROM public.config_bundle_nodes) <> 0
     OR (SELECT count(*) FROM public.refresh_families) <> 0
     OR (SELECT count(*) FROM public.device_proof_nonces) <> 0
     OR (SELECT count(*) FROM public.client_access_token_jtis) <> 0
     OR (SELECT count(*) FROM public.device_issuance_response_replays) <> 0
     OR (SELECT count(*) FROM public.client_refresh_response_replays) <> 0
     OR (SELECT count(*) FROM public.device_issuance_replay_uses) <> 0
     OR (SELECT count(*) FROM public.client_refresh_replay_uses) <> 0 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 runtime tables must remain empty';
  END IF;
  IF to_regclass('app.client_auth_00043_meta') IS NOT NULL THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 meta already exists';
  END IF;

  FOR v_index IN 1..19 LOOP
    SELECT count(*), min(ic.oid), min(con.oid)
      INTO v_count, v_index_oid, v_constraint_oid
    FROM pg_catalog.pg_class tc
    JOIN pg_catalog.pg_namespace n ON n.oid=tc.relnamespace
    JOIN pg_catalog.pg_class ic ON ic.relnamespace=n.oid AND ic.relname=v_names[v_index]
    JOIN pg_catalog.pg_index ix ON ix.indexrelid=ic.oid
    JOIN pg_catalog.pg_am am ON am.oid=ic.relam
    LEFT JOIN pg_catalog.pg_constraint con
      ON con.connamespace=n.oid AND con.conname=v_names[v_index]
       AND con.conrelid=tc.oid AND con.conindid=ic.oid
    WHERE n.nspname='public' AND tc.relname=v_tables[v_index]
      AND tc.relkind='r' AND tc.relpersistence='p'
      AND ic.relkind='i' AND ic.relpersistence='p'
      AND ic.relowner=tc.relowner AND ic.reltablespace=0 AND ic.reloptions IS NULL
      AND am.amname='btree' AND ix.indisunique AND ix.indimmediate
      AND ix.indisvalid AND ix.indisready AND ix.indislive
      AND NOT ix.indnullsnotdistinct AND ix.indexprs IS NULL
      AND pg_catalog.pg_get_indexdef(ic.oid)=v_defs[v_index]
      AND (
        (v_index=19 AND con.oid IS NULL AND ix.indpred IS NOT NULL
          AND pg_catalog.pg_get_expr(ix.indpred,ix.indrelid,false)='(status = ''active''::text)'
          AND (SELECT count(*) FROM pg_catalog.pg_depend d
               WHERE d.classid='pg_catalog.pg_class'::pg_catalog.regclass
                 AND d.objid=ic.oid AND d.deptype='i')=0)
        OR
        (v_index<19 AND ix.indpred IS NULL AND con.contype='u' AND con.convalidated
          AND NOT con.condeferrable AND NOT con.condeferred
          AND (SELECT count(*) FROM pg_catalog.pg_depend d
               WHERE d.classid='pg_catalog.pg_class'::pg_catalog.regclass
                  AND d.objid=ic.oid AND d.objsubid=0
                  AND d.refclassid='pg_catalog.pg_constraint'::pg_catalog.regclass
                  AND d.refobjid=con.oid AND d.refobjsubid=0 AND d.deptype='i')=1)
          AND (SELECT count(*) FROM pg_catalog.pg_depend d
               WHERE d.classid='pg_catalog.pg_class'::pg_catalog.regclass
                 AND d.objid=ic.oid AND d.deptype='i')=1)
      );
    IF v_count <> 1 OR v_index_oid IS NULL
       OR (v_index < 19 AND v_constraint_oid IS NULL)
       OR (v_index = 19 AND v_constraint_oid IS NOT NULL) THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00043 finalizer object mismatch: %', v_names[v_index];
    END IF;
    v_manifest := v_manifest || jsonb_build_array(jsonb_build_object(
      'id', format('U43-%s', lpad(v_index::text,2,'0')),
      'table', v_tables[v_index], 'name', v_names[v_index],
      'indexdef', v_defs[v_index], 'attached', v_index<19
    ));
  END LOOP;

  -- The runner has already installed the 19/18 exact U43 objects at this
  -- boundary.  Prove that the only objects normalized out of the frozen 00042
  -- protected surface are that fully classified allowlist.
  SELECT count(*) INTO v_excluded_index_count
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='public' AND c.relkind='i' AND c.relname=ANY(v_names);
  SELECT count(*) INTO v_excluded_constraint_count
  FROM pg_catalog.pg_constraint con
  JOIN pg_catalog.pg_namespace n ON n.oid=con.connamespace
  WHERE n.nspname='public' AND con.conname=ANY(v_names);
  IF v_excluded_index_count <> 19 OR v_excluded_constraint_count <> 18 THEN
    RAISE EXCEPTION
      'CLIENT-AUTH-00043 protected-surface allowlist cardinality mismatch: indexes %, constraints %',
      v_excluded_index_count,v_excluded_constraint_count;
  END IF;

  SELECT catalog_manifest INTO STRICT v_stored_catalog_manifest
  FROM app.client_auth_00042_meta
  WHERE singleton;
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
            AND NOT (con.conname=ANY(v_names))
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
            AND NOT (ci.relname=ANY(v_names))
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
  )
  SELECT jsonb_build_object(
           'format','client-auth-00042-catalog-v1',
           'relations',relation_catalog.document
         )
    INTO v_current_catalog_manifest
  FROM relation_catalog;
  v_stored_catalog_sha256 := encode(pg_catalog.sha256(
    pg_catalog.convert_to(v_stored_catalog_manifest::text,'UTF8')),'hex');
  v_current_catalog_sha256 := encode(pg_catalog.sha256(
    pg_catalog.convert_to(v_current_catalog_manifest::text,'UTF8')),'hex');
  IF jsonb_typeof(v_stored_catalog_manifest) IS DISTINCT FROM 'object'
     OR v_stored_catalog_manifest->>'format' IS DISTINCT FROM 'client-auth-00042-catalog-v1'
     OR jsonb_typeof(v_stored_catalog_manifest->'relations') IS DISTINCT FROM 'array'
     OR jsonb_array_length(v_stored_catalog_manifest->'relations') <> 15
     OR jsonb_typeof(v_current_catalog_manifest) IS DISTINCT FROM 'object'
     OR jsonb_typeof(v_current_catalog_manifest->'relations') IS DISTINCT FROM 'array'
     OR jsonb_array_length(v_current_catalog_manifest->'relations') <> 15
     OR v_current_catalog_manifest IS DISTINCT FROM v_stored_catalog_manifest
     OR v_current_catalog_sha256 IS DISTINCT FROM v_stored_catalog_sha256 THEN
    RAISE EXCEPTION
      'CLIENT-AUTH-00043 protected-surface drift before Up mutation: current %, stored %',
      v_current_catalog_sha256,v_stored_catalog_sha256;
  END IF;

  CREATE TABLE app.client_auth_00043_meta (
    singleton boolean PRIMARY KEY CHECK (singleton),
    v3_contract_sha256 text NOT NULL CHECK (v3_contract_sha256 ~ '^[0-9A-F]{64}$'),
    release_id text NOT NULL CHECK (release_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    runner_sha256 text NOT NULL CHECK (runner_sha256 ~ '^[0-9a-f]{64}$'),
    migration_sha256 text NOT NULL CHECK (migration_sha256 ~ '^[0-9a-f]{64}$'),
    run_id text NOT NULL CHECK (run_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'),
    evidence_sha256 text NOT NULL CHECK (evidence_sha256 ~ '^[0-9a-f]{64}$'),
    source_system_identifier text NOT NULL CHECK (source_system_identifier ~ '^[1-9][0-9]{0,19}$'),
    source_database_name text NOT NULL CHECK (source_database_name ~ '^[A-Za-z_][A-Za-z0-9_-]{0,62}$'),
    source_database_oid oid NOT NULL,
    catalog_manifest jsonb NOT NULL,
    finalized_at timestamptz(6) NOT NULL DEFAULT transaction_timestamp(),
    legacy_revocation_at timestamptz(6),
    first_client_write_at timestamptz(6),
    cutover_at timestamptz(6),
    constraints_validated_at timestamptz(6)
  );
  INSERT INTO app.client_auth_00043_meta(
    singleton,v3_contract_sha256,release_id,runner_sha256,migration_sha256,
    run_id,evidence_sha256,source_system_identifier,source_database_name,
    source_database_oid,catalog_manifest
  ) VALUES (
    true,v_contract,v_release,v_runner,v_migration,v_run_id,v_evidence,
    v_source_system,v_source_database,v_source_oid::oid,v_manifest
  );
  REVOKE ALL ON TABLE app.client_auth_00043_meta FROM PUBLIC;
END
$client_auth_00043_finalize_up$;
-- +goose StatementEnd

-- +goose Down

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
SET LOCAL search_path = pg_catalog;

-- +goose StatementBegin
DO $client_auth_00043_finalize_down$
DECLARE
  v_tables constant text[] := ARRAY[
    'subscriptions','devices','sessions','device_authorizations','device_tokens',
    'subscription_credentials','config_bundles','refresh_tokens'
  ];
  v_names constant text[] := ARRAY[
    'subscriptions_tenant_id_id_user_id_key','devices_tenant_id_id_user_id_key',
    'sessions_tenant_id_id_key','sessions_tenant_id_id_user_id_key',
    'sessions_tenant_id_id_device_id_key','sessions_tenant_id_id_user_id_device_id_key',
    'device_authorizations_tenant_id_id_key','device_authorizations_tenant_id_id_device_id_key',
    'device_authorizations_tenant_user_code_mac_key','device_tokens_tenant_id_id_key',
    'device_tokens_tenant_device_id_id_key','subscription_credentials_tenant_id_id_key',
    'subscription_credentials_tenant_id_id_sub_user_key','config_bundles_tenant_id_id_key',
    'refresh_tokens_tenant_id_id_key','refresh_tokens_tenant_family_id_id_key',
    'refresh_tokens_tenant_family_session_device_id_key','refresh_tokens_tenant_family_generation_key',
    'refresh_tokens_one_active_family'
  ];
  v_applied bigint;
  v_expected_runner constant text := '509935a161d2716edd4da6feefe87775de36d5dc1b3850bff4db94a942776df7';
  v_expected_release constant text := 'client-auth-00043-v3';
  v_run_id text := current_setting('aegis.client_auth_00043_run_id', true);
  v_down_evidence text := current_setting('aegis.client_auth_00043_down_evidence_sha256', true);
  v_prior_evidence text := current_setting('aegis.client_auth_00043_prior_up_evidence_sha256', true);
  v_runner text := current_setting('aegis.client_auth_00043_runner_sha256', true);
  v_migration text := current_setting('aegis.client_auth_00043_migration_sha256', true);
  v_release text := current_setting('aegis.client_auth_00043_release_id', true);
  v_source_system text := current_setting('aegis.client_auth_00043_source_system_identifier', true);
  v_source_database text := current_setting('aegis.client_auth_00043_source_database_name', true);
  v_source_oid text := current_setting('aegis.client_auth_00043_source_database_oid', true);
  v_stored_catalog_manifest jsonb;
  v_current_catalog_manifest jsonb;
  v_stored_catalog_sha256 text;
  v_current_catalog_sha256 text;
  v_excluded_index_count integer;
  v_excluded_constraint_count integer;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock(420042,1);
  IF current_setting('server_version_num')::integer NOT BETWEEN 180000 AND 189999 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 Down requires PostgreSQL 18';
  END IF;
  IF current_setting('aegis.client_auth_00043_downgrade_approved', true) IS DISTINCT FROM 'approved-v1'
     OR current_setting('aegis.client_auth_00043_writers_stopped', true) IS DISTINCT FROM 'stopped-v1'
     OR current_setting('aegis.client_auth_00043_callers_stopped', true) IS DISTINCT FROM 'stopped-v1'
     OR v_run_id !~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'
     OR v_down_evidence !~ '^[0-9a-f]{64}$'
     OR v_prior_evidence !~ '^[0-9a-f]{64}$'
     OR v_runner IS DISTINCT FROM v_expected_runner
     OR v_migration !~ '^[0-9a-f]{64}$'
     OR v_release IS DISTINCT FROM v_expected_release
     OR v_source_system !~ '^[1-9][0-9]{0,19}$'
     OR v_source_database !~ '^[A-Za-z_][A-Za-z0-9_-]{0,62}$'
     OR v_source_oid !~ '^[1-9][0-9]{0,9}$' THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 Down approvals missing';
  END IF;
  IF (SELECT system_identifier::text FROM pg_catalog.pg_control_system()) IS DISTINCT FROM v_source_system
     OR current_database() IS DISTINCT FROM v_source_database
     OR (SELECT oid::text FROM pg_catalog.pg_database WHERE datname=current_database()) IS DISTINCT FROM v_source_oid THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 Down source database identity mismatch';
  END IF;
  SELECT max(version_id) FILTER (WHERE is_applied) INTO v_applied FROM public.goose_db_version;
  IF v_applied IS DISTINCT FROM 43
     OR EXISTS (SELECT 1 FROM public.goose_db_version WHERE is_applied AND version_id > 43) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 Down requires exact applied version 43';
  END IF;
  IF to_regclass('app.client_auth_00043_meta') IS NULL
     OR (SELECT count(*) FROM app.client_auth_00043_meta) <> 1
     OR NOT EXISTS (
       SELECT 1 FROM app.client_auth_00043_meta
       WHERE singleton
         AND v3_contract_sha256='E8C323762014B5F97D04CF861287026F0D3784D56B2324E1E6B0BC14D833DAC7'
         AND release_id=v_release AND runner_sha256=v_runner AND migration_sha256=v_migration
         AND evidence_sha256=v_prior_evidence
         AND source_system_identifier=v_source_system
         AND source_database_name=v_source_database
         AND source_database_oid=v_source_oid::oid
         AND legacy_revocation_at IS NULL AND first_client_write_at IS NULL
         AND cutover_at IS NULL AND constraints_validated_at IS NULL
     )
     OR EXISTS (
       SELECT 1 FROM app.client_auth_00042_meta
       WHERE legacy_revocation_at IS NOT NULL OR first_client_write_at IS NOT NULL
          OR cutover_at IS NOT NULL OR constraints_validated_at IS NOT NULL
     ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 Down meta/forward-only boundary mismatch';
  END IF;
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='subscription_credentials' AND is_generated='ALWAYS'
  ) OR (SELECT count(*) FROM public.config_bundle_nodes) <> 0
     OR (SELECT count(*) FROM public.refresh_families) <> 0
     OR (SELECT count(*) FROM public.device_proof_nonces) <> 0
     OR (SELECT count(*) FROM public.client_access_token_jtis) <> 0
     OR (SELECT count(*) FROM public.device_issuance_response_replays) <> 0
     OR (SELECT count(*) FROM public.client_refresh_response_replays) <> 0
     OR (SELECT count(*) FROM public.device_issuance_replay_uses) <> 0
     OR (SELECT count(*) FROM public.client_refresh_replay_uses) <> 0 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 Down refuses future/runtime evidence';
  END IF;
  IF EXISTS (
    SELECT 1 FROM unnest(v_tables) AS wanted(name)
    WHERE (SELECT count(*) FROM pg_catalog.pg_class c
           JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
           WHERE n.nspname='public' AND c.relname=wanted.name
             AND c.relkind='r' AND c.relpersistence='p') <> 1
  ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 Down parent relation mismatch';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='public' AND c.relname=ANY(v_names)
  ) OR EXISTS (
    SELECT 1 FROM pg_catalog.pg_constraint con
    JOIN pg_catalog.pg_namespace n ON n.oid=con.connamespace
    WHERE n.nspname='public' AND con.conname=ANY(v_names)
  ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 Down requires runner-removed objects';
  END IF;

  -- Down runs only after the runner has removed every U43 object.  Require an
  -- empty exclusion set, then recompute the same normalized protected surface
  -- immediately before deleting the 00043 control-plane meta row.
  SELECT count(*) INTO v_excluded_index_count
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='public' AND c.relkind='i' AND c.relname=ANY(v_names);
  SELECT count(*) INTO v_excluded_constraint_count
  FROM pg_catalog.pg_constraint con
  JOIN pg_catalog.pg_namespace n ON n.oid=con.connamespace
  WHERE n.nspname='public' AND con.conname=ANY(v_names);
  IF v_excluded_index_count <> 0 OR v_excluded_constraint_count <> 0 THEN
    RAISE EXCEPTION
      'CLIENT-AUTH-00043 Down protected-surface exclusion set not empty: indexes %, constraints %',
      v_excluded_index_count,v_excluded_constraint_count;
  END IF;

  SELECT catalog_manifest INTO STRICT v_stored_catalog_manifest
  FROM app.client_auth_00042_meta
  WHERE singleton;
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
            AND NOT (con.conname=ANY(v_names))
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
            AND NOT (ci.relname=ANY(v_names))
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
  )
  SELECT jsonb_build_object(
           'format','client-auth-00042-catalog-v1',
           'relations',relation_catalog.document
         )
    INTO v_current_catalog_manifest
  FROM relation_catalog;
  v_stored_catalog_sha256 := encode(pg_catalog.sha256(
    pg_catalog.convert_to(v_stored_catalog_manifest::text,'UTF8')),'hex');
  v_current_catalog_sha256 := encode(pg_catalog.sha256(
    pg_catalog.convert_to(v_current_catalog_manifest::text,'UTF8')),'hex');
  IF jsonb_typeof(v_stored_catalog_manifest) IS DISTINCT FROM 'object'
     OR v_stored_catalog_manifest->>'format' IS DISTINCT FROM 'client-auth-00042-catalog-v1'
     OR jsonb_typeof(v_stored_catalog_manifest->'relations') IS DISTINCT FROM 'array'
     OR jsonb_array_length(v_stored_catalog_manifest->'relations') <> 15
     OR jsonb_typeof(v_current_catalog_manifest) IS DISTINCT FROM 'object'
     OR jsonb_typeof(v_current_catalog_manifest->'relations') IS DISTINCT FROM 'array'
     OR jsonb_array_length(v_current_catalog_manifest->'relations') <> 15
     OR v_current_catalog_manifest IS DISTINCT FROM v_stored_catalog_manifest
     OR v_current_catalog_sha256 IS DISTINCT FROM v_stored_catalog_sha256 THEN
    RAISE EXCEPTION
      'CLIENT-AUTH-00043 protected-surface drift before Down meta delete: current %, stored %',
      v_current_catalog_sha256,v_stored_catalog_sha256;
  END IF;

  DELETE FROM app.client_auth_00043_meta
   WHERE singleton
     AND v3_contract_sha256='E8C323762014B5F97D04CF861287026F0D3784D56B2324E1E6B0BC14D833DAC7'
     AND release_id=v_release AND runner_sha256=v_runner
     AND migration_sha256=v_migration AND evidence_sha256=v_prior_evidence;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 Down meta delete mismatch';
  END IF;
  DROP TABLE app.client_auth_00043_meta RESTRICT;
END
$client_auth_00043_finalize_down$;
-- +goose StatementEnd
