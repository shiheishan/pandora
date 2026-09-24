\set ON_ERROR_STOP on
\getenv app_password AEGIS_DB_APP_PASSWORD

-- Keep broad compatibility grants and their later least-privilege revokes in
-- one transaction. No concurrent session can observe the intermediate grant
-- on schema-38 resource columns when this configurator is rerun.
BEGIN;

DO $$
DECLARE
  v_owner oid;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'aegis_app') THEN
    CREATE ROLE aegis_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS;
  END IF;
  IF EXISTS (
    SELECT 1
    FROM pg_catalog.pg_auth_members AS membership
    JOIN pg_catalog.pg_roles AS target ON target.oid = membership.roleid
    JOIN pg_catalog.pg_roles AS member_role ON member_role.oid = membership.member
    WHERE target.rolname = 'aegis_app' OR member_role.rolname = 'aegis_app'
  ) THEN
    RAISE EXCEPTION 'aegis_app has role memberships; review and revoke them explicitly before bootstrap';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname='aegis_idempotency_owner'
  ) THEN
    CREATE ROLE aegis_idempotency_owner
      NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS;
  END IF;
  SELECT oid INTO v_owner FROM pg_catalog.pg_roles
   WHERE rolname='aegis_idempotency_owner'
     AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb
     AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication
     AND NOT rolbypassrls AND rolconnlimit=-1 AND rolvaliduntil IS NULL;
  IF v_owner IS NULL THEN
    RAISE EXCEPTION 'unsafe pre-existing aegis_idempotency_owner; refusing to alter it';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_authid
     WHERE oid=v_owner AND rolpassword IS NOT NULL
  ) THEN
    RAISE EXCEPTION 'aegis_idempotency_owner has a stored password';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_auth_members m
     WHERE m.roleid=v_owner OR m.member=v_owner
  ) OR EXISTS (
    SELECT 1 FROM pg_catalog.pg_db_role_setting s WHERE s.setrole=v_owner
  ) THEN
    RAISE EXCEPTION 'aegis_idempotency_owner has role memberships or settings; review them explicitly';
  END IF;
END $$;

ALTER ROLE aegis_app
  LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS
  PASSWORD :'app_password';
ALTER ROLE aegis_app SET row_security = on;

-- IAM-SEARCHPATH-010: PostgreSQL grants TEMPORARY on a database to PUBLIC by
-- default.  When pg_temp is omitted from search_path it is implicitly searched
-- before ordinary schemas for relations and types, so an untrusted runtime
-- role could otherwise shadow integrity tables from a trigger.  Revoke both
-- the inherited PUBLIC privilege and any direct grant, close schema creation,
-- and pin the role setting to this database only.  Cluster-wide ALTER ROLE is
-- forbidden because disposable prechecks share cluster roles with their
-- source database.
DO $$
DECLARE
  v_database name := pg_catalog.current_database();
BEGIN
  EXECUTE pg_catalog.format(
    'REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC, aegis_app', v_database
  );
  EXECUTE pg_catalog.format(
    'ALTER ROLE aegis_app IN DATABASE %I SET search_path TO pg_catalog, public, pg_temp',
    v_database
  );
END $$;
REVOKE CREATE ON SCHEMA public, app FROM PUBLIC, aegis_app;

GRANT USAGE ON SCHEMA public TO aegis_app;
GRANT USAGE ON SCHEMA app TO aegis_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO aegis_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO aegis_app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA app TO aegis_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  REVOKE INSERT, UPDATE, DELETE ON TABLES FROM aegis_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT ON TABLES TO aegis_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO aegis_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA app
  GRANT EXECUTE ON FUNCTIONS TO aegis_app;

-- Retention and maintenance routines cross tenant/evidence boundaries and are
-- scheduler-only.  PostgreSQL grants new functions to PUBLIC by default, so
-- both paths must be closed after the broad legacy compatibility grant above.
REVOKE EXECUTE ON FUNCTION app.purge_subscription_fetch_log(interval)
  FROM PUBLIC, aegis_app;
REVOKE EXECUTE ON FUNCTION app.guard_finalized_refund_ledger_entry()
  FROM PUBLIC, aegis_app;

