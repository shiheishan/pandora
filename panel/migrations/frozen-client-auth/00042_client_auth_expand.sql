-- CLIENT-AUTH-00042: reversible expand-only schema for the dedicated client API.
--
-- Frozen implementation manifest:
--   docs/潘多拉面板-CLIENT-AUTH-00042实现清单-20260730.md
--   SHA-256 F98D2D2E52BD73AD2D814505C9931B44B8D680484D92B8D8E76B2CA99B2E5CE5
--
-- This migration intentionally contains no backfill, FK to existing business
-- tables, runtime function/trigger, runtime grant, or cutover state.

-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';
SET LOCAL createrole_self_grant = '';

-- +goose StatementBegin
DO $client_auth_00042_up$
DECLARE
  v_names constant text[] := ARRAY[
    'aegis_client_auth_owner',
    'aegis_client_auth_gc_owner',
    'aegis_client_auth_gc',
    'aegis_client_keyring_preflight_owner'
  ];
  v_marker constant text :=
    'pandora:aegispanel:migration=00042;created=v1;contract=4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E';
  v_reserved_prefix constant text := 'pandora:aegispanel:migration=00042;';
  v_contract constant text := '4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E';
  v_name text;
  v_oid oid;
  v_comment text;
  v_mask smallint := 0;
  v_role_oids oid[] := ARRAY[]::oid[];
  v_role_oid_manifest jsonb;
  v_index integer;
  v_applied bigint;
  v_relation text;
BEGIN
  IF current_setting('server_version_num')::integer / 10000 <> 18 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 requires PostgreSQL 18';
  END IF;

  IF current_setting('aegis.client_auth_00042_upgrade_approved', true)
       IS DISTINCT FROM 'approved-v1'
     OR current_setting('aegis.client_auth_writers_stopped', true)
       IS DISTINCT FROM 'stopped-v1' THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 upgrade approval/writer-stop GUC missing';
  END IF;

  PERFORM pg_catalog.pg_advisory_xact_lock(420042, 1);

  -- Freeze cluster-global role catalogs before the first provenance check and
  -- retain the barrier through role creation and the final postconditions.
  LOCK TABLE pg_catalog.pg_authid IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE pg_catalog.pg_auth_members IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE pg_catalog.pg_db_role_setting IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE pg_catalog.pg_shdepend IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE pg_catalog.pg_shdescription IN SHARE ROW EXCLUSIVE MODE;

  IF to_regclass('public.goose_db_version') IS NULL THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 requires goose_db_version';
  END IF;
  SELECT max(version_id) FILTER (WHERE is_applied) INTO v_applied
  FROM public.goose_db_version;
  IF v_applied IS DISTINCT FROM 41
     OR EXISTS (
       SELECT 1 FROM public.goose_db_version
       WHERE is_applied AND version_id > 41
     ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 requires exact applied version 41';
  END IF;

  FOREACH v_relation IN ARRAY ARRAY[
    'app.client_auth_00042_meta',
    'public.config_bundle_nodes',
    'public.refresh_families',
    'public.client_refresh_response_replays',
    'public.device_proof_nonces',
    'public.client_access_token_jtis',
    'public.device_issuance_response_replays',
    'public.device_issuance_replay_uses',
    'public.client_refresh_replay_uses',
    'public.device_proof_nonces_id_seq',
    'public.client_access_token_jtis_id_seq'
  ] LOOP
    IF to_regclass(v_relation) IS NOT NULL THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 reserved relation already exists: %', v_relation;
    END IF;
  END LOOP;

  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema = 'public'
      AND (
        (table_name = 'devices' AND column_name IN ('public_key_spki','key_fingerprint'))
        OR (table_name = 'device_authorizations' AND column_name IN (
          'key_algorithm','public_key_spki','key_fingerprint','user_code_mac',
          'denied_at','consumed_at','poll_count'))
        OR (table_name = 'config_bundles' AND column_name IN ('user_id','revoked_at'))
        OR (table_name = 'refresh_tokens' AND column_name IN (
          'authority','family_id','device_id','generation','parent_id','key_fingerprint'))
      )
  ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 partial existing-table expansion detected';
  END IF;

  FOREACH v_relation IN ARRAY ARRAY[
    'public.devices',
    'public.device_authorizations',
    'public.config_bundles',
    'public.refresh_tokens'
  ] LOOP
    IF to_regclass(v_relation) IS NULL THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 baseline relation missing: %', v_relation;
    END IF;
  END LOOP;

  FOR v_index IN 1..array_length(v_names, 1) LOOP
    v_name := v_names[v_index];
    SELECT oid, pg_catalog.shobj_description(oid, 'pg_authid')
      INTO v_oid, v_comment
      FROM pg_catalog.pg_authid
     WHERE rolname = v_name;

    IF v_oid IS NULL THEN
      EXECUTE format(
        'CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE ' ||
        'NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT -1 PASSWORD NULL',
        v_name
      );
      EXECUTE format('COMMENT ON ROLE %I IS %L', v_name, v_marker);
      v_mask := (v_mask::integer | (1 << (v_index - 1)))::smallint;
      SELECT oid, pg_catalog.shobj_description(oid, 'pg_authid')
        INTO STRICT v_oid, v_comment
        FROM pg_catalog.pg_authid
       WHERE rolname = v_name;
    ELSE
      IF v_comment LIKE v_reserved_prefix || '%' THEN
        RAISE EXCEPTION 'CLIENT-AUTH-00042 reserved role provenance already exists: %', v_name;
      END IF;
    END IF;

    IF NOT EXISTS (
      SELECT 1
      FROM pg_catalog.pg_authid
      WHERE oid = v_oid
        AND NOT rolcanlogin
        AND NOT rolsuper
        AND NOT rolcreatedb
        AND NOT rolcreaterole
        AND NOT rolinherit
        AND NOT rolreplication
        AND NOT rolbypassrls
        AND rolconnlimit = -1
        AND rolvaliduntil IS NULL
        AND rolpassword IS NULL
    ) OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_auth_members
      WHERE roleid = v_oid OR member = v_oid
    ) OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_db_role_setting WHERE setrole = v_oid
    ) OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_shdepend
      WHERE refclassid = 'pg_catalog.pg_authid'::regclass
        AND refobjid = v_oid
    ) THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 unsafe or dependent role: %', v_name;
    END IF;
    v_role_oids := array_append(v_role_oids, v_oid);
  END LOOP;

  v_role_oid_manifest := jsonb_build_object(
    v_names[1], v_role_oids[1]::bigint,
    v_names[2], v_role_oids[2]::bigint,
    v_names[3], v_role_oids[3]::bigint,
    v_names[4], v_role_oids[4]::bigint
  );

  EXECUTE $ddl$
    CREATE TABLE app.client_auth_00042_meta (
      singleton boolean CONSTRAINT ca42_singleton_nn NOT NULL,
      contract_sha256 text CONSTRAINT ca42_contract_sha256_nn NOT NULL,
      expand_at timestamptz(6) CONSTRAINT ca42_expand_at_nn NOT NULL
        DEFAULT transaction_timestamp(),
      legacy_revocation_at timestamptz(6),
      first_client_write_at timestamptz(6),
      cutover_at timestamptz(6),
      constraints_validated_at timestamptz(6),
      role_creation_mask smallint CONSTRAINT ca42_role_creation_mask_nn NOT NULL,
      role_oid_manifest jsonb CONSTRAINT ca42_role_oid_manifest_nn NOT NULL,
      catalog_manifest jsonb CONSTRAINT ca42_catalog_manifest_nn NOT NULL
        DEFAULT '{}'::jsonb,
      CONSTRAINT client_auth_00042_meta_pkey PRIMARY KEY (singleton),
      CONSTRAINT client_auth_00042_meta_singleton_check CHECK (singleton),
      CONSTRAINT client_auth_00042_meta_contract_sha_check
        CHECK ((contract_sha256 COLLATE pg_catalog."C") ~ '^[0-9A-F]{64}$'),
      CONSTRAINT client_auth_00042_meta_role_creation_mask_check
        CHECK (role_creation_mask BETWEEN 0 AND 15)
    )
  $ddl$;

  EXECUTE
    'INSERT INTO app.client_auth_00042_meta ' ||
    '(singleton, contract_sha256, role_creation_mask, role_oid_manifest) ' ||
    'VALUES (true, $1, $2, $3)'
    USING v_contract, v_mask, v_role_oid_manifest;

  EXECUTE
    'REVOKE ALL ON TABLE app.client_auth_00042_meta FROM PUBLIC, aegis_app, ' ||
    'aegis_client_auth_owner, aegis_client_auth_gc_owner, aegis_client_auth_gc, ' ||
    'aegis_client_keyring_preflight_owner';
END
$client_auth_00042_up$;
-- +goose StatementEnd

ALTER TABLE public.devices
  ADD COLUMN public_key_spki bytea,
  ADD COLUMN key_fingerprint bytea,
  ADD CONSTRAINT devices_key_fingerprint_len_00042_check
    CHECK (key_fingerprint IS NULL OR octet_length(key_fingerprint) = 32) NOT VALID;

