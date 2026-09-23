-- CLIENT-AUTH-00043 exact post-Goose verifier.
-- Read-only. Caller must provide all psql variables listed below.
\set ON_ERROR_STOP on
\pset tuples_only on
\pset format unaligned
\set VERBOSITY terse

\if :{?verify_direction}
\else
  \echo client_auth_00043_post_goose=DENY reason=verify_direction_missing
  \quit 78
\endif
\if :{?expected_source_system_identifier}
\else
  \echo client_auth_00043_post_goose=DENY reason=source_system_identifier_missing
  \quit 78
\endif
\if :{?expected_database_name}
\else
  \echo client_auth_00043_post_goose=DENY reason=database_name_missing
  \quit 78
\endif
\if :{?expected_database_oid}
\else
  \echo client_auth_00043_post_goose=DENY reason=database_oid_missing
  \quit 78
\endif
\if :{?expected_release_id}
\else
  \echo client_auth_00043_post_goose=DENY reason=release_id_missing
  \quit 78
\endif
\if :{?expected_runner_sha256}
\else
  \echo client_auth_00043_post_goose=DENY reason=runner_sha256_missing
  \quit 78
\endif
\if :{?expected_migration_sha256}
\else
  \echo client_auth_00043_post_goose=DENY reason=migration_sha256_missing
  \quit 78
\endif
\if :{?expected_run_id}
\else
  \echo client_auth_00043_post_goose=DENY reason=run_id_missing
  \quit 78
\endif
\if :{?expected_evidence_sha256}
\else
  \echo client_auth_00043_post_goose=DENY reason=evidence_sha256_missing
  \quit 78
\endif
\if :{?future_gate_contract}
\else
  \echo client_auth_00043_post_goose=DENY reason=future_gate_contract_missing
  \quit 78
\endif

-- Acquire the session lock before opening the repeatable-read transaction.
-- If we have to wait for the Goose session, the snapshot is therefore taken
-- only after that session has committed and released the cooperative lock.
SELECT pg_catalog.pg_advisory_lock(420042,1);
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL search_path=pg_catalog;
SET LOCAL statement_timeout='30s';
SET LOCAL lock_timeout='5s';

SELECT pg_catalog.set_config('aegis.verify_00043.direction', :'verify_direction', true);
SELECT pg_catalog.set_config('aegis.verify_00043.source_system_identifier', :'expected_source_system_identifier', true);
SELECT pg_catalog.set_config('aegis.verify_00043.database_name', :'expected_database_name', true);
SELECT pg_catalog.set_config('aegis.verify_00043.database_oid', :'expected_database_oid', true);
SELECT pg_catalog.set_config('aegis.verify_00043.release_id', :'expected_release_id', true);
SELECT pg_catalog.set_config('aegis.verify_00043.runner_sha256', :'expected_runner_sha256', true);
SELECT pg_catalog.set_config('aegis.verify_00043.migration_sha256', :'expected_migration_sha256', true);
SELECT pg_catalog.set_config('aegis.verify_00043.run_id', :'expected_run_id', true);
SELECT pg_catalog.set_config('aegis.verify_00043.evidence_sha256', :'expected_evidence_sha256', true);
SELECT pg_catalog.set_config('aegis.verify_00043.future_gate_contract', :'future_gate_contract', true);

-- Source identity, PostgreSQL major and known pre-00044 future gates.
SELECT 1 / CASE
  WHEN current_setting('server_version_num')::integer BETWEEN 180000 AND 189999
   AND current_setting('aegis.verify_00043.direction') IN ('up','down')
   AND current_setting('aegis.verify_00043.source_system_identifier') ~ '^[1-9][0-9]{0,19}$'
   AND (SELECT system_identifier::text FROM pg_catalog.pg_control_system())
       = current_setting('aegis.verify_00043.source_system_identifier')
   AND current_database()=current_setting('aegis.verify_00043.database_name')
   AND (SELECT oid::text FROM pg_catalog.pg_database WHERE datname=current_database())
       = current_setting('aegis.verify_00043.database_oid')
   AND current_setting('aegis.verify_00043.release_id')
       = 'client-auth-00043-v3'
   AND current_setting('aegis.verify_00043.runner_sha256') ~ '^[0-9a-f]{64}$'
   AND current_setting('aegis.verify_00043.migration_sha256') ~ '^[0-9a-f]{64}$'
   AND current_setting('aegis.verify_00043.run_id') ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'
   AND current_setting('aegis.verify_00043.evidence_sha256') ~ '^[0-9a-f]{64}$'
   AND current_setting('aegis.verify_00043.future_gate_contract')
       = 'client-auth-00043-known-pre00044-v1'
   AND NOT EXISTS (
     SELECT 1 FROM pg_catalog.pg_attribute a
     JOIN pg_catalog.pg_class c ON c.oid=a.attrelid
     JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
     WHERE n.nspname='public' AND c.relname='subscription_credentials'
       AND a.attnum>0 AND NOT a.attisdropped AND a.attgenerated<>''
   )
  THEN 1 ELSE 0
