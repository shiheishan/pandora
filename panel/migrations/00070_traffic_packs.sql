-- 流量包（C1，D-E-1）：流量包商品目录 + 挂在用户身上的流量包余额。
--
-- 用户拍板的规则（D-E-1）：
--   * 流量包挂用户，不挂订阅；永不过期，用完为止；可叠加（剩 30G 再买 100G 即 130G）。
--   * 每个周期先扣套餐额度，扣完再扣流量包；流量包不随周期重置，
--     订阅到期或续费时余量保留。
--   * 必须有生效订阅才能消耗（节点只给生效订阅下发）；没有订阅时可以买、留着。
--   * 礼品卡赠送的流量并入同一余额；已发放未用完的 granted_addon 一次性转入。
--
-- 此前唯一相近的东西是 quota_balances.granted_addon：礼品卡把流量加在订阅的
-- 配额行上，而配额行跨周期复用、重置与续费都不清它，一次性追加于是每个周期
-- 重新可用（缺陷 16）。这里把存量 addon 折成流量包余额后清零，并用 CHECK 把
-- 这一列钉死为 0，那条路径从此不再存在。
--
-- 订单：流量包订单 kind='addon'，一行订单项，订单项不指向套餐而指向流量包
-- （traffic_pack_id），购买时的容量快照在 snapshot_quotas 里。00036 的通用
-- 形状检查（非充值单的订单项合计 = 小计、币种一致、零元单必须同事务履约）
-- 对它照常生效；这里再加一道 addon 专属的形状与履约证据检查。
-- 幂等域沿用 order_create（与人工单同一做法），只需让订单/幂等对称绑定认识
-- addon，不必改 00038/00039 的绑定与完成函数。

-- +goose Up

-- +goose StatementBegin
CREATE TABLE traffic_packs (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name          text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 60),
  traffic_bytes bigint NOT NULL CHECK (traffic_bytes > 0),
  currency      app.currency_code NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY','USD')),
  unit_amount   app.minor_amount NOT NULL CHECK (unit_amount > 0),
  recommended   boolean NOT NULL DEFAULT false,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
  sort_order    int NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT traffic_packs_tenant_id_id_key UNIQUE (tenant_id, id)
);
CREATE INDEX idx_traffic_packs_catalog ON traffic_packs (tenant_id, status, sort_order);
CREATE TRIGGER trg_traffic_packs_updated_at BEFORE UPDATE ON traffic_packs
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();
SELECT app.enable_tenant_rls('traffic_packs');
GRANT SELECT, INSERT, UPDATE ON traffic_packs TO aegis_app;
REVOKE DELETE, TRUNCATE ON traffic_packs FROM aegis_app;

-- 一笔流量包余额。source 说明它从哪来：
--   order     —— 购买（source_id = orders.id，一单一笔）
--   gift_card —— 礼品卡赠送（source_id = gift_card_codes.id，一码一笔）
--   migration —— 00070 从存量 granted_addon 折算（source_id = subscriptions.id）
CREATE TABLE traffic_pack_grants (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id        uuid NOT NULL,
  source         text NOT NULL CHECK (source IN ('order','gift_card','migration')),
  source_id      uuid NOT NULL,
  granted_bytes  bigint NOT NULL CHECK (granted_bytes > 0),
  consumed_bytes bigint NOT NULL DEFAULT 0
                   CHECK (consumed_bytes >= 0 AND consumed_bytes <= granted_bytes),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT traffic_pack_grants_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT traffic_pack_grants_source_unique UNIQUE (tenant_id, source, source_id),
  CONSTRAINT traffic_pack_grants_user_fk
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE RESTRICT
);
-- 扣量与节点下发只关心「还有剩余」的那几笔，按先到先扣排序。
CREATE INDEX idx_traffic_pack_grants_open
  ON traffic_pack_grants (tenant_id, user_id, created_at, id)
  WHERE consumed_bytes < granted_bytes;
CREATE INDEX idx_traffic_pack_grants_user
  ON traffic_pack_grants (tenant_id, user_id, created_at DESC);
SELECT app.enable_tenant_rls('traffic_pack_grants');
GRANT SELECT, INSERT, UPDATE ON traffic_pack_grants TO aegis_app;
REVOKE DELETE, TRUNCATE ON traffic_pack_grants FROM aegis_app;
-- +goose StatementEnd