ALTER TABLE public.device_authorizations
  ADD COLUMN key_algorithm text,
  ADD COLUMN public_key_spki bytea,
  ADD COLUMN key_fingerprint bytea,
  ADD COLUMN user_code_mac bytea,
  ADD COLUMN denied_at timestamptz(6),
  ADD COLUMN consumed_at timestamptz(6),
  ADD COLUMN poll_count integer DEFAULT 0,
  ADD CONSTRAINT device_auth_public_key_spki_len_00042_check
    CHECK (public_key_spki IS NULL OR octet_length(public_key_spki) > 0) NOT VALID,
  ADD CONSTRAINT device_auth_key_fingerprint_len_00042_check
    CHECK (key_fingerprint IS NULL OR octet_length(key_fingerprint) = 32) NOT VALID,
  ADD CONSTRAINT device_auth_user_code_mac_len_00042_check
    CHECK (user_code_mac IS NULL OR octet_length(user_code_mac) = 32) NOT VALID,
  ADD CONSTRAINT device_auth_poll_count_00042_check
    CHECK (poll_count IS NULL OR poll_count >= 0) NOT VALID;

ALTER TABLE public.config_bundles
  ADD COLUMN user_id uuid,
  ADD COLUMN revoked_at timestamptz(6);

ALTER TABLE public.refresh_tokens
  ADD COLUMN authority text,
  ADD COLUMN family_id uuid,
  ADD COLUMN device_id uuid,
  ADD COLUMN generation integer,
  ADD COLUMN parent_id uuid,
  ADD COLUMN key_fingerprint bytea,
  ADD CONSTRAINT refresh_tokens_key_fp_len_00042_check
    CHECK (key_fingerprint IS NULL OR octet_length(key_fingerprint) = 32) NOT VALID,
  ADD CONSTRAINT refresh_tokens_generation_00042_check
    CHECK (generation IS NULL OR generation >= 0) NOT VALID;

CREATE TABLE public.config_bundle_nodes (
  tenant_id uuid CONSTRAINT cbn_tenant_id_nn NOT NULL,
  bundle_id uuid CONSTRAINT cbn_bundle_id_nn NOT NULL,
  node_id uuid CONSTRAINT cbn_node_id_nn NOT NULL,
  created_at timestamptz(6) CONSTRAINT cbn_created_at_nn NOT NULL
    DEFAULT transaction_timestamp(),
  CONSTRAINT config_bundle_nodes_pkey PRIMARY KEY (tenant_id, bundle_id, node_id)
);

CREATE TABLE public.refresh_families (
  tenant_id uuid CONSTRAINT rf_tenant_id_nn NOT NULL,
  id uuid CONSTRAINT rf_id_nn NOT NULL,
  session_id uuid CONSTRAINT rf_session_id_nn NOT NULL,
  user_id uuid CONSTRAINT rf_user_id_nn NOT NULL,
  device_id uuid CONSTRAINT rf_device_id_nn NOT NULL,
  key_fingerprint bytea CONSTRAINT rf_key_fingerprint_nn NOT NULL,
  status text CONSTRAINT rf_status_nn NOT NULL,
  absolute_expires_at timestamptz(6) CONSTRAINT rf_absolute_expires_at_nn NOT NULL,
  idle_expires_at timestamptz(6) CONSTRAINT rf_idle_expires_at_nn NOT NULL,
  last_used_at timestamptz(6),
  revoked_at timestamptz(6),
  compromised_at timestamptz(6),
  created_at timestamptz(6) CONSTRAINT rf_created_at_nn NOT NULL
    DEFAULT transaction_timestamp(),
  CONSTRAINT refresh_families_pkey PRIMARY KEY (tenant_id, id),
  CONSTRAINT refresh_families_identity_key
    UNIQUE (tenant_id, id, session_id, user_id, device_id, key_fingerprint),
  CONSTRAINT refresh_families_session_device_key
    UNIQUE (tenant_id, id, session_id, device_id),
  CONSTRAINT refresh_families_key_fingerprint_check
    CHECK (octet_length(key_fingerprint) = 32),
  CONSTRAINT refresh_families_status_check
    CHECK (status IN ('active','revoked','compromised','expired')),
  CONSTRAINT refresh_families_absolute_expiry_check
    CHECK (absolute_expires_at > created_at),
  CONSTRAINT refresh_families_idle_expiry_check
    CHECK (idle_expires_at <= absolute_expires_at),
  CONSTRAINT refresh_families_revoked_state_check
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL)),
  CONSTRAINT refresh_families_compromised_state_check
    CHECK ((status = 'compromised') = (compromised_at IS NOT NULL))
);

CREATE TABLE public.device_proof_nonces (
  id bigint CONSTRAINT dpn_id_nn NOT NULL
    GENERATED ALWAYS AS IDENTITY (
      SEQUENCE NAME public.device_proof_nonces_id_seq
      START WITH 1 INCREMENT BY 1 MINVALUE 1 MAXVALUE 9223372036854775807
      CACHE 1 NO CYCLE
    ),
  tenant_id uuid CONSTRAINT dpn_tenant_id_nn NOT NULL,
  key_fingerprint bytea CONSTRAINT dpn_key_fingerprint_nn NOT NULL,
  nonce_hash bytea CONSTRAINT dpn_nonce_hash_nn NOT NULL,
  credential_kind text CONSTRAINT dpn_credential_kind_nn NOT NULL,
  request_hash bytea CONSTRAINT dpn_request_hash_nn NOT NULL,
  occurred_at timestamptz(6) CONSTRAINT dpn_occurred_at_nn NOT NULL,
  expires_at timestamptz(6) CONSTRAINT dpn_expires_at_nn NOT NULL,
  CONSTRAINT device_proof_nonces_pkey PRIMARY KEY (id),
  CONSTRAINT device_proof_nonces_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT device_proof_nonces_key_nonce_key
    UNIQUE (tenant_id, key_fingerprint, nonce_hash),
  CONSTRAINT device_proof_nonces_key_fingerprint_check
    CHECK (octet_length(key_fingerprint) = 32),
  CONSTRAINT device_proof_nonces_nonce_hash_check CHECK (octet_length(nonce_hash) = 32),
  CONSTRAINT device_proof_nonces_credential_kind_check
    CHECK (credential_kind IN ('none','device_code','access_token','refresh_token')),
  CONSTRAINT device_proof_nonces_request_hash_check CHECK (octet_length(request_hash) = 32),
  CONSTRAINT device_proof_nonces_expiry_check
    CHECK (expires_at = occurred_at + interval '15 minutes')
);

CREATE TABLE public.client_access_token_jtis (
  id bigint CONSTRAINT catj_id_nn NOT NULL
    GENERATED ALWAYS AS IDENTITY (
      SEQUENCE NAME public.client_access_token_jtis_id_seq
      START WITH 1 INCREMENT BY 1 MINVALUE 1 MAXVALUE 9223372036854775807
      CACHE 1 NO CYCLE
    ),
  tenant_id uuid CONSTRAINT catj_tenant_id_nn NOT NULL,
  issuer text CONSTRAINT catj_issuer_nn NOT NULL,
  jti_hash bytea CONSTRAINT catj_jti_hash_nn NOT NULL,
  session_id uuid CONSTRAINT catj_session_id_nn NOT NULL,
  device_id uuid CONSTRAINT catj_device_id_nn NOT NULL,
  signing_kid text CONSTRAINT catj_signing_kid_nn NOT NULL,
  signing_key_version integer CONSTRAINT catj_signing_key_version_nn NOT NULL,
  issued_at timestamptz(6) CONSTRAINT catj_issued_at_nn NOT NULL,
  expires_at timestamptz(6) CONSTRAINT catj_expires_at_nn NOT NULL,
  revoked_at timestamptz(6),
  CONSTRAINT client_access_token_jtis_pkey PRIMARY KEY (id),
  CONSTRAINT client_access_jtis_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT client_access_jtis_tenant_issuer_jti_key UNIQUE (tenant_id, issuer, jti_hash),
  CONSTRAINT client_access_jtis_issuer_check CHECK (
    octet_length(issuer) BETWEEN 9 AND 261
    AND (issuer COLLATE pg_catalog."C") ~
      '^https://[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$'
  ),
  CONSTRAINT client_access_jtis_jti_hash_check CHECK (octet_length(jti_hash) = 32),
  CONSTRAINT client_access_jtis_signing_kid_check CHECK (
    (signing_kid COLLATE pg_catalog."C") ~ '^[A-Za-z][A-Za-z0-9_-]{0,63}$'
  ),
  CONSTRAINT client_access_jtis_signing_version_check
    CHECK (signing_key_version BETWEEN 1 AND 2147483647),
  CONSTRAINT client_access_jtis_expiry_check CHECK (
    expires_at > issued_at AND expires_at <= issued_at + interval '10 minutes'
  )
);

