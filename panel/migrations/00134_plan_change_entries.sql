-- 换套餐的三个入口统一（用户 2026-10-07，w6plan）：后台人工开单、套餐卡遇到不同套餐时，
-- 不再新开订阅、换链接，而是在用户原订阅上换套餐。折算与履约与门户改套餐是同一份
-- （billing 的 plan_change_quote.go / applyPlanChangeTx）：原套餐还没用完的已付费剩余价值
-- 先抵新价，抵不完的退进余额；赠送单与套餐卡本身算 0 元，剩余价值全额退进余额。
--
-- 两处数据库守卫要认识新入口，都是 CREATE OR REPLACE，其余逐字不变：
--
-- 1) 订单 / 幂等对称绑定（00125 版 app.assert_idempotency_order_binding）。后台人工开单的
--    入口 POST /orders/manual 用新购的幂等域 order_create（声明属于管理员，订单 created_by
--    是管理员）。用户已有别的套餐的订阅时，CreateManualOrder 在原订阅上建一张
--    kind = 'upgrade' 的变更单。这里只放开一种组合：upgrade 单且 created_by 非空（管理员
--    代开）时，期望的幂等域是 order_create；created_by 为空的门户变更单仍然只认
--    subscription_change_plan_create。与 00125 放开人工续费单同一写法。
--
-- 2) 变更证据（00071 版 app.assert_plan_change_order_trigger）。套餐卡兑换必须和「标记码
--    已用」在同一个事务里，没有订单；00071 要求 plan_changed 事件与 plan_change_refund
--    分录都挂在一张 upgrade 单上。这里加一种来源：卡密。
--      * plan_changed 事件 order_id 为空、payload.source = 'gift_card'、payload 带
--        gift_card_code_id；
--      * 退余额分录 source_type = 'gift_card_code'、source_id = 卡密；
--      * 两者都交给新函数 app.assert_gift_plan_change 整组核对（提交时）：卡密有兑换流水；
--        事件落在兑换人自己的订阅上，且兑换人的订阅上恰好一条这张卡的 plan_changed 事件；事件记的 balance_refund 为 0
--        时一笔退款都没有，大于 0 时恰好一笔、两条分录（借 平台收入、贷 兑换人同币种
--        余额），金额与币种和事件一致。
--    订单来源的核对（assert_plan_change_order）不变；既不是订单也不是卡密的仍然拒绝。

