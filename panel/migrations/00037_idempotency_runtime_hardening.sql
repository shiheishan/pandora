-- Actor-fenced idempotency claims and byte-exact replay evidence.
-- This migration is intentionally stop-the-world and refuses ambiguous data.

-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
DECLARE
  v_policies text[];
BEGIN
  IF current_setting('app.idempotency_writers_stopped',true) IS DISTINCT FROM 'yes'
     OR current_setting('app.allow_idempotency_schema37_up',true) IS DISTINCT FROM 'yes' THEN
    RAISE EXCEPTION '00037 Up requires explicit stopped-writer and upgrade approval';
  END IF;
  IF to_regclass('public.idempotency_keys') IS NULL
     OR NOT EXISTS (
       SELECT 1 FROM information_schema.columns
        WHERE table_schema='public' AND table_name='idempotency_keys'
          AND column_name='actor_id' AND data_type='uuid'
     ) THEN
    RAISE EXCEPTION '00037 requires schema 00036';
  END IF;
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema='public' AND table_name='idempotency_keys'
       AND column_name IN ('claim_generation','response_format','response_payload',
                           'response_content_type','response_location','response_etag',
                           'response_cache_control','response_content_language')
  ) OR to_regclass('app.idempotency_runtime_00037_snapshot') IS NOT NULL
    OR to_regprocedure('app.idempotency_actor_scope(text,uuid)') IS NOT NULL
    OR to_regprocedure('app.lookup_legacy_idempotency_key(uuid,text,text)') IS NOT NULL
  THEN
    RAISE EXCEPTION '00037 found a partial or repeated runtime-hardening migration';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_class
     WHERE oid='public.idempotency_keys'::regclass
       AND relrowsecurity AND relforcerowsecurity
  ) THEN
    RAISE EXCEPTION '00037 requires enabled and forced idempotency row security';
  END IF;
  SELECT array_agg(polname ORDER BY polname) INTO v_policies
    FROM pg_policy WHERE polrelid='public.idempotency_keys'::regclass;
  IF v_policies IS DISTINCT FROM ARRAY['tenant_isolation']::text[]
     OR NOT EXISTS (
       SELECT 1 FROM pg_policy
        WHERE polrelid='public.idempotency_keys'::regclass
          AND polname='tenant_isolation' AND polcmd='*' AND polpermissive
     ) THEN
    RAISE EXCEPTION '00037 refused unexpected idempotency policy catalog: %',v_policies;
  END IF;
  IF EXISTS (
    SELECT 1 FROM (VALUES
      ('idempotency_keys_resource_pair'),
      ('idempotency_keys_evidence_shape'),
      ('idempotency_keys_tenant_id_id_key'),
      ('idempotency_keys_actor_tenant_fk')
    ) expected(name)
    LEFT JOIN pg_constraint c
      ON c.conrelid='public.idempotency_keys'::regclass AND c.conname=expected.name
   WHERE c.oid IS NULL OR NOT c.convalidated
  ) THEN
    RAISE EXCEPTION '00037 requires validated schema-36 idempotency constraints';
  END IF;
  IF EXISTS (
    SELECT 1 FROM (VALUES
      ('zz_idempotency_evidence_guard'),
      ('trg_idempotency_no_delete'),
      ('trg_refund_idempotency_commit')
    ) expected(name)
    LEFT JOIN pg_trigger t
      ON t.tgrelid='public.idempotency_keys'::regclass
     AND t.tgname=expected.name AND NOT t.tgisinternal
   WHERE t.oid IS NULL OR t.tgenabled<>'O'
  ) OR to_regprocedure('app.guard_idempotency_evidence()') IS NULL
    OR to_regprocedure('app.guard_refund_request()') IS NULL
    OR to_regprocedure('app.assert_refund_request(uuid,uuid)') IS NULL
    OR to_regprocedure('app.assert_refund_idempotency_key(uuid,uuid)') IS NULL
  THEN
    RAISE EXCEPTION '00037 requires the exact enabled schema-36 guard surface';
  END IF;
  IF (SELECT count(*) FROM pg_trigger
       WHERE tgrelid='public.idempotency_keys'::regclass AND NOT tgisinternal)<>3 THEN
    RAISE EXCEPTION '00037 refused unexpected idempotency trigger catalog';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='aegis_app')
     OR EXISTS (
       SELECT 1 FROM pg_roles
        WHERE rolname='aegis_app' AND (rolsuper OR rolbypassrls)
     ) THEN
    RAISE EXCEPTION '00037 requires a non-superuser, non-BYPASSRLS aegis_app role';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_auth_members m
    JOIN pg_roles target ON target.oid=m.roleid
    JOIN pg_roles member_role ON member_role.oid=m.member
    WHERE target.rolname='aegis_app' OR member_role.rolname='aegis_app'
  ) THEN
    RAISE EXCEPTION '00037 refused aegis_app role memberships';
  END IF;
  IF EXISTS (
       SELECT 1
         FROM pg_namespace n
         CROSS JOIN LATERAL aclexplode(
           COALESCE(n.nspacl,acldefault('n',n.nspowner))
         ) a
        WHERE n.nspname IN ('public','app')
          AND a.grantee=0 AND a.privilege_type='CREATE'
     )
     OR has_schema_privilege('aegis_app','public','CREATE')
     OR has_schema_privilege('aegis_app','app','CREATE') THEN
    RAISE EXCEPTION '00037 refused writable trusted schemas';
  END IF;
  IF has_table_privilege('aegis_app','idempotency_keys','INSERT')
     OR has_table_privilege('aegis_app','idempotency_keys','UPDATE')
     OR has_table_privilege('aegis_app','idempotency_keys','DELETE')
     OR has_table_privilege('aegis_app','idempotency_keys','TRUNCATE')
     OR NOT has_column_privilege('aegis_app','idempotency_keys','status','INSERT')
     OR NOT has_column_privilege('aegis_app','idempotency_keys','response_body','UPDATE')
     OR has_column_privilege('aegis_app','idempotency_keys','request_hash','UPDATE') THEN
    RAISE EXCEPTION '00037 refused unexpected schema-36 idempotency ACL';
  END IF;
END $$;
-- +goose StatementEnd

-- Snapshot the exact schema-36 row image. Any later insert, update or delete
-- makes Down refuse instead of guessing whether evidence can be discarded.
CREATE TABLE app.idempotency_runtime_00037_snapshot AS
  SELECT id,to_jsonb(k) AS row_data FROM public.idempotency_keys k;
ALTER TABLE app.idempotency_runtime_00037_snapshot ADD PRIMARY KEY (id);
ALTER TABLE app.idempotency_runtime_00037_snapshot ALTER COLUMN row_data SET NOT NULL;
REVOKE ALL ON app.idempotency_runtime_00037_snapshot FROM PUBLIC, aegis_app;

