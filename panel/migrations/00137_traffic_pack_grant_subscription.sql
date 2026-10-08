-- 流量包挂到订阅上（购买模型统一 Q5，用户 2026-10-07 定；w7buya）。
--
-- 00070 的流量包余额挂在用户身上，一个人多份订阅时共用一池。购买模型统一之后一个人可以有多份
-- （给家人另买一份），流量包要加到指定的那一份上：
--   traffic_pack_grants.subscription_id   这笔余额挂在哪一份；为空表示「还没加到任何一份」（未分配：
--                                        无订阅时兑换的送流量卡、00138 回填时没找到可挂的）。
--                                        billing 的 GrantTrafficPackTx 写；节点下发、扣量、门户
--                                        改成按它查（B 路）。
--   traffic_pack_transfers（新表，追加写） 每一次改挂一条：「我买的 100G 去哪了」要能逐笔追到。
--                                        billing.TransferTrafficPacks 与 00138 回填写。
--
-- 改挂只允许三种情形（app.guard_traffic_pack_grant，基于 00070 版，其余逐字不变）：
--   1. 原来为空，挂到同一用户一份生效中或可救回（过期 30 天内）的订阅；
--   2. 原来那份已经彻底停用（cancelled，或 expired 且 renewal_closed_at 非空），改挂到同一用户
--      另一份生效中或可救回的订阅；
--   3. 其余一律拒绝（包括摘回为空）。
-- 判断用 IF NEW.subscription_id IS DISTINCT FROM OLD.subscription_id 包住：扣量的 UPDATE 不改这一列，
-- 不多跑一次查询，节点上报这条热路径不受影响。
--
-- 约束触发器（提交时检查）：余额挂的订阅必须属于同一用户；改挂必须在同一事务里写了转移流水
-- （transfer.created_at = now()，即同一事务时刻）。
--
-- app.assert_traffic_pack_order（基于 00070 版）：addon 单带 subscription_id 时，余额必须挂在同一份上，
-- 或者是从那一份按规则转走的（最早一条转移流水的来源就是那一份）。
--
-- 节点下发纪元（00101）：流量包那条在「有余量 / 用完」翻转之外，再加「改挂到另一份」，转移后
-- 节点立即把那一份重新算进下发名单。
--
-- 锁与耗时：traffic_pack_grants 是大表。加可空列只改元数据；外键 NOT VALID 再 VALIDATE（同一事务里
-- 仍在 ADD COLUMN 的 ACCESS EXCLUSIVE 下，新列全为空，扫描只读）。5k 副本（对照机空闲）整个 Up 约 90ms。
--
-- Down 带数据守卫：只要转移流水里有 actor_kind <> 'migration' 的行（用户或后台真的转过），就拒绝回滚：
-- 那是用户的证据，不能随回滚丢掉。前滚修复：保留本迁移，修代码后再发一版。没有这类行时：两个函数
-- 恢复 00070 原文，纪元触发器恢复 00101 原样，删表、删列（先跑 00138 的 Down 清掉回填流水）。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE public.traffic_pack_grants ADD COLUMN subscription_id uuid;
ALTER TABLE public.traffic_pack_grants
  ADD CONSTRAINT traffic_pack_grants_subscription_fk
  FOREIGN KEY (tenant_id, subscription_id) REFERENCES public.subscriptions (tenant_id, id)
  ON DELETE RESTRICT NOT VALID;
ALTER TABLE public.traffic_pack_grants VALIDATE CONSTRAINT traffic_pack_grants_subscription_fk;
COMMENT ON COLUMN public.traffic_pack_grants.subscription_id IS
  '这笔流量包余额挂在哪一份订阅上；为空表示还没加到任何一份（未分配）';