CREATE TABLE public.device_issuance_response_replays (
  id uuid CONSTRAINT dirr_id_nn NOT NULL,
  tenant_id uuid CONSTRAINT dirr_tenant_id_nn NOT NULL,
  authorization_id uuid CONSTRAINT dirr_authorization_id_nn NOT NULL,
  device_id uuid CONSTRAINT dirr_device_id_nn NOT NULL,
  session_id uuid CONSTRAINT dirr_session_id_nn NOT NULL,
  refresh_family_id uuid CONSTRAINT dirr_refresh_family_id_nn NOT NULL,
  stable_request_hash bytea CONSTRAINT dirr_stable_request_hash_nn NOT NULL,
  key_fingerprint bytea CONSTRAINT dirr_key_fingerprint_nn NOT NULL,
  state text CONSTRAINT dirr_state_nn NOT NULL,
  prepared_xid xid8 CONSTRAINT dirr_prepared_xid_nn NOT NULL,
  response_envelope_ciphertext bytea,
  response_body_hash bytea,
  envelope_kid text,
  envelope_key_version integer,
  aead_nonce bytea,
  aead_tag bytea,
  created_at timestamptz(6) CONSTRAINT dirr_created_at_nn NOT NULL,
  expires_at timestamptz(6) CONSTRAINT dirr_expires_at_nn NOT NULL,
  replay_count bigint CONSTRAINT dirr_replay_count_nn NOT NULL DEFAULT 0,
  replayed_at timestamptz(6),
  CONSTRAINT device_issuance_response_replays_pkey PRIMARY KEY (id),
  CONSTRAINT device_issuance_replays_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT device_issuance_replays_stable_request_key
    UNIQUE (tenant_id, authorization_id, device_id, key_fingerprint, stable_request_hash),
  CONSTRAINT device_issuance_replays_request_hash_check
    CHECK (octet_length(stable_request_hash) = 32),
  CONSTRAINT device_issuance_replays_key_fingerprint_check
    CHECK (octet_length(key_fingerprint) = 32),
  CONSTRAINT device_issuance_replays_state_check CHECK (state IN ('prepared','finalized')),
  CONSTRAINT device_issuance_replays_envelope_group_check CHECK (
    (state = 'prepared'
      AND response_envelope_ciphertext IS NULL
      AND response_body_hash IS NULL
      AND envelope_kid IS NULL
      AND envelope_key_version IS NULL
      AND aead_nonce IS NULL
      AND aead_tag IS NULL)
    OR
    (state = 'finalized'
      AND response_envelope_ciphertext IS NOT NULL
      AND response_body_hash IS NOT NULL
      AND octet_length(response_body_hash) = 32
      AND envelope_kid IS NOT NULL
      AND (envelope_kid COLLATE pg_catalog."C") ~ '^[A-Za-z][A-Za-z0-9_-]{0,63}$'
      AND envelope_key_version IS NOT NULL
      AND envelope_key_version BETWEEN 1 AND 2147483647
      AND aead_nonce IS NOT NULL
      AND octet_length(aead_nonce) = 12
      AND aead_tag IS NOT NULL
      AND octet_length(aead_tag) = 16)
  ),
  CONSTRAINT device_issuance_replays_expiry_check
    CHECK (expires_at = created_at + interval '30 seconds'),
  CONSTRAINT device_issuance_replays_count_check CHECK (replay_count >= 0)
);

CREATE TABLE public.client_refresh_response_replays (
  id uuid CONSTRAINT crr_id_nn NOT NULL,
  tenant_id uuid CONSTRAINT crr_tenant_id_nn NOT NULL,
  family_id uuid CONSTRAINT crr_family_id_nn NOT NULL,
  old_token_id uuid CONSTRAINT crr_old_token_id_nn NOT NULL,
  new_token_id uuid CONSTRAINT crr_new_token_id_nn NOT NULL,
  session_id uuid CONSTRAINT crr_session_id_nn NOT NULL,
  device_id uuid CONSTRAINT crr_device_id_nn NOT NULL,
  refresh_request_id uuid CONSTRAINT crr_refresh_request_id_nn NOT NULL,
  body_hash bytea CONSTRAINT crr_body_hash_nn NOT NULL,
  replay_request_hash bytea CONSTRAINT crr_replay_request_hash_nn NOT NULL,
  key_fingerprint bytea CONSTRAINT crr_key_fingerprint_nn NOT NULL,
  state text CONSTRAINT crr_state_nn NOT NULL,
  prepared_xid xid8 CONSTRAINT crr_prepared_xid_nn NOT NULL,
  response_envelope_ciphertext bytea,
  response_body_hash bytea,
  envelope_kid text,
  envelope_key_version integer,
  aead_nonce bytea,
  aead_tag bytea,
  created_at timestamptz(6) CONSTRAINT crr_created_at_nn NOT NULL,
  replay_started_at timestamptz(6) CONSTRAINT crr_replay_started_at_nn NOT NULL,
  expires_at timestamptz(6) CONSTRAINT crr_expires_at_nn NOT NULL,
  replay_count bigint CONSTRAINT crr_replay_count_nn NOT NULL DEFAULT 0,
  replayed_at timestamptz(6),
  CONSTRAINT client_refresh_response_replays_pkey PRIMARY KEY (id),
  CONSTRAINT client_refresh_replays_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT client_refresh_replays_old_token_request_key
    UNIQUE (tenant_id, old_token_id, refresh_request_id),
  CONSTRAINT client_refresh_replays_body_hash_check CHECK (octet_length(body_hash) = 32),
  CONSTRAINT client_refresh_replays_request_hash_check
    CHECK (octet_length(replay_request_hash) = 32),
  CONSTRAINT client_refresh_replays_key_fingerprint_check
    CHECK (octet_length(key_fingerprint) = 32),
  CONSTRAINT client_refresh_replays_state_check CHECK (state IN ('prepared','finalized')),
  CONSTRAINT client_refresh_replays_envelope_group_check CHECK (
    (state = 'prepared'
      AND response_envelope_ciphertext IS NULL
      AND response_body_hash IS NULL
      AND envelope_kid IS NULL
      AND envelope_key_version IS NULL
      AND aead_nonce IS NULL
      AND aead_tag IS NULL)
    OR
    (state = 'finalized'
      AND response_envelope_ciphertext IS NOT NULL
      AND response_body_hash IS NOT NULL
      AND octet_length(response_body_hash) = 32
      AND envelope_kid IS NOT NULL
      AND (envelope_kid COLLATE pg_catalog."C") ~ '^[A-Za-z][A-Za-z0-9_-]{0,63}$'
      AND envelope_key_version IS NOT NULL
      AND envelope_key_version BETWEEN 1 AND 2147483647
      AND aead_nonce IS NOT NULL
      AND octet_length(aead_nonce) = 12
      AND aead_tag IS NOT NULL
      AND octet_length(aead_tag) = 16)
  ),
  CONSTRAINT client_refresh_replays_created_time_check CHECK (created_at = replay_started_at),
  CONSTRAINT client_refresh_replays_expiry_check
    CHECK (expires_at = replay_started_at + interval '30 seconds'),
  CONSTRAINT client_refresh_replays_count_check CHECK (replay_count >= 0)
);

CREATE TABLE public.device_issuance_replay_uses (
  tenant_id uuid CONSTRAINT diu_tenant_id_nn NOT NULL,
  replay_id uuid CONSTRAINT diu_replay_id_nn NOT NULL,
  nonce_id bigint CONSTRAINT diu_nonce_id_nn NOT NULL,
  occurred_at timestamptz(6) CONSTRAINT diu_occurred_at_nn NOT NULL
    DEFAULT transaction_timestamp(),
  CONSTRAINT device_issuance_replay_uses_pkey PRIMARY KEY (tenant_id, replay_id, nonce_id),
  CONSTRAINT device_issuance_replay_uses_nonce_key UNIQUE (tenant_id, nonce_id)
);

CREATE TABLE public.client_refresh_replay_uses (
  tenant_id uuid CONSTRAINT cru_tenant_id_nn NOT NULL,
  replay_id uuid CONSTRAINT cru_replay_id_nn NOT NULL,
  nonce_id bigint CONSTRAINT cru_nonce_id_nn NOT NULL,
  occurred_at timestamptz(6) CONSTRAINT cru_occurred_at_nn NOT NULL
    DEFAULT transaction_timestamp(),
  CONSTRAINT client_refresh_replay_uses_pkey PRIMARY KEY (tenant_id, replay_id, nonce_id),
  CONSTRAINT client_refresh_replay_uses_nonce_key UNIQUE (tenant_id, nonce_id)
);

ALTER TABLE public.config_bundle_nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.config_bundle_nodes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.config_bundle_nodes
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());
ALTER TABLE public.refresh_families ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.refresh_families FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.refresh_families
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());
ALTER TABLE public.device_proof_nonces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.device_proof_nonces FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.device_proof_nonces
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());
ALTER TABLE public.client_access_token_jtis ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.client_access_token_jtis FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.client_access_token_jtis
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());
ALTER TABLE public.device_issuance_response_replays ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.device_issuance_response_replays FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.device_issuance_response_replays
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());
ALTER TABLE public.client_refresh_response_replays ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.client_refresh_response_replays FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.client_refresh_response_replays
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());
ALTER TABLE public.device_issuance_replay_uses ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.device_issuance_replay_uses FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.device_issuance_replay_uses
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());
ALTER TABLE public.client_refresh_replay_uses ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.client_refresh_replay_uses FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.client_refresh_replay_uses
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());

REVOKE ALL ON TABLE
  public.config_bundle_nodes,
  public.refresh_families,
  public.device_proof_nonces,
  public.client_access_token_jtis,
  public.device_issuance_response_replays,
  public.client_refresh_response_replays,
  public.device_issuance_replay_uses,
  public.client_refresh_replay_uses
