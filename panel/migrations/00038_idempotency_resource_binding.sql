-- Full-tuple, least-privilege idempotency resource binding.
-- Commerce writers remain stopped until they consume this binder and the
-- existing deferred order/refund symmetry constraints in one transaction.

-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
DECLARE
  v_owner oid;
  v_later boolean := false;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock(380038,1);
  IF current_setting('app.idempotency_writers_stopped',true) IS DISTINCT FROM 'yes'
     OR current_setting('app.allow_idempotency_schema38_up',true) IS DISTINCT FROM 'yes' THEN
    RAISE EXCEPTION '00038 Up requires explicit stopped-writer and upgrade approval';
  END IF;
  IF to_regclass('public.goose_db_version') IS NOT NULL THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM public.goose_db_version WHERE version_id>38 AND is_applied)'
      INTO v_later;
  END IF;
  IF v_later THEN
    RAISE EXCEPTION '00038 Up refused because a later migration is applied';
  END IF;
  IF to_regprocedure('app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)')
       IS NOT NULL
     OR to_regclass('app.idempotency_resource_binding_00038_usage') IS NOT NULL THEN
    RAISE EXCEPTION '00038 found a partial or repeated resource-binding migration';
  END IF;
  IF to_regprocedure('app.idempotency_actor_scope(text,uuid)') IS NULL
     OR to_regprocedure('app.idempotency_scope_matches_actor(text,uuid)') IS NULL
     OR to_regprocedure('app.assert_idempotency_order_binding(uuid,uuid,uuid)') IS NULL
     OR NOT EXISTS (
       SELECT 1 FROM pg_catalog.pg_class
        WHERE oid='public.idempotency_keys'::regclass
          AND relrowsecurity AND relforcerowsecurity
     )
     OR NOT EXISTS (
       SELECT 1 FROM information_schema.columns
        WHERE table_schema='public' AND table_name='idempotency_keys'
          AND column_name='claim_generation' AND data_type='bigint'
     ) THEN
    RAISE EXCEPTION '00038 requires the complete schema-37 runtime';
  END IF;
  IF NOT has_column_privilege('aegis_app','public.idempotency_keys','resource_type','UPDATE')
     OR NOT has_column_privilege('aegis_app','public.idempotency_keys','resource_id','UPDATE')
     OR has_table_privilege('aegis_app','public.idempotency_keys','UPDATE') THEN
    RAISE EXCEPTION '00038 requires the exact schema-37 resource-column ACL';
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname='aegis_idempotency_owner') THEN
    CREATE ROLE aegis_idempotency_owner
      NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS;
  END IF;
  SELECT oid INTO v_owner FROM pg_catalog.pg_roles
   WHERE rolname='aegis_idempotency_owner'
     AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb
     AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication
     AND NOT rolbypassrls AND rolconnlimit=-1 AND rolvaliduntil IS NULL;
  IF v_owner IS NULL THEN
    RAISE EXCEPTION '00038 refused an unsafe pre-existing aegis_idempotency_owner';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_authid
     WHERE oid=v_owner AND rolpassword IS NOT NULL
  ) THEN
    RAISE EXCEPTION '00038 refused an owner role with a stored password';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_auth_members m
     WHERE m.roleid=v_owner OR m.member=v_owner
  ) OR EXISTS (
    SELECT 1 FROM pg_catalog.pg_shdepend d WHERE d.refobjid=v_owner
  ) OR EXISTS (
    SELECT 1 FROM pg_catalog.pg_db_role_setting s WHERE s.setrole=v_owner
  ) THEN
    RAISE EXCEPTION '00038 refused owner memberships, settings or pre-existing dependencies';
  END IF;
END $$;
-- +goose StatementEnd

CREATE TABLE app.idempotency_resource_binding_00038_usage (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  used boolean NOT NULL DEFAULT false,
  first_bound_at timestamptz,
  CHECK ((used AND first_bound_at IS NOT NULL)
      OR (NOT used AND first_bound_at IS NULL))
);
INSERT INTO app.idempotency_resource_binding_00038_usage(singleton) VALUES (true);
REVOKE ALL ON app.idempotency_resource_binding_00038_usage
  FROM PUBLIC,aegis_app,aegis_idempotency_owner;