-- +goose StatementBegin
CREATE FUNCTION app.idempotency_actor_scope(p_base_scope text,p_actor uuid)
RETURNS text
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE
SET search_path = pg_catalog
AS $$
BEGIN
  IF p_base_scope='' OR octet_length(p_base_scope)>255
     OR p_base_scope ~ '[[:cntrl:]]'
     OR p_base_scope ~ ':actor:[0-9a-f]{24}$' THEN
    RAISE EXCEPTION 'invalid idempotency base scope'
      USING ERRCODE='invalid_parameter_value';
  END IF;
  RETURN p_base_scope || ':actor:' ||
         substr(encode(public.digest(p_actor::text,'sha256'),'hex'),1,24);
END;
$$;

CREATE FUNCTION app.idempotency_scope_matches_actor(p_scope text,p_actor uuid)
RETURNS boolean
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
SET search_path = pg_catalog
AS $$
  SELECT p_scope ~ '^.+:actor:[0-9a-f]{24}$'
     AND p_scope = app.idempotency_actor_scope(
       regexp_replace(p_scope,':actor:[0-9a-f]{24}$',''),p_actor
     )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
  v_detail text;
BEGIN
  SELECT format('key %s has ambiguous _aegis_envelope JSON evidence',idempotency_key)
    INTO v_detail FROM idempotency_keys
   WHERE response_body IS NOT NULL
     AND jsonb_typeof(response_body)='object'
     AND response_body ? '_aegis_envelope'
   LIMIT 1;
  IF v_detail IS NOT NULL THEN RAISE EXCEPTION '00037 refused %',v_detail; END IF;

  SELECT format('scope %s/key %s has malformed reserved actor suffix',scope,idempotency_key)
    INTO v_detail FROM idempotency_keys
   WHERE scope LIKE '%:actor:%' AND scope !~ '^.+:actor:[0-9a-f]{24}$'
   LIMIT 1;
  IF v_detail IS NOT NULL THEN RAISE EXCEPTION '00037 refused %',v_detail; END IF;

  SELECT format('scope %s/key %s has null or mismatched actor digest',scope,idempotency_key)
    INTO v_detail FROM idempotency_keys
   WHERE scope ~ '^.+:actor:[0-9a-f]{24}$'
     AND (actor_id IS NULL OR NOT app.idempotency_scope_matches_actor(scope,actor_id))
   LIMIT 1;
  IF v_detail IS NOT NULL THEN RAISE EXCEPTION '00037 refused %',v_detail; END IF;

  SELECT format('tenant %s/base %s/actor %s/key %s has duplicate canonical claims',
                tenant_id,base_scope,actor_id,idempotency_key)
    INTO v_detail
    FROM (
      SELECT tenant_id,
             regexp_replace(scope,':actor:[0-9a-f]{24}$','') AS base_scope,
             actor_id,idempotency_key,count(*) AS n
        FROM idempotency_keys
       WHERE scope ~ '^.+:actor:[0-9a-f]{24}$'
       GROUP BY 1,2,3,4 HAVING count(*)>1
    ) duplicates LIMIT 1;
  IF v_detail IS NOT NULL THEN RAISE EXCEPTION '00037 refused %',v_detail; END IF;

  SELECT format('tenant %s/base %s/key %s has both raw and actor-bound claims',
                raw.tenant_id,raw.scope,raw.idempotency_key)
    INTO v_detail
    FROM idempotency_keys raw
    JOIN idempotency_keys bound
      ON bound.tenant_id=raw.tenant_id
     AND bound.idempotency_key=raw.idempotency_key
     AND bound.scope ~ '^.+:actor:[0-9a-f]{24}$'
     AND regexp_replace(bound.scope,':actor:[0-9a-f]{24}$','')=raw.scope
   WHERE raw.scope !~ ':actor:[0-9a-f]{24}$'
   LIMIT 1;
  IF v_detail IS NOT NULL THEN RAISE EXCEPTION '00037 refused %',v_detail; END IF;

  SELECT format('refund key %s has malformed actor-bound scope %s',id,scope)
    INTO v_detail FROM idempotency_keys
   WHERE resource_type='refund_request'
     AND scope<>'refund_create'
     AND (actor_id IS NULL OR scope IS DISTINCT FROM
          app.idempotency_actor_scope('refund_create',actor_id))
   LIMIT 1;
  IF v_detail IS NOT NULL THEN RAISE EXCEPTION '00037 refused %',v_detail; END IF;

  SELECT format('order resource %s/%s is bound by %s idempotency keys',
                tenant_id,resource_id,count(*))
    INTO v_detail FROM idempotency_keys
   WHERE resource_type='order'
   GROUP BY tenant_id,resource_id HAVING count(*)>1 LIMIT 1;
  IF v_detail IS NOT NULL THEN RAISE EXCEPTION '00037 refused %',v_detail; END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE idempotency_keys DISABLE TRIGGER zz_idempotency_evidence_guard;
ALTER TABLE idempotency_keys DISABLE TRIGGER trg_refund_idempotency_commit;

ALTER TABLE idempotency_keys
  ADD COLUMN claim_generation bigint NOT NULL DEFAULT 1,
  ADD COLUMN response_format text NOT NULL DEFAULT 'none',
  ADD COLUMN response_payload bytea,
  ADD COLUMN response_content_type text,
  ADD COLUMN response_location text,
  ADD COLUMN response_etag text,
  ADD COLUMN response_cache_control text,
  ADD COLUMN response_content_language text;

UPDATE idempotency_keys
   SET response_format=CASE
     WHEN status='in_flight' THEN 'none'
     WHEN response_body IS NOT NULL THEN 'legacy_json'
     ELSE 'legacy_empty'
   END;

ALTER TABLE idempotency_keys
  ADD CONSTRAINT idempotency_keys_claim_generation CHECK (
    claim_generation>=1 AND claim_generation<9223372036854775807
  ) NOT VALID,
  ADD CONSTRAINT idempotency_keys_response_format CHECK (
    response_format IN ('none','legacy_json','legacy_empty','bytes','unavailable')
  ) NOT VALID,
  ADD CONSTRAINT idempotency_keys_actor_scope_shape CHECK (
    scope !~ ':actor:' OR
    (scope ~ '^.+:actor:[0-9a-f]{24}$' AND actor_id IS NOT NULL
     AND app.idempotency_scope_matches_actor(scope,actor_id))
  ) NOT VALID,
  ADD CONSTRAINT idempotency_keys_runtime_evidence_shape CHECK (
    (response_content_type IS NULL OR
       (octet_length(response_content_type)<=255 AND response_content_type !~ '[[:cntrl:]]'))
    AND (response_location IS NULL OR
       (octet_length(response_location)<=2048 AND response_location !~ '[[:cntrl:]]'
        AND position(E'\\' in response_location)=0
        AND left(response_location,1)='/' AND left(response_location,2)<>'//'))
    AND (response_etag IS NULL OR
       (octet_length(response_etag)<=255 AND response_etag !~ '[[:cntrl:]]'))
    AND (response_cache_control IS NULL OR
       (octet_length(response_cache_control)<=512 AND response_cache_control !~ '[[:cntrl:]]'))
    AND (response_content_language IS NULL OR
       (octet_length(response_content_language)<=128 AND response_content_language !~ '[[:cntrl:]]'))
    AND (response_payload IS NULL OR octet_length(response_payload)<=1048576)
    AND (
      (status='in_flight' AND response_format='none'
       AND response_code IS NULL AND response_body IS NULL AND response_payload IS NULL
       AND completed_at IS NULL AND locked_until IS NOT NULL
       AND response_content_type IS NULL AND response_location IS NULL
       AND response_etag IS NULL AND response_cache_control IS NULL
       AND response_content_language IS NULL)
      OR
      (status IN ('succeeded','failed') AND completed_at IS NOT NULL
       AND locked_until IS NULL AND response_code IS NOT NULL
       AND response_format IN ('legacy_json','legacy_empty','bytes','unavailable')
       AND (
         (response_format='legacy_json' AND response_body IS NOT NULL
          AND response_payload IS NULL AND response_content_type IS NULL
          AND response_location IS NULL AND response_etag IS NULL
          AND response_cache_control IS NULL AND response_content_language IS NULL)
         OR
         (response_format='legacy_empty' AND response_body IS NULL
          AND response_payload IS NULL AND response_content_type IS NULL
          AND response_location IS NULL AND response_etag IS NULL
          AND response_cache_control IS NULL AND response_content_language IS NULL)
         OR
         (response_format='bytes' AND response_body IS NULL
          AND response_payload IS NOT NULL)
         OR
         (response_format='unavailable' AND response_body IS NULL
          AND response_payload IS NULL AND response_content_type IS NULL
          AND response_location IS NULL AND response_etag IS NULL
          AND response_cache_control IS NULL AND response_content_language IS NULL)
       ))
    )
  ) NOT VALID;

