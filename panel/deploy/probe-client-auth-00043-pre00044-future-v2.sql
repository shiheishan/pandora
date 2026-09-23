-- CLIENT-AUTH-00043 pre-00044 future-object refusal gate v2.
--
-- Read-only candidate. It is intentionally not wired into the signed V3
-- runner yet: doing so changes the runner/migration/release SHA chain and must
-- be a separately reviewed V4 release. A PASS here is not 00044 approval.
--
-- This file is a stacked verifier, not an abbreviated replacement for the
-- exact 00043 verifier.  The outer advisory-lock reference is deliberately
-- acquired first.  The included verifier acquires and releases one nested
-- reference while this file retains the outer reference, so there is no
-- cooperative-DDL window between exact 00042/00043 equality and the additional
-- pre-00044 absence checks below.  The caller must provide every psql variable
-- required by verify-client-auth-00043-post-goose.sql, including
-- future_gate_contract=client-auth-00043-known-pre00044-v1.
\set ON_ERROR_STOP on
\pset tuples_only on
\pset format unaligned
\set VERBOSITY terse

-- Fail closed until the external, immutable post-00043 catalog baseline for
-- every 00044 classification target is generated on a trusted PG18 fixture,
-- independently reviewed, hash-pinned, and compared here.  The checks below
-- remain useful draft assertions, but counts/names are not a catalog proof and
-- must never emit release-authorizing PASS by themselves.
\echo client_auth_pre00044_future_gate_v2=DENY reason=exact_target_catalog_manifest_not_integrated release_status=NOT_WIRED_NOT_APPROVED
\quit 78

SELECT pg_catalog.pg_advisory_lock(420042,1);
\ir verify-client-auth-00043-post-goose.sql

BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL search_path = pg_catalog;
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '5s';