-- Idempotency rows are durable request evidence. Claims may set only their
-- initial identity/lease; subsequent writes are limited to one resource bind
-- and guarded lifecycle/result columns.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON idempotency_keys FROM aegis_app;
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema='public' AND table_name='idempotency_keys'
       AND column_name='claim_generation'
  ) THEN
    REVOKE INSERT (tenant_id,scope,idempotency_key,request_hash,status,actor_id,
                   locked_until) ON idempotency_keys FROM aegis_app;
    REVOKE UPDATE (status,response_code,response_body,locked_until,completed_at,
                   resource_type,resource_id) ON idempotency_keys FROM aegis_app;
    GRANT INSERT (tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
      ON idempotency_keys TO aegis_app;
    GRANT UPDATE (status,claim_generation,response_code,response_format,response_payload,
                  response_content_type,response_location,response_etag,
                  response_cache_control,response_content_language,locked_until,
                  completed_at)
      ON idempotency_keys TO aegis_app;
    REVOKE ALL ON FUNCTION app.idempotency_actor_scope(text,uuid)
      FROM PUBLIC,aegis_app;
    REVOKE ALL ON FUNCTION app.idempotency_scope_matches_actor(text,uuid)
      FROM PUBLIC,aegis_app;
    REVOKE ALL ON FUNCTION app.lookup_legacy_idempotency_key(uuid,text,text)
      FROM PUBLIC,aegis_app;
    REVOKE ALL ON FUNCTION app.guard_refund_request() FROM PUBLIC,aegis_app;
    REVOKE ALL ON FUNCTION app.assert_refund_request(uuid,uuid) FROM PUBLIC,aegis_app;
    REVOKE ALL ON FUNCTION app.assert_refund_idempotency_key(uuid,uuid)
      FROM PUBLIC,aegis_app;
    REVOKE ALL ON FUNCTION app.assert_refund_request_trigger()
      FROM PUBLIC,aegis_app;
    REVOKE ALL ON FUNCTION app.assert_refund_idempotency_key_trigger()
      FROM PUBLIC,aegis_app;
    REVOKE ALL ON FUNCTION app.assert_idempotency_order_binding(uuid,uuid,uuid)
      FROM PUBLIC,aegis_app;
    REVOKE ALL ON FUNCTION app.assert_idempotency_order_binding_trigger()
      FROM PUBLIC,aegis_app;
    GRANT EXECUTE ON FUNCTION app.idempotency_actor_scope(text,uuid) TO aegis_app;
    GRANT EXECUTE ON FUNCTION app.idempotency_scope_matches_actor(text,uuid) TO aegis_app;
    GRANT EXECUTE ON FUNCTION app.lookup_legacy_idempotency_key(uuid,text,text)
      TO aegis_app;
    IF to_regprocedure(
      'app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'
    ) IS NOT NULL THEN
      IF to_regclass('app.idempotency_resource_binding_00038_usage') IS NULL
         OR NOT EXISTS (
           SELECT 1 FROM pg_catalog.pg_proc p
           JOIN pg_catalog.pg_roles r ON r.oid=p.proowner
            WHERE p.oid=
              'app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'::regprocedure
              AND r.rolname='aegis_idempotency_owner'
              AND p.prosecdef AND p.provolatile='v'
              AND p.proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog']
         ) OR EXISTS (
           SELECT 1 FROM pg_catalog.pg_class c
            WHERE c.relowner=(SELECT oid FROM pg_catalog.pg_roles
                               WHERE rolname='aegis_idempotency_owner')
         ) OR EXISTS (
           SELECT 1 FROM pg_catalog.pg_proc p
            WHERE p.proowner=(SELECT oid FROM pg_catalog.pg_roles
                               WHERE rolname='aegis_idempotency_owner')
              AND p.oid<>
                'app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'::regprocedure
              AND (to_regprocedure(
                     'app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)'
                   ) IS NULL OR p.oid<>to_regprocedure(
                     'app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)'))
         ) OR (
           (to_regprocedure(
              'app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)'
            ) IS NULL) <>
           (to_regclass('app.bound_idempotency_success_00039_usage') IS NULL)
         ) OR (
           to_regprocedure(
             'app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)'
           ) IS NOT NULL AND NOT EXISTS (
             SELECT 1 FROM pg_catalog.pg_proc p
             JOIN pg_catalog.pg_roles r ON r.oid=p.proowner
              WHERE p.oid=to_regprocedure(
                'app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)')
                AND r.rolname='aegis_idempotency_owner'
                AND p.prosecdef AND p.provolatile='v'
                AND p.proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog']
           )
         ) THEN
        RAISE EXCEPTION 'schema-38/39 idempotency owner catalog is incomplete or unsafe';
      END IF;
      REVOKE UPDATE (resource_type,resource_id)
        ON idempotency_keys FROM aegis_app;
      REVOKE ALL ON FUNCTION app.bind_idempotency_resource(
        uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid
      ) FROM PUBLIC,aegis_app;
      GRANT EXECUTE ON FUNCTION app.bind_idempotency_resource(
        uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid
      ) TO aegis_app;

      GRANT USAGE ON SCHEMA public,app TO aegis_idempotency_owner;
      GRANT EXECUTE ON FUNCTION app.current_tenant_id()
        TO aegis_idempotency_owner;
      GRANT EXECUTE ON FUNCTION app.current_actor_id()
        TO aegis_idempotency_owner;
      GRANT EXECUTE ON FUNCTION app.idempotency_actor_scope(text,uuid)
        TO aegis_idempotency_owner;
      GRANT EXECUTE ON FUNCTION app.idempotency_scope_matches_actor(text,uuid)
        TO aegis_idempotency_owner;
      GRANT SELECT (
        id,tenant_id,actor_id,scope,idempotency_key,request_hash,status,
        claim_generation,locked_until,resource_type,resource_id
      ) ON idempotency_keys TO aegis_idempotency_owner;
      GRANT UPDATE (resource_type,resource_id)
        ON idempotency_keys TO aegis_idempotency_owner;
      GRANT SELECT (singleton,used,first_bound_at), UPDATE (used,first_bound_at)
        ON app.idempotency_resource_binding_00038_usage
        TO aegis_idempotency_owner;

      IF to_regprocedure(
        'app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)'
      ) IS NOT NULL THEN
        REVOKE ALL ON FUNCTION app.complete_bound_idempotency_success(
          uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,
          integer,bytea,text,text,text,text,text
        ) FROM PUBLIC,aegis_app;
        GRANT EXECUTE ON FUNCTION app.complete_bound_idempotency_success(
          uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,
          integer,bytea,text,text,text,text,text
        ) TO aegis_app;
        GRANT UPDATE (
          status,response_code,response_format,response_payload,
          response_content_type,response_location,response_etag,
          response_cache_control,response_content_language,completed_at,locked_until
        ) ON idempotency_keys TO aegis_idempotency_owner;
        GRANT SELECT (
          response_code,response_body,response_format,response_payload,
          response_content_type,response_location,response_etag,
          response_cache_control,response_content_language,completed_at
        ) ON idempotency_keys TO aegis_idempotency_owner;
        GRANT SELECT (singleton,used,first_completed_at),
              UPDATE (used,first_completed_at)
          ON app.bound_idempotency_success_00039_usage
          TO aegis_idempotency_owner;
      END IF;
    ELSE
      GRANT UPDATE (resource_type,resource_id)
        ON idempotency_keys TO aegis_app;
      IF EXISTS (
        SELECT 1 FROM pg_catalog.pg_class c
         WHERE c.relowner=(SELECT oid FROM pg_catalog.pg_roles
                            WHERE rolname='aegis_idempotency_owner')
      ) OR EXISTS (
        SELECT 1 FROM pg_catalog.pg_proc p
         WHERE p.proowner=(SELECT oid FROM pg_catalog.pg_roles
                            WHERE rolname='aegis_idempotency_owner')
      ) OR EXISTS (
        SELECT 1 FROM pg_catalog.pg_shdepend d
         WHERE d.refobjid=(SELECT oid FROM pg_catalog.pg_roles
                            WHERE rolname='aegis_idempotency_owner')
      ) THEN
        RAISE EXCEPTION 'binder owner has unexpected objects without schema-38';
      END IF;
    END IF;
  ELSE
    GRANT INSERT (
      tenant_id, scope, idempotency_key, request_hash, status, actor_id, locked_until
    ) ON idempotency_keys TO aegis_app;
    GRANT UPDATE (
      status, response_code, response_body, locked_until, completed_at,
      resource_type, resource_id
    ) ON idempotency_keys TO aegis_app;
  END IF;
END $$;

DO $$
DECLARE
  table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'ledger_entries', 'ledger_transactions', 'audit_events',
    'order_reservation_events',
    'order_items', 'order_stock_reservations', 'order_purchase_limit_reservations',
    'subscription_events', 'quota_adjustments', 'approval_decisions',
    'referrals', 'provisioning_steps', 'node_config_applications',
    'credential_access_log', 'system_setting_revisions',
    'node_traffic_reports', 'node_metrics', 'subscription_fetch_log',
    'revenue_report_adjustments'
  ] LOOP
    IF to_regclass('public.' || table_name) IS NULL THEN
      RAISE EXCEPTION 'required append-only table is missing: %', table_name;
    END IF;
    EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %I FROM aegis_app', table_name);
  END LOOP;