ALTER TABLE idempotency_keys VALIDATE CONSTRAINT idempotency_keys_claim_generation;
ALTER TABLE idempotency_keys VALIDATE CONSTRAINT idempotency_keys_response_format;
ALTER TABLE idempotency_keys VALIDATE CONSTRAINT idempotency_keys_actor_scope_shape;
ALTER TABLE idempotency_keys VALIDATE CONSTRAINT idempotency_keys_runtime_evidence_shape;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_idempotency_evidence() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.status<>'in_flight' OR NEW.claim_generation<>1
       OR NEW.response_format<>'none' OR NEW.response_code IS NOT NULL
       OR NEW.response_body IS NOT NULL OR NEW.response_payload IS NOT NULL
       OR NEW.completed_at IS NOT NULL OR NEW.resource_type IS NOT NULL
       OR NEW.resource_id IS NOT NULL OR NEW.actor_id IS NULL
       OR NOT app.idempotency_scope_matches_actor(NEW.scope,NEW.actor_id) THEN
      RAISE EXCEPTION 'idempotency claims must start actor-bound, generation 1, unbound and in_flight'
        USING ERRCODE='check_violation';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.status='succeeded' AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'successful idempotency evidence is immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.status='failed' AND NEW.status='failed'
     AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'failed idempotency evidence changes only through controlled retry'
      USING ERRCODE='check_violation';
  END IF;
  IF (NEW.id,NEW.tenant_id,NEW.scope,NEW.idempotency_key,NEW.request_hash,
      NEW.actor_id,NEW.created_at,NEW.expires_at) IS DISTINCT FROM
     (OLD.id,OLD.tenant_id,OLD.scope,OLD.idempotency_key,OLD.request_hash,
      OLD.actor_id,OLD.created_at,OLD.expires_at) THEN
    RAISE EXCEPTION 'idempotency tenant/scope/key/hash/actor/time identity is immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.resource_type IS NOT NULL AND
     (NEW.resource_type,NEW.resource_id) IS DISTINCT FROM
     (OLD.resource_type,OLD.resource_id) THEN
    RAISE EXCEPTION 'idempotency resource binding is write-once'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.resource_type IS NULL AND NEW.resource_type IS NOT NULL
     AND OLD.status<>'in_flight' THEN
    RAISE EXCEPTION 'only an in-flight owner may bind a resource'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.status='failed' AND NEW.status='in_flight' THEN
    IF OLD.claim_generation>=9223372036854775806
       OR NEW.claim_generation<>OLD.claim_generation+1 THEN
      RAISE EXCEPTION 'failed retry must increment claim generation exactly once'
        USING ERRCODE='check_violation';
    END IF;
  ELSIF NEW.claim_generation<>OLD.claim_generation THEN
    RAISE EXCEPTION 'claim generation changed outside a failed retry'
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='in_flight' AND NEW.status IN ('succeeded','failed'))
    OR (OLD.status='failed' AND NEW.status='in_flight')
  ) THEN
    RAISE EXCEPTION 'illegal idempotency transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE idempotency_keys ENABLE TRIGGER zz_idempotency_evidence_guard;
ALTER TABLE idempotency_keys ENABLE TRIGGER trg_refund_idempotency_commit;

CREATE UNIQUE INDEX idempotency_keys_one_order_resource_00037
  ON idempotency_keys(tenant_id,resource_id) WHERE resource_type='order';

-- +goose StatementBegin
CREATE FUNCTION app.lookup_legacy_idempotency_key(
  p_tenant uuid,p_base_scope text,p_idempotency_key text
) RETURNS TABLE(created_at timestamptz,completed_at timestamptz)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
  IF p_tenant IS NULL OR p_tenant IS DISTINCT FROM app.current_tenant_id()
     OR app.current_actor_id() IS NULL THEN
    RAISE EXCEPTION 'legacy idempotency lookup requires current tenant and actor'
      USING ERRCODE='insufficient_privilege';
  END IF;
  IF p_base_scope IS NULL OR p_base_scope='' OR octet_length(p_base_scope)>255
     OR p_base_scope ~ '[[:cntrl:]]'
     OR p_base_scope ~ ':actor:[0-9a-f]{24}$'
     OR p_idempotency_key IS NULL OR p_idempotency_key=''
     OR octet_length(p_idempotency_key)>255
     OR p_idempotency_key ~ '[[:cntrl:]]' THEN
    RAISE EXCEPTION 'invalid legacy idempotency lookup input'
      USING ERRCODE='invalid_parameter_value';
  END IF;
  RETURN QUERY
    SELECT k.created_at,k.completed_at
      FROM public.idempotency_keys k
     WHERE k.tenant_id=p_tenant AND k.scope=p_base_scope
       AND k.idempotency_key=p_idempotency_key
       AND NOT app.idempotency_scope_matches_actor(k.scope,app.current_actor_id())
     LIMIT 1;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_refund_request() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
  IF session_user='aegis_app' AND NEW.tenant_id IS DISTINCT FROM app.current_tenant_id() THEN
    RAISE EXCEPTION 'refund invariant tenant context mismatch'
      USING ERRCODE='insufficient_privilege';
  END IF;
  IF session_user='aegis_app' AND
     (app.current_actor_id() IS NULL OR
      NEW.requested_by IS DISTINCT FROM app.current_actor_id()) THEN
    RAISE EXCEPTION 'refund invariant actor context mismatch'
      USING ERRCODE='insufficient_privilege';
  END IF;
  IF TG_OP='UPDATE' AND
     (to_jsonb(NEW)-ARRAY['status','approval_request_id','updated_at']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['status','approval_request_id','updated_at']) THEN
    RAISE EXCEPTION 'refund request identity, amount, actor and business key are immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF TG_OP='UPDATE' AND OLD.status='succeeded'
     AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'terminal refund request is immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF TG_OP='UPDATE' AND NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='pending' AND NEW.status IN ('processing','failed','manual_review'))
    OR (OLD.status='processing' AND NEW.status IN
          ('succeeded','failed','manual_review','partially_succeeded'))
    OR (OLD.status='failed' AND NEW.status='processing')
    OR (OLD.status IN ('manual_review','partially_succeeded') AND
        NEW.status IN ('processing','succeeded','failed','partially_succeeded','manual_review'))
  ) THEN
    RAISE EXCEPTION 'illegal refund request transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM public.idempotency_keys k
     WHERE k.tenant_id=NEW.tenant_id AND k.id=NEW.business_request_id
       AND k.scope IN ('refund_create',
            app.idempotency_actor_scope('refund_create',k.actor_id))
       AND k.resource_type='refund_request' AND k.resource_id=NEW.id
       AND k.status IN ('in_flight','succeeded','failed')
       AND k.request_hash=NEW.request_hash
       AND k.actor_id IS NOT DISTINCT FROM NEW.requested_by
  ) THEN
    RAISE EXCEPTION 'refund request requires a bound refund_create idempotency record'
      USING ERRCODE='foreign_key_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app.assert_refund_request(p_tenant uuid,p_request uuid)
