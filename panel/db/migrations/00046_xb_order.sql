-- +goose Up
-- Xboard 补齐「xb-order」块（条目 #32 #33 #34，见 .claude/runs/xboard-parity.md 第 2 节；共用约定一块只许一个迁移，
-- 所以 catalog、promo、order、subscription 四个模块的改动写在同一个文件里，各表属主不变）：
--   1. catalog：套餐的「流量重置价格」plan_reset_prices（#32）。每个套餐每个币种至多一档；没有行 = 不卖重置。
--      订单行快照了金额，所以价格直接可改，不随版本冻结。
--   2. order：新订单种类 traffic_reset（用户自己买「把当期已用流量清零」）。必须指向一份订阅；订单行是第三种形状
--      （有套餐与版本、没有价格档与周期）；同一订阅同时只许一张未完结的重置单；永远按原价收（没有减免、不挂券）。
--   3. subscription：流量重置日志的原因加 order（用户购买，结算履约时写）。
--   4. promo：单个用户的会员折扣 member_discounts（#33，与 Xboard v2_user.discount 同语义：减免百分比 1–100）。
--   5. order：会员折扣那部分单独记一列 member_discount_amount。discount_amount 仍是订单上的全部减免
--      （券 + 会员，人工赠送是全额），金额恒等式不变；新列不超过 discount_amount，后台人工单恒为 0。
--   6. promo：优惠券限定计费周期 coupon_intervals（#34，Xboard limit_period）。取值是 catalog 价格档的
--      billing_interval 枚举；没有行 = 不限周期。
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：orders、order_items、traffic_reset_logs 换 CHECK 与加列都要
--   ACCESS EXCLUSIVE 并整表校验一遍，挡住下单、结算与流量重置，耗时与三张表的行数成正比；加列带常量缺省值，
--   PG 11 起不重写表。部分唯一索引在事务里普通建。新表都是空表。
--   预检：SELECT count(*) FROM orders; SELECT count(*) FROM order_items; SELECT count(*) FROM traffic_reset_logs;
--   到百万行级时改成 NO TRANSACTION 迁移：ADD CONSTRAINT … NOT VALID 后另起语句 VALIDATE、索引 CONCURRENTLY。
--   还没有生产部署（用户 10-09 确认），按一个事务做。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在 00045，换低峰重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- 1. 流量重置价格：属主 catalog。后台整组替换（删了重插），门户按订阅的币种取一档。
CREATE TABLE plan_reset_prices (
  plan_id     uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  currency    text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  -- 不收 0：0 元的重置等于让用户无限次免费清零；要送就由后台手动重置
  unit_amount numeric(20,2) NOT NULL CHECK (unit_amount > 0),
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (plan_id, currency)
);

-- 2. 订单种类加 traffic_reset
ALTER TABLE orders DROP CONSTRAINT orders_kind_check;
ALTER TABLE orders ADD CONSTRAINT orders_kind_check
  CHECK (kind IN ('new', 'renewal', 'addon', 'topup', 'upgrade', 'traffic_reset'));

-- 续费、换套餐、流量包、流量重置必须指向一份订阅
ALTER TABLE orders DROP CONSTRAINT orders_target_subscription;
ALTER TABLE orders ADD CONSTRAINT orders_target_subscription
  CHECK (kind NOT IN ('renewal', 'upgrade', 'addon', 'traffic_reset') OR subscription_id IS NOT NULL);

-- 同一订阅同时只许一张未完结的重置单：两张都付了就白清两次。与续费、换套餐的那条索引分开，不互相挡
CREATE UNIQUE INDEX orders_one_open_traffic_reset ON orders (subscription_id)
  WHERE kind = 'traffic_reset' AND status IN ('pending_payment', 'paid');

-- 重置单永远按原价收：不吃券、不吃会员折扣（用户 10-14 定），所以没有减免、也不挂券
ALTER TABLE orders ADD CONSTRAINT orders_traffic_reset_no_discount
  CHECK (kind <> 'traffic_reset' OR (discount_amount = 0 AND coupon_id IS NULL));

-- 5. 会员折扣那部分：discount_amount 里属于会员折扣的钱（券的部分 = discount_amount − 它）
ALTER TABLE orders
  ADD COLUMN member_discount_amount numeric(20,2) NOT NULL DEFAULT 0 CHECK (member_discount_amount >= 0),
  ADD CONSTRAINT orders_member_discount_le_discount CHECK (member_discount_amount <= discount_amount),
  -- 后台人工单不打会员折扣（赠送是全额减免，待支付与线下已收款按原价）
  ADD CONSTRAINT orders_member_discount_portal_only CHECK (member_discount_amount = 0 OR created_by IS NULL);

-- 订单行第三种形状：流量重置（指向订阅当时的套餐与版本，没有价格档、周期与流量包）
ALTER TABLE order_items DROP CONSTRAINT order_items_target;
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

-- 3. 流量重置日志原因加 order（用户购买）
ALTER TABLE traffic_reset_logs DROP CONSTRAINT traffic_reset_logs_reason_check;
ALTER TABLE traffic_reset_logs ADD CONSTRAINT traffic_reset_logs_reason_check
  CHECK (reason IN ('renewal', 'cycle_roll', 'manual', 'gift_card', 'plan_change', 'order'));

