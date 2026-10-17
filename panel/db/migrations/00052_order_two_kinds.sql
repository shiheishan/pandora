-- +goose Up
-- order：套餐只分订阅与一次性两类（用户 2026-10-16 定，见 .claude/rules/redesign-2026-10-16.md「套餐」）。属 order 模块。
--   1. 去掉流量包：订单种类删 addon，订单行删 traffic_pack_id 与只为流量包存在的 snapshot_traffic_bytes，
--      订单行形状只剩「买套餐（有价格档）」与「流量重置（没有价格档）」两种。
--   2. 新增订单种类 stack：已有一次性套餐的用户再买同一个，选「叠加到这一份」（总流量加上、天数顺延）。
--      必须指向那一份订阅；与续费、换套餐共用「同一份订阅同时只有一张未完结单」的唯一索引。
--      「换掉」仍是 upgrade（带抵扣），「新开」仍是 new。
--   3. 删会员折扣那一列 member_discount_amount（会员折扣整体删除）。discount_amount 仍是订单上的全部减免，
--      已有订单里会员折扣占多少的拆分随列丢失，总额不变。
--   4. 删库存与每人限购：plan_purchase_counters、order_stock_reservations、order_purchase_limit_reservations。
--      订单预留 order_reservations 本身还管券占用与余额冻结，保留。
-- 还没有生产部署（用户 10-09 确认），不留兼容、不分两步删列。本迁移基于 00051（已去掉币种）之后的结构。
--
-- 锁与耗时（一个事务，锁持到提交）：orders、order_items 换 CHECK 要整表校验一遍；删列只改目录；
--   三张预留明细与计数表直接 DROP。要 orders、order_items、order_reservations 的 ACCESS EXCLUSIVE，挡住下单与结算。
--   部署顺序：先停 serve / worker，再 migrate，再起新进程（旧进程会写 addon 单和预留明细）。
-- 预检（下面的 DO 块会再核一遍）：
--   SELECT count(*) FROM orders WHERE kind = 'addon';   -- 应为 0：订单不许删，流量包单在新结构里放不下
--   SELECT count(*) FROM order_items WHERE traffic_pack_id IS NOT NULL;   -- 应为 0
--   有就拒绝迁移：还没有生产库，清空那个开发库重装。
-- 失败回滚：任一步失败（含 lock_timeout 超时、预检不过）整笔回滚，库停在上一版，排查后重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM orders WHERE kind = 'addon')
     OR EXISTS (SELECT 1 FROM order_items WHERE traffic_pack_id IS NOT NULL) THEN
    RAISE EXCEPTION '库里有流量包订单，新结构放不下（订单不许删）；还没有生产库，清空这个库再迁移';
  END IF;
END
$$;
-- +goose StatementEnd

-- 1. 订单种类：去 addon，加 stack
ALTER TABLE orders DROP CONSTRAINT orders_kind_check;
ALTER TABLE orders ADD CONSTRAINT orders_kind_check
  CHECK (kind IN ('new', 'renewal', 'topup', 'upgrade', 'traffic_reset', 'stack'));

-- 续费、换套餐、流量重置、叠加必须指向一份订阅
ALTER TABLE orders DROP CONSTRAINT orders_target_subscription;
ALTER TABLE orders ADD CONSTRAINT orders_target_subscription
  CHECK (kind NOT IN ('renewal', 'upgrade', 'traffic_reset', 'stack') OR subscription_id IS NOT NULL);

-- 同一份订阅同时只能有一张未完结的续费、换套餐或叠加单：各按下单时的订阅状态算钱，先履约的会让另一张作废
DROP INDEX orders_one_open_subscription_change;
CREATE UNIQUE INDEX orders_one_open_subscription_change ON orders (subscription_id)
  WHERE kind IN ('renewal', 'upgrade', 'stack') AND status IN ('pending_payment', 'paid');

-- 2. 订单行：去掉流量包形状
ALTER TABLE order_items DROP CONSTRAINT order_items_target;
DROP INDEX order_items_pack_idx;
ALTER TABLE order_items DROP COLUMN traffic_pack_id, DROP COLUMN snapshot_traffic_bytes;
-- 买套餐（新购、续费、换套餐、叠加）指向价格档与周期；流量重置指向订阅当时的套餐与版本，没有价格档与周期。
-- 订单行只插不改（语句级 deny_mutation），换 CHECK 只校验存量。
ALTER TABLE order_items ADD CONSTRAINT order_items_target CHECK (
  plan_id IS NOT NULL AND plan_version_id IS NOT NULL AND snapshot_plan_version IS NOT NULL
  AND ((price_id IS NOT NULL AND snapshot_interval IS NOT NULL AND snapshot_interval_count IS NOT NULL)
    OR (price_id IS NULL AND snapshot_interval IS NULL AND snapshot_interval_count IS NULL))
);
ALTER TABLE order_items ALTER COLUMN plan_id SET NOT NULL;

-- 3. 会员折扣
ALTER TABLE orders
  DROP CONSTRAINT orders_member_discount_portal_only,
  DROP CONSTRAINT orders_member_discount_le_discount,
  DROP COLUMN member_discount_amount;

-- 4. 库存与每人限购（明细表先删，计数表后删）
DROP TABLE order_purchase_limit_reservations;
DROP TABLE order_stock_reservations;
DROP TABLE plan_purchase_counters;