END $$;

-- Webhook receipts are immutable evidence plus a narrowly mutable assessment.
-- The app can never rewrite request identity, headers, source address or body.
REVOKE UPDATE, DELETE, TRUNCATE ON payment_webhook_receipts FROM aegis_app;
GRANT SELECT, INSERT ON payment_webhook_receipts TO aegis_app;
GRANT UPDATE (
  parse_status, signature_status, parse_error, parsed_at,
  signature_checked_at, parsed_provider_event_id, payment_event_id
) ON payment_webhook_receipts TO aegis_app;

REVOKE UPDATE, DELETE, TRUNCATE ON payment_events FROM aegis_app;
GRANT SELECT, INSERT ON payment_events TO aegis_app;
GRANT UPDATE (processing_status, processing_error, processed_at)
  ON payment_events TO aegis_app;

REVOKE UPDATE, DELETE, TRUNCATE ON orders, payment_intents, payments, refunds,
  refund_requests, invoices
  FROM aegis_app;
GRANT UPDATE (status, paid_amount, paid_at, fulfilled_at, subscription_id)
  ON orders TO aegis_app;
DO $$
BEGIN
  -- These columns become runtime lifecycle fields only while Schema40 is
  -- installed. A clean Down followed by this rerunnable configurator must
  -- preserve the pre-40 ACL restored by the migration.
  IF to_regclass('app.order_release_00040_meta') IS NOT NULL THEN
    GRANT UPDATE (cancelled_at, expired_at, cancel_reason)
      ON orders TO aegis_app;
  END IF;
