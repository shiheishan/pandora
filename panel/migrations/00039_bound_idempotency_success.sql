-- Complete an already-bound idempotency claim in the same transaction as its
-- business resource. The runtime role receives EXECUTE only; the narrowly
-- privileged owner performs the full-tuple terminal CAS.

-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
DECLARE
  v_owner oid;
  v_binder oid;
  v_later boolean := false;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock(390039,1);
  IF current_setting('app.idempotency_writers_stopped',true) IS DISTINCT FROM 'yes'
     OR current_setting('app.allow_idempotency_schema39_up',true) IS DISTINCT FROM 'yes' THEN
    RAISE EXCEPTION '00039 Up requires explicit stopped-writer and upgrade approval';
  END IF;
  IF to_regclass('public.goose_db_version') IS NOT NULL THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM public.goose_db_version WHERE version_id>39 AND is_applied)'
      INTO v_later;
  END IF;
  IF v_later THEN
    RAISE EXCEPTION '00039 Up refused because a later migration is applied';
  END IF;
  IF to_regprocedure(
       'app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)'
     ) IS NOT NULL
     OR to_regclass('app.bound_idempotency_success_00039_usage') IS NOT NULL THEN
    RAISE EXCEPTION '00039 found a partial or repeated completion migration';
  END IF;

  SELECT oid INTO v_owner FROM pg_catalog.pg_roles
   WHERE rolname='aegis_idempotency_owner'
     AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb
     AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication
     AND NOT rolbypassrls AND rolconnlimit=-1 AND rolvaliduntil IS NULL;
  SELECT oid INTO v_binder FROM pg_catalog.pg_proc
   WHERE oid=
     'app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'::regprocedure
     AND proowner=v_owner AND prosecdef AND provolatile='v'
     AND proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog'];
  IF v_owner IS NULL OR v_binder IS NULL
     OR to_regclass('app.idempotency_resource_binding_00038_usage') IS NULL
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members m
                 WHERE m.roleid=v_owner OR m.member=v_owner)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_db_role_setting s
                 WHERE s.setrole=v_owner)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_class c WHERE c.relowner=v_owner)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc p
                 WHERE p.proowner=v_owner AND p.oid<>v_binder)
     OR has_table_privilege('aegis_idempotency_owner','public.idempotency_keys','UPDATE')
     OR has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','status','UPDATE')
     OR NOT has_function_privilege('aegis_app',v_binder,'EXECUTE')
     OR has_function_privilege('public',v_binder,'EXECUTE') THEN
    RAISE EXCEPTION '00039 requires the exact schema-38 owner and binder state';
  END IF;
END $$;
-- +goose StatementEnd

CREATE TABLE app.bound_idempotency_success_00039_usage (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  used boolean NOT NULL DEFAULT false,
  first_completed_at timestamptz,
  CHECK ((used AND first_completed_at IS NOT NULL)
      OR (NOT used AND first_completed_at IS NULL))
);
INSERT INTO app.bound_idempotency_success_00039_usage(singleton) VALUES (true);
REVOKE ALL ON app.bound_idempotency_success_00039_usage
  FROM PUBLIC,aegis_app,aegis_idempotency_owner;