-- Top-up orders use the existing order resource type, but their base scope is
-- distinct. Other order kinds remain intentionally unmapped and fail closed.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.assert_idempotency_order_binding(
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
                WHEN 'topup' THEN 'balance_topup_create'
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
                WHEN 'topup' THEN 'balance_topup_create'
              END,k.actor_id))
     LIMIT 1;
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'order/idempotency invariant: %',v_detail
        USING ERRCODE='foreign_key_violation';
    END IF;
  END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.bind_idempotency_resource(
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
  p_resource_id uuid
) RETURNS TABLE(bound_claim_id uuid)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_bound uuid;
BEGIN
  IF session_user<>'aegis_app'
     OR app.current_tenant_id() IS NULL
     OR app.current_actor_id() IS NULL THEN
    RAISE EXCEPTION 'idempotency resource binder session mismatch'
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
     OR p_resource_type IS NULL
     OR NOT (
       (p_resource_type='order' AND p_base_scope IN
          ('order_create','subscription_renewal_create','balance_topup_create'))
       OR (p_resource_type='refund_request' AND p_base_scope='refund_create')
     ) THEN
    RAISE EXCEPTION 'invalid idempotency resource binding input'
      USING ERRCODE='invalid_parameter_value';
  END IF;

  UPDATE public.idempotency_keys k
     SET resource_type=p_resource_type,resource_id=p_resource_id
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
     AND k.resource_type IS NULL AND k.resource_id IS NULL
  RETURNING k.id INTO v_bound;

  IF v_bound IS NULL THEN
    RETURN;
  END IF;

  UPDATE app.idempotency_resource_binding_00038_usage
     SET used=true,first_bound_at=coalesce(first_bound_at,clock_timestamp())
   WHERE singleton=true;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'idempotency resource binding usage watermark is missing'
      USING ERRCODE='object_not_in_prerequisite_state';
  END IF;
  RETURN QUERY SELECT v_bound;
END;
$$;
-- +goose StatementEnd

ALTER FUNCTION app.bind_idempotency_resource(
  uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid
) OWNER TO aegis_idempotency_owner;
REVOKE ALL ON FUNCTION app.bind_idempotency_resource(
  uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid
) FROM PUBLIC,aegis_app;
GRANT EXECUTE ON FUNCTION app.bind_idempotency_resource(
  uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid
) TO aegis_app;

GRANT USAGE ON SCHEMA public,app TO aegis_idempotency_owner;
GRANT EXECUTE ON FUNCTION app.current_tenant_id() TO aegis_idempotency_owner;
GRANT EXECUTE ON FUNCTION app.current_actor_id() TO aegis_idempotency_owner;
GRANT EXECUTE ON FUNCTION app.idempotency_actor_scope(text,uuid)
  TO aegis_idempotency_owner;
GRANT EXECUTE ON FUNCTION app.idempotency_scope_matches_actor(text,uuid)
  TO aegis_idempotency_owner;
GRANT SELECT (
  id,tenant_id,actor_id,scope,idempotency_key,request_hash,status,
  claim_generation,locked_until,resource_type,resource_id
) ON public.idempotency_keys TO aegis_idempotency_owner;
GRANT UPDATE (resource_type,resource_id)
  ON public.idempotency_keys TO aegis_idempotency_owner;
GRANT SELECT (singleton,used,first_bound_at), UPDATE (used,first_bound_at)
  ON app.idempotency_resource_binding_00038_usage TO aegis_idempotency_owner;

REVOKE UPDATE (resource_type,resource_id)
  ON public.idempotency_keys FROM aegis_app;

-- +goose StatementBegin
DO $$
DECLARE
  v_owner oid;
  v_proc oid;
  v_row record;
