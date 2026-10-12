-- +goose Up
-- giftcard 模块：礼品卡模板（含奖池、限定套餐）、生码批次、卡密、兑换流水。
-- 依赖 00005_identity.sql 的 users、00008_catalog.sql 的 plans 与 prices、00009_ledger.sql 的 ledger_transactions、
-- 00011_subscription.sql 的 subscriptions；plan_id、price_id 写入前由 giftcard service 经 catalog service 校验，库里再用外键兜底。
--
-- 卡密是等价现金，库里不存明文：gift_card_codes 只存 HMAC（主密钥派生的子密钥）与展示用掩码；
-- 整批明文用 AES-GCM 封存在 gift_card_batches.codes_sealed，一次性导出时清空。
--
-- 金额一律 NUMERIC(20,2)、单位元，币种列另存（docs/architecture.md 第 6 节）；余额奖励与奖池从 jsonb 拆成带类型的列（R9）。
-- 金额列不加 round(x, 2) 的 CHECK：NUMERIC(20,2) 先把值舍入到两位再查约束，那条永远成立；多余小数只靠写入前的 money.Check 拦（R2）。
-- 兑换流水只追加、不设保留期（与账本一样是发放凭据），不建清理函数。

CREATE TABLE gift_card_templates (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  name             text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
  description      text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  -- general 送余额 / 流量 / 加时长 / 重置流量；plan 兑换一个套餐；mystery 从奖池里加权抽一个
  type             text NOT NULL CHECK (type IN ('general', 'plan', 'mystery')),
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'archived')),
  -- 余额奖励（含奖池里的余额）的币种，建模板时取站点币种
  currency         text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  -- general 的奖励；没有的项为 NULL
  balance_amount   numeric(20,2) CHECK (balance_amount > 0),
  traffic_bytes    bigint CHECK (traffic_bytes BETWEEN 1 AND 1125899906842624),
  expire_days      int CHECK (expire_days BETWEEN 1 AND 3650),
  reset_quota      boolean NOT NULL DEFAULT false,
  -- plan 的套餐与可选的价格档（没有价格档时按一个月开通）
  plan_id          uuid REFERENCES plans(id) ON DELETE RESTRICT,
  price_id         uuid REFERENCES prices(id) ON DELETE RESTRICT,
  -- 领取条件（限定套餐在 gift_card_template_plans）
  new_user_only    boolean NOT NULL DEFAULT false,
  paid_user_only   boolean NOT NULL DEFAULT false,
  require_invite   boolean NOT NULL DEFAULT false,
  -- 使用限制：同一模板每人最多兑几次（0 不限）、两次兑换之间的冷却小时数（0 不限）
  max_use_per_user int NOT NULL DEFAULT 0 CHECK (max_use_per_user BETWEEN 0 AND 10000),
  cooldown_hours   int NOT NULL DEFAULT 0 CHECK (cooldown_hours BETWEEN 0 AND 87600),
  theme_color      text NOT NULL DEFAULT '' CHECK (char_length(theme_color) <= 32),
  created_by       uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT gift_card_templates_name_key UNIQUE (name),
  -- 「仅新用户」与「仅付费用户」同时勾选等于谁都不能兑
  CONSTRAINT gift_card_templates_conditions_exclusive CHECK (NOT (new_user_only AND paid_user_only)),
  CONSTRAINT gift_card_templates_price_needs_plan CHECK (price_id IS NULL OR plan_id IS NOT NULL),
  -- 各卡型只带自己的奖励列：通用卡至少送一样，套餐卡必须有套餐，盲盒的奖励在奖池表
  CONSTRAINT gift_card_templates_reward_shape CHECK (
    (type = 'general' AND plan_id IS NULL
      AND (balance_amount IS NOT NULL OR traffic_bytes IS NOT NULL OR expire_days IS NOT NULL OR reset_quota))
    OR (type = 'plan' AND plan_id IS NOT NULL
      AND balance_amount IS NULL AND traffic_bytes IS NULL AND expire_days IS NULL AND NOT reset_quota)
    OR (type = 'mystery' AND plan_id IS NULL
      AND balance_amount IS NULL AND traffic_bytes IS NULL AND expire_days IS NULL AND NOT reset_quota)
  )
);
CREATE INDEX gift_card_templates_created_idx ON gift_card_templates (created_at DESC);
CREATE INDEX gift_card_templates_plan_idx ON gift_card_templates (plan_id) WHERE plan_id IS NOT NULL;
CREATE INDEX gift_card_templates_price_idx ON gift_card_templates (price_id) WHERE price_id IS NOT NULL;

