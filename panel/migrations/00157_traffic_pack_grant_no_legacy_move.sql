-- 流量包改挂守卫去掉「升级前的旧包可以从生效中的那份挪一次」（用户 2026-10-09 定；w15legacy）。
--
-- 00137 的 app.guard_traffic_pack_grant 允许改挂的情形 3：只有 00138 回填写的 migration 流水、
-- 没有用户或后台流水的余额，可以从生效中的那份挪一次。它只为「升级时已有流量包、被回填挂到
-- 到期最晚那份」的老用户存在。项目没有任何生产部署，全新安装时 00138 面对的是空表、一条
-- migration 流水也写不出来，以后也再没有别的地方写 actor_kind = 'migration'；门户挪一次的入口
-- 同一提交删掉。
--
-- 这里在 00137 版上删掉情形 3，其余逐字不变。改挂只剩两种来源：原来为空（未分配），或原来那份
-- 已经彻底停用（cancelled，或 expired 且 renewal_closed_at 非空）；从生效中或可救回的那份转出，
-- 不论余额有没有 migration 流水，一律拒绝。守卫仍包在 IF NEW.subscription_id IS DISTINCT FROM
-- OLD.subscription_id 里，扣量的 UPDATE 不受影响；放行的路径（来源为空或已停用）查询次数不变，
-- 拒绝的路径少两次转移流水探测。
--
-- 同一口径收紧 app.assert_traffic_pack_order（00137 版）：addon 单带 subscription_id 才核对的
-- 写法，是给升级前没有指定订阅的在途单留的口子。现在只有 CreateTrafficPackOrder 建 addon 单，
-- 必带一份；履约时没有订阅也改成报错（billing.fulfillTrafficPackOrder）。这里改成 addon 单必须
-- 指定一份，否则拒绝；后面两处「带订阅时」的条件随之去掉，其余逐字不变。
--
-- 回滚：新口径比 00137 严，按新口径写下的数据在旧函数下都合法，Down 不需要数据守卫，两个函数
-- 直接还原 00137 原文。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
-- 基于 00137 版：删掉情形 3（00157），其余逐字不变。
CREATE OR REPLACE FUNCTION app.guard_traffic_pack_grant() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'traffic pack grants are append-only'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
     OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.source IS DISTINCT FROM OLD.source
     OR NEW.source_id IS DISTINCT FROM OLD.source_id
     OR NEW.granted_bytes IS DISTINCT FROM OLD.granted_bytes
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'traffic pack grant identity is immutable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.consumed_bytes < OLD.consumed_bytes THEN
    RAISE EXCEPTION 'traffic pack consumption cannot go backwards'
      USING ERRCODE = 'check_violation';
  END IF;
  -- 改挂（00137）：只能从「未分配」或「彻底停用」的那份，挂到同一用户生效中或可救回的一份
  IF NEW.subscription_id IS DISTINCT FROM OLD.subscription_id THEN
    IF NEW.subscription_id IS NULL THEN
      RAISE EXCEPTION 'traffic pack grant % cannot be detached from its subscription', OLD.id
        USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM subscriptions s
                    WHERE s.tenant_id = NEW.tenant_id AND s.id = NEW.subscription_id
                      AND s.user_id = NEW.user_id
                      AND (s.status IN ('active','trialing','grace','past_due')
                           OR (s.status = 'expired' AND s.renewal_closed_at IS NULL))) THEN
      RAISE EXCEPTION 'traffic pack grant % can only move to a live or revivable subscription of the same user', OLD.id
        USING ERRCODE = 'check_violation';
    END IF;
    -- 来源不为空时必须已经彻底停用（00157 删掉了升级前旧包的例外：不看转移流水）
    IF OLD.subscription_id IS NOT NULL AND NOT EXISTS (
         SELECT 1 FROM subscriptions s
          WHERE s.tenant_id = OLD.tenant_id AND s.id = OLD.subscription_id
            AND (s.status = 'cancelled'
                 OR (s.status = 'expired' AND s.renewal_closed_at IS NOT NULL))) THEN
      RAISE EXCEPTION 'traffic pack grant % can only leave an unattached or ended subscription', OLD.id
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  NEW.updated_at := now();
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- 基于 00137 版：addon 单必须带 subscription_id（00157），其余逐字不变。
CREATE OR REPLACE FUNCTION app.assert_traffic_pack_order(p_tenant uuid, p_order uuid)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_kind text;
  v_status text;
  v_user uuid;
  v_items int;
  v_pack_items int;
  v_snapshot bigint;
  v_grants int;
  v_granted bigint;
  v_sub uuid;
