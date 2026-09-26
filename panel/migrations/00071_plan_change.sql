-- 变更套餐（C2，D-E-2）：订单记剩余价值折算，数据库守住折算订单的形状与履约证据。
--
-- 用户拍板的规则（D-E-2 与 2026-09-24 的补充）：
--   * 升级、降级都允许；订单 kind 一律 'upgrade'（契约定名），方向由金额决定。
--   * 剩余价值 = 本周期付费合计 × min(剩余时间比例, 剩余流量比例)，向下取整到分；
--     时间比按「已付费天数先用、赠送天数最后用」算，赠送 / 礼品卡 / 0 元单实付为 0。
--     这个数由应用在下单时算好，写进 orders.proration_credit_amount 并随订单冻结。
--   * 新价（优惠后）高于剩余价值：补差价，total = 新价 − 折扣 − 剩余价值；
--     低于：total = 0，差额 = 剩余价值 − (新价 − 折扣) 退进余额（账本分录，不可提现）。
--   * 新周期从履约当天按新套餐起算，凭据不变。
--
-- 于是金额恒等式从 total = subtotal − discount + tax 扩成
--   total = max(subtotal − discount − proration_credit, 0) + tax
-- 存量订单的 proration_credit 都是 0，而它们的 discount 从不超过 subtotal，
-- 新式子对它们与旧式子逐行相等。
--
-- 00036 的通用预留图检查（订单项合计 = 小计、零元单必须同事务履约、非新购不占
-- 库存与限购）对 upgrade 照常生效；这里再加一道 upgrade 专属的形状与履约证据：
--   * 挂在一条同用户的订阅上，恰好一行指向套餐的订单项；
--   * 已履约的恰好有一条 plan_changed 订阅事件；
--   * 退余额的恰好有一笔 plan_change_refund 分录，金额等于上式的差额，
--     记在本人同币种的余额科目上；不该退的一笔都没有。
--
-- 同一条订阅同时只能有一张未完结的续费或变更单：两张单各按下单时的订阅状态
-- 算钱，先履约的那张会让另一张的折算作废。00036 的「一条订阅一张在途续费」
-- 唯一索引扩成覆盖续费与变更两种。

-- +goose Up

-- +goose StatementBegin
ALTER TABLE orders
  ADD COLUMN proration_credit_amount app.minor_amount NOT NULL DEFAULT 0
    CONSTRAINT orders_proration_credit_nonnegative CHECK (proration_credit_amount >= 0),
  ADD CONSTRAINT orders_proration_only_plan_change
    CHECK (proration_credit_amount = 0 OR kind = 'upgrade');
ALTER TABLE orders DROP CONSTRAINT orders_total_identity;
ALTER TABLE orders ADD CONSTRAINT orders_total_identity
  CHECK (total_amount = greatest(subtotal_amount - discount_amount - proration_credit_amount, 0)
                        + tax_amount);
-- orders 是按列授权的（00036 / 00060 的白名单），新列单独放给运行时角色。
GRANT INSERT (proration_credit_amount) ON orders TO aegis_app;

DROP INDEX IF EXISTS orders_one_active_renewal;
CREATE UNIQUE INDEX orders_one_active_subscription_change
  ON orders (tenant_id, subscription_id)
  WHERE kind IN ('renewal', 'upgrade')
    AND status IN ('draft', 'pending_payment', 'processing', 'paid');

-- 变更单的履约证据按订单反查，只索引这一种事件。
CREATE INDEX idx_subscription_events_plan_change_order
  ON subscription_events (tenant_id, order_id) WHERE event_type = 'plan_changed';

-- 一张变更单至多一笔退余额分录。
CREATE UNIQUE INDEX ledger_transactions_plan_change_refund_unique
  ON ledger_transactions (tenant_id, source_id)
  WHERE source_type = 'order' AND kind = 'plan_change_refund';

-- 变更套餐按新套餐重新计量，清零前的用量照例留一条重置日志。
ALTER TABLE traffic_reset_logs DROP CONSTRAINT traffic_reset_logs_reason_check;
ALTER TABLE traffic_reset_logs ADD CONSTRAINT traffic_reset_logs_reason_check
  CHECK (reason IN ('renewal','cycle_roll','manual','gift_card','plan_change'));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.assert_plan_change_order(p_tenant uuid, p_order uuid)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_order record;
  v_items int;
  v_plan_items int;
  v_events int;
  v_refund bigint;
  v_txns int;
  v_refund_ok boolean;
