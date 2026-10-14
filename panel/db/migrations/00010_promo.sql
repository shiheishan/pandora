-- +goose Up
-- promo 模块：优惠券与券的占用记录。
-- 依赖 00005_identity.sql 的 users、user_groups 与 00008_catalog.sql 的 plans。限定的套餐放在关联表 coupon_plans，
-- plan_id 写入前由 promo service 经 catalog service 校验，库里再用外键兜底。
-- order_id 指向订单，外键由 00013 补。
--
-- 金额一律 NUMERIC(20,2)、单位元（docs/architecture.md 第 6 节）；面值拆成定额 amount_off 与
-- 万分比 percent_off_bp 两列，恰好一个非空（R10）。
-- 金额列不加 round(x, 2) 的 CHECK：NUMERIC(20,2) 先把值舍入到两位再查约束，那条永远成立；
-- 多余小数只靠写入前的 money.Check 拦（R2）。

CREATE TABLE coupons (
  id                       uuid PRIMARY KEY DEFAULT uuidv7(),
  -- 码统一存大写、只用 ASCII 字母数字与 _ -（与 service 的 codePattern 同一字符集）：
  -- 用户从聊天记录里复制来的大小写差异不该让券失效，也避免 Go 与 PG 的大写规则不一致
  code                     text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{1,64}$'),
  name                     text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
  amount_off               numeric(20,2) CHECK (amount_off > 0),
  percent_off_bp           int CHECK (percent_off_bp BETWEEN 1 AND 10000),
  -- 券只对这个币种的订单有效；定额、封顶、门槛都按它计
  currency                 text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  -- 百分比券的折扣封顶；定额券没有
  max_discount             numeric(20,2) CHECK (max_discount > 0),
  min_order_amount         numeric(20,2) NOT NULL DEFAULT 0 CHECK (min_order_amount >= 0),
  -- 总容量（NULL 为不限）与每人限次
  max_redemptions          int CHECK (max_redemptions > 0),
  max_redemptions_per_user int NOT NULL DEFAULT 1 CHECK (max_redemptions_per_user > 0),
  -- 已确认（订单已支付）与占用中（订单待支付）的次数，与 coupon_redemptions 同事务维护
  redeemed_count           int NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
  reserved_count           int NOT NULL DEFAULT 0 CHECK (reserved_count >= 0),
  -- 限定用户组（NULL 为不限）；组被券引用时不许删组
  user_group_id            uuid REFERENCES user_groups(id) ON DELETE RESTRICT,
  valid_from               timestamptz,
  valid_until              timestamptz,
  -- 过期由 valid_until 判断，用完由计数判断，库里只存后台能设的两种
  status                   text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
  created_by               uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at               timestamptz NOT NULL DEFAULT now(),
  updated_at               timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT coupons_code_key UNIQUE (code),
  -- 给 coupon_redemptions 的 (coupon_id, currency) 复合外键用：占用的币种必须是券的币种
  CONSTRAINT coupons_id_currency_key UNIQUE (id, currency),
  CONSTRAINT coupons_face_exactly_one CHECK ((amount_off IS NULL) <> (percent_off_bp IS NULL)),
  CONSTRAINT coupons_max_discount_percent_only CHECK (max_discount IS NULL OR percent_off_bp IS NOT NULL),
  -- 并发占用不超发的底线：占用用带条件的 UPDATE 加一，这里再兜一次
  CONSTRAINT coupons_capacity
    CHECK (max_redemptions IS NULL OR redeemed_count + reserved_count <= max_redemptions),
  CONSTRAINT coupons_valid_window
    CHECK (valid_from IS NULL OR valid_until IS NULL OR valid_until > valid_from)
);

-- 后台列表按 (created_at DESC, code) 翻页
CREATE INDEX coupons_created_idx ON coupons (created_at DESC, code);
CREATE INDEX coupons_user_group_idx ON coupons (user_group_id) WHERE user_group_id IS NOT NULL;

-- 券限定的套餐：没有行即适用全部套餐；流量包订单没有套餐，限定了套餐的券对它不适用。
-- 套餐只归档不删，外键用 RESTRICT。
CREATE TABLE coupon_plans (
  coupon_id uuid NOT NULL REFERENCES coupons(id) ON DELETE CASCADE,
  plan_id   uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  PRIMARY KEY (coupon_id, plan_id)
);
CREATE INDEX coupon_plans_plan_idx ON coupon_plans (plan_id);

-- 券在一张订单上的占用：下单时 held，订单取消或超时 released，支付成功 captured。
-- 只能从 held 走到另外两种终态（service 的转换表 + WHERE status = 'held'）；行不许删，是折扣的凭据。
CREATE TABLE coupon_redemptions (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  coupon_id       uuid NOT NULL,
  user_id         uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  -- 外键由 00013 补
  order_id        uuid NOT NULL,
  -- 订单号快照，后台核销明细直接显示，不回查订单
  order_no        text NOT NULL CHECK (char_length(order_no) BETWEEN 1 AND 64),
  discount_amount numeric(20,2) NOT NULL CHECK (discount_amount >= 0),
  currency        text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  status          text NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'captured', 'released')),
  release_reason  text CHECK (char_length(release_reason) <= 200),
  captured_at     timestamptz,
  released_at     timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  -- 币种与券一致由库保证（复合外键指向 coupons 的 UNIQUE (id, currency)）
  CONSTRAINT coupon_redemptions_coupon_fkey FOREIGN KEY (coupon_id, currency)
    REFERENCES coupons (id, currency) ON DELETE RESTRICT,
  -- 一张订单至多用一张券
  CONSTRAINT coupon_redemptions_order_key UNIQUE (order_id),
  CONSTRAINT coupon_redemptions_state_times CHECK (
    (status = 'held' AND captured_at IS NULL AND released_at IS NULL)
    OR (status = 'captured' AND captured_at IS NOT NULL AND released_at IS NULL)
    OR (status = 'released' AND released_at IS NOT NULL AND captured_at IS NULL)
  )
);

-- 后台核销明细与列表里的「已减总额」
CREATE INDEX coupon_redemptions_coupon_idx ON coupon_redemptions (coupon_id, created_at DESC);
-- 每人限次：数这个人在这张券上未释放的占用
CREATE INDEX coupon_redemptions_user_active_idx ON coupon_redemptions (coupon_id, user_id)
  WHERE status IN ('held', 'captured');
-- users 外键的 RESTRICT 检查
CREATE INDEX coupon_redemptions_user_idx ON coupon_redemptions (user_id);

CREATE TRIGGER coupon_redemptions_no_delete
  BEFORE DELETE ON coupon_redemptions
  FOR EACH ROW EXECUTE FUNCTION deny_delete();

-- +goose Down
DROP TABLE IF EXISTS coupon_redemptions;
DROP TABLE IF EXISTS coupon_plans;
DROP TABLE IF EXISTS coupons;