BEGIN
  SELECT o.kind, o.status, o.user_id, o.subscription_id INTO v_kind, v_status, v_user, v_sub
    FROM orders o WHERE o.tenant_id = p_tenant AND o.id = p_order;
  IF NOT FOUND THEN
    RETURN;
  END IF;

  SELECT count(*), count(*) FILTER (WHERE oi.traffic_pack_id IS NOT NULL),
         max((oi.snapshot_quotas->0->>'limit')::bigint)
           FILTER (WHERE oi.traffic_pack_id IS NOT NULL
                     AND oi.snapshot_quotas->0->>'metric' = 'traffic.bytes')
    INTO v_items, v_pack_items, v_snapshot
    FROM order_items oi WHERE oi.tenant_id = p_tenant AND oi.order_id = p_order;

  IF v_kind <> 'addon' THEN
    IF v_pack_items <> 0 THEN
      RAISE EXCEPTION 'order % kind % cannot carry a traffic pack line', p_order, v_kind
        USING ERRCODE = 'check_violation';
    END IF;
    RETURN;
  END IF;

  IF v_items <> 1 OR v_pack_items <> 1 OR v_snapshot IS NULL OR v_snapshot <= 0 THEN
    RAISE EXCEPTION 'addon order % must have exactly one traffic pack line with a byte snapshot', p_order
      USING ERRCODE = 'check_violation';
  END IF;

  -- addon 单必须指定加到哪一份（00157：不再放行升级前没有订阅的在途单）
  IF v_sub IS NULL THEN
    RAISE EXCEPTION 'addon order % must target a subscription', p_order
      USING ERRCODE = 'check_violation';
  END IF;

  -- addon 单指定的那一份必须是下单人自己的（00137）
  IF NOT EXISTS (
       SELECT 1 FROM subscriptions s
        WHERE s.tenant_id = p_tenant AND s.id = v_sub AND s.user_id = v_user) THEN
    RAISE EXCEPTION 'addon order % targets a subscription of another user', p_order
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT count(*), max(g.granted_bytes) INTO v_grants, v_granted
    FROM traffic_pack_grants g
   WHERE g.tenant_id = p_tenant AND g.source = 'order' AND g.source_id = p_order;
  IF v_status IN ('fulfilled','partially_refunded','refunded') THEN
    IF v_grants <> 1 OR v_granted <> v_snapshot THEN
      RAISE EXCEPTION 'fulfilled addon order % lacks its exact traffic pack grant', p_order
        USING ERRCODE = 'check_violation';
    END IF;
  ELSIF v_grants <> 0 THEN
    RAISE EXCEPTION 'addon order % in status % already has a traffic pack grant', p_order, v_status
      USING ERRCODE = 'check_violation';
  END IF;
  IF EXISTS (SELECT 1 FROM traffic_pack_grants g
              WHERE g.tenant_id = p_tenant AND g.source = 'order'
                AND g.source_id = p_order AND g.user_id <> v_user) THEN
    RAISE EXCEPTION 'traffic pack grant for order % belongs to another user', p_order
      USING ERRCODE = 'check_violation';
  END IF;
  -- 余额挂在订单指定的那一份上；按规则转走过的，最早一条转移流水的来源必须是那一份（00137）
  IF EXISTS (
       SELECT 1 FROM traffic_pack_grants g
        WHERE g.tenant_id = p_tenant AND g.source = 'order' AND g.source_id = p_order
          AND g.subscription_id IS DISTINCT FROM v_sub
          AND (SELECT t.from_subscription_id FROM traffic_pack_transfers t
                WHERE t.tenant_id = g.tenant_id AND t.grant_id = g.id
                ORDER BY t.created_at, t.id LIMIT 1) IS DISTINCT FROM v_sub) THEN
    RAISE EXCEPTION 'traffic pack grant for order % is not attached to the ordered subscription', p_order
      USING ERRCODE = 'check_violation';
  END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 还原成 00137 的原文（find-def.py app.guard_traffic_pack_grant --before 00157 --body，逐字）
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_traffic_pack_grant() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'traffic pack grants are append-only'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
     OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.source IS DISTINCT FROM OLD.source
     OR NEW.source_id IS DISTINCT FROM OLD.source_id
     OR NEW.granted_bytes IS DISTINCT FROM OLD.granted_bytes
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'traffic pack grant identity is immutable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.consumed_bytes < OLD.consumed_bytes THEN
    RAISE EXCEPTION 'traffic pack consumption cannot go backwards'
      USING ERRCODE = 'check_violation';
  END IF;
  -- 改挂（00137）：只能从「未分配」或「彻底停用」的那份，挂到同一用户生效中或可救回的一份
  IF NEW.subscription_id IS DISTINCT FROM OLD.subscription_id THEN
    IF NEW.subscription_id IS NULL THEN
      RAISE EXCEPTION 'traffic pack grant % cannot be detached from its subscription', OLD.id
        USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM subscriptions s
                    WHERE s.tenant_id = NEW.tenant_id AND s.id = NEW.subscription_id
                      AND s.user_id = NEW.user_id
                      AND (s.status IN ('active','trialing','grace','past_due')
                           OR (s.status = 'expired' AND s.renewal_closed_at IS NULL))) THEN
      RAISE EXCEPTION 'traffic pack grant % can only move to a live or revivable subscription of the same user', OLD.id
        USING ERRCODE = 'check_violation';
    END IF;
    -- 例外（用户 2026-10-07 定）：升级前的旧余额由 00138 回填挂上，只有 migration 流水、没有
    -- 用户或后台挪过的，可以从生效中的那份挪一次（这次挪动写 user 流水，之后按原规则）
    IF OLD.subscription_id IS NOT NULL AND NOT EXISTS (
         SELECT 1 FROM subscriptions s
          WHERE s.tenant_id = OLD.tenant_id AND s.id = OLD.subscription_id
            AND (s.status = 'cancelled'
                 OR (s.status = 'expired' AND s.renewal_closed_at IS NOT NULL)))
       AND NOT (EXISTS (SELECT 1 FROM traffic_pack_transfers t
                         WHERE t.tenant_id = OLD.tenant_id AND t.grant_id = OLD.id
                           AND t.actor_kind = 'migration')
                AND NOT EXISTS (SELECT 1 FROM traffic_pack_transfers t
                                 WHERE t.tenant_id = OLD.tenant_id AND t.grant_id = OLD.id
                                   AND t.actor_kind <> 'migration')) THEN
      RAISE EXCEPTION 'traffic pack grant % can only leave an unattached or ended subscription', OLD.id
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  NEW.updated_at := now();
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- 还原成 00137 的原文
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.assert_traffic_pack_order(p_tenant uuid, p_order uuid)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_kind text;
  v_status text;
  v_user uuid;
  v_items int;
  v_pack_items int;
  v_snapshot bigint;
  v_grants int;
  v_granted bigint;
  v_sub uuid;