BEGIN
  SELECT o.kind, o.status, o.user_id, o.currency, o.subscription_id,
         o.subtotal_amount, o.discount_amount, o.proration_credit_amount
    INTO v_order
    FROM orders o WHERE o.tenant_id = p_tenant AND o.id = p_order;
  IF NOT FOUND THEN
    RETURN;
  END IF;

  -- 别的 kind 不可能带上变更证据：事件与分录在插入时就要求来源是 upgrade 单
  -- （见下方触发器函数），而订单的 kind 不可改（00036 的状态守卫）。这里直接
  -- 返回，免得每张普通订单的每次状态变化都去扫一遍订阅事件。
  IF v_order.kind <> 'upgrade' THEN
    RETURN;
  END IF;

  IF v_order.subscription_id IS NULL OR NOT EXISTS (
       SELECT 1 FROM subscriptions s
        WHERE s.tenant_id = p_tenant AND s.id = v_order.subscription_id
          AND s.user_id = v_order.user_id) THEN
    RAISE EXCEPTION 'plan change order % must target a subscription of its user', p_order
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT count(*), count(*) FILTER (WHERE oi.plan_id IS NOT NULL AND oi.traffic_pack_id IS NULL)
    INTO v_items, v_plan_items
    FROM order_items oi WHERE oi.tenant_id = p_tenant AND oi.order_id = p_order;
  IF v_items <> 1 OR v_plan_items <> 1 THEN
    RAISE EXCEPTION 'plan change order % must have exactly one plan line', p_order
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT count(*) INTO v_events
    FROM subscription_events e
   WHERE e.tenant_id = p_tenant AND e.order_id = p_order AND e.event_type = 'plan_changed';
  IF v_events <> 0 AND EXISTS (
       SELECT 1 FROM subscription_events e
        WHERE e.tenant_id = p_tenant AND e.order_id = p_order
          AND e.event_type = 'plan_changed'
          AND e.subscription_id IS DISTINCT FROM v_order.subscription_id) THEN
    RAISE EXCEPTION 'plan change order % event points at another subscription', p_order
      USING ERRCODE = 'check_violation';
  END IF;

  v_refund := greatest(v_order.proration_credit_amount
                       - (v_order.subtotal_amount - v_order.discount_amount), 0);
  SELECT count(*) INTO v_txns
    FROM ledger_transactions t
   WHERE t.tenant_id = p_tenant AND t.source_type = 'order'
     AND t.source_id = p_order AND t.kind = 'plan_change_refund';

  IF v_order.status IN ('fulfilled','partially_refunded','refunded') THEN
    IF v_events <> 1 THEN
      RAISE EXCEPTION 'fulfilled plan change order % lacks its plan_changed event', p_order
        USING ERRCODE = 'check_violation';
    END IF;
    IF v_refund = 0 THEN
      IF v_txns <> 0 THEN
        RAISE EXCEPTION 'plan change order % owes no balance refund but has one', p_order
          USING ERRCODE = 'check_violation';
      END IF;
    ELSE
      -- 两条分录：借 平台收入、贷 本人同币种余额，金额都等于差额。
      SELECT v_txns = 1
         AND (SELECT count(*) FROM ledger_entries e
               JOIN ledger_transactions t ON t.id = e.transaction_id
              WHERE t.tenant_id = p_tenant AND t.source_type = 'order'
                AND t.source_id = p_order AND t.kind = 'plan_change_refund') = 2
         AND EXISTS (
           SELECT 1 FROM ledger_transactions t
             JOIN ledger_entries c ON c.transaction_id = t.id AND c.direction = 'credit'
             JOIN ledger_accounts ca ON ca.id = c.account_id
             JOIN ledger_entries d ON d.transaction_id = t.id AND d.direction = 'debit'
             JOIN ledger_accounts da ON da.id = d.account_id
            WHERE t.tenant_id = p_tenant AND t.source_type = 'order'
              AND t.source_id = p_order AND t.kind = 'plan_change_refund'
              AND t.currency = v_order.currency
              AND c.amount = v_refund AND c.currency = v_order.currency
              AND ca.tenant_id = p_tenant AND ca.account_type = 'user_balance'
              AND ca.owner_user_id = v_order.user_id AND ca.currency = v_order.currency
              AND d.amount = v_refund AND d.currency = v_order.currency
              AND da.tenant_id = p_tenant AND da.account_type = 'platform_revenue'
              AND da.currency = v_order.currency)
        INTO v_refund_ok;
      IF NOT v_refund_ok THEN
        RAISE EXCEPTION 'plan change order % lacks its exact balance refund of %', p_order, v_refund
          USING ERRCODE = 'check_violation';
      END IF;
    END IF;
  ELSIF v_events <> 0 OR v_txns <> 0 THEN
    RAISE EXCEPTION 'plan change order % in status % already carries fulfilment evidence',
      p_order, v_order.status USING ERRCODE = 'check_violation';
  END IF;