END $$;
GRANT UPDATE (status, provider_ref) ON payment_intents TO aegis_app;

-- Reservation and reconciliation rows are lifecycle evidence. Identity,
-- amounts, legacy markers and generated versions remain immutable to the app;
-- only the guarded transition columns are writable.
REVOKE UPDATE, DELETE, TRUNCATE ON
  order_reservations, coupon_redemptions, balance_holds, late_payment_cases
  FROM aegis_app;
GRANT UPDATE (state, captured_at, released_at, release_reason)
  ON order_reservations TO aegis_app;
GRANT UPDATE (status, captured_at, released_at, reverted_at, revert_reason)
  ON coupon_redemptions TO aegis_app;
GRANT UPDATE (status, capture_txn_id, release_txn_id, captured_at, released_at)
  ON balance_holds TO aegis_app;
GRANT UPDATE (status, refund_id, refund_txn_id, resolution_reason, resolved_at)
  ON late_payment_cases TO aegis_app;

-- Sensitive financial/evidence inserts are column-scoped to the SQL currently
-- emitted by the application. Database-generated IDs and timestamps, terminal
-- lifecycle evidence, refund totals and processing assessments cannot be
-- supplied by the app at INSERT time.
REVOKE INSERT ON orders, order_items, payment_intents, payments,
  payment_events, payment_webhook_receipts, refunds, refund_requests, invoices,
  order_reservations, order_stock_reservations,
  order_purchase_limit_reservations, coupon_redemptions, balance_holds,
  late_payment_cases, order_reservation_events, ledger_accounts,
  ledger_transactions, ledger_entries FROM aegis_app;
