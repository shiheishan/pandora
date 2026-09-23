-- 商品订阅域：products / prices / plans / plan_versions / entitlements / quotas /
--              subscriptions / subscription_items
--
-- 对应 PRD 第 6 章 SUB-001..SUB-010，以及 XBD-006/007/011/012/013。
--
-- 本域最重要的两条不变量：
--   1. SUB-001「修改当前价格不改变历史订单和旧订阅」
--      → 订阅与订单持有 plan_version_id 与价格快照，永不反查当前 prices 表。
--   2. SUB-004「禁止非法状态跳转」
--      → 合法转换写进 subscription_transitions 表，由触发器强制，应用层绕不过去。

-- +goose Up

--------------------------------------------------------------------------------
-- 商品与价格（SUB-001）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE products (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code         text NOT NULL,
  name         text NOT NULL,
  description  text,
  kind         text NOT NULL DEFAULT 'subscription'
                 CHECK (kind IN ('subscription', 'addon', 'topup', 'one_time')),
  status       text NOT NULL DEFAULT 'draft'
                 CHECK (status IN ('draft', 'active', 'archived')),
  sort_order   int NOT NULL DEFAULT 0,
  metadata     jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT products_tenant_code_unique UNIQUE (tenant_id, code)
);
CREATE UNIQUE INDEX products_tenant_id_id_key ON products (tenant_id, id);

CREATE TRIGGER trg_products_updated_at BEFORE UPDATE ON products
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('products');
-- +goose StatementEnd