END;
$$;

CREATE FUNCTION app.assert_plan_change_order_trigger() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_row jsonb := CASE WHEN TG_OP = 'DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
  v_tenant uuid := (v_row->>'tenant_id')::uuid;
  v_order uuid;
BEGIN
  IF TG_TABLE_NAME = 'orders' THEN
    v_order := (v_row->>'id')::uuid;
  ELSIF TG_TABLE_NAME = 'order_items' THEN
    v_order := (v_row->>'order_id')::uuid;
  ELSIF TG_TABLE_NAME = 'subscription_events' THEN
    v_order := (v_row->>'order_id')::uuid;
    IF v_order IS NULL OR NOT EXISTS (SELECT 1 FROM orders o
                  WHERE o.tenant_id = v_tenant AND o.id = v_order AND o.kind = 'upgrade') THEN
      RAISE EXCEPTION 'plan_changed event % has no plan change order', v_row->>'id'
        USING ERRCODE = 'check_violation';
    END IF;
  ELSIF TG_TABLE_NAME = 'ledger_transactions' THEN
    v_order := (v_row->>'source_id')::uuid;
    IF v_row->>'source_type' IS DISTINCT FROM 'order' OR v_order IS NULL
       OR NOT EXISTS (SELECT 1 FROM orders o
                       WHERE o.tenant_id = v_tenant AND o.id = v_order AND o.kind = 'upgrade') THEN
      RAISE EXCEPTION 'plan_change_refund transaction % has no plan change order', v_row->>'id'
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  IF v_order IS NOT NULL THEN
    PERFORM app.assert_plan_change_order(v_tenant, v_order);
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER trg_plan_change_order_shape_orders
  AFTER INSERT OR UPDATE ON orders DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION app.assert_plan_change_order_trigger();
CREATE CONSTRAINT TRIGGER trg_plan_change_order_shape_items
  AFTER INSERT OR UPDATE OR DELETE ON order_items DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION app.assert_plan_change_order_trigger();
CREATE CONSTRAINT TRIGGER trg_plan_change_order_shape_events
  AFTER INSERT ON subscription_events DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.event_type = 'plan_changed')
  EXECUTE FUNCTION app.assert_plan_change_order_trigger();
CREATE CONSTRAINT TRIGGER trg_plan_change_order_shape_refunds
  AFTER INSERT ON ledger_transactions DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.kind = 'plan_change_refund')
  EXECUTE FUNCTION app.assert_plan_change_order_trigger();
-- +goose StatementEnd

