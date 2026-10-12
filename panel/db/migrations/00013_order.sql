-- +goose Up
-- order 模块：订单、订单行、订单预留（库存、限购、券、余额冻结的总开关）、预留流水、库存与限购的预留明细、
-- 每人每套餐的限购计数；并给早于本迁移建好的「指向订单的列」补上外键。
-- 依赖 00005 users、00008 plans / plan_versions / prices / traffic_packs、00010 coupons、00011 subscriptions。
--
-- 与旧表相比：去掉租户列与 RLS；不建订单状态流水表（状态机在 Go：转换表 + CanTransition，更新带
-- WHERE status = 旧状态）；不建退款相关的表与列（Q2）；金额改为 numeric(20,2)，多余小数由写入前的
-- money.Check 拦（库里 CHECK 写不出来）；不再有幂等键列（幂等记录在 idempotency 模块按资源绑定订单）。
-- 状态只写 pending_payment、paid、fulfilled、cancelled、expired 五种；paid 只在结算事务内短暂出现。
-- 换套餐抵扣后免掉的零头单独一列（waived_amount），不并进券折扣。

-- 订单。金额恒等式与状态形状全部在库里兜底（docs/architecture.md 第 5.3 节）。
CREATE TABLE orders (
  id                      uuid PRIMARY KEY DEFAULT uuidv7(),
  order_no                text NOT NULL CHECK (order_no ~ '^[A-Z0-9-]{1,64}$'),
  user_id                 uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  -- new 新购、renewal 续费、addon 流量包、topup 充值、upgrade 换套餐
  kind                    text NOT NULL CHECK (kind IN ('new', 'renewal', 'addon', 'topup', 'upgrade')),
  status                  text NOT NULL DEFAULT 'pending_payment'
                            CHECK (status IN ('pending_payment', 'paid', 'fulfilled', 'cancelled', 'expired')),
  currency                text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  subtotal_amount         numeric(20,2) NOT NULL CHECK (subtotal_amount >= 0),
  -- 券折扣，或人工赠送的全额减免
  discount_amount         numeric(20,2) NOT NULL DEFAULT 0 CHECK (discount_amount >= 0),
  -- 换套餐时原套餐剩余价值抵掉的部分（抵不完的部分不进 total，由换套餐履约时退回余额）
  proration_credit_amount numeric(20,2) NOT NULL DEFAULT 0 CHECK (proration_credit_amount >= 0),
  -- 换套餐抵扣后免掉的零头，一次最多 0.99
  waived_amount           numeric(20,2) NOT NULL DEFAULT 0 CHECK (waived_amount >= 0 AND waived_amount <= 0.99),
  total_amount            numeric(20,2) NOT NULL CHECK (total_amount >= 0),
  balance_applied         numeric(20,2) NOT NULL DEFAULT 0 CHECK (balance_applied >= 0),
  -- 需要渠道实付的部分
  payable_amount          numeric(20,2) NOT NULL CHECK (payable_amount >= 0),
  -- 已付总额（含余额部分）；paid / fulfilled 时等于 total，其余为 0
  paid_amount             numeric(20,2) NOT NULL DEFAULT 0 CHECK (paid_amount >= 0),
  -- 币种与券一致由复合外键保证（见表尾）
  coupon_id               uuid,
  -- 管理员代开的单（人工单、后台续费单）：开单人与原因
  created_by              uuid REFERENCES users(id) ON DELETE RESTRICT,
  manual_reason           text CHECK (manual_reason IS NULL OR char_length(manual_reason) <= 500),
  -- 续费、换套餐、流量包：目标订阅；新购：履约时开出的那一份；充值：空。同一用户由复合外键保证
  subscription_id         uuid,
  -- 新购时用户给新那一份起的备注名（与订阅 label 同一口径）
  subscription_label      text CHECK (subscription_label IS NULL OR (char_length(subscription_label) BETWEEN 1 AND 16
                                      AND subscription_label !~ '^[[:space:]]' AND subscription_label !~ '[[:space:]]$'
                                      AND subscription_label !~ '[[:cntrl:]]')),
  -- 乐观锁：后台取消带 expected_state_version；每次状态变化 +1
  state_version           bigint NOT NULL DEFAULT 1 CHECK (state_version > 0),
  -- 付款期限（建单 30 分钟后）
  expires_at              timestamptz NOT NULL,
  paid_at                 timestamptz,
  fulfilled_at            timestamptz,
  cancelled_at            timestamptz,
  expired_at              timestamptz,
  cancel_reason           text CHECK (cancel_reason IS NULL OR char_length(cancel_reason) BETWEEN 1 AND 500),
  created_at              timestamptz NOT NULL DEFAULT now(),
  updated_at              timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT orders_order_no_key UNIQUE (order_no),
  -- 给订单行、预留的复合外键用
  CONSTRAINT orders_id_currency_key UNIQUE (id, currency),
  CONSTRAINT orders_coupon_fk FOREIGN KEY (coupon_id, currency)
    REFERENCES coupons (id, currency) ON DELETE RESTRICT,
  CONSTRAINT orders_subscription_fk FOREIGN KEY (subscription_id, user_id)
    REFERENCES subscriptions (id, user_id) ON DELETE RESTRICT,
  -- 金额恒等式
  CONSTRAINT orders_payable_identity CHECK (payable_amount = total_amount - balance_applied),
  CONSTRAINT orders_total_identity
    CHECK (total_amount = greatest(subtotal_amount - discount_amount - proration_credit_amount, 0) - waived_amount),
  CONSTRAINT orders_discount_le_subtotal CHECK (discount_amount <= subtotal_amount),
  CONSTRAINT orders_proration_only_upgrade CHECK (proration_credit_amount = 0 OR kind = 'upgrade'),
  CONSTRAINT orders_waived_only_upgrade CHECK (waived_amount = 0 OR kind = 'upgrade'),
  -- 充值单只有它自己：没有折扣、余额抵扣、券与订阅
  CONSTRAINT orders_topup_shape CHECK (kind <> 'topup' OR (discount_amount = 0 AND balance_applied = 0
                                       AND coupon_id IS NULL AND subscription_id IS NULL AND created_by IS NULL)),
  -- 续费、换套餐、流量包必须指向一份订阅
  CONSTRAINT orders_target_subscription CHECK (kind NOT IN ('renewal', 'upgrade', 'addon') OR subscription_id IS NOT NULL),
  -- 新购：备注名只属于新购；履约后必须记下开出的那一份
  CONSTRAINT orders_label_only_new CHECK (subscription_label IS NULL OR kind = 'new'),
  CONSTRAINT orders_new_fulfilled_subscription
    CHECK (kind <> 'new' OR status <> 'fulfilled' OR subscription_id IS NOT NULL),
  -- 管理员代开的单必须写明理由（≥5 字），有理由就必须有开单人
  CONSTRAINT orders_manual_reason
    CHECK ((created_by IS NULL AND manual_reason IS NULL)
        OR (created_by IS NOT NULL AND manual_reason IS NOT NULL AND char_length(btrim(manual_reason)) >= 5)),
  -- 状态形状
  CONSTRAINT orders_paid_shape CHECK (
    (status IN ('paid', 'fulfilled') AND paid_amount = total_amount AND paid_at IS NOT NULL)
    OR (status NOT IN ('paid', 'fulfilled') AND paid_amount = 0 AND paid_at IS NULL)),
  CONSTRAINT orders_fulfilled_shape CHECK ((status = 'fulfilled') = (fulfilled_at IS NOT NULL)),
  CONSTRAINT orders_cancelled_shape CHECK (
    (status = 'cancelled') = (cancelled_at IS NOT NULL)
    AND (status <> 'cancelled' OR cancel_reason IS NOT NULL)),
  CONSTRAINT orders_expired_shape CHECK ((status = 'expired') = (expired_at IS NOT NULL)),
  CONSTRAINT orders_cancel_reason_closed CHECK (cancel_reason IS NULL OR status IN ('cancelled', 'expired'))
);