-- 4. 会员折扣：属主 promo。一人一行，没有行 = 没有折扣；后台设置覆盖、清除删行（写审计）
CREATE TABLE member_discounts (
  user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
  -- 减免百分比：10 = 打九折，100 = 免费（与 Xboard 同语义）
  percent    smallint NOT NULL CHECK (percent BETWEEN 1 AND 100),
  updated_by uuid REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- users 外键的 RESTRICT 检查
CREATE INDEX member_discounts_updated_by_idx ON member_discounts (updated_by) WHERE updated_by IS NOT NULL;

-- 6. 优惠券限定的计费周期：属主 promo。与 coupon_plans 同样建模；券只建不改，随券一起插入
CREATE TABLE coupon_intervals (
  coupon_id        uuid NOT NULL REFERENCES coupons(id) ON DELETE CASCADE,
  billing_interval text NOT NULL
                     CHECK (billing_interval IN ('day', 'week', 'month', 'quarter', 'year', 'one_time')),
  PRIMARY KEY (coupon_id, billing_interval)
);

-- 授权不写在迁移里：安装与升级在 migrate up 之后跑 configure-app-role.sql。

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- 重置单、重置订单行、按购买写的重置日志、记了会员折扣的订单在旧结构里放不下；订单与日志不许删，
-- 有就拒绝回退（要退先人工处理这些行）。测试库只会在跑过本块用例后才有这类行。
-- 券的周期限定与会员折扣也拒绝回退：删掉 coupon_intervals 会让限定了周期的券悄悄对所有周期可用（限制被放宽），
-- 删掉 member_discounts 会无提示地丢掉后台设的折扣、且无法恢复。要退先由管理员清掉这些设置（停用相关的券、清除会员折扣）。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM orders WHERE kind = 'traffic_reset' OR member_discount_amount <> 0)
     OR EXISTS (SELECT 1 FROM order_items WHERE plan_id IS NOT NULL AND price_id IS NULL)
     OR EXISTS (SELECT 1 FROM traffic_reset_logs WHERE reason = 'order') THEN
    RAISE EXCEPTION '库里已有流量重置单、会员折扣订单或按购买写的重置日志，不能退回 00045；订单与日志不可删，先人工处理';
  END IF;
  IF EXISTS (SELECT 1 FROM coupon_intervals) OR EXISTS (SELECT 1 FROM member_discounts) THEN
    RAISE EXCEPTION '库里还有券的周期限定（coupon_intervals）或会员折扣（member_discounts），不能退回 00045：回退会放宽券的周期限制、丢掉会员折扣；先清掉券的周期限定与会员折扣';
  END IF;
END
$$;
-- +goose StatementEnd

DROP TABLE IF EXISTS coupon_intervals;
DROP TABLE IF EXISTS member_discounts;

ALTER TABLE traffic_reset_logs DROP CONSTRAINT traffic_reset_logs_reason_check;
ALTER TABLE traffic_reset_logs ADD CONSTRAINT traffic_reset_logs_reason_check
  CHECK (reason IN ('renewal', 'cycle_roll', 'manual', 'gift_card', 'plan_change'));

ALTER TABLE order_items DROP CONSTRAINT order_items_target;
ALTER TABLE order_items ADD CONSTRAINT order_items_target CHECK (
  (plan_id IS NOT NULL AND traffic_pack_id IS NULL AND price_id IS NOT NULL AND plan_version_id IS NOT NULL
    AND snapshot_plan_version IS NOT NULL AND snapshot_interval IS NOT NULL AND snapshot_interval_count IS NOT NULL
    AND snapshot_traffic_bytes IS NULL)
  OR (traffic_pack_id IS NOT NULL AND plan_id IS NULL AND price_id IS NULL AND plan_version_id IS NULL
    AND snapshot_plan_version IS NULL AND snapshot_interval IS NULL AND snapshot_interval_count IS NULL
    AND snapshot_traffic_bytes IS NOT NULL));

ALTER TABLE orders DROP CONSTRAINT orders_traffic_reset_no_discount;

ALTER TABLE orders
  DROP CONSTRAINT orders_member_discount_portal_only,
  DROP CONSTRAINT orders_member_discount_le_discount,
  DROP COLUMN member_discount_amount;

DROP INDEX IF EXISTS orders_one_open_traffic_reset;

ALTER TABLE orders DROP CONSTRAINT orders_target_subscription;
ALTER TABLE orders ADD CONSTRAINT orders_target_subscription
  CHECK (kind NOT IN ('renewal', 'upgrade', 'addon') OR subscription_id IS NOT NULL);

ALTER TABLE orders DROP CONSTRAINT orders_kind_check;
ALTER TABLE orders ADD CONSTRAINT orders_kind_check
  CHECK (kind IN ('new', 'renewal', 'addon', 'topup', 'upgrade'));

DROP TABLE IF EXISTS plan_reset_prices;