RETURNS void
LANGUAGE plpgsql SECURITY INVOKER
SET search_path = pg_catalog
AS $$
DECLARE v_detail text;
BEGIN
  IF session_user='aegis_app' AND p_tenant IS DISTINCT FROM app.current_tenant_id() THEN
    RAISE EXCEPTION 'refund invariant tenant context mismatch'
      USING ERRCODE='insufficient_privilege';
  END IF;
  IF session_user='aegis_app' AND app.current_actor_id() IS NULL THEN
    RAISE EXCEPTION 'refund invariant actor context mismatch'
      USING ERRCODE='insufficient_privilege';
  END IF;
  SELECT format('request %s status %s/key %s has %s legs/%s amount/%s succeeded',
                rr.id,rr.status,k.status,count(r.id),coalesce(sum(r.amount),0),
                count(r.id) FILTER (WHERE r.status='succeeded'))
    INTO v_detail
    FROM public.refund_requests rr
    LEFT JOIN public.idempotency_keys k
      ON k.tenant_id=rr.tenant_id AND k.id=rr.business_request_id
    LEFT JOIN public.refunds r
      ON r.tenant_id=rr.tenant_id AND r.refund_request_id=rr.id
   WHERE rr.tenant_id=p_tenant AND rr.id=p_request
   GROUP BY rr.id,rr.status,rr.requested_amount,k.id,k.status,k.scope,
            k.request_hash,k.actor_id,k.resource_type,k.resource_id
  HAVING k.id IS NULL OR k.scope NOT IN (
           'refund_create',app.idempotency_actor_scope('refund_create',k.actor_id))
      OR (session_user='aegis_app' AND
          rr.requested_by IS DISTINCT FROM app.current_actor_id())
      OR k.request_hash<>rr.request_hash
      OR k.actor_id IS DISTINCT FROM rr.requested_by
      OR k.resource_type<>'refund_request' OR k.resource_id<>rr.id
      OR (rr.status IN ('pending','processing','manual_review','partially_succeeded')
          AND k.status<>'in_flight')
      OR (rr.status='succeeded' AND k.status<>'succeeded')
      OR (rr.status='failed' AND k.status<>'failed')
      OR count(r.id)=0 OR coalesce(sum(r.amount),0)<>rr.requested_amount
      OR (rr.status='succeeded' AND
          count(r.id) FILTER (WHERE r.status='succeeded')<>count(r.id))
      OR (rr.status='failed' AND
          count(r.id) FILTER (WHERE r.status IN ('failed','rejected'))<>count(r.id))
      OR (rr.status IN ('pending','processing') AND
          count(r.id) FILTER (WHERE r.status IN ('succeeded','failed','rejected'))>0)
      OR (rr.status IN ('manual_review','partially_succeeded') AND
          (count(r.id) FILTER (WHERE r.status='succeeded')=0
           OR count(r.id) FILTER (WHERE r.status='succeeded')=count(r.id)));
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'refund request invariant: %',v_detail USING ERRCODE='check_violation';
  END IF;
END;
$$;

CREATE OR REPLACE FUNCTION app.assert_refund_idempotency_key(p_tenant uuid,p_key uuid)
RETURNS void
LANGUAGE plpgsql SECURITY INVOKER
SET search_path = pg_catalog
AS $$
DECLARE v_detail text;
BEGIN
  IF session_user='aegis_app' AND p_tenant IS DISTINCT FROM app.current_tenant_id() THEN
    RAISE EXCEPTION 'refund invariant tenant context mismatch'
      USING ERRCODE='insufficient_privilege';
  END IF;
  IF session_user='aegis_app' AND app.current_actor_id() IS NULL THEN
    RAISE EXCEPTION 'refund invariant actor context mismatch'
      USING ERRCODE='insufficient_privilege';
  END IF;
  SELECT format('refund key %s status %s is not bidirectionally bound',k.id,k.status)
    INTO v_detail
    FROM public.idempotency_keys k
    LEFT JOIN public.refund_requests rr
      ON rr.tenant_id=k.tenant_id AND rr.business_request_id=k.id
   WHERE k.tenant_id=p_tenant AND k.id=p_key
     AND (k.resource_type='refund_request' OR k.scope='refund_create'
          OR (k.actor_id IS NOT NULL AND
              k.scope=app.idempotency_actor_scope('refund_create',k.actor_id)))
   GROUP BY k.id,k.status,k.request_hash,k.actor_id,k.resource_type,k.resource_id,
            rr.id,rr.status,rr.request_hash,rr.requested_by
  HAVING k.scope NOT IN ('refund_create',
            app.idempotency_actor_scope('refund_create',k.actor_id))
      OR (session_user='aegis_app' AND
          rr.requested_by IS DISTINCT FROM app.current_actor_id())
      OR rr.id IS NULL OR k.request_hash<>rr.request_hash
      OR k.actor_id IS DISTINCT FROM rr.requested_by
      OR k.resource_type<>'refund_request' OR k.resource_id<>rr.id
      OR (rr.status IN ('pending','processing','manual_review','partially_succeeded')
          AND k.status<>'in_flight')
      OR (rr.status='succeeded' AND k.status<>'succeeded')
      OR (rr.status='failed' AND k.status<>'failed');
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'refund idempotency invariant: %',v_detail
      USING ERRCODE='check_violation';
  END IF;
END;
$$;