-- 我的订单
CREATE INDEX orders_user_idx ON orders (user_id, created_at DESC, id DESC);
-- 后台订单列表（全部、按状态）
CREATE INDEX orders_created_idx ON orders (created_at DESC, id DESC);
CREATE INDEX orders_status_idx ON orders (status, created_at DESC, id DESC);
CREATE INDEX orders_coupon_idx ON orders (coupon_id) WHERE coupon_id IS NOT NULL;
CREATE INDEX orders_subscription_idx ON orders (subscription_id) WHERE subscription_id IS NOT NULL;
CREATE INDEX orders_created_by_idx ON orders (created_by) WHERE created_by IS NOT NULL;
-- 同一条订阅同时只能有一张未完结的续费或换套餐单：两张单各按下单时的订阅状态算钱，先履约的会让另一张作废
CREATE UNIQUE INDEX orders_one_open_subscription_change ON orders (subscription_id)
  WHERE kind IN ('renewal', 'upgrade') AND status IN ('pending_payment', 'paid');

-- 订单是收入与履约的凭据：只改状态，不许删
CREATE TRIGGER orders_no_delete
  BEFORE DELETE OR TRUNCATE ON orders
  FOR EACH STATEMENT EXECUTE FUNCTION deny_delete();

-- 订单行：下单瞬间的快照（名称、版本、周期、权益、配额、金额），之后目录怎么改都不变。只插不改。
-- 套餐类订单指向价格与套餐版本，流量包订单指向流量包，恰好一种；充值单没有订单行。
CREATE TABLE order_items (
  id                      uuid PRIMARY KEY DEFAULT uuidv7(),
  order_id                uuid NOT NULL,
  price_id                uuid REFERENCES prices(id) ON DELETE RESTRICT,
  plan_id                 uuid REFERENCES plans(id) ON DELETE RESTRICT,
  plan_version_id         uuid,
  traffic_pack_id         uuid REFERENCES traffic_packs(id) ON DELETE RESTRICT,
  -- 显示名：套餐名或流量包名
  snapshot_name           text NOT NULL CHECK (char_length(snapshot_name) BETWEEN 1 AND 200),
  snapshot_plan_version   int CHECK (snapshot_plan_version IS NULL OR snapshot_plan_version > 0),
  snapshot_interval       text CHECK (snapshot_interval IS NULL
                                      OR snapshot_interval IN ('day', 'week', 'month', 'quarter', 'year', 'one_time')),
  snapshot_interval_count smallint CHECK (snapshot_interval_count IS NULL OR snapshot_interval_count > 0),
  -- 流量包容量（字节）
  snapshot_traffic_bytes  bigint CHECK (snapshot_traffic_bytes IS NULL OR snapshot_traffic_bytes > 0),
  snapshot_entitlements   jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(snapshot_entitlements) = 'array'),
  snapshot_quotas         jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(snapshot_quotas) = 'array'),
  quantity                int NOT NULL DEFAULT 1 CHECK (quantity > 0),
  unit_amount             numeric(20,2) NOT NULL CHECK (unit_amount >= 0),
  line_amount             numeric(20,2) NOT NULL CHECK (line_amount >= 0),
  currency                text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  created_at              timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_items_order_fk FOREIGN KEY (order_id, currency)
    REFERENCES orders (id, currency) ON DELETE RESTRICT,
  CONSTRAINT order_items_version_fk FOREIGN KEY (plan_id, plan_version_id)
    REFERENCES plan_versions (plan_id, id) ON DELETE RESTRICT,
  CONSTRAINT order_items_line_identity CHECK (line_amount = unit_amount * quantity),
  CONSTRAINT order_items_target CHECK (
    (plan_id IS NOT NULL AND traffic_pack_id IS NULL AND price_id IS NOT NULL AND plan_version_id IS NOT NULL
      AND snapshot_plan_version IS NOT NULL AND snapshot_interval IS NOT NULL AND snapshot_interval_count IS NOT NULL
      AND snapshot_traffic_bytes IS NULL)
    OR (traffic_pack_id IS NOT NULL AND plan_id IS NULL AND price_id IS NULL AND plan_version_id IS NULL
      AND snapshot_plan_version IS NULL AND snapshot_interval IS NULL AND snapshot_interval_count IS NULL
      AND snapshot_traffic_bytes IS NOT NULL))
);
CREATE INDEX order_items_order_idx ON order_items (order_id);
CREATE INDEX order_items_plan_idx ON order_items (plan_id) WHERE plan_id IS NOT NULL;
CREATE INDEX order_items_price_idx ON order_items (price_id) WHERE price_id IS NOT NULL;
CREATE INDEX order_items_pack_idx ON order_items (traffic_pack_id) WHERE traffic_pack_id IS NOT NULL;