FROM PUBLIC, aegis_app, aegis_client_auth_owner, aegis_client_auth_gc_owner,
  aegis_client_auth_gc, aegis_client_keyring_preflight_owner;

REVOKE ALL ON SEQUENCE
  public.device_proof_nonces_id_seq,
  public.client_access_token_jtis_id_seq
FROM PUBLIC, aegis_app, aegis_client_auth_owner, aegis_client_auth_gc_owner,
  aegis_client_auth_gc, aegis_client_keyring_preflight_owner;

ALTER TABLE app.client_auth_00042_meta
  ALTER COLUMN catalog_manifest DROP DEFAULT;

-- Capture a canonical, OID-free catalog manifest only after every 00042 object
-- has reached its final expand state. Down recomputes this document while all
-- affected relations and shared role catalogs are locked and refuses any drift.
SELECT pg_catalog.set_config(
  'aegis.client_auth_00042_saved_search_path',
  current_setting('search_path'),
  true
);
SELECT pg_catalog.set_config('search_path','pg_catalog',true);
WITH target(schema_name, relation_name) AS (
  VALUES
    ('app','client_auth_00042_meta'),
    ('public','devices'),
    ('public','device_authorizations'),
    ('public','config_bundles'),
    ('public','refresh_tokens'),
    ('public','config_bundle_nodes'),
    ('public','refresh_families'),
    ('public','device_proof_nonces'),
    ('public','client_access_token_jtis'),
    ('public','device_issuance_response_replays'),
    ('public','client_refresh_response_replays'),
    ('public','device_issuance_replay_uses'),
    ('public','client_refresh_replay_uses'),
    ('public','device_proof_nonces_id_seq'),
    ('public','client_access_token_jtis_id_seq')
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
      'comment', pg_catalog.obj_description(c.oid, 'pg_class'),
      'columns', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          a.attname,
          pg_catalog.format_type(a.atttypid,a.atttypmod),
          a.attnotnull,
          pg_catalog.pg_get_expr(d.adbin,d.adrelid,false),
          a.attidentity::text,
          a.attgenerated::text,
          CASE WHEN a.attcollation=0 THEN NULL
               ELSE cn.nspname || '.' || co.collname END,
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
          ), '[]'::jsonb)
        ) ORDER BY a.attnum)
        FROM pg_catalog.pg_attribute a
        LEFT JOIN pg_catalog.pg_attrdef d
          ON d.adrelid=a.attrelid AND d.adnum=a.attnum
        LEFT JOIN pg_catalog.pg_collation co ON co.oid=a.attcollation
        LEFT JOIN pg_catalog.pg_namespace cn ON cn.oid=co.collnamespace
        WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
      ), '[]'::jsonb),
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
        FROM pg_catalog.pg_constraint con WHERE con.conrelid=c.oid
      ), '[]'::jsonb),
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
      ), '[]'::jsonb),
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
        FROM pg_catalog.pg_policy p WHERE p.polrelid=c.oid
      ), '[]'::jsonb),
      'triggers', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          tg.tgname,
          tg.tgenabled::text,
          tg.tgisinternal,
          pg_catalog.pg_get_triggerdef(tg.oid,false)
        ) ORDER BY tg.tgname)
        FROM pg_catalog.pg_trigger tg WHERE tg.tgrelid=c.oid
      ), '[]'::jsonb),
      'acl', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          pg_catalog.pg_get_userbyid(x.grantor),
          CASE WHEN x.grantee=0 THEN 'PUBLIC'
               ELSE pg_catalog.pg_get_userbyid(x.grantee) END,
          x.privilege_type,
          x.is_grantable
        ) ORDER BY x.grantee,x.grantor,x.privilege_type)
        FROM pg_catalog.aclexplode(c.relacl) x
      ), '[]'::jsonb),
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
          ), '[]'::jsonb)
        ) FROM pg_catalog.pg_sequence s WHERE s.seqrelid=c.oid
      ) ELSE NULL END
    ) ORDER BY n.nspname,c.relname
  ) AS document
  FROM target t
  JOIN pg_catalog.pg_namespace n ON n.nspname=t.schema_name
  JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=t.relation_name
)
UPDATE app.client_auth_00042_meta
SET catalog_manifest=jsonb_build_object(
  'format','client-auth-00042-catalog-v1',
  'relations',relation_catalog.document
)
FROM relation_catalog
WHERE singleton;
SELECT pg_catalog.set_config(
  'search_path',
  current_setting('aegis.client_auth_00042_saved_search_path'),
  true
);

-- +goose StatementBegin
DO $client_auth_00042_up_post$
DECLARE
  v_table text;
  v_rows bigint;
  v_owner oid;
  v_columns text[];
  v_expected_columns text[];
  v_nn_columns text[];
  v_expected_nn_columns text[];
  v_indexes text[];
  v_expected_indexes text[];
  v_constraints text[];
  v_expected_constraints text[];
  v_prefix text;
  v_name text;
  v_oid oid;
  v_comment text;
  v_mask smallint;
  v_role_oid_manifest jsonb;
  v_index integer;
  v_saved_search_path text;
  v_names constant text[] := ARRAY[
    'aegis_client_auth_owner','aegis_client_auth_gc_owner',
    'aegis_client_auth_gc','aegis_client_keyring_preflight_owner'
  ];
  v_marker constant text :=
    'pandora:aegispanel:migration=00042;created=v1;contract=4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E';
  v_reserved_prefix constant text := 'pandora:aegispanel:migration=00042;';