-- +goose StatementBegin
-- 价格独立于商品：同一商品可有多币种、多周期、多渠道价格。
-- 价格一经被订单引用便不可修改，只能新建价格并把旧价格 archived。
CREATE TABLE prices (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  product_id        uuid NOT NULL,
  currency          app.currency_code NOT NULL,
  -- 最小单位整数金额（SUB-010）
  unit_amount       app.minor_amount NOT NULL CHECK (unit_amount >= 0),
  -- 计费周期；one_time 表示一次性商品
  billing_interval  text NOT NULL DEFAULT 'month'
                      CHECK (billing_interval IN ('day', 'week', 'month', 'quarter', 'year', 'one_time')),
  interval_count    smallint NOT NULL DEFAULT 1 CHECK (interval_count > 0),
  -- SUB-007 试用：天数为 0 表示无试用
  trial_days        smallint NOT NULL DEFAULT 0 CHECK (trial_days >= 0),
  status            text NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active', 'archived')),
  -- 该价格仅对特定用户组开放（XBD-009 组级价格）
  user_group_id     uuid,
  valid_from        timestamptz,
  valid_until       timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT prices_product_fk
    FOREIGN KEY (tenant_id, product_id) REFERENCES products (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT prices_user_group_fk
    FOREIGN KEY (tenant_id, user_group_id) REFERENCES user_groups (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT prices_validity_range CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from)
);
CREATE UNIQUE INDEX prices_tenant_id_id_key ON prices (tenant_id, id);
CREATE INDEX idx_prices_product ON prices (tenant_id, product_id) WHERE status = 'active';

SELECT app.enable_tenant_rls('prices');

COMMENT ON TABLE prices IS
  'SUB-001：价格与商品分离。已被订单引用的价格只能归档不能改，历史订单据此保持金额稳定。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 套餐与套餐版本（SUB-002）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE plans (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  product_id   uuid NOT NULL,
  code         text NOT NULL,
  name         text NOT NULL,
  description  text,
  -- 当前对外销售的版本；历史订阅仍指向自己的旧版本
  current_version_id uuid,

  -- ---- XBD-011 套餐可见性 ----
  visibility   text NOT NULL DEFAULT 'public'
                 CHECK (visibility IN ('public', 'authenticated', 'group', 'invite_only', 'hidden')),
  visible_group_ids uuid[] NOT NULL DEFAULT '{}',
  visible_from timestamptz,
  visible_until timestamptz,

  -- ---- XBD-012 购买限制 ----
  allow_new_purchase boolean NOT NULL DEFAULT true,
  allow_renewal      boolean NOT NULL DEFAULT true,
  allow_upgrade      boolean NOT NULL DEFAULT true,
  -- NULL 表示不限购；否则为单用户累计可购买次数
  purchase_limit_per_user int CHECK (purchase_limit_per_user IS NULL OR purchase_limit_per_user > 0),
  -- NULL 表示无限库存；有限库存由 stock_reservations 行锁保证并发不超卖
  stock_total        int CHECK (stock_total IS NULL OR stock_total >= 0),
  stock_reserved     int NOT NULL DEFAULT 0 CHECK (stock_reserved >= 0),

  status       text NOT NULL DEFAULT 'draft'
                 CHECK (status IN ('draft', 'active', 'archived')),
  sort_order   int NOT NULL DEFAULT 0,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT plans_tenant_code_unique UNIQUE (tenant_id, code),
  CONSTRAINT plans_product_fk
    FOREIGN KEY (tenant_id, product_id) REFERENCES products (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT plans_stock_not_oversold CHECK (stock_total IS NULL OR stock_reserved <= stock_total)
);
CREATE UNIQUE INDEX plans_tenant_id_id_key ON plans (tenant_id, id);

CREATE TRIGGER trg_plans_updated_at BEFORE UPDATE ON plans
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('plans');
-- +goose StatementEnd

-- +goose StatementBegin
-- 套餐版本：权益的不可变快照单位。
-- 改套餐 = 发新版本，老用户仍跑老版本 —— 直接兑现 4.3「补齐订阅状态机和权益版本，
-- 避免套餐修改影响历史用户」。
CREATE TABLE plan_versions (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  plan_id        uuid NOT NULL,
  version        int NOT NULL CHECK (version > 0),
  -- 冻结后不可再改，只能发下一个版本
  frozen_at      timestamptz,

  -- ---- XBD-006 流量周期重置策略 ----
  quota_reset_strategy text NOT NULL DEFAULT 'billing_cycle'
    CHECK (quota_reset_strategy IN ('never', 'natural_month', 'billing_cycle', 'fixed_day')),
  -- fixed_day 策略下的日期（1–28，避开月末歧义）
  quota_reset_day smallint CHECK (quota_reset_day IS NULL OR quota_reset_day BETWEEN 1 AND 28),

  -- ---- SUB-008 宽限期 ----
  grace_period_hours int NOT NULL DEFAULT 0 CHECK (grace_period_hours >= 0),
  -- 宽限期内是否保留服务能力（false = 仅保留数据不提供服务）
  grace_keeps_service boolean NOT NULL DEFAULT true,

  -- ---- XBD-013 续费规则 ----
  renewal_extends_period boolean NOT NULL DEFAULT true,
  renewal_resets_quota   boolean NOT NULL DEFAULT true,
  renewal_keeps_addons   boolean NOT NULL DEFAULT true,

  -- ---- XBD-008 设备与并发限制 ----
  max_devices        int CHECK (max_devices IS NULL OR max_devices > 0),
  max_concurrent     int CHECK (max_concurrent IS NULL OR max_concurrent > 0),
  -- 设备解绑宽容：避免正常重装永久占位（XBD-008 验收标准）
  device_release_hours int NOT NULL DEFAULT 24 CHECK (device_release_hours >= 0),

  -- ---- USE-007 超额策略 ----
  overage_policy text NOT NULL DEFAULT 'suspend'
    CHECK (overage_policy IN ('suspend', 'throttle', 'metered_billing')),
  -- throttle 时的降速值（kbps），metered_billing 时的单价在 prices 中另挂
  throttle_kbps  int CHECK (throttle_kbps IS NULL OR throttle_kbps > 0),

  notes          text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid REFERENCES users(id) ON DELETE SET NULL,

  CONSTRAINT plan_versions_plan_version_unique UNIQUE (plan_id, version),
  CONSTRAINT plan_versions_plan_fk
    FOREIGN KEY (tenant_id, plan_id) REFERENCES plans (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT plan_versions_reset_day_required
    CHECK (quota_reset_strategy <> 'fixed_day' OR quota_reset_day IS NOT NULL)
);
CREATE UNIQUE INDEX plan_versions_tenant_id_id_key ON plan_versions (tenant_id, id);

SELECT app.enable_tenant_rls('plan_versions');

ALTER TABLE plans ADD CONSTRAINT plans_current_version_fk
  FOREIGN KEY (tenant_id, current_version_id) REFERENCES plan_versions (tenant_id, id)
  ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose StatementBegin
-- 冻结保护：已冻结的版本不允许再修改任何字段。
CREATE OR REPLACE FUNCTION app.guard_frozen_plan_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.frozen_at IS NOT NULL THEN
    RAISE EXCEPTION
      '套餐版本 % 已于 % 冻结（SUB-002），如需变更请发布新版本',
      OLD.id, OLD.frozen_at
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER trg_plan_versions_frozen BEFORE UPDATE ON plan_versions
  FOR EACH ROW EXECUTE FUNCTION app.guard_frozen_plan_version();
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 权益与配额（SUB-002）
--------------------------------------------------------------------------------

-- +goose StatementBegin
-- 权益 = 能力开关 / 资源池访问权 / 特性标记
CREATE TABLE entitlements (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  plan_version_id  uuid NOT NULL,
  -- 权益码，如 node_pool.access / feature.multi_device / support.priority
  code             text NOT NULL,
  value            jsonb NOT NULL DEFAULT 'true'::jsonb,
  created_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT entitlements_version_code_unique UNIQUE (plan_version_id, code),
  CONSTRAINT entitlements_plan_version_fk
    FOREIGN KEY (tenant_id, plan_version_id) REFERENCES plan_versions (tenant_id, id) ON DELETE CASCADE
);

SELECT app.enable_tenant_rls('entitlements');

COMMENT ON TABLE entitlements IS
  'SUB-002 验收「可解释用户最终获得的每一项权益来源」：每条权益都能追到具体 plan_version。';
-- +goose StatementEnd

-- +goose StatementBegin
-- 配额 = 可计量、可扣减的额度定义（USE-005）
CREATE TABLE quota_definitions (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  plan_version_id  uuid NOT NULL,
  -- 指标码：traffic.bytes / requests.count / devices.active / concurrent.sessions
  metric           text NOT NULL,
  -- 额度上限，NULL 表示不限量
  limit_value      bigint CHECK (limit_value IS NULL OR limit_value >= 0),
  unit             text NOT NULL DEFAULT 'bytes',
  -- 周期类型：total 表示订阅存续期内总量不重置
  period           text NOT NULL DEFAULT 'cycle'
                     CHECK (period IN ('total', 'cycle', 'day', 'month')),
  created_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT quota_definitions_version_metric_unique UNIQUE (plan_version_id, metric, period),
  CONSTRAINT quota_definitions_plan_version_fk
    FOREIGN KEY (tenant_id, plan_version_id) REFERENCES plan_versions (tenant_id, id) ON DELETE CASCADE
);

SELECT app.enable_tenant_rls('quota_definitions');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 订阅（SUB-003 / SUB-004）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE subscriptions (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id           uuid NOT NULL,
  organization_id   uuid,
  plan_id           uuid NOT NULL,
  -- 关键：锁定到具体版本，套餐后续改版不影响本订阅（SUB-002）
  plan_version_id   uuid NOT NULL,

  status            text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'trialing', 'active', 'past_due',
                      'grace', 'paused', 'cancelled', 'expired')),

  -- ---- 周期 ----
  current_period_start timestamptz,
  current_period_end   timestamptz,
  -- 固定日续费锚点（SUB-003）
  billing_anchor_day   smallint CHECK (billing_anchor_day IS NULL OR billing_anchor_day BETWEEN 1 AND 28),
  trial_end            timestamptz,
  grace_end            timestamptz,
  -- 取消后仍服务到期末；立即取消则同时置 ended_at
  cancel_at_period_end boolean NOT NULL DEFAULT false,
  cancelled_at         timestamptz,
  ended_at             timestamptz,
  paused_at            timestamptz,

  auto_renew        boolean NOT NULL DEFAULT true,
  -- 价格快照：续费金额不随 prices 表变动（SUB-001）
  price_id          uuid,
  snapshot_currency app.currency_code NOT NULL,
  snapshot_amount   app.minor_amount NOT NULL CHECK (snapshot_amount >= 0),

  -- XBD-002 订阅凭据在 subscription_credentials 表，此处只记当前活跃条数便于展示
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT subscriptions_user_fk
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT subscriptions_org_fk
    FOREIGN KEY (tenant_id, organization_id) REFERENCES organizations (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT subscriptions_plan_fk
    FOREIGN KEY (tenant_id, plan_id) REFERENCES plans (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT subscriptions_plan_version_fk
    FOREIGN KEY (tenant_id, plan_version_id) REFERENCES plan_versions (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT subscriptions_price_fk
    FOREIGN KEY (tenant_id, price_id) REFERENCES prices (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT subscriptions_period_order
    CHECK (current_period_end IS NULL OR current_period_start IS NULL
           OR current_period_end > current_period_start)
);
CREATE UNIQUE INDEX subscriptions_tenant_id_id_key ON subscriptions (tenant_id, id);

CREATE INDEX idx_subscriptions_user ON subscriptions (tenant_id, user_id, created_at DESC);
-- 到期扫描任务的驱动索引（XBD-024 幂等定时任务）
CREATE INDEX idx_subscriptions_period_end ON subscriptions (current_period_end)
  WHERE status IN ('active', 'trialing', 'past_due', 'grace');

CREATE TRIGGER trg_subscriptions_updated_at BEFORE UPDATE ON subscriptions
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('subscriptions');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- SUB-004 状态机：合法转换表 + 触发器强制
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE subscription_transitions (
  from_status text NOT NULL,
  to_status   text NOT NULL,
  PRIMARY KEY (from_status, to_status)
);

INSERT INTO subscription_transitions (from_status, to_status) VALUES
  -- 开通
  ('pending',   'trialing'),   ('pending',   'active'),   ('pending',   'cancelled'),
  ('pending',   'expired'),
  -- 试用结束
  ('trialing',  'active'),     ('trialing',  'past_due'), ('trialing',  'cancelled'),
  ('trialing',  'expired'),
  -- 正常运行
  ('active',    'past_due'),   ('active',    'paused'),   ('active',    'cancelled'),
  ('active',    'expired'),
  -- 逾期 → 宽限 → 回收
  ('past_due',  'active'),     ('past_due',  'grace'),    ('past_due',  'cancelled'),
  ('past_due',  'expired'),
  ('grace',     'active'),     ('grace',     'expired'),  ('grace',     'cancelled'),
  -- 暂停恢复
  ('paused',    'active'),     ('paused',    'cancelled'), ('paused',   'expired'),
  -- 取消后在期末结束
  ('cancelled', 'expired');

COMMENT ON TABLE subscription_transitions IS
  'SUB-004：唯一的状态机真相来源。缺失的组合即为非法跳转，由触发器拒绝。';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_subscription_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.status IS DISTINCT FROM OLD.status THEN
    IF NOT EXISTS (
      SELECT 1 FROM subscription_transitions
      WHERE from_status = OLD.status AND to_status = NEW.status
    ) THEN
      RAISE EXCEPTION
        '订阅 % 非法状态跳转：% -> %（SUB-004）',
        OLD.id, OLD.status, NEW.status
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER trg_subscriptions_transition BEFORE UPDATE ON subscriptions
  FOR EACH ROW EXECUTE FUNCTION app.guard_subscription_transition();
-- +goose StatementEnd

-- +goose StatementBegin
-- 状态变更事件流（SUB-004「并记录事件」），追加写。
CREATE TABLE subscription_events (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  subscription_id uuid NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  -- created / activated / renewed / upgraded / downgraded / paused / resumed /
  -- cancelled / expired / quota_reset / entitlement_changed
  event_type      text NOT NULL,
  from_status     text,
  to_status       text,
  -- 触发来源：user / admin / system / payment / scheduler
  actor_kind      text NOT NULL DEFAULT 'system',
  actor_id        uuid,
  -- 关联的订单或支付，便于「为什么我的订阅变了」自助解释
  order_id        uuid,
  payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_subscription_events_sub
  ON subscription_events (subscription_id, occurred_at DESC);

SELECT app.enable_tenant_rls('subscription_events');
SELECT app.make_append_only('subscription_events');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 订阅项：主套餐之外的附加包（SUB-006）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE subscription_items (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  subscription_id   uuid NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  product_id        uuid NOT NULL,
  price_id          uuid,
  kind              text NOT NULL DEFAULT 'addon'
                      CHECK (kind IN ('base', 'addon', 'quota_pack')),
  quantity          int NOT NULL DEFAULT 1 CHECK (quantity > 0),
  -- XBD-007 流量重置包：作为权益事件而非直接改库存值
  grants_metric     text,
  grants_amount     bigint CHECK (grants_amount IS NULL OR grants_amount > 0),
  snapshot_currency app.currency_code,
  snapshot_amount   app.minor_amount CHECK (snapshot_amount IS NULL OR snapshot_amount >= 0),
  status            text NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active', 'expired', 'refunded', 'cancelled')),
  starts_at         timestamptz NOT NULL DEFAULT now(),
  ends_at           timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT subscription_items_product_fk
    FOREIGN KEY (tenant_id, product_id) REFERENCES products (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT subscription_items_price_fk
    FOREIGN KEY (tenant_id, price_id) REFERENCES prices (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT subscription_items_grant_pair
    CHECK ((grants_metric IS NULL) = (grants_amount IS NULL))
);

CREATE INDEX idx_subscription_items_sub ON subscription_items (subscription_id)
  WHERE status = 'active';

CREATE TRIGGER trg_subscription_items_updated_at BEFORE UPDATE ON subscription_items
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('subscription_items');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- SUB-007 试用领取记录：防止重复注册无限领试用
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE trial_grants (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  plan_id       uuid NOT NULL,
  user_id       uuid,
  organization_id uuid,
  -- 跨账号识别：设备指纹哈希、支付主体指纹哈希
  device_fingerprint_hash bytea,
  payment_subject_hash    bytea,
  subscription_id uuid REFERENCES subscriptions(id) ON DELETE SET NULL,
  granted_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT trial_grants_plan_fk
    FOREIGN KEY (tenant_id, plan_id) REFERENCES plans (tenant_id, id) ON DELETE CASCADE
);

-- 三条部分唯一索引：同一套餐下，用户/设备/支付主体各自只能领一次
CREATE UNIQUE INDEX uq_trial_grants_user
  ON trial_grants (tenant_id, plan_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX uq_trial_grants_device
  ON trial_grants (tenant_id, plan_id, device_fingerprint_hash)
  WHERE device_fingerprint_hash IS NOT NULL;
CREATE UNIQUE INDEX uq_trial_grants_payment
  ON trial_grants (tenant_id, plan_id, payment_subject_hash)
  WHERE payment_subject_hash IS NOT NULL;

SELECT app.enable_tenant_rls('trial_grants');

COMMENT ON TABLE trial_grants IS
  'SUB-007 验收「无法通过重复注册无限领取试用」：唯一索引在数据库层挡住，不靠应用判重。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- XBD-012 购买次数：并发安全的计数
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE plan_purchase_counters (
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  plan_id    uuid NOT NULL,
  user_id    uuid NOT NULL,
  purchased  int NOT NULL DEFAULT 0 CHECK (purchased >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (tenant_id, plan_id, user_id),
  CONSTRAINT plan_purchase_counters_plan_fk
    FOREIGN KEY (tenant_id, plan_id) REFERENCES plans (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT plan_purchase_counters_user_fk
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);

SELECT app.enable_tenant_rls('plan_purchase_counters');

COMMENT ON TABLE plan_purchase_counters IS
  'XBD-012 验收「并发购买不突破库存和次数限制」：下单事务内 SELECT FOR UPDATE 本行再自增。';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS plan_purchase_counters;
DROP TABLE IF EXISTS trial_grants;
DROP TABLE IF EXISTS subscription_items;
DROP TABLE IF EXISTS subscription_events;
DROP TRIGGER IF EXISTS trg_subscriptions_transition ON subscriptions;
DROP FUNCTION IF EXISTS app.guard_subscription_transition();
DROP TABLE IF EXISTS subscription_transitions;
DROP TABLE IF EXISTS subscriptions;
DROP TABLE IF EXISTS quota_definitions;
DROP TABLE IF EXISTS entitlements;
DROP TRIGGER IF EXISTS trg_plan_versions_frozen ON plan_versions;
DROP FUNCTION IF EXISTS app.guard_frozen_plan_version();
ALTER TABLE plans DROP CONSTRAINT IF EXISTS plans_current_version_fk;
DROP TABLE IF EXISTS plan_versions;
DROP TABLE IF EXISTS plans;
DROP TABLE IF EXISTS prices;
DROP TABLE IF EXISTS products;
-- +goose StatementEnd