CREATE TRIGGER order_items_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON order_items
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- 订单预留：每单一行，是库存、限购、券占用、余额冻结的总开关。held → captured（结算）或 released（取消、超时），只走一次。
CREATE TABLE order_reservations (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  order_id       uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  user_id        uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  state          text NOT NULL DEFAULT 'held' CHECK (state IN ('held', 'captured', 'released')),
  expires_at     timestamptz NOT NULL,
  held_at        timestamptz NOT NULL DEFAULT now(),
  captured_at    timestamptz,
  released_at    timestamptz,
  release_reason text CHECK (release_reason IS NULL OR char_length(release_reason) BETWEEN 1 AND 500),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_reservations_order_key UNIQUE (order_id),
  -- 给流水与明细的复合外键用
  CONSTRAINT order_reservations_id_order_key UNIQUE (id, order_id),
  CONSTRAINT order_reservations_state_times CHECK (
    (state = 'held' AND captured_at IS NULL AND released_at IS NULL AND release_reason IS NULL)
    OR (state = 'captured' AND captured_at IS NOT NULL AND released_at IS NULL AND release_reason IS NULL)
    OR (state = 'released' AND released_at IS NOT NULL AND captured_at IS NULL AND release_reason IS NOT NULL)
  )
);
-- 超时释放扫描
CREATE INDEX order_reservations_expiry_idx ON order_reservations (expires_at, id) WHERE state = 'held';
CREATE INDEX order_reservations_user_idx ON order_reservations (user_id);