-- +goose StatementBegin
-- 一笔余额发出去之后，只有「已用」可以往上涨：来源、归属、容量都不可改，
-- 也不能删 —— 否则「我买的 100G 去哪了」没有任何东西能对质。
CREATE FUNCTION app.guard_traffic_pack_grant() RETURNS trigger
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
  NEW.updated_at := now();
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER traffic_pack_grants_guard
  BEFORE UPDATE OR DELETE ON traffic_pack_grants
  FOR EACH ROW EXECUTE FUNCTION app.guard_traffic_pack_grant();

-- 用户侧的余额与目录变化推给在线的前端（notify_change 按 user_id 定向）。
-- 余额只在「新增一笔」时推：UPDATE 只会是扣量，频率就是节点上报流量的频率，
-- 推它等于每次上报给用户发一条 subscriptions.changed（与 00076 摘掉
-- quota_balances 同一个理由）；DELETE 被 guard 拒绝，不会发生。
CREATE TRIGGER zz_notify_traffic_pack_grants
  AFTER INSERT ON traffic_pack_grants
  FOR EACH ROW EXECUTE FUNCTION app.notify_change();
CREATE TRIGGER zz_notify_traffic_packs
  AFTER INSERT OR UPDATE OR DELETE ON traffic_packs
  FOR EACH ROW EXECUTE FUNCTION app.notify_change();
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE order_items
  ADD COLUMN traffic_pack_id uuid,
  ADD CONSTRAINT order_items_traffic_pack_fk
    FOREIGN KEY (tenant_id, traffic_pack_id)
    REFERENCES traffic_packs (tenant_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT order_items_plan_or_traffic_pack
    CHECK (traffic_pack_id IS NULL OR plan_id IS NULL);
-- order_items 是按列授权的（00036 的白名单）：新列要单独放给运行时角色，
-- 否则全新部署的库里流量包下单会被 permission denied。
GRANT INSERT (traffic_pack_id) ON order_items TO aegis_app;
-- +goose StatementEnd

-- +goose StatementBegin
-- addon 订单的形状与履约证据：
--   * 恰好一行订单项，指向流量包、不指向套餐，快照里写明容量；
--   * 别的 kind 的订单项不能指向流量包；
--   * 已履约的 addon 订单恰好有一笔 source='order' 的余额，容量等于快照；
--   * source='order' 的余额必须对应一张同用户、已履约的 addon 订单。
CREATE FUNCTION app.assert_traffic_pack_order(p_tenant uuid, p_order uuid)
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
BEGIN
  SELECT o.kind, o.status, o.user_id INTO v_kind, v_status, v_user
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
END;
$$;

CREATE FUNCTION app.assert_traffic_pack_order_trigger() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_row jsonb := CASE WHEN TG_OP = 'DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
BEGIN
  IF TG_TABLE_NAME = 'orders' THEN
    PERFORM app.assert_traffic_pack_order((v_row->>'tenant_id')::uuid, (v_row->>'id')::uuid);
  ELSIF TG_TABLE_NAME = 'order_items' THEN
    PERFORM app.assert_traffic_pack_order((v_row->>'tenant_id')::uuid, (v_row->>'order_id')::uuid);
  ELSIF TG_TABLE_NAME = 'traffic_pack_grants' AND v_row->>'source' = 'order' THEN
    IF NOT EXISTS (SELECT 1 FROM orders o
                    WHERE o.tenant_id = (v_row->>'tenant_id')::uuid
                      AND o.id = (v_row->>'source_id')::uuid AND o.kind = 'addon') THEN
      RAISE EXCEPTION 'order-sourced traffic pack grant % has no addon order', v_row->>'id'
        USING ERRCODE = 'check_violation';
    END IF;
    PERFORM app.assert_traffic_pack_order((v_row->>'tenant_id')::uuid, (v_row->>'source_id')::uuid);
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER trg_traffic_pack_order_shape_orders
  AFTER INSERT OR UPDATE ON orders DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION app.assert_traffic_pack_order_trigger();
CREATE CONSTRAINT TRIGGER trg_traffic_pack_order_shape_items
  AFTER INSERT OR UPDATE OR DELETE ON order_items DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION app.assert_traffic_pack_order_trigger();
CREATE CONSTRAINT TRIGGER trg_traffic_pack_order_shape_grants
  AFTER INSERT ON traffic_pack_grants DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION app.assert_traffic_pack_order_trigger();
-- +goose StatementEnd

-- +goose StatementBegin
-- 订单/幂等对称绑定认识 addon：流量包订单与普通新购共用 order_create 幂等域。
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
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- 存量 granted_addon 一次性折成流量包余额（D-E-1）。
--
-- 礼品卡流量此前加在订阅每一条 traffic.bytes 配额行上。每个订阅取一条代表行
-- （优先 cycle，其次最近的周期）：
--   已用掉的 addon = clamp(consumed − (limit + adjusted), 0, addon)
--   余额           = addon，已用 = 上式；不限量的行没有「超出套餐」这回事，已用记 0。
-- 然后把每条带 addon 的限量行的 consumed 减去它自己那份已用 addon，让 consumed
-- 回到「只算套餐额度」的口径；最后 addon 全部清零并钉死。
INSERT INTO traffic_pack_grants
  (tenant_id, user_id, source, source_id, granted_bytes, consumed_bytes, created_at)
SELECT r.tenant_id, s.user_id, 'migration', r.subscription_id, r.granted_addon,
       CASE WHEN r.limit_value IS NULL THEN 0
            ELSE least(greatest(r.consumed - (r.limit_value + r.adjusted), 0), r.granted_addon)
       END,
       now()
  FROM (SELECT DISTINCT ON (qb.subscription_id)
               qb.tenant_id, qb.subscription_id, qb.granted_addon, qb.consumed,
               qb.limit_value, qb.adjusted
          FROM quota_balances qb
         WHERE qb.metric = 'traffic.bytes' AND qb.granted_addon > 0
         ORDER BY qb.subscription_id, (qb.period = 'cycle') DESC,
                  qb.period_end DESC NULLS LAST, qb.id) r
  JOIN subscriptions s ON s.tenant_id = r.tenant_id AND s.id = r.subscription_id;

UPDATE quota_balances
   SET consumed = consumed
         - least(greatest(consumed - (limit_value + adjusted), 0), granted_addon),
       updated_at = now()
 WHERE metric = 'traffic.bytes' AND granted_addon > 0 AND limit_value IS NOT NULL;

UPDATE quota_balances SET granted_addon = 0, updated_at = now() WHERE granted_addon <> 0;

ALTER TABLE quota_balances
  ADD CONSTRAINT quota_balances_addon_retired_00070 CHECK (granted_addon = 0);
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM orders WHERE kind = 'addon')
     OR EXISTS (SELECT 1 FROM traffic_pack_grants WHERE source <> 'migration') THEN
    RAISE EXCEPTION '00070 Down refused: traffic pack orders or non-migrated grants exist';
  END IF;