BEGIN
  v_saved_search_path := current_setting('search_path');
  PERFORM pg_catalog.set_config('search_path','pg_catalog',true);
  SELECT c.relowner INTO STRICT v_owner
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = 'app' AND c.relname = 'client_auth_00042_meta';

  IF (SELECT count(*) FROM app.client_auth_00042_meta) <> 1
     OR NOT EXISTS (
       SELECT 1 FROM app.client_auth_00042_meta
       WHERE singleton
         AND contract_sha256 = '4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E'
         AND expand_at IS NOT NULL
         AND legacy_revocation_at IS NULL
         AND first_client_write_at IS NULL
         AND cutover_at IS NULL
         AND constraints_validated_at IS NULL
         AND role_creation_mask BETWEEN 0 AND 15
         AND jsonb_typeof(role_oid_manifest)='object'
         AND (SELECT count(*) FROM jsonb_object_keys(role_oid_manifest))=4
         AND catalog_manifest->>'format'='client-auth-00042-catalog-v1'
         AND jsonb_typeof(catalog_manifest->'relations')='array'
         AND jsonb_array_length(catalog_manifest->'relations')=15
     ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 meta postcondition failed';
  END IF;

  SELECT role_creation_mask, role_oid_manifest
    INTO STRICT v_mask, v_role_oid_manifest
  FROM app.client_auth_00042_meta WHERE singleton;

  FOREACH v_table IN ARRAY ARRAY[
    'config_bundle_nodes','refresh_families','device_proof_nonces',
    'client_access_token_jtis','device_issuance_response_replays',
    'client_refresh_response_replays','device_issuance_replay_uses',
    'client_refresh_replay_uses'
  ] LOOP
    EXECUTE format('SELECT count(*) FROM public.%I', v_table) INTO v_rows;
    IF v_rows <> 0 THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 new table not empty: %', v_table;
    END IF;
    IF NOT EXISTS (
      SELECT 1
      FROM pg_catalog.pg_class c
      JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
      WHERE n.nspname = 'public'
        AND c.relname = v_table
        AND c.relkind = 'r'
        AND c.relowner = v_owner
        AND c.relrowsecurity
        AND c.relforcerowsecurity
    ) OR (
      SELECT count(*) FROM pg_catalog.pg_policy p
      WHERE p.polrelid = format('public.%I', v_table)::regclass
    ) <> 1 OR NOT EXISTS (
      SELECT 1 FROM pg_catalog.pg_policy p
      WHERE p.polrelid = format('public.%I', v_table)::regclass
        AND p.polname = 'tenant_isolation'
        AND p.polpermissive
        AND p.polcmd = '*'
        AND p.polroles = ARRAY[0::oid]
        AND pg_catalog.pg_get_expr(p.polqual, p.polrelid) =
          '(tenant_id = app.current_tenant_id())'
        AND pg_catalog.pg_get_expr(p.polwithcheck, p.polrelid) =
          '(tenant_id = app.current_tenant_id())'
    ) THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 RLS/owner postcondition failed: %', v_table;
    END IF;

    SELECT array_agg(a.attname::text ORDER BY a.attnum),
           array_agg(a.attname::text ORDER BY a.attnum) FILTER (WHERE a.attnotnull)
      INTO v_columns, v_nn_columns
      FROM pg_catalog.pg_attribute a
     WHERE a.attrelid = format('public.%I', v_table)::regclass
       AND a.attnum > 0 AND NOT a.attisdropped;

    CASE v_table
      WHEN 'config_bundle_nodes' THEN
        v_prefix := 'cbn';
        v_expected_columns := ARRAY['tenant_id','bundle_id','node_id','created_at'];
        v_expected_nn_columns := v_expected_columns;
        v_expected_indexes := ARRAY['config_bundle_nodes_pkey'];
        v_expected_constraints := ARRAY['config_bundle_nodes_pkey'];
      WHEN 'refresh_families' THEN
        v_prefix := 'rf';
        v_expected_columns := ARRAY['tenant_id','id','session_id','user_id','device_id',
          'key_fingerprint','status','absolute_expires_at','idle_expires_at','last_used_at',
          'revoked_at','compromised_at','created_at'];
        v_expected_nn_columns := ARRAY['tenant_id','id','session_id','user_id','device_id',
          'key_fingerprint','status','absolute_expires_at','idle_expires_at','created_at'];
        v_expected_indexes := ARRAY['refresh_families_identity_key',
          'refresh_families_pkey','refresh_families_session_device_key'];
        v_expected_constraints := ARRAY['refresh_families_absolute_expiry_check',
          'refresh_families_compromised_state_check','refresh_families_identity_key',
          'refresh_families_idle_expiry_check','refresh_families_key_fingerprint_check',
          'refresh_families_pkey','refresh_families_revoked_state_check',
          'refresh_families_session_device_key','refresh_families_status_check'];
      WHEN 'device_proof_nonces' THEN
        v_prefix := 'dpn';
        v_expected_columns := ARRAY['id','tenant_id','key_fingerprint','nonce_hash',
          'credential_kind','request_hash','occurred_at','expires_at'];
        v_expected_nn_columns := v_expected_columns;
        v_expected_indexes := ARRAY['device_proof_nonces_key_nonce_key',
          'device_proof_nonces_pkey','device_proof_nonces_tenant_id_id_key'];
        v_expected_constraints := ARRAY['device_proof_nonces_credential_kind_check',
          'device_proof_nonces_expiry_check','device_proof_nonces_key_fingerprint_check',
          'device_proof_nonces_key_nonce_key','device_proof_nonces_nonce_hash_check',
          'device_proof_nonces_pkey','device_proof_nonces_request_hash_check',
          'device_proof_nonces_tenant_id_id_key'];
      WHEN 'client_access_token_jtis' THEN
        v_prefix := 'catj';
        v_expected_columns := ARRAY['id','tenant_id','issuer','jti_hash','session_id',
          'device_id','signing_kid','signing_key_version','issued_at','expires_at','revoked_at'];
        v_expected_nn_columns := ARRAY['id','tenant_id','issuer','jti_hash','session_id',
          'device_id','signing_kid','signing_key_version','issued_at','expires_at'];
        v_expected_indexes := ARRAY['client_access_jtis_tenant_id_id_key',
          'client_access_jtis_tenant_issuer_jti_key','client_access_token_jtis_pkey'];
        v_expected_constraints := ARRAY['client_access_jtis_expiry_check',
          'client_access_jtis_issuer_check','client_access_jtis_jti_hash_check',
          'client_access_jtis_signing_kid_check','client_access_jtis_signing_version_check',
          'client_access_jtis_tenant_id_id_key','client_access_jtis_tenant_issuer_jti_key',
          'client_access_token_jtis_pkey'];
      WHEN 'device_issuance_response_replays' THEN
        v_prefix := 'dirr';
        v_expected_columns := ARRAY['id','tenant_id','authorization_id','device_id','session_id',
          'refresh_family_id','stable_request_hash','key_fingerprint','state','prepared_xid',
          'response_envelope_ciphertext','response_body_hash','envelope_kid',
          'envelope_key_version','aead_nonce','aead_tag','created_at','expires_at',
          'replay_count','replayed_at'];
        v_expected_nn_columns := ARRAY['id','tenant_id','authorization_id','device_id','session_id',
          'refresh_family_id','stable_request_hash','key_fingerprint','state','prepared_xid',
          'created_at','expires_at','replay_count'];
        v_expected_indexes := ARRAY['device_issuance_replays_stable_request_key',
          'device_issuance_replays_tenant_id_id_key','device_issuance_response_replays_pkey'];
        v_expected_constraints := ARRAY['device_issuance_replays_count_check',
          'device_issuance_replays_envelope_group_check','device_issuance_replays_expiry_check',
          'device_issuance_replays_key_fingerprint_check',
          'device_issuance_replays_request_hash_check','device_issuance_replays_stable_request_key',
          'device_issuance_replays_state_check','device_issuance_replays_tenant_id_id_key',
          'device_issuance_response_replays_pkey'];
      WHEN 'client_refresh_response_replays' THEN
        v_prefix := 'crr';
        v_expected_columns := ARRAY['id','tenant_id','family_id','old_token_id','new_token_id',
          'session_id','device_id','refresh_request_id','body_hash','replay_request_hash',
          'key_fingerprint','state','prepared_xid','response_envelope_ciphertext',
          'response_body_hash','envelope_kid','envelope_key_version','aead_nonce','aead_tag',
          'created_at','replay_started_at','expires_at','replay_count','replayed_at'];
        v_expected_nn_columns := ARRAY['id','tenant_id','family_id','old_token_id','new_token_id',
          'session_id','device_id','refresh_request_id','body_hash','replay_request_hash',
          'key_fingerprint','state','prepared_xid','created_at','replay_started_at','expires_at',
          'replay_count'];
        v_expected_indexes := ARRAY['client_refresh_replays_old_token_request_key',
          'client_refresh_replays_tenant_id_id_key','client_refresh_response_replays_pkey'];
        v_expected_constraints := ARRAY['client_refresh_replays_body_hash_check',
          'client_refresh_replays_count_check','client_refresh_replays_created_time_check',
          'client_refresh_replays_envelope_group_check','client_refresh_replays_expiry_check',
          'client_refresh_replays_key_fingerprint_check',
          'client_refresh_replays_old_token_request_key',
          'client_refresh_replays_request_hash_check','client_refresh_replays_state_check',
          'client_refresh_replays_tenant_id_id_key','client_refresh_response_replays_pkey'];
      WHEN 'device_issuance_replay_uses' THEN
        v_prefix := 'diu';
        v_expected_columns := ARRAY['tenant_id','replay_id','nonce_id','occurred_at'];
        v_expected_nn_columns := v_expected_columns;
        v_expected_indexes := ARRAY['device_issuance_replay_uses_nonce_key',
          'device_issuance_replay_uses_pkey'];
        v_expected_constraints := ARRAY['device_issuance_replay_uses_nonce_key',
          'device_issuance_replay_uses_pkey'];
      WHEN 'client_refresh_replay_uses' THEN
        v_prefix := 'cru';
        v_expected_columns := ARRAY['tenant_id','replay_id','nonce_id','occurred_at'];
        v_expected_nn_columns := v_expected_columns;
        v_expected_indexes := ARRAY['client_refresh_replay_uses_nonce_key',
          'client_refresh_replay_uses_pkey'];
        v_expected_constraints := ARRAY['client_refresh_replay_uses_nonce_key',
          'client_refresh_replay_uses_pkey'];
    END CASE;

    IF v_columns IS DISTINCT FROM v_expected_columns
       OR v_nn_columns IS DISTINCT FROM v_expected_nn_columns THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 column/NOT NULL inventory mismatch: %', v_table;
    END IF;

    SELECT array_agg(c2.relname::text ORDER BY c2.relname)
      INTO v_indexes
      FROM pg_catalog.pg_index i
      JOIN pg_catalog.pg_class c2 ON c2.oid = i.indexrelid
     WHERE i.indrelid = format('public.%I', v_table)::regclass
       AND i.indisvalid AND i.indisready AND i.indislive;
    IF v_indexes IS DISTINCT FROM v_expected_indexes THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 index inventory mismatch: %', v_table;
    END IF;

    SELECT array_agg(con.conname::text ORDER BY con.conname)
      INTO v_constraints
      FROM pg_catalog.pg_constraint con
     WHERE con.conrelid = format('public.%I', v_table)::regclass;
    SELECT array_agg(x ORDER BY x) INTO v_expected_constraints
    FROM unnest(v_expected_constraints || ARRAY(
      SELECT format('%s_%s_nn', v_prefix, c)::text FROM unnest(v_expected_nn_columns) c
    )) x;
    IF v_constraints IS DISTINCT FROM v_expected_constraints THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 constraint inventory mismatch: %', v_table;
    END IF;
  END LOOP;

  IF EXISTS (
    SELECT 1
    FROM pg_catalog.pg_constraint con
    JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
    JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public'
      AND c.relname = ANY (ARRAY[
        'config_bundle_nodes','refresh_families','device_proof_nonces',
        'client_access_token_jtis','device_issuance_response_replays',
        'client_refresh_response_replays','device_issuance_replay_uses',
        'client_refresh_replay_uses'
      ])
      AND con.contype = 'f'
  ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 must not create foreign keys';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM pg_catalog.pg_class c
    CROSS JOIN LATERAL pg_catalog.aclexplode(
      coalesce(c.relacl, pg_catalog.acldefault(
        CASE WHEN c.relkind = 'S' THEN 'S'::"char" ELSE 'r'::"char" END,
        c.relowner
      ))
    ) acl
    WHERE c.oid = ANY (ARRAY[
      'app.client_auth_00042_meta'::regclass,
      'public.config_bundle_nodes'::regclass,
      'public.refresh_families'::regclass,
      'public.device_proof_nonces'::regclass,
      'public.client_access_token_jtis'::regclass,
      'public.device_issuance_response_replays'::regclass,
      'public.client_refresh_response_replays'::regclass,
      'public.device_issuance_replay_uses'::regclass,
      'public.client_refresh_replay_uses'::regclass,
      'public.device_proof_nonces_id_seq'::regclass,
      'public.client_access_token_jtis_id_seq'::regclass
    ])
      AND acl.grantee <> c.relowner
  ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 non-owner ACL postcondition failed';
  END IF;

  FOR v_index IN 1..array_length(v_names, 1) LOOP
    v_name := v_names[v_index];
    SELECT oid, pg_catalog.shobj_description(oid, 'pg_authid')
      INTO STRICT v_oid, v_comment FROM pg_catalog.pg_authid WHERE rolname=v_name;
    IF v_oid IS DISTINCT FROM (v_role_oid_manifest->>v_name)::oid
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
      RAISE EXCEPTION 'CLIENT-AUTH-00042 role postcondition failed: %', v_name;
    END IF;
    IF (v_mask::integer & (1 << (v_index-1))) <> 0 THEN
      IF v_comment IS DISTINCT FROM v_marker THEN
        RAISE EXCEPTION 'CLIENT-AUTH-00042 created role marker postcondition failed: %', v_name;
      END IF;
    ELSIF v_comment LIKE v_reserved_prefix || '%' THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 retained role marker postcondition failed: %', v_name;
    END IF;
  END LOOP;
  PERFORM pg_catalog.set_config('search_path',v_saved_search_path,true);
END
$client_auth_00042_up_post$;
-- +goose StatementEnd

-- +goose Down

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';
SET LOCAL createrole_self_grant = '';

-- +goose StatementBegin
DO $client_auth_00042_down_pre$
DECLARE
  v_applied bigint;
BEGIN
  IF current_setting('server_version_num')::integer / 10000 <> 18 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down requires PostgreSQL 18';
  END IF;
  IF current_setting('aegis.client_auth_00042_downgrade_approved', true)
       IS DISTINCT FROM 'approved-v1'
     OR current_setting('aegis.client_auth_writers_stopped', true)
       IS DISTINCT FROM 'stopped-v1'
     OR current_setting('aegis.client_auth_callers_stopped', true)
       IS DISTINCT FROM 'stopped-v1' THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 downgrade/writer/caller GUC missing';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(420042, 1);
  IF to_regclass('public.goose_db_version') IS NULL THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down requires goose_db_version';
  END IF;
  SELECT max(version_id) FILTER (WHERE is_applied) INTO v_applied
  FROM public.goose_db_version;
  IF v_applied IS DISTINCT FROM 42
     OR EXISTS (
       SELECT 1 FROM public.goose_db_version
       WHERE is_applied AND version_id > 42
     ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down requires exact applied version 42';
  END IF;
END
$client_auth_00042_down_pre$;
-- +goose StatementEnd

LOCK TABLE app.client_auth_00042_meta IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.client_refresh_replay_uses IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.device_issuance_replay_uses IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.client_refresh_response_replays IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.device_issuance_response_replays IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.client_access_token_jtis IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.config_bundle_nodes IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.device_proof_nonces IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.refresh_families IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.refresh_tokens IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.config_bundles IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.device_authorizations IN ACCESS EXCLUSIVE MODE;
LOCK TABLE public.devices IN ACCESS EXCLUSIVE MODE;
LOCK TABLE pg_catalog.pg_authid IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE pg_catalog.pg_auth_members IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE pg_catalog.pg_db_role_setting IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE pg_catalog.pg_shdepend IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE pg_catalog.pg_shdescription IN SHARE ROW EXCLUSIVE MODE;

-- +goose StatementBegin
DO $client_auth_00042_down_guard$
DECLARE
  v_table text;
  v_rows bigint;
  v_mask smallint;
  v_names constant text[] := ARRAY[
    'aegis_client_auth_owner','aegis_client_auth_gc_owner',
    'aegis_client_auth_gc','aegis_client_keyring_preflight_owner'
  ];
  v_marker constant text :=
    'pandora:aegispanel:migration=00042;created=v1;contract=4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E';
  v_reserved_prefix constant text := 'pandora:aegispanel:migration=00042;';
  v_name text;
  v_oid oid;
  v_comment text;
  v_index integer;
  v_expected_columns integer;
  v_expected_constraints integer;
  v_expected_indexes integer;
  v_owner oid;
  v_catalog_manifest jsonb;
  v_role_oid_manifest jsonb;
  v_expected_relation jsonb;
  v_current_relation jsonb;
  v_schema text;
  v_relation text;
  v_saved_search_path text;
BEGIN
  v_saved_search_path := current_setting('search_path');
  PERFORM pg_catalog.set_config('search_path','pg_catalog',true);
  IF (SELECT count(*) FROM app.client_auth_00042_meta) <> 1 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down meta row count mismatch';
  END IF;
  SELECT role_creation_mask, role_oid_manifest, catalog_manifest
    INTO STRICT v_mask, v_role_oid_manifest, v_catalog_manifest
  FROM app.client_auth_00042_meta
  WHERE singleton
    AND contract_sha256 = '4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E'
    AND expand_at IS NOT NULL
    AND legacy_revocation_at IS NULL
    AND first_client_write_at IS NULL
    AND cutover_at IS NULL
    AND constraints_validated_at IS NULL
  FOR UPDATE;

  IF jsonb_typeof(v_role_oid_manifest) IS DISTINCT FROM 'object'
     OR (SELECT count(*) FROM jsonb_object_keys(v_role_oid_manifest)) <> 4 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down role OID manifest mismatch';
  END IF;

  SELECT c.relowner INTO STRICT v_owner
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='app' AND c.relname='client_auth_00042_meta';

  IF jsonb_typeof(v_catalog_manifest) IS DISTINCT FROM 'object'
     OR v_catalog_manifest->>'format' IS DISTINCT FROM 'client-auth-00042-catalog-v1'
     OR jsonb_typeof(v_catalog_manifest->'relations') IS DISTINCT FROM 'array'
     OR jsonb_array_length(v_catalog_manifest->'relations') <> 15 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down catalog manifest envelope mismatch';
  END IF;

  FOR v_schema,v_relation IN
    SELECT * FROM (VALUES
      ('app','client_auth_00042_meta'),
      ('public','devices'),
      ('public','device_authorizations'),
      ('public','config_bundles'),
      ('public','refresh_tokens'),
      ('public','config_bundle_nodes'),
      ('public','refresh_families'),
      ('public','device_proof_nonces'),
      ('public','client_access_token_jtis'),
      ('public','device_issuance_response_replays'),
      ('public','client_refresh_response_replays'),
      ('public','device_issuance_replay_uses'),
      ('public','client_refresh_replay_uses'),
      ('public','device_proof_nonces_id_seq'),
      ('public','client_access_token_jtis_id_seq')
    ) expected(schema_name,relation_name)
  LOOP
    SELECT element INTO STRICT v_expected_relation
    FROM jsonb_array_elements(v_catalog_manifest->'relations') element
    WHERE element->>'schema'=v_schema AND element->>'name'=v_relation;

    SELECT jsonb_build_object(
      'schema', n.nspname,
      'name', c.relname,
      'kind', c.relkind::text,
      'persistence', c.relpersistence::text,
      'owner', pg_catalog.pg_get_userbyid(c.relowner),
      'rls', c.relrowsecurity,
      'force_rls', c.relforcerowsecurity,
      'replica_identity', c.relreplident::text,
      'comment', pg_catalog.obj_description(c.oid, 'pg_class'),
      'columns', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          a.attname,
          pg_catalog.format_type(a.atttypid,a.atttypmod),
          a.attnotnull,
          pg_catalog.pg_get_expr(d.adbin,d.adrelid,false),
          a.attidentity::text,
          a.attgenerated::text,
          CASE WHEN a.attcollation=0 THEN NULL
               ELSE cn.nspname || '.' || co.collname END,
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
          ), '[]'::jsonb)
        ) ORDER BY a.attnum)
        FROM pg_catalog.pg_attribute a
        LEFT JOIN pg_catalog.pg_attrdef d
          ON d.adrelid=a.attrelid AND d.adnum=a.attnum
        LEFT JOIN pg_catalog.pg_collation co ON co.oid=a.attcollation
        LEFT JOIN pg_catalog.pg_namespace cn ON cn.oid=co.collnamespace
        WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
      ), '[]'::jsonb),
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
        FROM pg_catalog.pg_constraint con WHERE con.conrelid=c.oid
      ), '[]'::jsonb),
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
      ), '[]'::jsonb),
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
        FROM pg_catalog.pg_policy p WHERE p.polrelid=c.oid
      ), '[]'::jsonb),
      'triggers', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          tg.tgname,
          tg.tgenabled::text,
          tg.tgisinternal,
          pg_catalog.pg_get_triggerdef(tg.oid,false)
        ) ORDER BY tg.tgname)
        FROM pg_catalog.pg_trigger tg WHERE tg.tgrelid=c.oid
      ), '[]'::jsonb),
      'acl', coalesce((
        SELECT jsonb_agg(jsonb_build_array(
          pg_catalog.pg_get_userbyid(x.grantor),
          CASE WHEN x.grantee=0 THEN 'PUBLIC'
               ELSE pg_catalog.pg_get_userbyid(x.grantee) END,
          x.privilege_type,
          x.is_grantable
        ) ORDER BY x.grantee,x.grantor,x.privilege_type)
        FROM pg_catalog.aclexplode(c.relacl) x
      ), '[]'::jsonb),
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
          ), '[]'::jsonb)
        ) FROM pg_catalog.pg_sequence s WHERE s.seqrelid=c.oid
      ) ELSE NULL END
    ) INTO STRICT v_current_relation
    FROM pg_catalog.pg_namespace n
    JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid
    WHERE n.nspname=v_schema AND c.relname=v_relation;

    IF v_current_relation IS DISTINCT FROM v_expected_relation THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 Down exact catalog drift: %.%',
        v_schema,v_relation;
    END IF;
  END LOOP;

  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_class c
    CROSS JOIN LATERAL pg_catalog.aclexplode(
      coalesce(c.relacl,pg_catalog.acldefault('r',c.relowner))) acl
    WHERE c.oid='app.client_auth_00042_meta'::regclass
      AND acl.grantee<>c.relowner
  ) OR (SELECT count(*) FROM pg_catalog.pg_policy
        WHERE polrelid='app.client_auth_00042_meta'::regclass)<>0
     OR (SELECT count(*) FROM pg_catalog.pg_attribute
         WHERE attrelid='app.client_auth_00042_meta'::regclass
           AND attnum>0 AND NOT attisdropped)<>10
     OR (SELECT count(*) FROM pg_catalog.pg_constraint
         WHERE conrelid='app.client_auth_00042_meta'::regclass)<>10
     OR (SELECT count(*) FROM pg_catalog.pg_index
         WHERE indrelid='app.client_auth_00042_meta'::regclass
           AND indisvalid AND indisready AND indislive)<>1 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down meta catalog/ACL mismatch';
  END IF;

  IF (
    WITH expected(table_name,column_name,type_name,default_expr) AS (
      VALUES
        ('devices','public_key_spki','bytea',NULL::text),
        ('devices','key_fingerprint','bytea',NULL::text),
        ('device_authorizations','key_algorithm','text',NULL::text),
        ('device_authorizations','public_key_spki','bytea',NULL::text),
        ('device_authorizations','key_fingerprint','bytea',NULL::text),
        ('device_authorizations','user_code_mac','bytea',NULL::text),
        ('device_authorizations','denied_at','timestamp(6) with time zone',NULL::text),
        ('device_authorizations','consumed_at','timestamp(6) with time zone',NULL::text),
        ('device_authorizations','poll_count','integer','0'),
        ('config_bundles','user_id','uuid',NULL::text),
        ('config_bundles','revoked_at','timestamp(6) with time zone',NULL::text),
        ('refresh_tokens','authority','text',NULL::text),
        ('refresh_tokens','family_id','uuid',NULL::text),
        ('refresh_tokens','device_id','uuid',NULL::text),
        ('refresh_tokens','generation','integer',NULL::text),
        ('refresh_tokens','parent_id','uuid',NULL::text),
        ('refresh_tokens','key_fingerprint','bytea',NULL::text)
    )
    SELECT count(*)
    FROM expected e
    JOIN pg_catalog.pg_class c ON c.relname=e.table_name
    JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace AND n.nspname='public'
    JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attname=e.column_name
      AND a.attnum>0 AND NOT a.attisdropped
    LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
    WHERE pg_catalog.format_type(a.atttypid,a.atttypmod)=e.type_name
      AND NOT a.attnotnull AND a.attidentity='' AND a.attgenerated=''
      AND pg_catalog.pg_get_expr(d.adbin,d.adrelid) IS NOT DISTINCT FROM e.default_expr
  ) <> 17 THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down existing-column signature mismatch';
  END IF;

  IF (
    SELECT count(*) FROM pg_catalog.pg_constraint con
    JOIN pg_catalog.pg_class c ON c.oid=con.conrelid
    JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='public' AND NOT con.convalidated
      AND (c.relname,con.conname) IN (
        ('devices','devices_key_fingerprint_len_00042_check'),
        ('device_authorizations','device_auth_public_key_spki_len_00042_check'),
        ('device_authorizations','device_auth_key_fingerprint_len_00042_check'),
        ('device_authorizations','device_auth_user_code_mac_len_00042_check'),
        ('device_authorizations','device_auth_poll_count_00042_check'),
        ('refresh_tokens','refresh_tokens_key_fp_len_00042_check'),
        ('refresh_tokens','refresh_tokens_generation_00042_check')
      )
  ) <> 7 OR EXISTS (
    SELECT 1
    FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace AND n.nspname='public'
    JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum>0
    JOIN pg_catalog.pg_index i ON i.indrelid=c.oid AND a.attnum=ANY(i.indkey)
    WHERE (c.relname,a.attname) IN (
      ('devices','public_key_spki'),('devices','key_fingerprint'),
      ('device_authorizations','key_algorithm'),('device_authorizations','public_key_spki'),
      ('device_authorizations','key_fingerprint'),('device_authorizations','user_code_mac'),
      ('device_authorizations','denied_at'),('device_authorizations','consumed_at'),
      ('device_authorizations','poll_count'),('config_bundles','user_id'),
      ('config_bundles','revoked_at'),('refresh_tokens','authority'),
      ('refresh_tokens','family_id'),('refresh_tokens','device_id'),
      ('refresh_tokens','generation'),('refresh_tokens','parent_id'),
      ('refresh_tokens','key_fingerprint')
    )
  ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down existing-column dependency mismatch';
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_class c
    CROSS JOIN LATERAL pg_catalog.aclexplode(
      coalesce(c.relacl,pg_catalog.acldefault('S',c.relowner))) acl
    WHERE c.oid=ANY(ARRAY['public.device_proof_nonces_id_seq'::regclass,
      'public.client_access_token_jtis_id_seq'::regclass])
      AND acl.grantee<>c.relowner
  ) OR EXISTS (
    SELECT 1 FROM pg_catalog.pg_sequence s
    WHERE s.seqrelid=ANY(ARRAY['public.device_proof_nonces_id_seq'::regclass,
      'public.client_access_token_jtis_id_seq'::regclass])
      AND (s.seqstart<>1 OR s.seqincrement<>1 OR s.seqmin<>1
        OR s.seqmax<>9223372036854775807 OR s.seqcache<>1 OR s.seqcycle)
  ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down identity sequence mismatch';
  END IF;

  FOREACH v_table IN ARRAY ARRAY[
    'client_refresh_replay_uses','device_issuance_replay_uses',
    'client_refresh_response_replays','device_issuance_response_replays',
    'client_access_token_jtis','config_bundle_nodes','device_proof_nonces',
    'refresh_families'
  ] LOOP
    EXECUTE format('SELECT count(*) FROM public.%I', v_table) INTO v_rows;
    IF v_rows <> 0 THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 Down refused non-empty table: %', v_table;
    END IF;
    CASE v_table
      WHEN 'client_refresh_replay_uses' THEN
        v_expected_columns:=4; v_expected_constraints:=6; v_expected_indexes:=2;
      WHEN 'device_issuance_replay_uses' THEN
        v_expected_columns:=4; v_expected_constraints:=6; v_expected_indexes:=2;
      WHEN 'client_refresh_response_replays' THEN
        v_expected_columns:=24; v_expected_constraints:=28; v_expected_indexes:=3;
      WHEN 'device_issuance_response_replays' THEN
        v_expected_columns:=20; v_expected_constraints:=22; v_expected_indexes:=3;
      WHEN 'client_access_token_jtis' THEN
        v_expected_columns:=11; v_expected_constraints:=18; v_expected_indexes:=3;
      WHEN 'config_bundle_nodes' THEN
        v_expected_columns:=4; v_expected_constraints:=5; v_expected_indexes:=1;
      WHEN 'device_proof_nonces' THEN
        v_expected_columns:=8; v_expected_constraints:=16; v_expected_indexes:=3;
      WHEN 'refresh_families' THEN
        v_expected_columns:=13; v_expected_constraints:=19; v_expected_indexes:=3;
    END CASE;

    IF NOT EXISTS (
      SELECT 1 FROM pg_catalog.pg_class c
      JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
      WHERE c.oid=format('public.%I',v_table)::regclass
        AND n.nspname='public' AND c.relkind='r' AND c.relowner=v_owner
        AND c.relrowsecurity AND c.relforcerowsecurity
    ) OR (
      SELECT count(*) FROM pg_catalog.pg_policy
      WHERE polrelid = format('public.%I', v_table)::regclass
    ) <> 1 OR NOT EXISTS (
      SELECT 1 FROM pg_catalog.pg_policy p
      WHERE p.polrelid=format('public.%I',v_table)::regclass
        AND p.polname='tenant_isolation' AND p.polpermissive AND p.polcmd='*'
        AND p.polroles=ARRAY[0::oid]
        AND pg_catalog.pg_get_expr(p.polqual,p.polrelid)=
          '(tenant_id = app.current_tenant_id())'
        AND pg_catalog.pg_get_expr(p.polwithcheck,p.polrelid)=
          '(tenant_id = app.current_tenant_id())'
    ) OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_trigger
      WHERE tgrelid = format('public.%I', v_table)::regclass
        AND NOT tgisinternal
    ) OR (
      SELECT count(*) FROM pg_catalog.pg_attribute a
      WHERE a.attrelid=format('public.%I',v_table)::regclass
        AND a.attnum>0 AND NOT a.attisdropped
    ) <> v_expected_columns OR (
      SELECT count(*) FROM pg_catalog.pg_constraint con
      WHERE con.conrelid=format('public.%I',v_table)::regclass
    ) <> v_expected_constraints OR (
      SELECT count(*) FROM pg_catalog.pg_index i
      WHERE i.indrelid=format('public.%I',v_table)::regclass
        AND i.indisvalid AND i.indisready AND i.indislive
    ) <> v_expected_indexes OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_class c
      CROSS JOIN LATERAL pg_catalog.aclexplode(
        coalesce(c.relacl,pg_catalog.acldefault('r',c.relowner))) acl
      WHERE c.oid=format('public.%I',v_table)::regclass
        AND acl.grantee<>c.relowner
    ) THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 Down catalog/ACL mismatch: %', v_table;
    END IF;
  END LOOP;

  FOR v_index IN 1..array_length(v_names, 1) LOOP
    v_name := v_names[v_index];
    SELECT oid, pg_catalog.shobj_description(oid, 'pg_authid')
      INTO v_oid, v_comment
      FROM pg_catalog.pg_authid
     WHERE rolname = v_name;
    IF v_oid IS NULL
       OR v_oid IS DISTINCT FROM (v_role_oid_manifest->>v_name)::oid
       OR NOT EXISTS (
      SELECT 1 FROM pg_catalog.pg_authid
      WHERE oid = v_oid
        AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb
        AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication
        AND NOT rolbypassrls AND rolconnlimit = -1
        AND rolvaliduntil IS NULL AND rolpassword IS NULL
    ) OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_auth_members
      WHERE roleid = v_oid OR member = v_oid
    ) OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_db_role_setting WHERE setrole = v_oid
    ) OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_shdepend
      WHERE refclassid='pg_catalog.pg_authid'::regclass AND refobjid=v_oid
    ) THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 Down unsafe/missing role: %', v_name;
    END IF;
    IF (v_mask::integer & (1 << (v_index - 1))) <> 0 THEN
      IF v_comment IS DISTINCT FROM v_marker THEN
        RAISE EXCEPTION 'CLIENT-AUTH-00042 Down created-role marker mismatch: %', v_name;
      END IF;
    ELSIF v_comment LIKE v_reserved_prefix || '%' THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 Down pre-existing role has reserved marker: %', v_name;
    END IF;
  END LOOP;
  PERFORM pg_catalog.set_config('search_path',v_saved_search_path,true);