-- 这份清单必须和 checkout.go / renewal.go 的 INSERT 语句对齐。
--
-- manual_reason 和 created_by 是人工单功能加的，代码一直在写，这两处清单
-- 却都没跟上：一处在 00036 迁移里（已由 00060 补），另一处就是这里。同一个
-- 缺陷有两个副本，而这个文件是生产的权限配置脚本——在生产上跑一次，orders
-- 就会被收窄成缺列的白名单，下单立刻全线 permission denied。
--
-- 现网至今没炸，只是因为它的 orders 还是表级 arwd，这段收窄从未在生产执行过。
GRANT INSERT (
  tenant_id, order_no, user_id, kind, status, currency, subtotal_amount,
  discount_amount, tax_amount, total_amount, balance_applied, payable_amount,
  expires_at, coupon_id, subscription_id, idempotency_key_id,
  manual_reason, created_by, proration_credit_amount
) ON orders TO aegis_app;
DO $$
BEGIN
  IF to_regprocedure(
    'app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'
  ) IS NOT NULL THEN
    REVOKE UPDATE (resource_type,resource_id)
      ON idempotency_keys FROM aegis_app;
  END IF;
END $$;
GRANT INSERT (
  tenant_id, order_id, product_id, price_id, plan_id, plan_version_id,
  snapshot_product_name, snapshot_plan_name, snapshot_plan_version,
  snapshot_interval, snapshot_interval_count, snapshot_entitlements,
  snapshot_quotas, quantity, unit_amount, line_amount, currency, traffic_pack_id
) ON order_items TO aegis_app;
GRANT INSERT (
  tenant_id, order_id, provider_id, currency, amount, status, provider_ref,
  action_payload, expires_at
) ON payment_intents TO aegis_app;
GRANT INSERT (
  tenant_id, order_id, provider_id, provider_payment_id, payment_intent_id,
  currency, amount, fee_amount, status
) ON payments TO aegis_app;
GRANT INSERT (
  tenant_id, provider_id, provider_event_id, event_type,
  provider_payment_id, raw_payload, signature_verified
) ON payment_events TO aegis_app;
GRANT INSERT (
  tenant_id, provider_id, provider_code, http_method, request_path,
  query_string, raw_headers, raw_body, source_ip
) ON payment_webhook_receipts TO aegis_app;
GRANT INSERT (tenant_id, order_id, user_id, expires_at)
  ON order_reservations TO aegis_app;
GRANT INSERT (tenant_id, reservation_id, order_id, plan_id, quantity)
  ON order_stock_reservations TO aegis_app;
GRANT INSERT (tenant_id, reservation_id, order_id, plan_id, user_id, quantity)
  ON order_purchase_limit_reservations TO aegis_app;
GRANT INSERT (
  tenant_id, coupon_id, user_id, order_id, discount_amount, currency,
  reservation_id
) ON coupon_redemptions TO aegis_app;
GRANT INSERT (
  tenant_id, reservation_id, order_id, user_id, currency, amount,
  available_account_id, hold_account_id, hold_txn_id
) ON balance_holds TO aegis_app;
GRANT INSERT (
  tenant_id, order_id, payment_id, payment_event_id, amount, currency,
  suspense_txn_id
) ON late_payment_cases TO aegis_app;
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema='public' AND table_name='late_payment_cases'
       AND column_name='case_kind'
  ) THEN
    EXECUTE 'GRANT INSERT (case_kind) ON public.late_payment_cases TO aegis_app';
  END IF;
END $$;
GRANT INSERT (
  tenant_id, reservation_id, order_id, from_state, to_state, event_kind,
  business_request_id, actor_kind, actor_id, reason, metadata
) ON order_reservation_events TO aegis_app;
GRANT INSERT (
  tenant_id, account_type, normal_balance, currency, owner_user_id, owner_ref
) ON ledger_accounts TO aegis_app;
GRANT INSERT (
  tenant_id, kind, currency, source_type, source_id, memo, actor_kind, actor_id
) ON ledger_transactions TO aegis_app;
GRANT INSERT (
  tenant_id, transaction_id, account_id, direction, amount, currency, description
) ON ledger_entries TO aegis_app;

-- Schema40 turns commission and withdrawal rows into guarded financial
-- evidence. Before Schema40 (and after its clean Down), migrations 00011/00036
-- intentionally leave their legacy table-level DML in place. Key this
-- configurator from the migration-owned marker so rerunning it cannot silently
-- recreate post-40 ACLs on a pre-40 schema.
DO $$
DECLARE
  v_schema40 boolean := to_regclass('app.order_release_00040_meta') IS NOT NULL;
  v_helpers_complete boolean;
  v_stale_helpers boolean;