CREATE OR REPLACE FUNCTION app.assert_refund_request_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE v_tenant uuid; v_request uuid;
BEGIN
  IF TG_OP<>'DELETE' THEN
    v_tenant:=NEW.tenant_id;
    IF TG_TABLE_NAME='refund_requests' THEN v_request:=NEW.id;
    ELSE v_request:=NEW.refund_request_id; END IF;
    PERFORM app.assert_refund_request(v_tenant,v_request);
  END IF;
  IF TG_OP<>'INSERT' THEN
    v_tenant:=OLD.tenant_id;
    IF TG_TABLE_NAME='refund_requests' THEN v_request:=OLD.id;
    ELSE v_request:=OLD.refund_request_id; END IF;
    PERFORM app.assert_refund_request(v_tenant,v_request);
  END IF;
  RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION app.assert_refund_idempotency_key_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
  IF TG_OP<>'DELETE' THEN
    PERFORM app.assert_refund_idempotency_key(NEW.tenant_id,NEW.id);
  END IF;
  IF TG_OP<>'INSERT' THEN
    PERFORM app.assert_refund_idempotency_key(OLD.tenant_id,OLD.id);
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Order and claim references are a deferred, bidirectional aggregate. The
-- assertion deliberately does not confuse claim_generation with order state.
-- +goose StatementBegin
CREATE FUNCTION app.assert_idempotency_order_binding(
  p_tenant uuid,p_order uuid,p_key uuid
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE v_detail text;
BEGIN
  IF p_order IS NOT NULL THEN
    SELECT format('order %s/idempotency key %s is not symmetrically bound',o.id,o.idempotency_key_id)
      INTO v_detail
      FROM public.orders o
      LEFT JOIN public.idempotency_keys k
        ON k.tenant_id=o.tenant_id AND k.id=o.idempotency_key_id
     WHERE o.tenant_id=p_tenant AND o.id=p_order AND o.idempotency_key_id IS NOT NULL
       AND (k.id IS NULL OR o.business_request_id IS DISTINCT FROM o.idempotency_key_id
            OR k.resource_type<>'order' OR k.resource_id<>o.id
            OR k.actor_id IS DISTINCT FROM o.user_id
            OR k.scope IS DISTINCT FROM app.idempotency_actor_scope(
              CASE o.kind
                WHEN 'new' THEN 'order_create'
                WHEN 'renewal' THEN 'subscription_renewal_create'
              END,k.actor_id))
     LIMIT 1;
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'order/idempotency invariant: %',v_detail
        USING ERRCODE='foreign_key_violation';
    END IF;
  END IF;
  IF p_key IS NOT NULL THEN
    SELECT format('order-bound idempotency key %s has no reverse order link',k.id)
      INTO v_detail
      FROM public.idempotency_keys k
      LEFT JOIN public.orders o
        ON o.tenant_id=k.tenant_id AND o.id=k.resource_id
       AND o.idempotency_key_id=k.id
     WHERE k.tenant_id=p_tenant AND k.id=p_key AND k.resource_type='order'
       AND (o.id IS NULL OR o.business_request_id IS DISTINCT FROM o.idempotency_key_id
            OR o.user_id IS DISTINCT FROM k.actor_id
            OR k.scope IS DISTINCT FROM app.idempotency_actor_scope(
              CASE o.kind
                WHEN 'new' THEN 'order_create'
                WHEN 'renewal' THEN 'subscription_renewal_create'
              END,k.actor_id))
     LIMIT 1;
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'order/idempotency invariant: %',v_detail
        USING ERRCODE='foreign_key_violation';
    END IF;
  END IF;
END;
$$;

CREATE FUNCTION app.assert_idempotency_order_binding_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
  IF TG_TABLE_NAME='orders' THEN
    IF TG_OP<>'DELETE' THEN
      PERFORM app.assert_idempotency_order_binding(NEW.tenant_id,NEW.id,NEW.idempotency_key_id);
    END IF;
    IF TG_OP<>'INSERT' THEN
      PERFORM app.assert_idempotency_order_binding(OLD.tenant_id,OLD.id,OLD.idempotency_key_id);
    END IF;
  ELSE
    IF TG_OP<>'DELETE' THEN
      PERFORM app.assert_idempotency_order_binding(
        NEW.tenant_id,
        CASE WHEN NEW.resource_type='order' THEN NEW.resource_id ELSE NULL END,
        NEW.id);
    END IF;
    IF TG_OP<>'INSERT' THEN
      PERFORM app.assert_idempotency_order_binding(
        OLD.tenant_id,
        CASE WHEN OLD.resource_type='order' THEN OLD.resource_id ELSE NULL END,
        OLD.id);
    END IF;
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER trg_orders_idempotency_00037_commit
  AFTER INSERT OR UPDATE OR DELETE ON orders
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_idempotency_order_binding_trigger();
CREATE CONSTRAINT TRIGGER trg_idempotency_orders_00037_commit
  AFTER INSERT OR UPDATE OR DELETE ON idempotency_keys
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_idempotency_order_binding_trigger();

DROP POLICY tenant_isolation ON idempotency_keys;
CREATE POLICY idempotency_keys_actor_select ON idempotency_keys
  FOR SELECT USING (
    tenant_id=app.current_tenant_id() AND actor_id=app.current_actor_id()
    AND app.idempotency_scope_matches_actor(scope,actor_id)
  );
CREATE POLICY idempotency_keys_actor_insert ON idempotency_keys
  FOR INSERT WITH CHECK (
    tenant_id=app.current_tenant_id() AND actor_id=app.current_actor_id()
    AND app.idempotency_scope_matches_actor(scope,actor_id)
  );
CREATE POLICY idempotency_keys_actor_update ON idempotency_keys
  FOR UPDATE USING (
    tenant_id=app.current_tenant_id() AND actor_id=app.current_actor_id()
    AND app.idempotency_scope_matches_actor(scope,actor_id)
  ) WITH CHECK (
    tenant_id=app.current_tenant_id() AND actor_id=app.current_actor_id()
    AND app.idempotency_scope_matches_actor(scope,actor_id)
  );

REVOKE ALL ON FUNCTION app.idempotency_actor_scope(text,uuid) FROM PUBLIC, aegis_app;
REVOKE ALL ON FUNCTION app.idempotency_scope_matches_actor(text,uuid) FROM PUBLIC, aegis_app;
REVOKE ALL ON FUNCTION app.lookup_legacy_idempotency_key(uuid,text,text) FROM PUBLIC, aegis_app;
REVOKE ALL ON FUNCTION app.guard_refund_request() FROM PUBLIC, aegis_app;
REVOKE ALL ON FUNCTION app.assert_refund_request(uuid,uuid) FROM PUBLIC, aegis_app;
REVOKE ALL ON FUNCTION app.assert_refund_idempotency_key(uuid,uuid) FROM PUBLIC, aegis_app;
REVOKE ALL ON FUNCTION app.assert_refund_request_trigger() FROM PUBLIC, aegis_app;
REVOKE ALL ON FUNCTION app.assert_refund_idempotency_key_trigger() FROM PUBLIC, aegis_app;
REVOKE ALL ON FUNCTION app.assert_idempotency_order_binding(uuid,uuid,uuid) FROM PUBLIC, aegis_app;
REVOKE ALL ON FUNCTION app.assert_idempotency_order_binding_trigger() FROM PUBLIC, aegis_app;
GRANT EXECUTE ON FUNCTION app.idempotency_actor_scope(text,uuid) TO aegis_app;
GRANT EXECUTE ON FUNCTION app.idempotency_scope_matches_actor(text,uuid) TO aegis_app;
GRANT EXECUTE ON FUNCTION app.lookup_legacy_idempotency_key(uuid,text,text) TO aegis_app;

REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON idempotency_keys FROM aegis_app;
REVOKE INSERT (tenant_id,scope,idempotency_key,request_hash,status,actor_id,
               locked_until) ON idempotency_keys FROM aegis_app;
REVOKE UPDATE (status,response_code,response_body,locked_until,completed_at,
               resource_type,resource_id) ON idempotency_keys FROM aegis_app;
GRANT INSERT (tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
  ON idempotency_keys TO aegis_app;
GRANT UPDATE (status,claim_generation,response_code,response_format,response_payload,
              response_content_type,response_location,response_etag,
              response_cache_control,response_content_language,locked_until,
              completed_at,resource_type,resource_id)
  ON idempotency_keys TO aegis_app;

-- +goose StatementBegin
DO $$
DECLARE v_row record; v_policies text[]; v_owner oid;
BEGIN
  FOR v_row IN SELECT tenant_id,id FROM refund_requests LOOP
    PERFORM app.assert_refund_request(v_row.tenant_id,v_row.id);
  END LOOP;
  FOR v_row IN
    SELECT tenant_id,id FROM idempotency_keys
     WHERE scope='refund_create'
        OR (actor_id IS NOT NULL AND
            scope=app.idempotency_actor_scope('refund_create',actor_id))
  LOOP
    PERFORM app.assert_refund_idempotency_key(v_row.tenant_id,v_row.id);
  END LOOP;
  FOR v_row IN
    SELECT tenant_id,id,idempotency_key_id FROM orders
     WHERE idempotency_key_id IS NOT NULL
  LOOP
    PERFORM app.assert_idempotency_order_binding(
      v_row.tenant_id,v_row.id,v_row.idempotency_key_id);
  END LOOP;
  FOR v_row IN
    SELECT tenant_id,id,resource_id FROM idempotency_keys
     WHERE resource_type='order'
  LOOP
    PERFORM app.assert_idempotency_order_binding(
      v_row.tenant_id,v_row.resource_id,v_row.id);
  END LOOP;
  SELECT array_agg(polname ORDER BY polname) INTO v_policies
    FROM pg_policy WHERE polrelid='public.idempotency_keys'::regclass;
  SELECT relowner INTO v_owner FROM pg_class WHERE oid='public.idempotency_keys'::regclass;
  IF v_policies IS DISTINCT FROM ARRAY[
       'idempotency_keys_actor_insert','idempotency_keys_actor_select',
       'idempotency_keys_actor_update']::text[]
     OR NOT EXISTS (SELECT 1 FROM pg_class WHERE oid='public.idempotency_keys'::regclass
                    AND relrowsecurity AND relforcerowsecurity)
     OR EXISTS (
       SELECT 1 FROM pg_policy
        WHERE polrelid='public.idempotency_keys'::regclass
          AND (NOT polpermissive OR polroles<>ARRAY[0::oid]
               OR polcmd NOT IN ('r','a','w')
               OR position('idempotency_scope_matches_actor' in
                    coalesce(pg_get_expr(polqual,polrelid),'')||
                    coalesce(pg_get_expr(polwithcheck,polrelid),''))=0)
     )
     OR EXISTS (
       SELECT 1 FROM pg_constraint
        WHERE conrelid='public.idempotency_keys'::regclass
          AND conname LIKE 'idempotency_keys_%'
          AND NOT convalidated
     ) OR EXISTS (
       SELECT 1 FROM idempotency_keys
        WHERE claim_generation<>1
           OR response_format<>CASE WHEN status='in_flight' THEN 'none'
                                    WHEN response_body IS NOT NULL THEN 'legacy_json'
                                    ELSE 'legacy_empty' END
     )
      OR EXISTS (
        SELECT 1 FROM pg_proc p
       JOIN pg_roles owner_role ON owner_role.oid=p.proowner
        WHERE p.oid IN (
          'app.lookup_legacy_idempotency_key(uuid,text,text)'::regprocedure,
          'app.guard_refund_request()'::regprocedure,
          'app.assert_refund_request_trigger()'::regprocedure,
          'app.assert_refund_idempotency_key_trigger()'::regprocedure,
          'app.assert_idempotency_order_binding(uuid,uuid,uuid)'::regprocedure,
          'app.assert_idempotency_order_binding_trigger()'::regprocedure)
          AND (NOT p.prosecdef OR p.proowner<>v_owner
               OR NOT (owner_role.rolsuper OR owner_role.rolbypassrls)
                OR p.proconfig IS DISTINCT FROM ARRAY['search_path=pg_catalog'])
      )
      OR EXISTS (
        SELECT 1 FROM pg_proc p
         WHERE p.oid IN (
           'app.assert_refund_request(uuid,uuid)'::regprocedure,
           'app.assert_refund_idempotency_key(uuid,uuid)'::regprocedure)
           AND (p.prosecdef OR
                p.proconfig IS DISTINCT FROM ARRAY['search_path=pg_catalog'])
      )
     OR EXISTS (
       SELECT 1 FROM (VALUES
         ('trg_orders_idempotency_00037_commit','public.orders'::regclass),
         ('trg_idempotency_orders_00037_commit','public.idempotency_keys'::regclass)
       ) expected(name,relation)
       LEFT JOIN pg_trigger t ON t.tgname=expected.name AND t.tgrelid=expected.relation
      WHERE t.oid IS NULL OR t.tgenabled<>'O' OR NOT t.tgdeferrable OR NOT t.tginitdeferred
     )
     OR has_table_privilege('aegis_app','idempotency_keys','INSERT')
     OR has_table_privilege('aegis_app','idempotency_keys','UPDATE')
     OR has_table_privilege('aegis_app','idempotency_keys','DELETE')
     OR has_table_privilege('aegis_app','idempotency_keys','TRUNCATE')
     OR has_column_privilege('aegis_app','idempotency_keys','status','INSERT')
     OR has_column_privilege('aegis_app','idempotency_keys','response_body','UPDATE')
     OR NOT has_column_privilege('aegis_app','idempotency_keys','claim_generation','UPDATE')
     OR NOT has_function_privilege('aegis_app',
          'app.lookup_legacy_idempotency_key(uuid,text,text)','EXECUTE')
     OR has_function_privilege('public',
          'app.lookup_legacy_idempotency_key(uuid,text,text)','EXECUTE')
     OR has_function_privilege('aegis_app',
          'app.guard_refund_request()','EXECUTE')
     OR has_function_privilege('aegis_app',
          'app.assert_refund_request(uuid,uuid)','EXECUTE')
     OR has_function_privilege('aegis_app',
          'app.assert_refund_request_trigger()','EXECUTE')
     OR has_function_privilege('public',
          'app.assert_idempotency_order_binding(uuid,uuid,uuid)','EXECUTE')
     OR EXISTS (
          SELECT 1
            FROM pg_namespace n
            CROSS JOIN LATERAL aclexplode(
              COALESCE(n.nspacl,acldefault('n',n.nspowner))
            ) a
           WHERE n.nspname IN ('public','app')
             AND a.grantee=0 AND a.privilege_type='CREATE'
        )
     OR has_schema_privilege('aegis_app','public','CREATE')
     OR has_schema_privilege('aegis_app','app','CREATE') THEN
    RAISE EXCEPTION '00037 postcondition failed';
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
DECLARE v_detail text; v_later boolean:=false;
BEGIN
  IF current_setting('app.idempotency_writers_stopped',true) IS DISTINCT FROM 'yes'
     OR current_setting('app.idempotency_probe_callers_stopped',true) IS DISTINCT FROM 'yes'
     OR current_setting('app.allow_idempotency_schema37_down',true) IS DISTINCT FROM 'yes' THEN
    RAISE EXCEPTION '00037 Down requires explicit stopped-writer, stopped-probe and downgrade approval';
  END IF;
  IF to_regclass('public.goose_db_version') IS NOT NULL THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM public.goose_db_version WHERE version_id>37 AND is_applied)'
      INTO v_later;
  END IF;
  IF v_later THEN
    RAISE EXCEPTION '00037 Down refused because a later migration is applied';
  END IF;
  IF to_regclass('app.idempotency_runtime_00037_snapshot') IS NULL THEN
    RAISE EXCEPTION '00037 Down requires its intact pre-Up snapshot';
  END IF;
  SELECT format('idempotency row %s uses schema-37-only evidence or generation',id)
    INTO v_detail FROM idempotency_keys
   WHERE claim_generation<>1 OR response_format IN ('bytes','unavailable')
      OR response_payload IS NOT NULL OR response_content_type IS NOT NULL
      OR response_location IS NOT NULL OR response_etag IS NOT NULL
      OR response_cache_control IS NOT NULL OR response_content_language IS NOT NULL
   LIMIT 1;
  IF v_detail IS NOT NULL THEN RAISE EXCEPTION '00037 Down refused: %',v_detail; END IF;
  SELECT format('refund key %s uses an actor-bound scope that schema 36 cannot assert',k.id)
    INTO v_detail FROM idempotency_keys k
    JOIN refund_requests rr
      ON rr.tenant_id=k.tenant_id AND rr.business_request_id=k.id
   WHERE k.scope ~ ':actor:[0-9a-f]{24}$' LIMIT 1;
  IF v_detail IS NOT NULL THEN RAISE EXCEPTION '00037 Down refused: %',v_detail; END IF;
  IF EXISTS (
    SELECT 1
      FROM idempotency_keys k
      FULL JOIN app.idempotency_runtime_00037_snapshot s ON s.id=k.id
     WHERE k.id IS NULL OR s.id IS NULL
        OR (to_jsonb(k)-ARRAY['claim_generation','response_format','response_payload',
                              'response_content_type','response_location','response_etag',
                              'response_cache_control','response_content_language'])
           IS DISTINCT FROM s.row_data
  ) THEN
    RAISE EXCEPTION '00037 Down refused because idempotency evidence changed after Up';
  END IF;
END $$;
-- +goose StatementEnd

DROP POLICY idempotency_keys_actor_select ON idempotency_keys;
DROP POLICY idempotency_keys_actor_insert ON idempotency_keys;
DROP POLICY idempotency_keys_actor_update ON idempotency_keys;
CREATE POLICY tenant_isolation ON idempotency_keys
  USING (tenant_id=app.current_tenant_id())
  WITH CHECK (tenant_id=app.current_tenant_id());

-- Restore the exact schema-36 guard and refund function bodies.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_idempotency_evidence() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.status<>'in_flight' OR NEW.resource_type IS NOT NULL
       OR NEW.resource_id IS NOT NULL THEN
      RAISE EXCEPTION 'idempotency claims must start unbound and in_flight'
        USING ERRCODE='check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='succeeded' AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'successful idempotency evidence is immutable' USING ERRCODE='check_violation';
  END IF;
  IF OLD.status='failed' AND NEW.status='failed'
     AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'failed idempotency evidence changes only through controlled retry'
      USING ERRCODE='check_violation';
  END IF;
  IF (NEW.id,NEW.tenant_id,NEW.scope,NEW.idempotency_key,NEW.request_hash,
      NEW.actor_id,NEW.created_at,NEW.expires_at) IS DISTINCT FROM
     (OLD.id,OLD.tenant_id,OLD.scope,OLD.idempotency_key,OLD.request_hash,
      OLD.actor_id,OLD.created_at,OLD.expires_at) THEN
    RAISE EXCEPTION 'idempotency tenant/scope/key/hash/actor/time identity is immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.resource_type IS NOT NULL AND
     (NEW.resource_type,NEW.resource_id) IS DISTINCT FROM
     (OLD.resource_type,OLD.resource_id) THEN
    RAISE EXCEPTION 'idempotency resource binding is write-once' USING ERRCODE='check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='in_flight' AND NEW.status IN ('succeeded','failed'))
    OR (OLD.status='failed' AND NEW.status='in_flight')
  ) THEN
    RAISE EXCEPTION 'illegal idempotency transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app.guard_refund_request() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='UPDATE' AND
     (to_jsonb(NEW)-ARRAY['status','approval_request_id','updated_at']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['status','approval_request_id','updated_at']) THEN
    RAISE EXCEPTION 'refund request identity, amount, actor and business key are immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF TG_OP='UPDATE' AND OLD.status='succeeded'
     AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'terminal refund request is immutable' USING ERRCODE='check_violation';
  END IF;
  IF TG_OP='UPDATE' AND NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='pending' AND NEW.status IN ('processing','failed','manual_review'))
    OR (OLD.status='processing' AND NEW.status IN
          ('succeeded','failed','manual_review','partially_succeeded'))
    OR (OLD.status='failed' AND NEW.status='processing')
    OR (OLD.status IN ('manual_review','partially_succeeded') AND
        NEW.status IN ('processing','succeeded','failed','partially_succeeded','manual_review'))
  ) THEN
    RAISE EXCEPTION 'illegal refund request transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM idempotency_keys k
     WHERE k.tenant_id=NEW.tenant_id AND k.id=NEW.business_request_id
       AND k.scope='refund_create' AND k.resource_type='refund_request'
       AND k.resource_id=NEW.id AND k.status IN ('in_flight','succeeded','failed')
       AND k.request_hash=NEW.request_hash
       AND k.actor_id IS NOT DISTINCT FROM NEW.requested_by
  ) THEN
    RAISE EXCEPTION 'refund request requires a bound refund_create idempotency record'
      USING ERRCODE='foreign_key_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app.assert_refund_request(p_tenant uuid,p_request uuid)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE v_detail text;