END
$client_auth_00042_down_guard$;
-- +goose StatementEnd

DROP POLICY tenant_isolation ON public.client_refresh_replay_uses;
DROP POLICY tenant_isolation ON public.device_issuance_replay_uses;
DROP POLICY tenant_isolation ON public.client_refresh_response_replays;
DROP POLICY tenant_isolation ON public.device_issuance_response_replays;
DROP POLICY tenant_isolation ON public.client_access_token_jtis;
DROP POLICY tenant_isolation ON public.config_bundle_nodes;
DROP POLICY tenant_isolation ON public.device_proof_nonces;
DROP POLICY tenant_isolation ON public.refresh_families;

DROP TABLE public.client_refresh_replay_uses;
DROP TABLE public.device_issuance_replay_uses;
DROP TABLE public.client_refresh_response_replays;
DROP TABLE public.device_issuance_response_replays;
DROP TABLE public.client_access_token_jtis;
DROP TABLE public.config_bundle_nodes;
DROP TABLE public.device_proof_nonces;
DROP TABLE public.refresh_families;

-- +goose StatementBegin
DO $client_auth_00042_down_sequences$
BEGIN
  IF to_regclass('public.device_proof_nonces_id_seq') IS NOT NULL
     OR to_regclass('public.client_access_token_jtis_id_seq') IS NOT NULL THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down orphan identity sequence';
  END IF;