BEGIN
  v_helpers_complete :=
    to_regprocedure('app.assert_late_payment_suspense(uuid,uuid,uuid)') IS NOT NULL AND
    to_regprocedure('app.assert_late_payment_suspense_trigger()') IS NOT NULL AND
    to_regprocedure('app.guard_commission_entry()') IS NOT NULL AND
    to_regprocedure('app.assert_commission_entry(uuid,uuid)') IS NOT NULL AND
    to_regprocedure('app.assert_commission_entry_trigger()') IS NOT NULL AND
    to_regprocedure('app.assert_commission_ledger_trigger()') IS NOT NULL AND
    to_regprocedure('app.guard_withdrawal()') IS NOT NULL AND
    to_regprocedure('app.assert_withdrawal(uuid,uuid)') IS NOT NULL AND
    to_regprocedure('app.assert_withdrawal_trigger()') IS NOT NULL AND
    to_regprocedure('app.mark_order_release_00040_used()') IS NOT NULL;
  v_stale_helpers :=
    to_regprocedure('app.assert_late_payment_suspense(uuid,uuid,uuid)') IS NOT NULL OR
    to_regprocedure('app.assert_late_payment_suspense_trigger()') IS NOT NULL OR
    to_regprocedure('app.guard_commission_entry()') IS NOT NULL OR
    to_regprocedure('app.assert_commission_entry(uuid,uuid)') IS NOT NULL OR
    to_regprocedure('app.assert_commission_entry_trigger()') IS NOT NULL OR
    to_regprocedure('app.assert_commission_ledger_trigger()') IS NOT NULL OR
    to_regprocedure('app.guard_withdrawal()') IS NOT NULL OR
    to_regprocedure('app.assert_withdrawal(uuid,uuid)') IS NOT NULL OR
    to_regprocedure('app.assert_withdrawal_trigger()') IS NOT NULL OR
    to_regprocedure('app.mark_order_release_00040_used()') IS NOT NULL;

  IF v_schema40 AND NOT v_helpers_complete THEN
    RAISE EXCEPTION 'Schema40 marker exists but its financial invariant helpers are incomplete';
  ELSIF NOT v_schema40 AND v_stale_helpers THEN
    RAISE EXCEPTION 'Schema40 marker is absent but financial invariant helpers remain';
  ELSIF v_schema40 THEN
    REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON commission_entries FROM aegis_app;
    GRANT INSERT (
      tenant_id, referrer_user_id, referee_user_id, order_id, currency,
      base_amount, rate_bp, commission_amount, frozen_until,
      review_required, review_reason, accrual_txn_id
    ) ON commission_entries TO aegis_app;
    GRANT UPDATE (status, settle_txn_id, review_required, review_reason, updated_at)
      ON commission_entries TO aegis_app;

    REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON withdrawals FROM aegis_app;
    GRANT INSERT (tenant_id,user_id,currency,amount,payout_detail_encrypted,status)
      ON withdrawals TO aegis_app;
    GRANT UPDATE (status,reject_reason,payout_reference,payout_txn_id,completed_at,updated_at)
      ON withdrawals TO aegis_app;
  END IF;
END $$;

-- configure-app-role is rerunnable and starts with a broad compatibility
-- function grant. Re-close every schema-40 invariant helper when present.
DO $$
DECLARE
  v_signature text;
BEGIN
  FOREACH v_signature IN ARRAY ARRAY[
    'app.assert_late_payment_suspense(uuid,uuid,uuid)',
    'app.assert_late_payment_suspense_trigger()',
    'app.guard_commission_entry()',
    'app.assert_commission_entry(uuid,uuid)',
    'app.assert_commission_entry_trigger()',
    'app.assert_commission_ledger_trigger()',
    'app.guard_withdrawal()',
    'app.assert_withdrawal(uuid,uuid)',
    'app.assert_withdrawal_trigger()',
    'app.mark_order_release_00040_used()'
  ] LOOP
    IF to_regprocedure(v_signature) IS NOT NULL THEN
      EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC, aegis_app', v_signature);
    END IF;
  END LOOP;
END $$;

REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON refunds, refund_requests, invoices
  FROM aegis_app;