-- 盲盒奖池：一行一个奖品，position 是展示与抽奖的次序。权重是相对权重；余额按模板的币种。
-- 改模板时整池替换（先删后插）。
CREATE TABLE gift_card_prizes (
  template_id    uuid NOT NULL REFERENCES gift_card_templates(id) ON DELETE CASCADE,
  position       int NOT NULL CHECK (position BETWEEN 0 AND 49),
  label          text NOT NULL CHECK (char_length(btrim(label)) BETWEEN 1 AND 100),
  weight         int NOT NULL CHECK (weight BETWEEN 1 AND 1000000),
  balance_amount numeric(20,2) CHECK (balance_amount > 0),
  traffic_bytes  bigint CHECK (traffic_bytes BETWEEN 1 AND 1125899906842624),
  expire_days    int CHECK (expire_days BETWEEN 1 AND 3650),
  PRIMARY KEY (template_id, position),
  CONSTRAINT gift_card_prizes_gives_something
    CHECK (balance_amount IS NOT NULL OR traffic_bytes IS NOT NULL OR expire_days IS NOT NULL)
);

-- 「仅持有这些套餐的用户」：没有行即不限。套餐只归档不删，外键用 RESTRICT。
CREATE TABLE gift_card_template_plans (
  template_id uuid NOT NULL REFERENCES gift_card_templates(id) ON DELETE CASCADE,
  plan_id     uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  PRIMARY KEY (template_id, plan_id)
);
CREATE INDEX gift_card_template_plans_plan_idx ON gift_card_template_plans (plan_id);

-- 生码批次：一次生成的一批码。codes_sealed 是这批明文卡密的密文（AAD 绑批次 id），
-- 只为一次性导出而存：导出时在同一事务里清空并打上 exported_at，之后完整卡密再也取不回。
CREATE TABLE gift_card_batches (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  template_id  uuid NOT NULL REFERENCES gift_card_templates(id) ON DELETE RESTRICT,
  prefix       text NOT NULL DEFAULT '' CHECK (prefix ~ '^[A-Z0-9]{0,8}$'),
  count        int NOT NULL CHECK (count BETWEEN 1 AND 5000),
  expires_at   timestamptz,
  codes_sealed bytea,
  created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  exported_at  timestamptz,
  exported_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  -- 给卡密的 (batch_id, template_id) 复合外键用：码的模板必须是批次的模板
  CONSTRAINT gift_card_batches_id_template_key UNIQUE (id, template_id),
  -- 没导出时密文在、导出后密文清空：明文只能出去一次
  CONSTRAINT gift_card_batches_export_shape CHECK ((exported_at IS NULL) = (codes_sealed IS NOT NULL)),
  CONSTRAINT gift_card_batches_exporter_shape CHECK (exported_at IS NOT NULL OR exported_by IS NULL)
);
CREATE INDEX gift_card_batches_created_idx ON gift_card_batches (created_at DESC, id DESC);
CREATE INDEX gift_card_batches_template_idx ON gift_card_batches (template_id, created_at DESC);
CREATE INDEX gift_card_batches_created_by_idx ON gift_card_batches (created_by) WHERE created_by IS NOT NULL;
CREATE INDEX gift_card_batches_exported_by_idx ON gift_card_batches (exported_by) WHERE exported_by IS NOT NULL;

-- 卡密：只存 HMAC-SHA256 与掩码。状态转换由 service 的转换表约束（unused → used / disabled / expired，
-- disabled → unused），更新带 WHERE status = 旧状态。
CREATE TABLE gift_card_codes (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  template_id uuid NOT NULL REFERENCES gift_card_templates(id) ON DELETE RESTRICT,
  batch_id    uuid NOT NULL,
  code_hash   bytea NOT NULL CHECK (octet_length(code_hash) = 32),
  -- 前缀加随机段前 4 位，其余 8 位换成 •：够人工核对是哪张卡，不够拿去兑换
  code_mask   text NOT NULL CHECK (char_length(code_mask) BETWEEN 8 AND 32),
  status      text NOT NULL DEFAULT 'unused' CHECK (status IN ('unused', 'used', 'disabled', 'expired')),
  expires_at  timestamptz,
  used_by     uuid REFERENCES users(id) ON DELETE RESTRICT,
  used_at     timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT gift_card_codes_hash_key UNIQUE (code_hash),
  CONSTRAINT gift_card_codes_batch_fkey FOREIGN KEY (batch_id, template_id)
    REFERENCES gift_card_batches (id, template_id) ON DELETE RESTRICT,
  -- 已兑换必须同时有人和时间，其余状态两者都空
  CONSTRAINT gift_card_codes_used_shape CHECK (
    (status = 'used') = (used_by IS NOT NULL) AND (status = 'used') = (used_at IS NOT NULL)
  )
);
CREATE INDEX gift_card_codes_template_idx ON gift_card_codes (template_id, status);
CREATE INDEX gift_card_codes_batch_idx ON gift_card_codes (batch_id, status);
-- 后台卡密列表按 (created_at DESC, id DESC) 翻页
CREATE INDEX gift_card_codes_created_idx ON gift_card_codes (created_at DESC, id DESC);
CREATE INDEX gift_card_codes_used_by_idx ON gift_card_codes (used_by) WHERE used_by IS NOT NULL;