BEGIN
  SELECT oid INTO v_owner FROM pg_catalog.pg_roles
   WHERE rolname='aegis_idempotency_owner'
     AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb
     AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication
     AND NOT rolbypassrls;
  SELECT oid INTO v_proc FROM pg_catalog.pg_proc
   WHERE oid='app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'::regprocedure
     AND proowner=v_owner AND prosecdef AND provolatile='v'
     AND proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog'];
  IF v_owner IS NULL OR v_proc IS NULL
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members m
                 WHERE m.roleid=v_owner OR m.member=v_owner)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_class c WHERE c.relowner=v_owner)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc p
                 WHERE p.proowner=v_owner AND p.oid<>v_proc)
     OR has_function_privilege('public',v_proc,'EXECUTE')
     OR NOT has_function_privilege('aegis_app',v_proc,'EXECUTE')
     OR has_table_privilege('aegis_app','public.idempotency_keys','UPDATE')
     OR has_column_privilege('aegis_app','public.idempotency_keys','resource_type','UPDATE')
     OR has_column_privilege('aegis_app','public.idempotency_keys','resource_id','UPDATE')
     OR has_table_privilege('aegis_idempotency_owner','public.idempotency_keys','SELECT')
     OR has_table_privilege('aegis_idempotency_owner','public.idempotency_keys','UPDATE')
     OR has_table_privilege('aegis_idempotency_owner','public.idempotency_keys','INSERT')
     OR has_table_privilege('aegis_idempotency_owner','public.idempotency_keys','DELETE')
     OR NOT has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','request_hash','SELECT')
     OR NOT has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','resource_type','UPDATE')
     OR NOT has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','resource_id','UPDATE')
     OR NOT has_function_privilege('aegis_idempotency_owner',
          'app.idempotency_scope_matches_actor(text,uuid)','EXECUTE')
     OR EXISTS (SELECT 1 FROM app.idempotency_resource_binding_00038_usage
                 WHERE singleton IS DISTINCT FROM true OR used
                    OR first_bound_at IS NOT NULL) THEN
    RAISE EXCEPTION '00038 owner, function, watermark or ACL postcondition failed';
  END IF;

  FOR v_row IN
    SELECT tenant_id,id,idempotency_key_id FROM public.orders
     WHERE idempotency_key_id IS NOT NULL
  LOOP
    PERFORM app.assert_idempotency_order_binding(
      v_row.tenant_id,v_row.id,v_row.idempotency_key_id);
  END LOOP;
  FOR v_row IN
    SELECT tenant_id,id,resource_id FROM public.idempotency_keys
     WHERE resource_type='order'
  LOOP
    PERFORM app.assert_idempotency_order_binding(
      v_row.tenant_id,v_row.resource_id,v_row.id);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
DECLARE
  v_owner oid;
  v_proc oid;
  v_later boolean := false;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock(380038,1);
  IF current_setting('app.idempotency_writers_stopped',true) IS DISTINCT FROM 'yes'
     OR current_setting('app.idempotency_binder_callers_stopped',true) IS DISTINCT FROM 'yes'
     OR current_setting('app.allow_idempotency_schema38_down',true) IS DISTINCT FROM 'yes' THEN
    RAISE EXCEPTION '00038 Down requires stopped writers, stopped binder callers and downgrade approval';
  END IF;
  IF to_regclass('public.goose_db_version') IS NOT NULL THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM public.goose_db_version WHERE version_id>38 AND is_applied)'
      INTO v_later;
  END IF;
  IF v_later THEN
    RAISE EXCEPTION '00038 Down refused because a later migration is applied';
  END IF;
  IF to_regclass('app.idempotency_resource_binding_00038_usage') IS NULL
     OR to_regprocedure('app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)')
          IS NULL THEN
    RAISE EXCEPTION '00038 Down requires intact schema-38 objects';
  END IF;

  LOCK TABLE public.idempotency_keys IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE app.idempotency_resource_binding_00038_usage IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE public.orders IN ACCESS EXCLUSIVE MODE;

  SELECT oid INTO v_owner FROM pg_catalog.pg_roles
   WHERE rolname='aegis_idempotency_owner'
     AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb
     AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication
     AND NOT rolbypassrls;
  SELECT oid INTO v_proc FROM pg_catalog.pg_proc
   WHERE oid='app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'::regprocedure
     AND proowner=v_owner AND prosecdef AND provolatile='v'
     AND proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog'];
  IF v_owner IS NULL OR v_proc IS NULL
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members m
                 WHERE m.roleid=v_owner OR m.member=v_owner)
     OR has_function_privilege('public',v_proc,'EXECUTE')
     OR NOT has_function_privilege('aegis_app',v_proc,'EXECUTE')
     OR NOT has_function_privilege('aegis_idempotency_owner',
          'app.idempotency_scope_matches_actor(text,uuid)','EXECUTE')
     OR has_column_privilege('aegis_app','public.idempotency_keys','resource_type','UPDATE')
     OR has_column_privilege('aegis_app','public.idempotency_keys','resource_id','UPDATE') THEN
    RAISE EXCEPTION '00038 Down refused unexpected owner, function or ACL state';
  END IF;
  IF (SELECT count(*) FROM app.idempotency_resource_binding_00038_usage)<>1
     OR NOT EXISTS (
       SELECT 1 FROM app.idempotency_resource_binding_00038_usage
        WHERE singleton=true AND NOT used AND first_bound_at IS NULL
     ) THEN
    RAISE EXCEPTION '00038 Down refused because the resource binder was used';
  END IF;
  IF EXISTS (
    SELECT 1 FROM public.idempotency_keys k
    JOIN public.orders o ON o.tenant_id=k.tenant_id AND o.id=k.resource_id
     WHERE k.resource_type='order' AND o.kind='topup'
       AND k.scope=app.idempotency_actor_scope('balance_topup_create',k.actor_id)
  ) THEN
    RAISE EXCEPTION '00038 Down refused because a schema-38 top-up binding exists';
  END IF;