-- +goose StatementBegin
-- 订单/幂等对称绑定认识 upgrade：变更单有自己的幂等域。
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
            OR k.actor_id IS DISTINCT FROM coalesce(o.created_by,o.user_id)
            OR k.scope IS DISTINCT FROM app.idempotency_actor_scope(
              CASE o.kind
                WHEN 'new' THEN 'order_create'
                WHEN 'addon' THEN 'order_create'
                WHEN 'renewal' THEN 'subscription_renewal_create'
                WHEN 'upgrade' THEN 'subscription_change_plan_create'
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
            OR k.actor_id IS DISTINCT FROM coalesce(o.created_by,o.user_id)
            OR k.scope IS DISTINCT FROM app.idempotency_actor_scope(
              CASE o.kind
                WHEN 'new' THEN 'order_create'
                WHEN 'addon' THEN 'order_create'
                WHEN 'renewal' THEN 'subscription_renewal_create'
                WHEN 'upgrade' THEN 'subscription_change_plan_create'
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
-- 幂等键的资源绑定与成功回填（00038 / 00039，SECURITY DEFINER）按白名单只认
-- 已知的订单幂等域；变更单的 subscription_change_plan_create 加进去。
-- CREATE OR REPLACE 保留属主、ACL 与 SET search_path，函数体只改这一行白名单。
CREATE OR REPLACE FUNCTION app.bind_idempotency_resource(
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
          ('order_create','subscription_renewal_create','balance_topup_create',
           'subscription_change_plan_create'))
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.complete_bound_idempotency_success(
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
          ('order_create','subscription_renewal_create','balance_topup_create',
           'subscription_change_plan_create'))
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

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM orders WHERE kind = 'upgrade')
     OR EXISTS (SELECT 1 FROM ledger_transactions WHERE kind = 'plan_change_refund')
     OR EXISTS (SELECT 1 FROM subscription_events WHERE event_type = 'plan_changed')
     OR EXISTS (SELECT 1 FROM traffic_reset_logs WHERE reason = 'plan_change') THEN
    RAISE EXCEPTION '00071 Down refused: plan change orders or their evidence exist';
  END IF;
END $$;

CREATE OR REPLACE FUNCTION app.assert_idempotency_order_binding(
  p_tenant uuid,p_order uuid,p_key uuid
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog
AS $f$
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
            OR k.actor_id IS DISTINCT FROM coalesce(o.created_by,o.user_id)
            OR k.scope IS DISTINCT FROM app.idempotency_actor_scope(
              CASE o.kind
                WHEN 'new' THEN 'order_create'
                WHEN 'addon' THEN 'order_create'
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
            OR k.actor_id IS DISTINCT FROM coalesce(o.created_by,o.user_id)
            OR k.scope IS DISTINCT FROM app.idempotency_actor_scope(
              CASE o.kind
                WHEN 'new' THEN 'order_create'
                WHEN 'addon' THEN 'order_create'
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
$f$;

DROP TRIGGER IF EXISTS trg_plan_change_order_shape_refunds ON ledger_transactions;
DROP TRIGGER IF EXISTS trg_plan_change_order_shape_events ON subscription_events;
DROP TRIGGER IF EXISTS trg_plan_change_order_shape_items ON order_items;
DROP TRIGGER IF EXISTS trg_plan_change_order_shape_orders ON orders;
DROP FUNCTION IF EXISTS app.assert_plan_change_order_trigger();
DROP FUNCTION IF EXISTS app.assert_plan_change_order(uuid, uuid);

ALTER TABLE traffic_reset_logs DROP CONSTRAINT traffic_reset_logs_reason_check;
ALTER TABLE traffic_reset_logs ADD CONSTRAINT traffic_reset_logs_reason_check
  CHECK (reason IN ('renewal','cycle_roll','manual','gift_card'));

DROP INDEX IF EXISTS ledger_transactions_plan_change_refund_unique;
DROP INDEX IF EXISTS idx_subscription_events_plan_change_order;
DROP INDEX IF EXISTS orders_one_active_subscription_change;
CREATE UNIQUE INDEX orders_one_active_renewal
  ON orders (tenant_id, subscription_id)
  WHERE kind = 'renewal'
    AND status IN ('draft', 'pending_payment', 'processing', 'paid');

ALTER TABLE orders DROP CONSTRAINT orders_total_identity;
ALTER TABLE orders ADD CONSTRAINT orders_total_identity
  CHECK (total_amount = subtotal_amount - discount_amount + tax_amount);
ALTER TABLE orders
  DROP CONSTRAINT IF EXISTS orders_proration_only_plan_change,
  DROP COLUMN IF EXISTS proration_credit_amount;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.bind_idempotency_resource(
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.complete_bound_idempotency_success(
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