END
$client_auth_00042_down_sequences$;
-- +goose StatementEnd

ALTER TABLE public.refresh_tokens
  DROP CONSTRAINT refresh_tokens_generation_00042_check,
  DROP CONSTRAINT refresh_tokens_key_fp_len_00042_check,
  DROP COLUMN key_fingerprint,
  DROP COLUMN parent_id,
  DROP COLUMN generation,
  DROP COLUMN device_id,
  DROP COLUMN family_id,
  DROP COLUMN authority;

ALTER TABLE public.config_bundles
  DROP COLUMN revoked_at,
  DROP COLUMN user_id;

ALTER TABLE public.device_authorizations
  DROP CONSTRAINT device_auth_poll_count_00042_check,
  DROP CONSTRAINT device_auth_user_code_mac_len_00042_check,
  DROP CONSTRAINT device_auth_key_fingerprint_len_00042_check,
  DROP CONSTRAINT device_auth_public_key_spki_len_00042_check,
  DROP COLUMN poll_count,
  DROP COLUMN consumed_at,
  DROP COLUMN denied_at,
  DROP COLUMN user_code_mac,
  DROP COLUMN key_fingerprint,
  DROP COLUMN public_key_spki,
  DROP COLUMN key_algorithm;

ALTER TABLE public.devices
  DROP CONSTRAINT devices_key_fingerprint_len_00042_check,
  DROP COLUMN key_fingerprint,
  DROP COLUMN public_key_spki;