-- +goose StatementBegin
CREATE FUNCTION app.complete_bound_idempotency_success(
  p_claim_id uuid,
  p_tenant_id uuid,
  p_actor_id uuid,
  p_base_scope text,
  p_storage_scope text,
  p_idempotency_key text,
  p_request_hash bytea,
  p_claim_generation bigint,
  p_locked_until timestamptz,
  p_resource_type text,
  p_resource_id uuid,
  p_response_code integer,
  p_response_payload bytea,
  p_response_content_type text,
  p_response_location text,
  p_response_etag text,
  p_response_cache_control text,
  p_response_content_language text
) RETURNS TABLE(completed_claim_id uuid)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_completed uuid;
BEGIN
  IF session_user<>'aegis_app'
     OR app.current_tenant_id() IS DISTINCT FROM p_tenant_id
     OR app.current_actor_id() IS DISTINCT FROM p_actor_id THEN
    RAISE EXCEPTION 'idempotency success completer session mismatch'
      USING ERRCODE='insufficient_privilege';
  END IF;
  IF p_claim_id IS NULL OR p_tenant_id IS NULL OR p_actor_id IS NULL
     OR p_resource_id IS NULL OR p_locked_until IS NULL
     OR NOT isfinite(p_locked_until)
     OR p_base_scope IS NULL OR p_base_scope !~ '^[a-z0-9_]{1,64}$'
     OR p_storage_scope IS NULL OR octet_length(p_storage_scope) NOT BETWEEN 1 AND 255
     OR p_idempotency_key IS NULL OR octet_length(p_idempotency_key) NOT BETWEEN 1 AND 255
     OR p_idempotency_key ~ '[[:cntrl:]]'
     OR p_request_hash IS NULL OR octet_length(p_request_hash)<>32
     OR p_claim_generation IS NULL OR p_claim_generation<1
     OR p_claim_generation>=9223372036854775807
     OR p_response_code IS NULL OR p_response_code NOT BETWEEN 200 AND 299
     OR p_response_payload IS NULL OR octet_length(p_response_payload)>1048576
     OR p_resource_type IS NULL
     OR NOT (
       (p_resource_type='order' AND p_base_scope IN
          ('order_create','subscription_renewal_create','balance_topup_create'))
       OR (p_resource_type='refund_request' AND p_base_scope='refund_create')
     )
     OR (p_response_content_type IS NOT NULL AND
         (octet_length(p_response_content_type)>255
          OR p_response_content_type ~ '[[:cntrl:]]'))
     OR (p_response_location IS NOT NULL AND
         (octet_length(p_response_location)>2048
          OR p_response_location ~ '[[:cntrl:]]'
          OR position(E'\\' in p_response_location)<>0
          OR left(p_response_location,1)<>'/'
          OR left(p_response_location,2)='//'))
     OR (p_response_etag IS NOT NULL AND
         (octet_length(p_response_etag)>255 OR p_response_etag ~ '[[:cntrl:]]'))
     OR (p_response_cache_control IS NOT NULL AND
         (octet_length(p_response_cache_control)>512
          OR p_response_cache_control ~ '[[:cntrl:]]'))
     OR (p_response_content_language IS NOT NULL AND
         (octet_length(p_response_content_language)>128
          OR p_response_content_language ~ '[[:cntrl:]]')) THEN
    RAISE EXCEPTION 'invalid idempotency success completion input'
      USING ERRCODE='invalid_parameter_value';
  END IF;

  UPDATE public.idempotency_keys k
     SET status='succeeded',
         response_code=p_response_code,
         response_format='bytes',
         response_payload=p_response_payload,
         response_content_type=p_response_content_type,
         response_location=p_response_location,
         response_etag=p_response_etag,
         response_cache_control=p_response_cache_control,
         response_content_language=p_response_content_language,
         completed_at=clock_timestamp(),
         locked_until=NULL
   WHERE k.id=p_claim_id
     AND k.tenant_id=p_tenant_id
     AND k.actor_id=p_actor_id
     AND k.scope=p_storage_scope
     AND k.scope=app.idempotency_actor_scope(p_base_scope,p_actor_id)
     AND k.idempotency_key=p_idempotency_key
     AND k.request_hash=p_request_hash
     AND k.claim_generation=p_claim_generation
     AND k.locked_until=p_locked_until
     AND k.status='in_flight'
     AND k.response_code IS NULL
     AND k.response_body IS NULL
     AND k.response_format='none'
     AND k.response_payload IS NULL
     AND k.response_content_type IS NULL
     AND k.response_location IS NULL
     AND k.response_etag IS NULL
     AND k.response_cache_control IS NULL
     AND k.response_content_language IS NULL
     AND k.completed_at IS NULL
     AND k.resource_type=p_resource_type
     AND k.resource_id=p_resource_id
  RETURNING k.id INTO v_completed;

  IF v_completed IS NULL THEN
    RETURN;
  END IF;

  UPDATE app.bound_idempotency_success_00039_usage
     SET used=true,first_completed_at=coalesce(first_completed_at,clock_timestamp())
   WHERE singleton=true;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'idempotency completion usage watermark is missing'
      USING ERRCODE='object_not_in_prerequisite_state';
  END IF;
  RETURN QUERY SELECT v_completed;