CREATE TRIGGER order_reservations_no_delete
  BEFORE DELETE OR TRUNCATE ON order_reservations
  FOR EACH STATEMENT EXECUTE FUNCTION deny_delete();

-- 预留流水：每次状态变化一行，只追加。每个预留恰好一条初始事件，至多一条终态事件。
CREATE TABLE order_reservation_events (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  reservation_id uuid NOT NULL,
  order_id       uuid NOT NULL,
  from_state     text CHECK (from_state IS NULL OR from_state IN ('held', 'captured', 'released')),
  to_state       text NOT NULL CHECK (to_state IN ('held', 'captured', 'released')),
  event_kind     text NOT NULL CHECK (event_kind IN ('reserve', 'capture', 'release', 'expire', 'cancel')),
  actor_kind     text NOT NULL CHECK (actor_kind IN ('user', 'admin', 'system', 'payment')),
  actor_id       uuid REFERENCES users(id) ON DELETE RESTRICT,
  reason         text CHECK (reason IS NULL OR char_length(reason) BETWEEN 1 AND 500),
  occurred_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_reservation_events_parent_fk FOREIGN KEY (reservation_id, order_id)
    REFERENCES order_reservations (id, order_id) ON DELETE RESTRICT,
  CONSTRAINT order_reservation_events_actor_shape CHECK ((actor_kind IN ('user', 'admin')) = (actor_id IS NOT NULL)),
  CONSTRAINT order_reservation_events_shape CHECK (
    (event_kind = 'reserve' AND from_state IS NULL AND to_state = 'held')
    OR (event_kind = 'capture' AND from_state = 'held' AND to_state = 'captured')
    OR (event_kind IN ('release', 'expire', 'cancel') AND from_state = 'held' AND to_state = 'released'))
);
CREATE UNIQUE INDEX order_reservation_events_initial_key ON order_reservation_events (reservation_id)
  WHERE to_state = 'held';