BEGIN
  SELECT o.kind, o.status, o.user_id, o.subscription_id INTO v_kind, v_status, v_user, v_sub
    FROM orders o WHERE o.tenant_id = p_tenant AND o.id = p_order;
  IF NOT FOUND THEN
    RETURN;
  END IF;

  SELECT count(*), count(*) FILTER (WHERE oi.traffic_pack_id IS NOT NULL),
         max((oi.snapshot_quotas->0->>'limit')::bigint)
           FILTER (WHERE oi.traffic_pack_id IS NOT NULL
                     AND oi.snapshot_quotas->0->>'metric' = 'traffic.bytes')
    INTO v_items, v_pack_items, v_snapshot
    FROM order_items oi WHERE oi.tenant_id = p_tenant AND oi.order_id = p_order;

  IF v_kind <> 'addon' THEN
    IF v_pack_items <> 0 THEN
      RAISE EXCEPTION 'order % kind % cannot carry a traffic pack line', p_order, v_kind
        USING ERRCODE = 'check_violation';
    END IF;
    RETURN;
  END IF;

  IF v_items <> 1 OR v_pack_items <> 1 OR v_snapshot IS NULL OR v_snapshot <= 0 THEN
    RAISE EXCEPTION 'addon order % must have exactly one traffic pack line with a byte snapshot', p_order
      USING ERRCODE = 'check_violation';
  END IF;

  -- addon 单指定的那一份必须是下单人自己的（00137）
  IF v_sub IS NOT NULL AND NOT EXISTS (
       SELECT 1 FROM subscriptions s
        WHERE s.tenant_id = p_tenant AND s.id = v_sub AND s.user_id = v_user) THEN
    RAISE EXCEPTION 'addon order % targets a subscription of another user', p_order
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT count(*), max(g.granted_bytes) INTO v_grants, v_granted
    FROM traffic_pack_grants g
   WHERE g.tenant_id = p_tenant AND g.source = 'order' AND g.source_id = p_order;
  IF v_status IN ('fulfilled','partially_refunded','refunded') THEN
    IF v_grants <> 1 OR v_granted <> v_snapshot THEN
      RAISE EXCEPTION 'fulfilled addon order % lacks its exact traffic pack grant', p_order
        USING ERRCODE = 'check_violation';
    END IF;
  ELSIF v_grants <> 0 THEN
    RAISE EXCEPTION 'addon order % in status % already has a traffic pack grant', p_order, v_status
      USING ERRCODE = 'check_violation';
  END IF;
  IF EXISTS (SELECT 1 FROM traffic_pack_grants g
              WHERE g.tenant_id = p_tenant AND g.source = 'order'
                AND g.source_id = p_order AND g.user_id <> v_user) THEN
    RAISE EXCEPTION 'traffic pack grant for order % belongs to another user', p_order
      USING ERRCODE = 'check_violation';
  END IF;
  -- 余额挂在订单指定的那一份上；按规则转走过的，最早一条转移流水的来源必须是那一份（00137）
  IF v_sub IS NOT NULL AND EXISTS (
       SELECT 1 FROM traffic_pack_grants g
        WHERE g.tenant_id = p_tenant AND g.source = 'order' AND g.source_id = p_order
          AND g.subscription_id IS DISTINCT FROM v_sub
          AND (SELECT t.from_subscription_id FROM traffic_pack_transfers t
                WHERE t.tenant_id = g.tenant_id AND t.grant_id = g.id
                ORDER BY t.created_at, t.id LIMIT 1) IS DISTINCT FROM v_sub) THEN
    RAISE EXCEPTION 'traffic pack grant for order % is not attached to the ordered subscription', p_order
      USING ERRCODE = 'check_violation';
  END IF;
END;
$$;
-- +goose StatementEnd