BEGIN
  SELECT format('request %s status %s/key %s has %s legs/%s amount/%s succeeded',
                rr.id,rr.status,k.status,count(r.id),coalesce(sum(r.amount),0),
                count(r.id) FILTER (WHERE r.status='succeeded'))
    INTO v_detail FROM refund_requests rr
    LEFT JOIN idempotency_keys k ON k.tenant_id=rr.tenant_id AND k.id=rr.business_request_id
    LEFT JOIN refunds r ON r.tenant_id=rr.tenant_id AND r.refund_request_id=rr.id
   WHERE rr.tenant_id=p_tenant AND rr.id=p_request
   GROUP BY rr.id,rr.status,rr.requested_amount,k.id,k.status,k.scope,
            k.request_hash,k.actor_id,k.resource_type,k.resource_id
  HAVING k.id IS NULL OR k.scope<>'refund_create'
      OR k.request_hash<>rr.request_hash OR k.actor_id IS DISTINCT FROM rr.requested_by
      OR k.resource_type<>'refund_request' OR k.resource_id<>rr.id
      OR (rr.status IN ('pending','processing','manual_review','partially_succeeded') AND k.status<>'in_flight')
      OR (rr.status='succeeded' AND k.status<>'succeeded')
      OR (rr.status='failed' AND k.status<>'failed')
      OR count(r.id)=0 OR coalesce(sum(r.amount),0)<>rr.requested_amount
      OR (rr.status='succeeded' AND count(r.id) FILTER (WHERE r.status='succeeded')<>count(r.id))
      OR (rr.status='failed' AND count(r.id) FILTER (WHERE r.status IN ('failed','rejected'))<>count(r.id))
      OR (rr.status IN ('pending','processing') AND count(r.id) FILTER (WHERE r.status IN ('succeeded','failed','rejected'))>0)
      OR (rr.status IN ('manual_review','partially_succeeded') AND
          (count(r.id) FILTER (WHERE r.status='succeeded')=0
           OR count(r.id) FILTER (WHERE r.status='succeeded')=count(r.id)));
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'refund request invariant: %',v_detail USING ERRCODE='check_violation';
  END IF;
