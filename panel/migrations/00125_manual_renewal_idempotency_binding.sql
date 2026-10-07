-- 后台人工开单遇到同套餐订阅时改开续费单（规则 3，w5expiry）。
--
-- 人工开单的入口 POST /orders/manual 用的是新购的幂等域 order_create（声明属于管理员，
-- 订单 created_by 是管理员）。用户已有同一套餐的订阅时，billing.CreateManualOrder 不再
-- 新开订阅、换链接，而是在原订阅上建一张 kind = 'renewal' 的续费单。原有的订单 / 幂等
-- 对称绑定（00071 版 app.assert_idempotency_order_binding）把 renewal 钉死在门户续费的
-- subscription_renewal_create 域，这样的单在提交时会被判成绑定不对称。
--
-- 这里只放开一种组合：renewal 单且 created_by 非空（管理员代开）时，期望的幂等域是
-- order_create；created_by 为空的门户续费单仍然只认 subscription_renewal_create。
-- 声明人必须是 created_by 那条没有变（actor_id = coalesce(created_by, user_id)）。
-- 函数体其余部分与 00071 逐字相同。

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

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- 还原 00071 的函数体（逐字）。已存在的管理员续费单不受影响：这个断言只在订单或
-- 幂等键行被写时触发，回滚之后再改这类订单会被拒，需要先人工核对。
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
