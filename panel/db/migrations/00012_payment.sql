-- +goose Up
-- payment 模块：支付渠道、支付意图、渠道确认的收款、渠道回调事件、迟到支付挂账。
-- 金额列都是 numeric(20,2)，多余小数由写入前的 money.Check 拦（库里 CHECK 写不出来）。
-- 各表的 order_id 先建成普通 uuid 列加索引，外键由 00013 补（订单表在 00013_order.sql 才建）。
-- 状态机在 Go 里（转换表 + CanTransition，更新带 WHERE status = 旧状态），库里只留状态枚举。

-- 支付渠道。渠道配置拆成带类型的列（最低额等钱与标量不放 jsonb）；商户号与密钥封存后放 credentials_sealed，
-- AAD 绑定渠道 id，读出只在调渠道时解封。
CREATE TABLE payment_providers (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  code               text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_-]{1,31}$'),
  adapter            text NOT NULL CHECK (adapter IN ('epay', 'demo_hmac', 'offline')),
  display_name       text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 40),
  credentials_sealed bytea,
  -- 字符串数组（jsonb）：币种与方式都是封闭集合
  currencies         jsonb NOT NULL DEFAULT '[]'::jsonb
                       CHECK (jsonb_typeof(currencies) = 'array' AND currencies <@ '["CNY", "USD"]'::jsonb),
  base_url           text NOT NULL DEFAULT '' CHECK (char_length(base_url) <= 512),
  submit_path        text NOT NULL DEFAULT '' CHECK (char_length(submit_path) <= 128),
  api_path           text NOT NULL DEFAULT '' CHECK (char_length(api_path) <= 128),
  -- 易支付只出支付宝和微信（用户 2026-10-07 定）
  methods            jsonb NOT NULL DEFAULT '[]'::jsonb
                       CHECK (jsonb_typeof(methods) = 'array' AND methods <@ '["alipay", "wxpay"]'::jsonb),
  default_method     text NOT NULL DEFAULT '' CHECK (default_method IN ('', 'alipay', 'wxpay')),
  allow_private_host boolean NOT NULL DEFAULT false,
  -- 最低付款额（元）；0 表示不限（线下、demo）
  min_amount         numeric(20,2) NOT NULL DEFAULT 0 CHECK (min_amount >= 0 AND min_amount <= 1000),
  enabled            boolean NOT NULL DEFAULT false,
  -- 停收新单但照常处理回调与查单
  accepting_new      boolean NOT NULL DEFAULT false,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT payment_providers_code_key UNIQUE (code)
);

-- 线下收款渠道：后台「标记已支付」与人工单「线下已收款」记账用，结账页选不到（accepting_new = false）。
INSERT INTO payment_providers (code, adapter, display_name, currencies, enabled, accepting_new)
VALUES ('offline', 'offline', '线下收款', '["CNY", "USD"]'::jsonb, true, false);

-- 支付意图：一次「去支付」。金额、币种是建意图时的订单应付快照，回调与查单的金额必须与它相等。
-- 状态 requires_action → succeeded / cancelled / expired，只走一次。
CREATE TABLE payment_intents (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  -- 外键由 00013 补
  order_id       uuid NOT NULL,
  order_no       text NOT NULL CHECK (char_length(order_no) BETWEEN 1 AND 64),
  user_id        uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  provider_id    uuid NOT NULL REFERENCES payment_providers(id) ON DELETE RESTRICT,
  -- 交给渠道的商户单号：第一次是订单号，之后是「订单号-序号」
  out_trade_no   text NOT NULL CHECK (char_length(out_trade_no) BETWEEN 1 AND 64),
  -- 渠道建单时给的渠道侧单号（易支付不给）
  provider_ref   text CHECK (char_length(provider_ref) <= 128),
  method         text NOT NULL DEFAULT '' CHECK (method ~ '^[a-z0-9_]{0,32}$'),
  currency       text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  amount         numeric(20,2) NOT NULL CHECK (amount > 0),
  status         text NOT NULL DEFAULT 'requires_action'
                   CHECK (status IN ('requires_action', 'succeeded', 'cancelled', 'expired')),
  http_method    text NOT NULL CHECK (http_method IN ('GET', 'POST')),
  redirect_url   text NOT NULL CHECK (char_length(redirect_url) <= 4096),
  form_fields    jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(form_fields) = 'object'),
  expires_at     timestamptz,
  -- 巡检查单的排程：已查次数与下一次最早查的时间（为空 = 还没查过，按 created_at 起算）
  query_attempts integer NOT NULL DEFAULT 0 CHECK (query_attempts >= 0),
  next_query_at  timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT payment_intents_out_trade_no_key UNIQUE (provider_id, out_trade_no)
);
CREATE INDEX payment_intents_order_idx ON payment_intents (order_id, created_at DESC);
-- 一张订单同时至多一个在途意图：用户连点两次「去支付」回到同一个收银台
CREATE UNIQUE INDEX payment_intents_active_order_key ON payment_intents (order_id)
  WHERE status = 'requires_action';
CREATE INDEX payment_intents_patrol_idx ON payment_intents (next_query_at, created_at)
  WHERE status = 'requires_action';
CREATE INDEX payment_intents_provider_idx ON payment_intents (provider_id, updated_at DESC);