END;
$$;

CREATE OR REPLACE FUNCTION app.assert_refund_idempotency_key(p_tenant uuid,p_key uuid)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE v_detail text;
BEGIN
  SELECT format('refund key %s status %s is not bidirectionally bound',k.id,k.status)
    INTO v_detail FROM idempotency_keys k
    LEFT JOIN refund_requests rr ON rr.tenant_id=k.tenant_id AND rr.business_request_id=k.id
   WHERE k.tenant_id=p_tenant AND k.id=p_key AND k.scope='refund_create'
   GROUP BY k.id,k.status,k.request_hash,k.actor_id,k.resource_type,k.resource_id,
            rr.id,rr.status,rr.request_hash,rr.requested_by
  HAVING rr.id IS NULL OR k.request_hash<>rr.request_hash
      OR k.actor_id IS DISTINCT FROM rr.requested_by
      OR k.resource_type<>'refund_request' OR k.resource_id<>rr.id
      OR (rr.status IN ('pending','processing','manual_review','partially_succeeded') AND k.status<>'in_flight')
      OR (rr.status='succeeded' AND k.status<>'succeeded')
      OR (rr.status='failed' AND k.status<>'failed');
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'refund idempotency invariant: %',v_detail USING ERRCODE='check_violation';
  END IF;