END;
$$;
-- +goose StatementEnd

ALTER FUNCTION app.complete_bound_idempotency_success(
  uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,
  integer,bytea,text,text,text,text,text
) OWNER TO aegis_idempotency_owner;
REVOKE ALL ON FUNCTION app.complete_bound_idempotency_success(
  uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,
  integer,bytea,text,text,text,text,text
) FROM PUBLIC,aegis_app;
GRANT EXECUTE ON FUNCTION app.complete_bound_idempotency_success(
  uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,
  integer,bytea,text,text,text,text,text
) TO aegis_app;

GRANT UPDATE (
  status,response_code,response_format,response_payload,response_content_type,
  response_location,response_etag,response_cache_control,
  response_content_language,completed_at,locked_until
) ON public.idempotency_keys TO aegis_idempotency_owner;
GRANT SELECT (
  response_code,response_body,response_format,response_payload,
  response_content_type,response_location,response_etag,response_cache_control,
  response_content_language,completed_at
) ON public.idempotency_keys TO aegis_idempotency_owner;
GRANT SELECT (singleton,used,first_completed_at),
      UPDATE (used,first_completed_at)
  ON app.bound_idempotency_success_00039_usage TO aegis_idempotency_owner;

-- +goose StatementBegin
DO $$
DECLARE
  v_owner oid;
  v_binder oid;
  v_completer oid;