WITH expected_columns(table_name,column_count) AS (
  VALUES
    ('devices',22),
    ('device_authorizations',21),
    ('sessions',17),
    ('refresh_tokens',16),
    ('api_tokens',13),
    ('device_tokens',10),
    ('subscription_credentials',21),
    ('config_bundles',22),
    ('credential_access_log',10),
    ('config_bundle_nodes',4)
), actual_columns AS (
  SELECT c.relname AS table_name,count(a.attnum)::integer AS column_count
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  JOIN pg_catalog.pg_attribute a
    ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
  WHERE n.nspname='public'
    AND c.relname IN (
      'devices','device_authorizations','sessions','refresh_tokens','api_tokens',
      'device_tokens','subscription_credentials','config_bundles',
      'credential_access_log','config_bundle_nodes'
    )
    AND c.relkind='r' AND c.relpersistence='p'
  GROUP BY c.relname
), expected_fks(table_name,fk_count) AS (
  VALUES
    ('devices',2),
    ('device_authorizations',2),
    ('sessions',2),
    ('refresh_tokens',4),
    ('api_tokens',2),
    ('device_tokens',3),
    ('subscription_credentials',3),
    ('config_bundles',4),
    ('credential_access_log',2),
    ('config_bundle_nodes',0)
), actual_fks AS (
  SELECT c.relname AS table_name,
         count(con.oid) FILTER (WHERE con.contype='f')::integer AS fk_count
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  LEFT JOIN pg_catalog.pg_constraint con ON con.conrelid=c.oid
  WHERE n.nspname='public'
    AND c.relname IN (
      'devices','device_authorizations','sessions','refresh_tokens','api_tokens',
      'device_tokens','subscription_credentials','config_bundles',
      'credential_access_log','config_bundle_nodes'
    )
    AND c.relkind='r' AND c.relpersistence='p'
  GROUP BY c.relname
), expected_user_triggers(table_name,trigger_name) AS (
  VALUES
    ('devices','trg_devices_updated_at'),
    ('subscription_credentials','zz_notify_subscription_credentials'),
    ('credential_access_log','trg_credential_access_log_append_only')
), actual_user_triggers AS (
  SELECT c.relname AS table_name,t.tgname AS trigger_name
  FROM pg_catalog.pg_trigger t
  JOIN pg_catalog.pg_class c ON c.oid=t.tgrelid
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='public'
    AND c.relname IN (
      'devices','device_authorizations','sessions','refresh_tokens','api_tokens',
      'device_tokens','subscription_credentials','config_bundles',
      'credential_access_log','config_bundle_nodes'
    )
    AND NOT t.tgisinternal
), structural_drift AS (
  SELECT 1
  WHERE EXISTS (
    (SELECT * FROM expected_columns EXCEPT SELECT * FROM actual_columns)
    UNION ALL
    (SELECT * FROM actual_columns EXCEPT SELECT * FROM expected_columns)
    UNION ALL
    (SELECT * FROM expected_fks EXCEPT SELECT * FROM actual_fks)
    UNION ALL
    (SELECT * FROM actual_fks EXCEPT SELECT * FROM expected_fks)
    UNION ALL
    (SELECT * FROM expected_user_triggers EXCEPT SELECT * FROM actual_user_triggers)
    UNION ALL
    (SELECT * FROM actual_user_triggers EXCEPT SELECT * FROM expected_user_triggers)
  )
), future_relations AS (
  SELECT c.oid
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE
    (n.nspname='app' AND (
      c.relname IN ('client_auth_00044_meta','client_auth_00044_legacy_evidence')
      OR c.relname ~ '^client_auth_0004[4-7]_'
    ))
    OR
    (n.nspname='public' AND c.relname IN (
      'subscription_credentials_tenant_id_id_sub_user_binding_key',
      'devices_tenant_id_key_fingerprint_key'
    ))
), future_routines AS (
  SELECT p.oid
  FROM pg_catalog.pg_proc p
  JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
  WHERE n.nspname='app' AND (
    p.proname ~ '^client_auth_0004[4-7]_'
    OR
    p.proname IN (
      'guard_api_tokens_pre_client_auth_00044',
      'guard_device_tokens_pre_client_auth_00044',
      'guard_sessions_pre_client_auth_00044',
      'guard_refresh_tokens_pre_client_auth_00044',
      'client_keyring_live_references'
    )
    OR p.proname ~ '^(create|approve|deny|consume|prepare|finalize|read|mark|compromise|revoke|gc)_(client|device)_'
  )
), future_named_constraints AS (
  SELECT con.oid
  FROM pg_catalog.pg_constraint con
  JOIN pg_catalog.pg_namespace n ON n.oid=con.connamespace
  WHERE n.nspname='public' AND con.conname IN (
    'subscription_credentials_tenant_id_id_sub_user_binding_key',
    'devices_tenant_id_key_fingerprint_key'
  )
), generated_evidence AS (
  SELECT a.attrelid
  FROM pg_catalog.pg_attribute a
  JOIN pg_catalog.pg_class c ON c.oid=a.attrelid
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='public'
    AND c.relname IN ('subscription_credentials','config_bundles')
    AND a.attnum>0 AND NOT a.attisdropped
    AND a.attgenerated<>''
), runtime_rows AS (
  SELECT
    (SELECT count(*) FROM public.config_bundle_nodes) +
    (SELECT count(*) FROM public.refresh_families) +
    (SELECT count(*) FROM public.device_proof_nonces) +
    (SELECT count(*) FROM public.client_access_token_jtis) +
    (SELECT count(*) FROM public.device_issuance_response_replays) +
    (SELECT count(*) FROM public.client_refresh_response_replays) +
    (SELECT count(*) FROM public.device_issuance_replay_uses) +
    (SELECT count(*) FROM public.client_refresh_replay_uses) AS row_count
), semantic_backfill AS (
  SELECT 1
  WHERE EXISTS (
    SELECT 1 FROM public.devices
    WHERE public_key_spki IS NOT NULL OR key_fingerprint IS NOT NULL
  ) OR EXISTS (
    SELECT 1 FROM public.device_authorizations
    WHERE key_algorithm IS NOT NULL OR public_key_spki IS NOT NULL
       OR key_fingerprint IS NOT NULL OR user_code_mac IS NOT NULL
       OR denied_at IS NOT NULL OR consumed_at IS NOT NULL
  ) OR EXISTS (
    SELECT 1 FROM public.config_bundles
    WHERE user_id IS NOT NULL OR revoked_at IS NOT NULL
  ) OR EXISTS (
    SELECT 1 FROM public.refresh_tokens
    WHERE authority IS NOT NULL OR family_id IS NOT NULL OR device_id IS NOT NULL
       OR generation IS NOT NULL OR parent_id IS NOT NULL OR key_fingerprint IS NOT NULL
  )
)
SELECT 1 / CASE
  WHEN current_setting('server_version_num')::integer BETWEEN 180000 AND 189999
   AND to_regclass('public.goose_db_version') IS NOT NULL
   AND (SELECT max(version_id) FILTER (WHERE is_applied)
        FROM public.goose_db_version)=43
   AND NOT EXISTS (
     SELECT 1 FROM public.goose_db_version
     WHERE is_applied AND version_id>43
   )
   AND to_regclass('app.client_auth_00042_meta') IS NOT NULL
   AND to_regclass('app.client_auth_00043_meta') IS NOT NULL
   AND (SELECT count(*) FROM app.client_auth_00042_meta)=1
   AND (SELECT count(*) FROM app.client_auth_00043_meta)=1
   AND NOT EXISTS (
     SELECT 1 FROM app.client_auth_00042_meta
     WHERE legacy_revocation_at IS NOT NULL OR first_client_write_at IS NOT NULL
        OR cutover_at IS NOT NULL OR constraints_validated_at IS NOT NULL
   )
   AND NOT EXISTS (
     SELECT 1 FROM app.client_auth_00043_meta
     WHERE legacy_revocation_at IS NOT NULL OR first_client_write_at IS NOT NULL
        OR cutover_at IS NOT NULL OR constraints_validated_at IS NOT NULL
   )
   AND NOT EXISTS (SELECT 1 FROM structural_drift)
   AND NOT EXISTS (SELECT 1 FROM future_relations)
   AND NOT EXISTS (SELECT 1 FROM future_routines)
   AND NOT EXISTS (SELECT 1 FROM future_named_constraints)
   AND NOT EXISTS (SELECT 1 FROM generated_evidence)
   AND NOT EXISTS (SELECT 1 FROM semantic_backfill)
   AND (SELECT row_count FROM runtime_rows)=0
  THEN 1 ELSE 0
END;

ROLLBACK;
SELECT CASE
         WHEN pg_catalog.pg_advisory_unlock(420042,1) THEN 1
         ELSE 1/0
       END AS advisory_unlock_verified;
\echo client_auth_pre00044_future_gate_v2=INTERNAL_DRAFT_CHECKS_COMPLETE exact_00043=stacked release_status=NOT_WIRED_NOT_APPROVED