END;
$$;

CREATE OR REPLACE FUNCTION app.assert_refund_request_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY INVOKER AS $$
DECLARE
  v_tenant uuid;
  v_request uuid;
BEGIN
  IF TG_OP<>'DELETE' THEN
    v_tenant:=NEW.tenant_id;
    IF TG_TABLE_NAME='refund_requests' THEN v_request:=NEW.id;
    ELSE v_request:=NEW.refund_request_id;
    END IF;
    PERFORM app.assert_refund_request(v_tenant,v_request);
  END IF;
  IF TG_OP<>'INSERT' THEN
    v_tenant:=OLD.tenant_id;
    IF TG_TABLE_NAME='refund_requests' THEN v_request:=OLD.id;
    ELSE v_request:=OLD.refund_request_id;
    END IF;
    PERFORM app.assert_refund_request(v_tenant,v_request);
  END IF;
  RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION app.assert_refund_idempotency_key_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY INVOKER AS $$
BEGIN
  IF TG_OP<>'DELETE' THEN
    PERFORM app.assert_refund_idempotency_key(NEW.tenant_id,NEW.id);
  END IF;
  IF TG_OP<>'INSERT' THEN
    PERFORM app.assert_refund_idempotency_key(OLD.tenant_id,OLD.id);
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

GRANT EXECUTE ON FUNCTION app.guard_refund_request() TO PUBLIC,aegis_app;
GRANT EXECUTE ON FUNCTION app.assert_refund_request(uuid,uuid) TO PUBLIC,aegis_app;
GRANT EXECUTE ON FUNCTION app.assert_refund_idempotency_key(uuid,uuid) TO PUBLIC,aegis_app;
GRANT EXECUTE ON FUNCTION app.assert_refund_request_trigger() TO PUBLIC,aegis_app;
GRANT EXECUTE ON FUNCTION app.assert_refund_idempotency_key_trigger() TO PUBLIC,aegis_app;

REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON idempotency_keys FROM aegis_app;
REVOKE INSERT (tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
  ON idempotency_keys FROM aegis_app;
REVOKE UPDATE (status,claim_generation,response_code,response_format,response_payload,
               response_content_type,response_location,response_etag,
               response_cache_control,response_content_language,locked_until,
               completed_at,resource_type,resource_id)
  ON idempotency_keys FROM aegis_app;
GRANT INSERT (tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
  ON idempotency_keys TO aegis_app;
GRANT UPDATE (status,response_code,response_body,locked_until,completed_at,
              resource_type,resource_id) ON idempotency_keys TO aegis_app;

REVOKE ALL ON FUNCTION app.lookup_legacy_idempotency_key(uuid,text,text) FROM PUBLIC,aegis_app;
REVOKE ALL ON FUNCTION app.idempotency_scope_matches_actor(text,uuid) FROM PUBLIC,aegis_app;
REVOKE ALL ON FUNCTION app.idempotency_actor_scope(text,uuid) FROM PUBLIC,aegis_app;

DROP TRIGGER trg_idempotency_orders_00037_commit ON idempotency_keys;
DROP TRIGGER trg_orders_idempotency_00037_commit ON orders;
DROP INDEX idempotency_keys_one_order_resource_00037;
DROP FUNCTION app.assert_idempotency_order_binding_trigger();
DROP FUNCTION app.assert_idempotency_order_binding(uuid,uuid,uuid);

ALTER TABLE idempotency_keys
  DROP CONSTRAINT idempotency_keys_runtime_evidence_shape,
  DROP CONSTRAINT idempotency_keys_actor_scope_shape,
  DROP CONSTRAINT idempotency_keys_response_format,
  DROP CONSTRAINT idempotency_keys_claim_generation,
  DROP COLUMN response_content_language,
  DROP COLUMN response_cache_control,
  DROP COLUMN response_etag,
  DROP COLUMN response_location,
  DROP COLUMN response_content_type,
  DROP COLUMN response_payload,
  DROP COLUMN response_format,
  DROP COLUMN claim_generation;

DROP FUNCTION app.lookup_legacy_idempotency_key(uuid,text,text);
DROP FUNCTION app.idempotency_scope_matches_actor(text,uuid);
DROP FUNCTION app.idempotency_actor_scope(text,uuid);
DROP TABLE app.idempotency_runtime_00037_snapshot;