-- +goose StatementBegin
CREATE TABLE traffic_pack_transfers (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  grant_id             uuid NOT NULL,
  user_id              uuid NOT NULL,
  from_subscription_id uuid,
  to_subscription_id   uuid NOT NULL,
  -- 转移时这笔余额还剩多少（字节）
  remaining_bytes      bigint NOT NULL CHECK (remaining_bytes >= 0),
  actor_kind           text NOT NULL CHECK (actor_kind IN ('user','admin','system','migration')),
  actor_id             uuid,
  created_at           timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT traffic_pack_transfers_moves CHECK (from_subscription_id IS DISTINCT FROM to_subscription_id),
  CONSTRAINT traffic_pack_transfers_grant_fk
    FOREIGN KEY (tenant_id, grant_id) REFERENCES traffic_pack_grants (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT traffic_pack_transfers_user_fk
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT traffic_pack_transfers_from_fk
    FOREIGN KEY (tenant_id, from_subscription_id) REFERENCES subscriptions (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT traffic_pack_transfers_to_fk
    FOREIGN KEY (tenant_id, to_subscription_id) REFERENCES subscriptions (tenant_id, id) ON DELETE RESTRICT
);
-- 「这笔余额的转移经过」按时间读；改挂的提交时检查也走它
CREATE INDEX idx_traffic_pack_transfers_grant
  ON traffic_pack_transfers (tenant_id, grant_id, created_at);
SELECT app.enable_tenant_rls('traffic_pack_transfers');
SELECT app.make_append_only('traffic_pack_transfers');
GRANT SELECT, INSERT ON traffic_pack_transfers TO aegis_app;
REVOKE UPDATE, DELETE, TRUNCATE ON traffic_pack_transfers FROM aegis_app;
COMMENT ON TABLE traffic_pack_transfers IS
  '流量包余额改挂到另一份订阅的流水（追加写）：每改挂一笔一条，来源为空表示原来未分配';
-- +goose StatementEnd

-- +goose StatementBegin
-- 转移流水的归属：余额、目标订阅都属于流水上的用户（外键只保证存在，不保证同一个人）。
CREATE FUNCTION app.guard_traffic_pack_transfer() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM traffic_pack_grants g
                  WHERE g.tenant_id = NEW.tenant_id AND g.id = NEW.grant_id
                    AND g.user_id = NEW.user_id)
     OR NOT EXISTS (SELECT 1 FROM subscriptions s
                     WHERE s.tenant_id = NEW.tenant_id AND s.id = NEW.to_subscription_id
                       AND s.user_id = NEW.user_id) THEN
    RAISE EXCEPTION 'traffic pack transfer % must move the user''s own grant to the user''s own subscription', NEW.id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER traffic_pack_transfers_guard
  BEFORE INSERT ON traffic_pack_transfers
  FOR EACH ROW EXECUTE FUNCTION app.guard_traffic_pack_transfer();
-- +goose StatementEnd

-- +goose StatementBegin
-- 基于 00070 版：只多了 subscription_id 的改挂规则（00137），其余逐字不变。
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
-- 提交时检查（00137）：挂的那一份属于余额的主人；改挂必须同一事务写了转移流水。
CREATE FUNCTION app.assert_traffic_pack_grant_subscription() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF NEW.subscription_id IS NOT NULL AND NOT EXISTS (
       SELECT 1 FROM subscriptions s
        WHERE s.tenant_id = NEW.tenant_id AND s.id = NEW.subscription_id
          AND s.user_id = NEW.user_id) THEN
    RAISE EXCEPTION 'traffic pack grant % is attached to another user''s subscription', NEW.id
      USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND NOT EXISTS (
       SELECT 1 FROM traffic_pack_transfers t
        WHERE t.tenant_id = NEW.tenant_id AND t.grant_id = NEW.id
          AND t.from_subscription_id IS NOT DISTINCT FROM OLD.subscription_id
          AND t.to_subscription_id = NEW.subscription_id
          AND t.created_at = now()) THEN
    RAISE EXCEPTION 'traffic pack grant % moved without a transfer record', NEW.id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER trg_traffic_pack_grant_subscription
  AFTER UPDATE OF subscription_id ON traffic_pack_grants
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN (OLD.subscription_id IS DISTINCT FROM NEW.subscription_id)
  EXECUTE FUNCTION app.assert_traffic_pack_grant_subscription();
CREATE CONSTRAINT TRIGGER trg_traffic_pack_grant_subscription_insert
  AFTER INSERT ON traffic_pack_grants
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN (NEW.subscription_id IS NOT NULL)
  EXECUTE FUNCTION app.assert_traffic_pack_grant_subscription();
-- +goose StatementEnd

-- +goose StatementBegin
-- 基于 00070 版：addon 单带 subscription_id 时余额挂在同一份上（00137），其余逐字不变。
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

-- +goose StatementBegin
-- 流量包纪元（00101）：在「有余量 / 用完」翻转之外，再加「改挂到另一份」（00137）
DROP TRIGGER zz_node_delivery_epoch_traffic_pack_balance ON public.traffic_pack_grants;
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_traffic_pack_balance
  AFTER UPDATE ON traffic_pack_grants
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN (((OLD.consumed_bytes < OLD.granted_bytes)
         IS DISTINCT FROM (NEW.consumed_bytes < NEW.granted_bytes))
        OR OLD.subscription_id IS DISTINCT FROM NEW.subscription_id)
  EXECUTE FUNCTION app.bump_node_delivery_epoch();
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 用户或后台真的转移过的流水是证据，不能随回滚丢掉（回填写的 migration 流水由 00138 的 Down 清掉）
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM public.traffic_pack_transfers WHERE actor_kind <> 'migration') THEN
    RAISE EXCEPTION '00137 down refused: traffic pack transfers made by users or admins exist and are append-only evidence';
  END IF;
END
$$;
-- +goose StatementEnd

-- 纪元触发器恢复 00101 原样
-- +goose StatementBegin
DROP TRIGGER zz_node_delivery_epoch_traffic_pack_balance ON public.traffic_pack_grants;
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_traffic_pack_balance
  AFTER UPDATE ON traffic_pack_grants
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN ((OLD.consumed_bytes < OLD.granted_bytes)
        IS DISTINCT FROM (NEW.consumed_bytes < NEW.granted_bytes))
  EXECUTE FUNCTION app.bump_node_delivery_epoch();
-- +goose StatementEnd

-- 两个函数恢复 00070 原文
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
-- +goose StatementEnd

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
  NEW.updated_at := now();
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER trg_traffic_pack_grant_subscription_insert ON public.traffic_pack_grants;
DROP TRIGGER trg_traffic_pack_grant_subscription ON public.traffic_pack_grants;
DROP FUNCTION app.assert_traffic_pack_grant_subscription();
DROP TABLE public.traffic_pack_transfers;
DROP FUNCTION app.guard_traffic_pack_transfer();
ALTER TABLE public.traffic_pack_grants DROP COLUMN subscription_id;