END;

-- Recompute the exact 00042 protected surface while normalizing away only the
-- frozen 00043 allowlist. This permits Up verification without treating the
-- 19 expected index/constraint objects as protected-surface drift.
WITH candidate_names(name) AS (
  VALUES
    ('subscriptions_tenant_id_id_user_id_key'),
    ('devices_tenant_id_id_user_id_key'),
    ('sessions_tenant_id_id_key'),
    ('sessions_tenant_id_id_user_id_key'),
    ('sessions_tenant_id_id_device_id_key'),
    ('sessions_tenant_id_id_user_id_device_id_key'),
    ('device_authorizations_tenant_id_id_key'),
    ('device_authorizations_tenant_id_id_device_id_key'),
    ('device_authorizations_tenant_user_code_mac_key'),
    ('device_tokens_tenant_id_id_key'),
    ('device_tokens_tenant_device_id_id_key'),
    ('subscription_credentials_tenant_id_id_key'),
    ('subscription_credentials_tenant_id_id_sub_user_key'),
    ('config_bundles_tenant_id_id_key'),
    ('refresh_tokens_tenant_id_id_key'),
    ('refresh_tokens_tenant_family_id_id_key'),
    ('refresh_tokens_tenant_family_session_device_id_key'),
    ('refresh_tokens_tenant_family_generation_key'),
    ('refresh_tokens_one_active_family')
), target(ordinal,schema_name,relation_name) AS (
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
  SELECT t.ordinal,t.schema_name,t.relation_name,count(c.oid) catalog_rows
  FROM target t
  LEFT JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  LEFT JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
  GROUP BY t.ordinal,t.schema_name,t.relation_name
), relation_catalog AS (
  SELECT jsonb_agg(jsonb_build_object(
    'schema',n.nspname,
    'name',c.relname,
    'kind',c.relkind::text,
    'persistence',c.relpersistence::text,
    'owner',pg_catalog.pg_get_userbyid(c.relowner),
    'rls',c.relrowsecurity,
    'force_rls',c.relforcerowsecurity,
    'replica_identity',c.relreplident::text,
    'comment',pg_catalog.obj_description(c.oid,'pg_class'),
    'columns',coalesce((
      SELECT jsonb_agg(jsonb_build_array(
        a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,
        pg_catalog.pg_get_expr(d.adbin,d.adrelid,false),a.attidentity::text,
        a.attgenerated::text,
        CASE WHEN a.attcollation=0 THEN NULL ELSE cn.nspname||'.'||co.collname END,
        a.attstorage::text,a.attcompression::text,
        pg_catalog.col_description(a.attrelid,a.attnum),
        coalesce((
          SELECT jsonb_agg(jsonb_build_array(
            pg_catalog.pg_get_userbyid(x.grantor),
            CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(x.grantee) END,
            x.privilege_type,x.is_grantable
          ) ORDER BY x.grantee,x.grantor,x.privilege_type)
          FROM pg_catalog.aclexplode(a.attacl) x
        ),'[]'::jsonb)
      ) ORDER BY a.attnum)
      FROM pg_catalog.pg_attribute a
      LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
      LEFT JOIN pg_catalog.pg_collation co ON co.oid=a.attcollation
      LEFT JOIN pg_catalog.pg_namespace cn ON cn.oid=co.collnamespace
      WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
    ),'[]'::jsonb),
    'constraints',coalesce((
      SELECT jsonb_agg(jsonb_build_array(
        con.conname,con.contype::text,
        CASE WHEN con.conkey IS NULL THEN NULL ELSE (
          SELECT jsonb_agg(a.attname ORDER BY k.ord)
          FROM unnest(con.conkey) WITH ORDINALITY k(attnum,ord)
          JOIN pg_catalog.pg_attribute a
            ON a.attrelid=con.conrelid AND a.attnum=k.attnum
        ) END,
        pg_catalog.pg_get_constraintdef(con.oid,false),con.convalidated,
        con.condeferrable,con.condeferred
      ) ORDER BY con.conname)
      FROM pg_catalog.pg_constraint con
      WHERE con.conrelid=c.oid
        AND NOT EXISTS (SELECT 1 FROM candidate_names x WHERE x.name=con.conname)
    ),'[]'::jsonb),
    'indexes',coalesce((
      SELECT jsonb_agg(jsonb_build_array(
        ci.relname,pg_catalog.pg_get_indexdef(i.indexrelid,0,false),
        i.indisunique,i.indisprimary,i.indisvalid,i.indisready,i.indislive,
        i.indisreplident,i.indnullsnotdistinct
      ) ORDER BY ci.relname)
      FROM pg_catalog.pg_index i
      JOIN pg_catalog.pg_class ci ON ci.oid=i.indexrelid
      WHERE i.indrelid=c.oid
        AND NOT EXISTS (SELECT 1 FROM candidate_names x WHERE x.name=ci.relname)
    ),'[]'::jsonb),
    'policies',coalesce((
      SELECT jsonb_agg(jsonb_build_array(
        p.polname,p.polpermissive,p.polcmd::text,
        (SELECT jsonb_agg(pg_catalog.pg_get_userbyid(rid)
           ORDER BY pg_catalog.pg_get_userbyid(rid)) FROM unnest(p.polroles) rid),
        pg_catalog.pg_get_expr(p.polqual,p.polrelid,false),
        pg_catalog.pg_get_expr(p.polwithcheck,p.polrelid,false)
      ) ORDER BY p.polname)
      FROM pg_catalog.pg_policy p WHERE p.polrelid=c.oid
    ),'[]'::jsonb),
    'triggers',coalesce((
      SELECT jsonb_agg(jsonb_build_array(
        tg.tgname,tg.tgenabled::text,tg.tgisinternal,
        pg_catalog.pg_get_triggerdef(tg.oid,false)
      ) ORDER BY tg.tgname)
      FROM pg_catalog.pg_trigger tg WHERE tg.tgrelid=c.oid
    ),'[]'::jsonb),
    'acl',coalesce((
      SELECT jsonb_agg(jsonb_build_array(
        pg_catalog.pg_get_userbyid(x.grantor),
        CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(x.grantee) END,
        x.privilege_type,x.is_grantable
      ) ORDER BY x.grantee,x.grantor,x.privilege_type)
      FROM pg_catalog.aclexplode(c.relacl) x
    ),'[]'::jsonb),
    'sequence',CASE WHEN c.relkind='S' THEN (
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
          WHERE d.classid='pg_catalog.pg_class'::regclass AND d.objid=c.oid
            AND d.refclassid='pg_catalog.pg_class'::regclass AND d.refobjsubid>0
        ),'[]'::jsonb)
      )
      FROM pg_catalog.pg_sequence s WHERE s.seqrelid=c.oid
    ) ELSE NULL END
  ) ORDER BY n.nspname,c.relname) document
  FROM target t
  JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
), current_surface AS (
  SELECT jsonb_build_object('format','client-auth-00042-catalog-v1','relations',document) manifest
  FROM relation_catalog
), stored_surface AS (
  SELECT count(*) meta_rows,max(catalog_manifest::text)::jsonb manifest
  FROM app.client_auth_00042_meta WHERE singleton
)
SELECT 1 / CASE
  WHEN (SELECT count(*) FROM presence)=15
   AND (SELECT count(DISTINCT (schema_name,relation_name)) FROM presence)=15
   AND (SELECT bool_and(catalog_rows=1) FROM presence)
   AND stored_surface.meta_rows=1
   AND stored_surface.manifest->>'format'='client-auth-00042-catalog-v1'
   AND jsonb_typeof(stored_surface.manifest->'relations')='array'
   AND jsonb_array_length(stored_surface.manifest->'relations')=15
   AND current_surface.manifest=stored_surface.manifest
  THEN 1 ELSE 0 END