END $$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.bind_idempotency_resource(
  uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid
) FROM PUBLIC,aegis_app;
DROP FUNCTION app.bind_idempotency_resource(
  uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid
);

REVOKE SELECT,UPDATE ON app.idempotency_resource_binding_00038_usage
  FROM aegis_idempotency_owner;
DROP TABLE app.idempotency_resource_binding_00038_usage;

REVOKE SELECT (
  id,tenant_id,actor_id,scope,idempotency_key,request_hash,status,
  claim_generation,locked_until,resource_type,resource_id
) ON public.idempotency_keys FROM aegis_idempotency_owner;
REVOKE UPDATE (resource_type,resource_id)
  ON public.idempotency_keys FROM aegis_idempotency_owner;
REVOKE EXECUTE ON FUNCTION app.current_tenant_id() FROM aegis_idempotency_owner;
REVOKE EXECUTE ON FUNCTION app.current_actor_id() FROM aegis_idempotency_owner;
REVOKE EXECUTE ON FUNCTION app.idempotency_actor_scope(text,uuid)
  FROM aegis_idempotency_owner;
REVOKE EXECUTE ON FUNCTION app.idempotency_scope_matches_actor(text,uuid)
  FROM aegis_idempotency_owner;
REVOKE USAGE ON SCHEMA public,app FROM aegis_idempotency_owner;

-- Restore the exact schema-37 order mapping (new and renewal only).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.assert_idempotency_order_binding(
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
-- +goose StatementEnd

GRANT UPDATE (resource_type,resource_id)
  ON public.idempotency_keys TO aegis_app;

-- The NOLOGIN role is deliberately retained, stripped of all object
-- privileges. Cluster-wide role deletion belongs to a separately audited
-- cleanup after proving zero dependencies in every database.
-- +goose StatementBegin
DO $$
DECLARE v_owner oid;
BEGIN
  SELECT oid INTO v_owner FROM pg_catalog.pg_roles
   WHERE rolname='aegis_idempotency_owner'
     AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb
     AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication
     AND NOT rolbypassrls;
  IF v_owner IS NULL
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members m
                 WHERE m.roleid=v_owner OR m.member=v_owner)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_class c WHERE c.relowner=v_owner)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc p WHERE p.proowner=v_owner)
     OR has_table_privilege('aegis_idempotency_owner','public.idempotency_keys','SELECT')
     OR has_table_privilege('aegis_idempotency_owner','public.idempotency_keys','UPDATE')
     OR has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','request_hash','SELECT')
     OR has_column_privilege('aegis_idempotency_owner','public.idempotency_keys','resource_type','UPDATE')
     OR has_function_privilege('aegis_idempotency_owner',
          'app.idempotency_scope_matches_actor(text,uuid)','EXECUTE')
     OR EXISTS (
       SELECT 1
         FROM pg_catalog.pg_namespace n
         CROSS JOIN LATERAL pg_catalog.aclexplode(n.nspacl) a
        WHERE n.nspname IN ('public','app') AND a.grantee=v_owner
     )
     OR EXISTS (
       SELECT 1
         FROM pg_catalog.pg_class c
         CROSS JOIN LATERAL pg_catalog.aclexplode(c.relacl) a
        WHERE a.grantee=v_owner
     )
     OR EXISTS (
       SELECT 1
         FROM pg_catalog.pg_proc p
         CROSS JOIN LATERAL pg_catalog.aclexplode(p.proacl) a
        WHERE a.grantee=v_owner
     )
     OR NOT has_column_privilege('aegis_app','public.idempotency_keys','resource_type','UPDATE')
     OR NOT has_column_privilege('aegis_app','public.idempotency_keys','resource_id','UPDATE') THEN
    RAISE EXCEPTION '00038 Down failed to restore schema-37 ACL/catalog state';
  END IF;
END $$;
-- +goose StatementEnd