-- 兑换流水：实际发出去的东西逐项落列（盲盒抽中什么、套餐卡怎么落地、换套餐退回多少）。只追加。
CREATE TABLE gift_card_redemptions (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  code_id         uuid NOT NULL REFERENCES gift_card_codes(id) ON DELETE RESTRICT,
  template_id     uuid NOT NULL REFERENCES gift_card_templates(id) ON DELETE RESTRICT,
  user_id         uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  -- 盲盒抽中的奖品名
  prize_label     text CHECK (char_length(prize_label) BETWEEN 1 AND 100),
  -- 入余额的金额（币种 currency）与那笔账
  currency        text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  balance_amount  numeric(20,2) CHECK (balance_amount > 0),
  ledger_txn_id   uuid REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
  traffic_bytes   bigint CHECK (traffic_bytes > 0),
  expire_days     int CHECK (expire_days BETWEEN 1 AND 3650),
  quota_reset     boolean NOT NULL DEFAULT false,
  -- 套餐卡：套餐与落地方式（new 新开、renewed 续一期、changed 换套餐）
  plan_id         uuid REFERENCES plans(id) ON DELETE RESTRICT,
  plan_mode       text CHECK (plan_mode IN ('new', 'renewed', 'changed')),
  -- 这张卡落到的那一份；送流量没有可挂的那一份时为空（流量包记为未分配）
  subscription_id uuid REFERENCES subscriptions(id) ON DELETE RESTRICT,
  -- 换套餐时原套餐退进余额的剩余价值
  refund_amount   numeric(20,2) CHECK (refund_amount > 0),
  refund_currency text CHECK (refund_currency IN ('CNY', 'USD')),
  redeemed_at     timestamptz NOT NULL DEFAULT now(),
  -- 一码一次的最后一道防线（应用层先锁码行、先做状态转换）
  CONSTRAINT gift_card_redemptions_code_key UNIQUE (code_id),
  CONSTRAINT gift_card_redemptions_balance_shape CHECK ((balance_amount IS NULL) = (ledger_txn_id IS NULL)),
  CONSTRAINT gift_card_redemptions_plan_shape CHECK ((plan_id IS NULL) = (plan_mode IS NULL)),
  CONSTRAINT gift_card_redemptions_refund_shape CHECK (
    (refund_amount IS NULL) = (refund_currency IS NULL) AND (refund_amount IS NULL OR plan_mode = 'changed')
  )
);
-- 门户「我的兑换记录」
CREATE INDEX gift_card_redemptions_user_idx ON gift_card_redemptions (user_id, redeemed_at DESC);
-- 每人限次与冷却
CREATE INDEX gift_card_redemptions_user_template_idx ON gift_card_redemptions (user_id, template_id, redeemed_at DESC);
-- 后台使用明细（按模板筛或不筛）
CREATE INDEX gift_card_redemptions_template_idx ON gift_card_redemptions (template_id, redeemed_at DESC);
CREATE INDEX gift_card_redemptions_time_idx ON gift_card_redemptions (redeemed_at DESC, id DESC);
CREATE INDEX gift_card_redemptions_plan_idx ON gift_card_redemptions (plan_id) WHERE plan_id IS NOT NULL;
CREATE INDEX gift_card_redemptions_subscription_idx ON gift_card_redemptions (subscription_id) WHERE subscription_id IS NOT NULL;
CREATE INDEX gift_card_redemptions_ledger_idx ON gift_card_redemptions (ledger_txn_id) WHERE ledger_txn_id IS NOT NULL;

CREATE TRIGGER gift_card_redemptions_append_only
  BEFORE UPDATE OR DELETE ON gift_card_redemptions
  FOR EACH ROW EXECUTE FUNCTION deny_mutation();

-- +goose Down
DROP TABLE IF EXISTS gift_card_redemptions;
DROP TABLE IF EXISTS gift_card_codes;
DROP TABLE IF EXISTS gift_card_batches;
DROP TABLE IF EXISTS gift_card_template_plans;
DROP TABLE IF EXISTS gift_card_prizes;
DROP TABLE IF EXISTS gift_card_templates;