-- +goose Up
SET LOCAL lock_timeout = '5s';

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
            OR k.actor_id IS DISTINCT FROM coalesce(o.created_by,o.user_id)
            OR k.scope IS DISTINCT FROM app.idempotency_actor_scope(
              CASE o.kind
                WHEN 'new' THEN 'order_create'
                WHEN 'addon' THEN 'order_create'
                WHEN 'renewal' THEN CASE WHEN o.created_by IS NULL
                                         THEN 'subscription_renewal_create'
                                         ELSE 'order_create' END
                WHEN 'upgrade' THEN CASE WHEN o.created_by IS NULL
                                         THEN 'subscription_change_plan_create'
                                         ELSE 'order_create' END
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
                WHEN 'renewal' THEN CASE WHEN o.created_by IS NULL
                                         THEN 'subscription_renewal_create'
                                         ELSE 'order_create' END
                WHEN 'upgrade' THEN CASE WHEN o.created_by IS NULL
                                         THEN 'subscription_change_plan_create'
                                         ELSE 'order_create' END
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
CREATE FUNCTION app.assert_gift_plan_change(p_tenant uuid, p_code uuid, p_sub uuid)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_user uuid;
  v_events int;
  v_refund bigint;
  v_currency text;
  v_txns int;
  v_ok boolean;
BEGIN
  -- 兑换流水与事件、分录同一事务写入；守卫是延迟触发，提交时流水一定已经在了
  SELECT r.user_id INTO v_user
    FROM gift_card_redemptions r
   WHERE r.tenant_id = p_tenant AND r.code_id = p_code;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'gift card plan change for code % has no redemption', p_code
      USING ERRCODE = 'check_violation';
  END IF;
  -- 从事件一侧进来时，事件所在的订阅必须是兑换人自己的
  IF p_sub IS NOT NULL AND NOT EXISTS (
       SELECT 1 FROM subscriptions s
        WHERE s.tenant_id = p_tenant AND s.id = p_sub AND s.user_id = v_user) THEN
    RAISE EXCEPTION 'gift card plan change event for code % is on a subscription its redeemer does not own', p_code
      USING ERRCODE = 'check_violation';
  END IF;

  -- 恰好一条 plan_changed 事件，落在兑换人自己的订阅上
  SELECT count(*), max((e.payload->>'balance_refund')::bigint), max(e.payload->>'refund_currency')
    INTO v_events, v_refund, v_currency
    FROM subscriptions s
    JOIN subscription_events e
      ON e.tenant_id = s.tenant_id AND e.subscription_id = s.id
   WHERE s.tenant_id = p_tenant AND s.user_id = v_user
     AND e.event_type = 'plan_changed' AND e.order_id IS NULL
     AND e.payload->>'gift_card_code_id' = p_code::text;
  IF v_events <> 1 THEN
    RAISE EXCEPTION 'gift card code % must carry exactly one plan change event on its redeemer''s subscription, found %',
      p_code, v_events USING ERRCODE = 'check_violation';
  END IF;
  IF v_refund IS NULL OR v_refund < 0 THEN
    RAISE EXCEPTION 'gift card plan change for code % has no valid balance_refund', p_code
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT count(*) INTO v_txns
    FROM ledger_transactions t
   WHERE t.tenant_id = p_tenant AND t.source_type = 'gift_card_code'
     AND t.source_id = p_code AND t.kind = 'plan_change_refund';
  IF v_refund = 0 THEN
    IF v_txns <> 0 THEN
      RAISE EXCEPTION 'gift card plan change for code % owes no balance refund but has one', p_code
        USING ERRCODE = 'check_violation';
    END IF;
    RETURN;
  END IF;

  -- 两条分录：借 平台收入、贷 兑换人同币种余额，金额都等于事件里记的退款
  SELECT v_txns = 1
     AND (SELECT count(*) FROM ledger_entries e
           JOIN ledger_transactions t ON t.id = e.transaction_id
          WHERE t.tenant_id = p_tenant AND t.source_type = 'gift_card_code'
            AND t.source_id = p_code AND t.kind = 'plan_change_refund') = 2
     AND EXISTS (
       SELECT 1 FROM ledger_transactions t
         JOIN ledger_entries c ON c.transaction_id = t.id AND c.direction = 'credit'
         JOIN ledger_accounts ca ON ca.id = c.account_id
         JOIN ledger_entries d ON d.transaction_id = t.id AND d.direction = 'debit'
         JOIN ledger_accounts da ON da.id = d.account_id
        WHERE t.tenant_id = p_tenant AND t.source_type = 'gift_card_code'
          AND t.source_id = p_code AND t.kind = 'plan_change_refund'
          AND t.currency = v_currency
          AND c.amount = v_refund AND c.currency = v_currency
          AND ca.tenant_id = p_tenant AND ca.account_type = 'user_balance'
          AND ca.owner_user_id = v_user AND ca.currency = v_currency
          AND d.amount = v_refund AND d.currency = v_currency
          AND da.tenant_id = p_tenant AND da.account_type = 'platform_revenue'
          AND da.currency = v_currency)
    INTO v_ok;
  IF NOT v_ok THEN
    RAISE EXCEPTION 'gift card plan change for code % lacks its exact balance refund of %', p_code, v_refund
      USING ERRCODE = 'check_violation';
  END IF;
END;
$$;

CREATE OR REPLACE FUNCTION app.assert_plan_change_order_trigger() RETURNS trigger
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
    -- 套餐卡换套餐（00134）：没有订单，证据挂在卡密上，整组核对交给 assert_gift_plan_change
    IF v_order IS NULL AND v_row->'payload'->>'source' = 'gift_card'
       AND v_row->'payload'->>'gift_card_code_id' IS NOT NULL THEN
      PERFORM app.assert_gift_plan_change(v_tenant,
        (v_row->'payload'->>'gift_card_code_id')::uuid, (v_row->>'subscription_id')::uuid);
      RETURN NULL;
    END IF;
    IF v_order IS NULL OR NOT EXISTS (SELECT 1 FROM orders o
                  WHERE o.tenant_id = v_tenant AND o.id = v_order AND o.kind = 'upgrade') THEN
      RAISE EXCEPTION 'plan_changed event % has no plan change order', v_row->>'id'
        USING ERRCODE = 'check_violation';
    END IF;
  ELSIF TG_TABLE_NAME = 'ledger_transactions' THEN
    v_order := (v_row->>'source_id')::uuid;
    -- 套餐卡换套餐的退余额分录（00134）：来源是卡密
    IF v_row->>'source_type' = 'gift_card_code' AND v_order IS NOT NULL THEN
      PERFORM app.assert_gift_plan_change(v_tenant, v_order, NULL);
      RETURN NULL;
    END IF;
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
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- 还原 00125 的绑定函数与 00071 的证据触发函数（逐字），删掉卡密来源的核对函数。
-- 已经存在的后台代开变更单、套餐卡换套餐证据在还原之后会被旧断言拒绝（订单再被写时、
-- 或旧触发函数遇到无订单的 plan_changed 事件），所以有这些行时拒绝回滚，需要先人工核对。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM orders WHERE kind = 'upgrade' AND created_by IS NOT NULL)
     OR EXISTS (SELECT 1 FROM subscription_events
                 WHERE event_type = 'plan_changed' AND order_id IS NULL)
     OR EXISTS (SELECT 1 FROM ledger_transactions
                 WHERE kind = 'plan_change_refund' AND source_type = 'gift_card_code') THEN
    RAISE EXCEPTION '00134 Down refused: admin plan change orders or gift card plan change evidence exist';
  END IF;
END $$;
-- +goose StatementEnd

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
            OR k.actor_id IS DISTINCT FROM coalesce(o.created_by,o.user_id)
            OR k.scope IS DISTINCT FROM app.idempotency_actor_scope(
              CASE o.kind
                WHEN 'new' THEN 'order_create'
                WHEN 'addon' THEN 'order_create'
                WHEN 'renewal' THEN CASE WHEN o.created_by IS NULL
                                         THEN 'subscription_renewal_create'
                                         ELSE 'order_create' END
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
                WHEN 'renewal' THEN CASE WHEN o.created_by IS NULL
                                         THEN 'subscription_renewal_create'
                                         ELSE 'order_create' END
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
CREATE OR REPLACE FUNCTION app.assert_plan_change_order_trigger() RETURNS trigger
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

DROP FUNCTION IF EXISTS app.assert_gift_plan_change(uuid, uuid, uuid);
-- +goose StatementEnd