END $$;

ALTER TABLE quota_balances DROP CONSTRAINT IF EXISTS quota_balances_addon_retired_00070;

-- 把折算出去的余额原样还回配额行：addon 回到代表行所在订阅的每条流量行，
-- 已用的那部分加回 consumed。
UPDATE quota_balances qb
   SET granted_addon = g.granted_bytes,
       consumed = qb.consumed + CASE WHEN qb.limit_value IS NULL THEN 0 ELSE g.consumed_bytes END,
       updated_at = now()
  FROM traffic_pack_grants g
 WHERE g.source = 'migration' AND g.tenant_id = qb.tenant_id
   AND g.source_id = qb.subscription_id AND qb.metric = 'traffic.bytes';

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
$f$;

DROP TRIGGER IF EXISTS trg_traffic_pack_order_shape_grants ON traffic_pack_grants;
DROP TRIGGER IF EXISTS trg_traffic_pack_order_shape_items ON order_items;
DROP TRIGGER IF EXISTS trg_traffic_pack_order_shape_orders ON orders;
DROP FUNCTION IF EXISTS app.assert_traffic_pack_order_trigger();
DROP FUNCTION IF EXISTS app.assert_traffic_pack_order(uuid, uuid);

ALTER TABLE order_items
  DROP CONSTRAINT IF EXISTS order_items_plan_or_traffic_pack,
  DROP CONSTRAINT IF EXISTS order_items_traffic_pack_fk,
  DROP COLUMN IF EXISTS traffic_pack_id;

DROP TABLE IF EXISTS traffic_pack_grants;
DROP FUNCTION IF EXISTS app.guard_traffic_pack_grant();
DROP TABLE IF EXISTS traffic_packs;
-- +goose StatementEnd
