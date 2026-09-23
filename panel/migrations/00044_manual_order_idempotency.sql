-- +goose Up

-- 让订单/幂等的对称绑定不变量认识「管理员替用户开的单」（XBD-015）。
--
-- 原来的不变量写死了一条假设：幂等键的 actor 必须等于订单的 user_id，
-- 也就是「下单的人就是订单归属的人」。这对用户自助下单永远成立，
-- 但人工赠送单是管理员替用户开的 —— 发起人是管理员，归属人是收礼物的用户。
--
-- 绕开不变量（比如让人工单不写 idempotency_key_id）也能跑通，但那等于在
-- 唯一一条能防住「同一次点击开出两张赠送单」的防线上开个口子。
-- 正确的做法是把「谁发起的」显式化：
--
--     created_by IS NULL  → 用户自助下单，发起人 = user_id
--     created_by 有值      → 管理员代开，发起人 = created_by
--
-- 注意判断依据是 created_by 而不是 kind。人工单的 kind 仍然是 'new' ——
-- 它的形状（订单项、库存预留、限购预留、事件链）和普通新购完全一致，
-- 整套不变量正是围绕这几种形状写的，新造一个 kind 得把它们逐个改一遍。

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

-- 管理员代开的单必须写明理由。原来的 CHECK 只在 kind='manual' 时生效，
-- 而人工单现在的 kind 是 'new'，那条约束就管不到它了。
-- 改成按 created_by 判断，才对得上真正要防的事：
-- 一张没有说明为什么送出去的赠送单，事后没有任何人能解释。
ALTER TABLE public.orders
  ADD CONSTRAINT orders_admin_created_requires_reason_00044_check
  CHECK (created_by IS NULL
      OR (manual_reason IS NOT NULL AND length(manual_reason) >= 5));

-- +goose Down

ALTER TABLE public.orders
  DROP CONSTRAINT IF EXISTS orders_admin_created_requires_reason_00044_check;

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