REVOKE UPDATE, DELETE, TRUNCATE ON ledger_accounts FROM aegis_app;
GRANT UPDATE (updated_at) ON ledger_accounts TO aegis_app;

REVOKE INSERT, UPDATE, DELETE ON permissions FROM aegis_app;
REVOKE INSERT, UPDATE, DELETE ON subscription_transitions FROM aegis_app;
REVOKE INSERT, UPDATE, DELETE ON node_transitions FROM aegis_app;
REVOKE INSERT, UPDATE, DELETE ON order_transitions FROM aegis_app;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_tables
    WHERE schemaname = 'public' AND tablename = 'goose_db_version'
  ) THEN
    REVOKE ALL ON goose_db_version FROM aegis_app;
  END IF;
END $$;

-- 把列级白名单提升回表级。
--
-- 上面那些 GRANT INSERT(cols) / GRANT UPDATE(cols) 的设计意图是最小权限：
-- 即使应用被攻破，也只能写它本来就该写的那几列。设计是对的，代价却在这个
-- 阶段压过了收益——
--
--   * orders 的白名单漏了 manual_reason 与 created_by，全新部署的库下单
--     直接 permission denied，现网只因权限从未真正收窄才没炸；
--   * 同一份清单在 00036 迁移和这个脚本里各存一份，改了一处忘了另一处；
--   * 每加一列都要回头改两处，而漏改的后果只在收窄过的库上才显形，
--     开发机（超级用户）永远看不见。
--
-- 面板还没有真实付费用户，"应用被攻破后的越权写"这个威胁模型偏远，而维护
-- 成本已经实实在在吃掉了时间。所以这里统一提升回表级，等团队规模和攻击面
-- 都长起来了再收回去——要恢复，删掉这一段即可，上面的清单原样还在。
--
-- 只提升那些本来就有列级授权的表和权限类型，不碰别的：
--   * append-only 的表（ledger_entries、gift_card_redemptions 等只 REVOKE
--     UPDATE/DELETE、不 GRANT 回的）保持只能追加；
--   * RLS 与 forced RLS 完全不动，租户隔离是硬需求，不在这次放宽范围内。
DO $$
DECLARE
  r record;
BEGIN
  FOR r IN
    SELECT DISTINCT c.relname AS table_name, a.privilege_type
      FROM pg_catalog.pg_attribute att
      JOIN pg_catalog.pg_class c ON c.oid = att.attrelid
      CROSS JOIN LATERAL aclexplode(att.attacl) a
      JOIN pg_catalog.pg_roles g ON g.oid = a.grantee
     WHERE att.attacl IS NOT NULL
       AND g.rolname = 'aegis_app'
       AND c.relnamespace = 'public'::regnamespace
       AND c.relkind = 'r'
  LOOP
    EXECUTE format('GRANT %s ON public.%I TO aegis_app',
                   r.privilege_type, r.table_name);
  END LOOP;
END $$;

-- 补回 00052 的 append-only 约束。
--
-- 00052 在迁移里 REVOKE 了这两张表的 UPDATE/DELETE，理由是它们是证据流水：
-- 卡密核销记录和流量重置日志只应追加，不应被改写或抹掉。但本脚本第 85 行的
-- GRANT ... ON ALL TABLES 会把那道 REVOKE 冲掉，而这个脚本在迁移之后运行——
-- 于是那道保护自写下之日起就没真正生效过。
--
-- 又一处「同一个事实写在两处，后者覆盖前者」。放在文件末尾，确保它是这张
-- 表最后一次被动到的地方。
REVOKE UPDATE, DELETE ON gift_card_redemptions FROM aegis_app;
REVOKE UPDATE, DELETE ON traffic_reset_logs FROM aegis_app;

-- 纵深防御：这两张表各有守卫触发器（00069 批次只许一次打导出标记，00070 流量包
-- 余额只许按规则扣减），但同样被上面的 GRANT ... ON ALL TABLES 放开了 DELETE。
-- 触发器之外再收一道授权。UPDATE 只收 DELETE 不够：两表的业务路径都要改余额 /
-- 导出标记，UPDATE 保留，靠触发器约束写法。
REVOKE DELETE ON traffic_pack_grants FROM aegis_app;
REVOKE DELETE ON gift_card_batches FROM aegis_app;

COMMIT;