FROM current_surface CROSS JOIN stored_surface;

-- Runtime and 00042 forward-only watermarks are exact in both directions.
SELECT 1 / CASE WHEN
  (SELECT count(*) FROM app.client_auth_00042_meta)=1
  AND EXISTS (
    SELECT 1 FROM app.client_auth_00042_meta
    WHERE singleton
      AND contract_sha256='4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E'
      AND legacy_revocation_at IS NULL AND first_client_write_at IS NULL
      AND cutover_at IS NULL AND constraints_validated_at IS NULL
  )
  AND (SELECT count(*) FROM public.config_bundle_nodes)=0
  AND (SELECT count(*) FROM public.refresh_families)=0
  AND (SELECT count(*) FROM public.device_proof_nonces)=0
  AND (SELECT count(*) FROM public.client_access_token_jtis)=0
  AND (SELECT count(*) FROM public.device_issuance_response_replays)=0
  AND (SELECT count(*) FROM public.client_refresh_response_replays)=0
  AND (SELECT count(*) FROM public.device_issuance_replay_uses)=0
  AND (SELECT count(*) FROM public.client_refresh_replay_uses)=0
  THEN 1 ELSE 0 END;

-- Exact Up/Down object and meta verification.
DO $client_auth_00043_post_goose$
DECLARE
  v_contract constant text := 'E8C323762014B5F97D04CF861287026F0D3784D56B2324E1E6B0BC14D833DAC7';
  v_direction text := current_setting('aegis.verify_00043.direction');
  v_ids constant text[] := ARRAY[
    'U43-01','U43-02','U43-03','U43-04','U43-05','U43-06','U43-07',
    'U43-08','U43-09','U43-10','U43-11','U43-12','U43-13','U43-14',
    'U43-15','U43-16','U43-17','U43-18','U43-19'
  ];
  v_tables constant text[] := ARRAY[
    'subscriptions','devices','sessions','sessions','sessions','sessions',
    'device_authorizations','device_authorizations','device_authorizations',
    'device_tokens','device_tokens','subscription_credentials',
    'subscription_credentials','config_bundles','refresh_tokens',
    'refresh_tokens','refresh_tokens','refresh_tokens','refresh_tokens'
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
  v_i integer;
  v_parent_count integer;
  v_parent_exact boolean;
  v_class_count integer;
  v_constraint_count integer;
  v_index_oid oid;
  v_constraint_oid oid;
  v_object_exact boolean;
  v_internal_count integer;
  v_internal_total integer;
  v_dependency jsonb;
  v_candidates jsonb := '[]'::jsonb;
  v_live_manifest jsonb;
  v_meta_manifest jsonb;
  v_goose bigint;
BEGIN
  SELECT max(version_id) FILTER (WHERE is_applied) INTO v_goose
  FROM public.goose_db_version;
  IF (v_direction='up' AND (
        v_goose IS DISTINCT FROM 43 OR
        EXISTS (SELECT 1 FROM public.goose_db_version WHERE is_applied AND version_id>43)
      ))
     OR (v_direction='down' AND (
        v_goose IS DISTINCT FROM 42 OR
        EXISTS (SELECT 1 FROM public.goose_db_version WHERE is_applied AND version_id>42)
      )) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00043 post-Goose waterline mismatch';
  END IF;

  FOR v_i IN 1..19 LOOP
    SELECT count(*),coalesce(bool_and(
      c.relkind='r' AND c.relpersistence='p'
    ),false)
    INTO v_parent_count,v_parent_exact
    FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='public' AND c.relname=v_tables[v_i];
    IF v_parent_count<>1 OR NOT v_parent_exact THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00043 parent mismatch: %',v_tables[v_i];
    END IF;

    SELECT count(*) INTO v_class_count
    FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='public' AND c.relname=v_names[v_i];
    SELECT count(*) INTO v_constraint_count
    FROM pg_catalog.pg_constraint con
    JOIN pg_catalog.pg_namespace n ON n.oid=con.connamespace
    WHERE n.nspname='public' AND con.conname=v_names[v_i];

    IF v_direction='down' THEN
      IF v_class_count<>0 OR v_constraint_count<>0 THEN
        RAISE EXCEPTION 'CLIENT-AUTH-00043 Down name remains: %',v_names[v_i];
      END IF;
      CONTINUE;
    END IF;

    SELECT ic.oid,con.oid,
      ic.relkind='i' AND ic.relpersistence='p' AND ic.relowner=tc.relowner
      AND ic.reltablespace=0 AND ic.reloptions IS NULL
      AND am.amname='btree' AND ix.indisunique AND ix.indimmediate
      AND ix.indisvalid AND ix.indisready AND ix.indislive
      AND NOT ix.indnullsnotdistinct AND ix.indexprs IS NULL
      AND pg_catalog.pg_get_indexdef(ic.oid)=v_defs[v_i]
      AND (
        (v_i=19 AND ix.indpred IS NOT NULL
          AND pg_catalog.pg_get_expr(ix.indpred,ix.indrelid,false)
              ='(status = ''active''::text)' AND con.oid IS NULL)
        OR
        (v_i<19 AND ix.indpred IS NULL AND con.contype='u'
          AND con.convalidated AND NOT con.condeferrable AND NOT con.condeferred
          AND con.conindid=ic.oid)
      )
    INTO STRICT v_index_oid,v_constraint_oid,v_object_exact
    FROM pg_catalog.pg_class tc
    JOIN pg_catalog.pg_namespace n ON n.oid=tc.relnamespace
    JOIN pg_catalog.pg_class ic ON ic.relnamespace=n.oid AND ic.relname=v_names[v_i]
    JOIN pg_catalog.pg_index ix ON ix.indexrelid=ic.oid
    JOIN pg_catalog.pg_am am ON am.oid=ic.relam
    LEFT JOIN pg_catalog.pg_constraint con
      ON con.connamespace=n.oid AND con.conname=v_names[v_i]
       AND con.conrelid=tc.oid AND con.conindid=ic.oid
    WHERE n.nspname='public' AND tc.relname=v_tables[v_i];
    IF v_class_count<>1 OR v_constraint_count<>(CASE WHEN v_i<19 THEN 1 ELSE 0 END)
       OR NOT v_object_exact THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00043 Up object mismatch: %',v_names[v_i];
    END IF;

    SELECT count(*) FILTER (WHERE
             d.classid='pg_catalog.pg_class'::regclass AND d.objid=v_index_oid
             AND d.objsubid=0
             AND d.refclassid='pg_catalog.pg_constraint'::regclass
             AND d.refobjid=v_constraint_oid AND d.refobjsubid=0 AND d.deptype='i'),
           count(*) FILTER (WHERE d.deptype='i'),
           coalesce(jsonb_agg(jsonb_build_array(
             d.classid::regclass::text,d.objid,d.objsubid,
             d.refclassid::regclass::text,d.refobjid,d.refobjsubid,d.deptype
           ) ORDER BY d.classid,d.objid,d.objsubid,d.refclassid,d.refobjid,
                      d.refobjsubid,d.deptype),'[]'::jsonb)
    INTO v_internal_count,v_internal_total,v_dependency
    FROM pg_catalog.pg_depend d
    WHERE d.classid='pg_catalog.pg_class'::regclass AND d.objid=v_index_oid;
    IF (v_i<19 AND (v_internal_count<>1 OR v_internal_total<>1))
       OR (v_i=19 AND v_internal_total<>0) THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00043 dependency mismatch: %',v_names[v_i];
    END IF;

    v_candidates := v_candidates || jsonb_build_array(jsonb_build_object(
      'id',v_ids[v_i],'table',v_tables[v_i],'name',v_names[v_i],
      'indexdef',v_defs[v_i],'attached',v_i<19
    ));
  END LOOP;

  IF v_direction='up' THEN
    v_live_manifest := v_candidates;
    IF to_regclass('app.client_auth_00043_meta') IS NULL
       OR (SELECT count(*) FROM app.client_auth_00043_meta)<>1 THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00043 Up meta cardinality mismatch';
    END IF;
    SELECT catalog_manifest INTO STRICT v_meta_manifest
    FROM app.client_auth_00043_meta
    WHERE singleton
      AND v3_contract_sha256=v_contract
      AND release_id=current_setting('aegis.verify_00043.release_id')
      AND runner_sha256=current_setting('aegis.verify_00043.runner_sha256')
      AND migration_sha256=current_setting('aegis.verify_00043.migration_sha256')
      AND run_id=current_setting('aegis.verify_00043.run_id')
      AND evidence_sha256=current_setting('aegis.verify_00043.evidence_sha256')
      AND source_system_identifier=current_setting('aegis.verify_00043.source_system_identifier')
      AND source_database_name=current_setting('aegis.verify_00043.database_name')
      AND source_database_oid::text=current_setting('aegis.verify_00043.database_oid')
      AND legacy_revocation_at IS NULL AND first_client_write_at IS NULL
      AND cutover_at IS NULL AND constraints_validated_at IS NULL;
    IF v_meta_manifest IS DISTINCT FROM v_live_manifest THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00043 Up meta catalog identity mismatch';
    END IF;
  ELSE
    IF to_regclass('app.client_auth_00043_meta') IS NOT NULL THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00043 Down meta still present';
    END IF;
  END IF;
END
$client_auth_00043_post_goose$;

SELECT 'client_auth_00043_post_goose=PASS direction='
       ||current_setting('aegis.verify_00043.direction');
ROLLBACK;
SELECT CASE
         WHEN pg_catalog.pg_advisory_unlock(420042,1) THEN 1
         ELSE 1/0
       END AS advisory_unlock_verified;