-- +goose Down
-- 逐项退回上一版结构：三张表按 00013 原文重建（空表：库存、限购计数无从恢复，退回后从零算起）；
-- member_discount_amount 加回，已有订单记 0；traffic_pack_id、snapshot_traffic_bytes 加回为空；
-- 订单种类与行形状的 CHECK 按 00046 的原文重建。
-- 放不下的只有叠加单（stack）：订单不许删，有就拒绝回退。traffic_pack_id 的外键要 traffic_packs 表，
-- 它由 00056 的 Down 先重建（Down 按号倒序执行）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM orders WHERE kind = 'stack') THEN
    RAISE EXCEPTION '库里有叠加单（kind = stack），上一版结构放不下（订单不许删），不能回退';
  END IF;
END
$$;
-- +goose StatementEnd

CREATE TABLE plan_purchase_counters (
  plan_id    uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  purchased  int NOT NULL DEFAULT 0 CHECK (purchased >= 0),
  reserved   int NOT NULL DEFAULT 0 CHECK (reserved >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (plan_id, user_id)
);
CREATE INDEX plan_purchase_counters_user_idx ON plan_purchase_counters (user_id);

CREATE TABLE order_stock_reservations (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  reservation_id uuid NOT NULL,
  order_id       uuid NOT NULL,
  plan_id        uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  quantity       int NOT NULL CHECK (quantity > 0),
  created_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_stock_reservations_order_plan_key UNIQUE (order_id, plan_id),
  CONSTRAINT order_stock_reservations_parent_fk FOREIGN KEY (reservation_id, order_id)
    REFERENCES order_reservations (id, order_id) ON DELETE RESTRICT
);
CREATE INDEX order_stock_reservations_plan_idx ON order_stock_reservations (plan_id);
CREATE INDEX order_stock_reservations_reservation_idx ON order_stock_reservations (reservation_id);
CREATE TRIGGER order_stock_reservations_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON order_stock_reservations
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

CREATE TABLE order_purchase_limit_reservations (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  reservation_id uuid NOT NULL,
  order_id       uuid NOT NULL,
  plan_id        uuid NOT NULL,
  user_id        uuid NOT NULL,
  quantity       int NOT NULL CHECK (quantity > 0),
  created_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_purchase_limit_reservations_key UNIQUE (order_id, plan_id, user_id),
  CONSTRAINT order_purchase_limit_reservations_parent_fk FOREIGN KEY (reservation_id, order_id)
    REFERENCES order_reservations (id, order_id) ON DELETE RESTRICT,
  CONSTRAINT order_purchase_limit_reservations_counter_fk FOREIGN KEY (plan_id, user_id)
    REFERENCES plan_purchase_counters (plan_id, user_id) ON DELETE RESTRICT
);
CREATE INDEX order_purchase_limit_reservations_counter_idx ON order_purchase_limit_reservations (plan_id, user_id);
CREATE INDEX order_purchase_limit_reservations_reservation_idx ON order_purchase_limit_reservations (reservation_id);
CREATE TRIGGER order_purchase_limit_reservations_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON order_purchase_limit_reservations
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

ALTER TABLE orders
  ADD COLUMN member_discount_amount numeric(20,2) NOT NULL DEFAULT 0 CHECK (member_discount_amount >= 0),
  ADD CONSTRAINT orders_member_discount_le_discount CHECK (member_discount_amount <= discount_amount),
  ADD CONSTRAINT orders_member_discount_portal_only CHECK (member_discount_amount = 0 OR created_by IS NULL);

ALTER TABLE order_items DROP CONSTRAINT order_items_target;
ALTER TABLE order_items ALTER COLUMN plan_id DROP NOT NULL;
ALTER TABLE order_items
  ADD COLUMN traffic_pack_id uuid REFERENCES traffic_packs(id) ON DELETE RESTRICT,
  ADD COLUMN snapshot_traffic_bytes bigint CHECK (snapshot_traffic_bytes IS NULL OR snapshot_traffic_bytes > 0);
CREATE INDEX order_items_pack_idx ON order_items (traffic_pack_id) WHERE traffic_pack_id IS NOT NULL;
ALTER TABLE order_items ADD CONSTRAINT order_items_target CHECK (
  (plan_id IS NOT NULL AND traffic_pack_id IS NULL AND price_id IS NOT NULL AND plan_version_id IS NOT NULL
    AND snapshot_plan_version IS NOT NULL AND snapshot_interval IS NOT NULL AND snapshot_interval_count IS NOT NULL
    AND snapshot_traffic_bytes IS NULL)
  OR (traffic_pack_id IS NOT NULL AND plan_id IS NULL AND price_id IS NULL AND plan_version_id IS NULL
    AND snapshot_plan_version IS NULL AND snapshot_interval IS NULL AND snapshot_interval_count IS NULL
    AND snapshot_traffic_bytes IS NOT NULL)
  OR (plan_id IS NOT NULL AND traffic_pack_id IS NULL AND price_id IS NULL AND plan_version_id IS NOT NULL
    AND snapshot_plan_version IS NOT NULL AND snapshot_interval IS NULL AND snapshot_interval_count IS NULL
    AND snapshot_traffic_bytes IS NULL)
);

DROP INDEX orders_one_open_subscription_change;
CREATE UNIQUE INDEX orders_one_open_subscription_change ON orders (subscription_id)
  WHERE kind IN ('renewal', 'upgrade') AND status IN ('pending_payment', 'paid');

ALTER TABLE orders DROP CONSTRAINT orders_target_subscription;
ALTER TABLE orders ADD CONSTRAINT orders_target_subscription
  CHECK (kind NOT IN ('renewal', 'upgrade', 'addon', 'traffic_reset') OR subscription_id IS NOT NULL);

ALTER TABLE orders DROP CONSTRAINT orders_kind_check;
ALTER TABLE orders ADD CONSTRAINT orders_kind_check
  CHECK (kind IN ('new', 'renewal', 'addon', 'topup', 'upgrade', 'traffic_reset'));