CREATE UNIQUE INDEX order_reservation_events_terminal_key ON order_reservation_events (reservation_id)
  WHERE to_state IN ('captured', 'released');
CREATE INDEX order_reservation_events_order_idx ON order_reservation_events (order_id, occurred_at);
CREATE INDEX order_reservation_events_actor_idx ON order_reservation_events (actor_id) WHERE actor_id IS NOT NULL;

CREATE TRIGGER order_reservation_events_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON order_reservation_events
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- 每人每套餐的限购计数：purchased 已成交、reserved 在途。行在第一次下单时插入。
CREATE TABLE plan_purchase_counters (
  plan_id    uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  purchased  int NOT NULL DEFAULT 0 CHECK (purchased >= 0),
  reserved   int NOT NULL DEFAULT 0 CHECK (reserved >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (plan_id, user_id)
);
CREATE INDEX plan_purchase_counters_user_idx ON plan_purchase_counters (user_id);

-- 新购占用的库存：只追加，状态跟着父预留走（结算时转为已售，释放时退回）。不限量的套餐没有这一行。
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

-- 新购占用的限购次数：只追加，状态跟着父预留走。不限购的套餐没有这一行。
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

-- 补上早于本迁移的「指向订单」的外键（00009、00010、00011、00012 里注明「外键由 00013 补」的列）。
-- 新基线是空库，加约束不涉及存量校验；下单事务里一律先插订单行，再写这些表，所以用普通外键即可。
ALTER TABLE balance_holds
  ADD CONSTRAINT balance_holds_order_fk FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT;
ALTER TABLE coupon_redemptions
  ADD CONSTRAINT coupon_redemptions_order_fk FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT;
ALTER TABLE subscriptions
  ADD CONSTRAINT subscriptions_source_order_fk FOREIGN KEY (source_order_id) REFERENCES orders(id) ON DELETE RESTRICT;
ALTER TABLE subscription_events
  ADD CONSTRAINT subscription_events_order_fk FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT;
ALTER TABLE payment_intents
  ADD CONSTRAINT payment_intents_order_fk FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT;
ALTER TABLE payments
  ADD CONSTRAINT payments_order_fk FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT;
ALTER TABLE late_payment_cases
  ADD CONSTRAINT late_payment_cases_order_fk FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE late_payment_cases DROP CONSTRAINT IF EXISTS late_payment_cases_order_fk;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_order_fk;
ALTER TABLE payment_intents DROP CONSTRAINT IF EXISTS payment_intents_order_fk;
ALTER TABLE subscription_events DROP CONSTRAINT IF EXISTS subscription_events_order_fk;
ALTER TABLE subscriptions DROP CONSTRAINT IF EXISTS subscriptions_source_order_fk;
ALTER TABLE coupon_redemptions DROP CONSTRAINT IF EXISTS coupon_redemptions_order_fk;
ALTER TABLE balance_holds DROP CONSTRAINT IF EXISTS balance_holds_order_fk;
DROP TABLE IF EXISTS order_purchase_limit_reservations;
DROP TABLE IF EXISTS order_stock_reservations;
DROP TABLE IF EXISTS plan_purchase_counters;
DROP TABLE IF EXISTS order_reservation_events;
DROP TABLE IF EXISTS order_reservations;
DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;