BEGIN
  SELECT oid INTO v_owner FROM pg_catalog.pg_roles
   WHERE rolname='aegis_idempotency_owner';
  SELECT oid INTO v_binder FROM pg_catalog.pg_proc
   WHERE oid=
     'app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'::regprocedure;
  SELECT oid INTO v_completer FROM pg_catalog.pg_proc
   WHERE oid=
     'app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)'::regprocedure
     AND proowner=v_owner AND prosecdef AND provolatile='v'
     AND proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog'];
  IF v_owner IS NULL OR v_binder IS NULL OR v_completer IS NULL
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc p
                 WHERE p.proowner=v_owner AND p.oid NOT IN (v_binder,v_completer))
     OR has_function_privilege('public',v_completer,'EXECUTE')
     OR NOT has_function_privilege('aegis_app',v_completer,'EXECUTE')
     OR has_table_privilege('aegis_idempotency_owner','public.idempotency_keys','UPDATE')
     OR NOT has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','status','UPDATE')
     OR NOT has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','locked_until','UPDATE')
     OR NOT has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','response_body','SELECT')
     OR has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','response_body','UPDATE')
     OR (SELECT count(*) FROM app.bound_idempotency_success_00039_usage)<>1
     OR NOT EXISTS (
       SELECT 1 FROM app.bound_idempotency_success_00039_usage
        WHERE singleton=true AND NOT used AND first_completed_at IS NULL
     ) THEN
    RAISE EXCEPTION '00039 owner, function, privilege or watermark postcondition failed';
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
DECLARE
  v_owner oid;
  v_binder oid;
  v_completer oid;
  v_later boolean := false;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock(390039,1);
  IF current_setting('app.idempotency_writers_stopped',true) IS DISTINCT FROM 'yes'
     OR current_setting('app.idempotency_completer_callers_stopped',true) IS DISTINCT FROM 'yes'
     OR current_setting('app.allow_idempotency_schema39_down',true) IS DISTINCT FROM 'yes' THEN
    RAISE EXCEPTION '00039 Down requires stopped writers, stopped completer callers and downgrade approval';
  END IF;
  IF to_regclass('public.goose_db_version') IS NOT NULL THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM public.goose_db_version WHERE version_id>39 AND is_applied)'
      INTO v_later;
  END IF;
  IF v_later THEN
    RAISE EXCEPTION '00039 Down refused because a later migration is applied';
  END IF;
  IF to_regclass('app.bound_idempotency_success_00039_usage') IS NULL THEN
    RAISE EXCEPTION '00039 Down requires an intact usage watermark';
  END IF;

  LOCK TABLE public.idempotency_keys IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE app.bound_idempotency_success_00039_usage IN ACCESS EXCLUSIVE MODE;

  SELECT oid INTO v_owner FROM pg_catalog.pg_roles
   WHERE rolname='aegis_idempotency_owner';
  SELECT oid INTO v_binder FROM pg_catalog.pg_proc
   WHERE oid=
     'app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'::regprocedure;
  SELECT oid INTO v_completer FROM pg_catalog.pg_proc
   WHERE oid=
     'app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)'::regprocedure
     AND proowner=v_owner AND prosecdef AND provolatile='v'
     AND proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog'];
  IF v_owner IS NULL OR v_binder IS NULL OR v_completer IS NULL
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc p
                 WHERE p.proowner=v_owner AND p.oid NOT IN (v_binder,v_completer))
     OR has_function_privilege('public',v_completer,'EXECUTE')
     OR NOT has_function_privilege('aegis_app',v_completer,'EXECUTE')
     OR (SELECT count(*) FROM app.bound_idempotency_success_00039_usage)<>1
     OR NOT EXISTS (
       SELECT 1 FROM app.bound_idempotency_success_00039_usage
        WHERE singleton=true AND NOT used AND first_completed_at IS NULL
     ) THEN
    RAISE EXCEPTION '00039 Down refused used or unexpected completion state';
  END IF;
END $$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.complete_bound_idempotency_success(
  uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,
  integer,bytea,text,text,text,text,text
) FROM PUBLIC,aegis_app;
DROP FUNCTION app.complete_bound_idempotency_success(
  uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,
  integer,bytea,text,text,text,text,text
);

REVOKE UPDATE (
  status,response_code,response_format,response_payload,response_content_type,
  response_location,response_etag,response_cache_control,
  response_content_language,completed_at,locked_until
) ON public.idempotency_keys FROM aegis_idempotency_owner;
REVOKE SELECT (
  response_code,response_body,response_format,response_payload,
  response_content_type,response_location,response_etag,response_cache_control,
  response_content_language,completed_at
) ON public.idempotency_keys FROM aegis_idempotency_owner;
REVOKE SELECT,UPDATE ON app.bound_idempotency_success_00039_usage
  FROM aegis_idempotency_owner;
DROP TABLE app.bound_idempotency_success_00039_usage;

-- +goose StatementBegin
DO $$
DECLARE
  v_owner oid;
  v_binder oid;
BEGIN
  SELECT oid INTO v_owner FROM pg_catalog.pg_roles
   WHERE rolname='aegis_idempotency_owner';
  SELECT oid INTO v_binder FROM pg_catalog.pg_proc
   WHERE oid=
     'app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'::regprocedure
     AND proowner=v_owner AND prosecdef AND provolatile='v'
     AND proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog'];
  IF v_owner IS NULL OR v_binder IS NULL
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc p
                 WHERE p.proowner=v_owner AND p.oid<>v_binder)
     OR has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','status','UPDATE')
     OR has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','response_body','SELECT')
     OR NOT has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','resource_type','UPDATE') THEN
    RAISE EXCEPTION '00039 Down failed to restore the exact schema-38 owner state';
  END IF;
END $$;
-- +goose StatementEnd