-- 收款：渠道确认收到的钱，一笔渠道支付只记一次。
CREATE TABLE payments (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  -- 外键由 00013 补
  order_id            uuid NOT NULL,
  user_id             uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  payment_intent_id   uuid REFERENCES payment_intents(id) ON DELETE RESTRICT,
  provider_id         uuid NOT NULL REFERENCES payment_providers(id) ON DELETE RESTRICT,
  provider_payment_id text NOT NULL CHECK (char_length(provider_payment_id) BETWEEN 1 AND 128),
  currency            text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  amount              numeric(20,2) NOT NULL CHECK (amount > 0),
  fee_amount          numeric(20,2) NOT NULL DEFAULT 0 CHECK (fee_amount >= 0 AND fee_amount <= amount),
  status              text NOT NULL DEFAULT 'succeeded' CHECK (status IN ('succeeded')),
  method              text CHECK (method ~ '^[a-z0-9_]{1,32}$'),
  paid_at             timestamptz NOT NULL DEFAULT now(),
  created_at          timestamptz NOT NULL DEFAULT now(),
  -- 同一笔渠道支付不可能被记两次
  CONSTRAINT payments_provider_payment_key UNIQUE (provider_id, provider_payment_id)
);
CREATE INDEX payments_order_idx ON payments (order_id, paid_at DESC);
CREATE INDEX payments_provider_paid_idx ON payments (provider_id, paid_at DESC);

-- 渠道回调与查单事件：(provider_id, provider_event_id) 唯一是重放的防线；证据，不许删。
CREATE TABLE payment_events (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  provider_id         uuid NOT NULL REFERENCES payment_providers(id) ON DELETE RESTRICT,
  provider_event_id   text NOT NULL CHECK (char_length(provider_event_id) BETWEEN 1 AND 200),
  event_type          text NOT NULL CHECK (event_type IN (
                        'payment.succeeded', 'payment.failed', 'payment.refunded', 'payment.unknown')),
  provider_payment_id text CHECK (char_length(provider_payment_id) <= 128),
  raw_payload         jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(raw_payload) = 'object'),
  signature_verified  boolean NOT NULL,
  processing_status   text NOT NULL DEFAULT 'pending'
                        CHECK (processing_status IN ('pending', 'processed', 'ignored')),
  processing_error    text CHECK (char_length(processing_error) <= 500),
  processed_at        timestamptz,
  received_at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT payment_events_provider_event_key UNIQUE (provider_id, provider_event_id),
  CONSTRAINT payment_events_processed_at CHECK ((processing_status = 'pending') = (processed_at IS NULL))
);
CREATE INDEX payment_events_provider_received_idx ON payment_events (provider_id, received_at DESC);

CREATE TRIGGER payment_events_no_delete
  BEFORE DELETE OR TRUNCATE ON payment_events
  FOR EACH STATEMENT EXECUTE FUNCTION deny_delete();

-- 迟到支付挂账：订单已取消、超时或已付之后才到的钱，记进 late_payment_suspense 科目，等管理员处理。
-- 每笔收款至多一个挂账案；处理只有一种：转入用户余额（applied）。
CREATE TABLE late_payment_cases (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  -- 外键由 00013 补
  order_id          uuid NOT NULL,
  order_no          text NOT NULL CHECK (char_length(order_no) BETWEEN 1 AND 64),
  -- 收款到达时订单的状态（由 order 的结算回调告知）
  order_status      text NOT NULL CHECK (order_status ~ '^[a-z_]{1,32}$'),
  user_id           uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  payment_id        uuid NOT NULL REFERENCES payments(id) ON DELETE RESTRICT,
  payment_event_id  uuid REFERENCES payment_events(id) ON DELETE RESTRICT,
  case_kind         text NOT NULL CHECK (case_kind IN ('released_order', 'excess_capture', 'ineligible_subscription')),
  currency          text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  amount            numeric(20,2) NOT NULL CHECK (amount > 0),
  status            text NOT NULL DEFAULT 'suspense' CHECK (status IN ('suspense', 'applied')),
  suspense_txn_id   uuid NOT NULL,
  applied_txn_id    uuid,
  resolution_reason text CHECK (char_length(resolution_reason) <= 500),
  received_at       timestamptz NOT NULL DEFAULT now(),
  resolved_at       timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT late_payment_cases_payment_key UNIQUE (payment_id),
  CONSTRAINT late_payment_cases_suspense_txn_key UNIQUE (suspense_txn_id),
  CONSTRAINT late_payment_cases_suspense_txn_fk FOREIGN KEY (suspense_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  CONSTRAINT late_payment_cases_applied_txn_fk FOREIGN KEY (applied_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  CONSTRAINT late_payment_cases_resolution CHECK (
    (status = 'suspense' AND applied_txn_id IS NULL AND resolved_at IS NULL AND resolution_reason IS NULL)
    OR (status = 'applied' AND applied_txn_id IS NOT NULL AND resolved_at IS NOT NULL
        AND char_length(btrim(resolution_reason)) >= 5)
  )
);
CREATE INDEX late_payment_cases_status_idx ON late_payment_cases (status, received_at DESC);
CREATE INDEX late_payment_cases_order_idx ON late_payment_cases (order_id);
CREATE INDEX late_payment_cases_user_idx ON late_payment_cases (user_id);

CREATE TRIGGER late_payment_cases_no_delete
  BEFORE DELETE OR TRUNCATE ON late_payment_cases
  FOR EACH STATEMENT EXECUTE FUNCTION deny_delete();

-- +goose Down
DROP TABLE IF EXISTS late_payment_cases;
DROP TABLE IF EXISTS payment_events;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS payment_intents;
DROP TABLE IF EXISTS payment_providers;