-- +goose StatementBegin
DO $client_auth_00042_down_roles$
DECLARE
  v_names constant text[] := ARRAY[
    'aegis_client_auth_owner',
    'aegis_client_auth_gc_owner',
    'aegis_client_auth_gc',
    'aegis_client_keyring_preflight_owner'
  ];
  v_drop_order constant integer[] := ARRAY[3,2,4,1];
  v_marker constant text :=
    'pandora:aegispanel:migration=00042;created=v1;contract=4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E';
  v_reserved_prefix constant text := 'pandora:aegispanel:migration=00042;';
  v_mask smallint;
  v_role_oid_manifest jsonb;
  v_index integer;
  v_name text;
  v_oid oid;
  v_comment text;
BEGIN
  SELECT role_creation_mask, role_oid_manifest
    INTO STRICT v_mask, v_role_oid_manifest
  FROM app.client_auth_00042_meta
  WHERE singleton
    AND contract_sha256 = '4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E';

  DROP TABLE app.client_auth_00042_meta;

  FOREACH v_index IN ARRAY v_drop_order LOOP
    v_name := v_names[v_index];
    SELECT oid, pg_catalog.shobj_description(oid, 'pg_authid')
      INTO v_oid, v_comment
      FROM pg_catalog.pg_authid
     WHERE rolname = v_name;
    IF v_oid IS NULL
       OR v_oid IS DISTINCT FROM (v_role_oid_manifest->>v_name)::oid THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 Down role disappeared: %', v_name;
    END IF;
    IF NOT EXISTS (
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
      RAISE EXCEPTION 'CLIENT-AUTH-00042 Down final role catalog mismatch: %', v_name;
    END IF;
    IF (v_mask::integer & (1 << (v_index - 1))) <> 0 THEN
      IF v_comment IS DISTINCT FROM v_marker THEN
        RAISE EXCEPTION 'CLIENT-AUTH-00042 Down role marker changed: %', v_name;
      END IF;
      EXECUTE format('DROP ROLE %I', v_name);
    ELSIF v_comment LIKE v_reserved_prefix || '%' THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 Down retained role has reserved marker: %', v_name;
    END IF;
  END LOOP;
END
$client_auth_00042_down_roles$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $client_auth_00042_down_post$
DECLARE
  v_relation text;
BEGIN
  FOREACH v_relation IN ARRAY ARRAY[
    'app.client_auth_00042_meta',
    'public.config_bundle_nodes','public.refresh_families',
    'public.client_refresh_response_replays','public.device_proof_nonces',
    'public.client_access_token_jtis','public.device_issuance_response_replays',
    'public.device_issuance_replay_uses','public.client_refresh_replay_uses',
    'public.device_proof_nonces_id_seq','public.client_access_token_jtis_id_seq'
  ] LOOP
    IF to_regclass(v_relation) IS NOT NULL THEN
      RAISE EXCEPTION 'CLIENT-AUTH-00042 Down residue: %', v_relation;
    END IF;
  END LOOP;
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND (
      (table_name='devices' AND column_name IN ('public_key_spki','key_fingerprint'))
      OR (table_name='device_authorizations' AND column_name IN (
        'key_algorithm','public_key_spki','key_fingerprint','user_code_mac',
        'denied_at','consumed_at','poll_count'))
      OR (table_name='config_bundles' AND column_name IN ('user_id','revoked_at'))
      OR (table_name='refresh_tokens' AND column_name IN (
        'authority','family_id','device_id','generation','parent_id','key_fingerprint'))
    )
  ) THEN
    RAISE EXCEPTION 'CLIENT-AUTH-00042 Down existing-table residue';
  END IF;
END
$client_auth_00042_down_post$;
-- +goose StatementEnd
