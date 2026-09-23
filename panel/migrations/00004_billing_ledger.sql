-- 交易域：orders / order_items / payment_intents / payments / refunds / invoices /
--          ledger_accounts / ledger_entries
--
-- 对应 PRD 第 7 章 PAY-001..PAY-010，以及 4.3「补齐统一账本，杜绝直接修改余额字段」。
--
-- 本域四条硬不变量，全部由数据库强制而非应用自觉：
--   1. PAY-001 订单 ≠ 支付：支付处理中绝不把订单标记为完成。
--   2. PAY-003 幂等：(provider, provider_event_id) 唯一，重复回调 100 次只落一次。
--   3. PAY-005 复式记账：一笔账务交易的借贷必须配平，由延迟约束触发器在提交时校验。
--   4. PAY-004 退款不超额：已退金额 + 本次退款 ≤ 已收金额，由 CHECK + 行锁保证。

-- +goose Up

--------------------------------------------------------------------------------
-- 订单（PAY-001）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE orders (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 对用户展示的短单号，与内部 UUID 分离（DATA-001：公网不可通过连续 ID 推断规模）
  order_no         text NOT NULL,
  user_id          uuid NOT NULL,
  organization_id  uuid,

  kind             text NOT NULL DEFAULT 'new'
                     CHECK (kind IN ('new', 'renewal', 'upgrade', 'downgrade',
                                     'addon', 'topup', 'manual')),
  status           text NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft', 'pending_payment', 'processing',
                                       'paid', 'fulfilled', 'cancelled',
                                       'expired', 'refunded', 'partially_refunded')),

  currency         app.currency_code NOT NULL,
  -- 全部为币种最小单位整数
  subtotal_amount  app.minor_amount NOT NULL DEFAULT 0 CHECK (subtotal_amount >= 0),
  discount_amount  app.minor_amount NOT NULL DEFAULT 0 CHECK (discount_amount >= 0),
  tax_amount       app.minor_amount NOT NULL DEFAULT 0 CHECK (tax_amount >= 0),
  -- 余额抵扣部分（XBD-014：余额作为账本账户参与支付）
  balance_applied  app.minor_amount NOT NULL DEFAULT 0 CHECK (balance_applied >= 0),
  total_amount     app.minor_amount NOT NULL DEFAULT 0 CHECK (total_amount >= 0),
  -- 需要外部渠道实付的金额 = total - balance_applied
  payable_amount   app.minor_amount NOT NULL DEFAULT 0 CHECK (payable_amount >= 0),
  paid_amount      app.minor_amount NOT NULL DEFAULT 0 CHECK (paid_amount >= 0),
  refunded_amount  app.minor_amount NOT NULL DEFAULT 0 CHECK (refunded_amount >= 0),

  coupon_id        uuid,
  -- XBD-015 人工订单必须填原因并走审批
  manual_reason    text,
  approval_request_id uuid,
  created_by       uuid,

  -- 关联的订阅（续费/升级时指向既有订阅）
  subscription_id  uuid REFERENCES subscriptions(id) ON DELETE SET NULL,

  expires_at       timestamptz,
  paid_at          timestamptz,
  fulfilled_at     timestamptz,
  cancelled_at     timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT orders_tenant_no_unique UNIQUE (tenant_id, order_no),
  CONSTRAINT orders_user_fk
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT orders_org_fk
    FOREIGN KEY (tenant_id, organization_id) REFERENCES organizations (tenant_id, id) ON DELETE SET NULL,
  -- 金额恒等式：应付 = 总额 − 余额抵扣
  CONSTRAINT orders_payable_identity CHECK (payable_amount = total_amount - balance_applied),
  -- 总额恒等式：总额 = 小计 − 折扣 + 税
  CONSTRAINT orders_total_identity CHECK (total_amount = subtotal_amount - discount_amount + tax_amount),
  -- PAY-004：退款永不超过实收
  CONSTRAINT orders_refund_not_exceed_paid CHECK (refunded_amount <= paid_amount),
  -- XBD-015：人工订单必须有原因
  CONSTRAINT orders_manual_requires_reason
    CHECK (kind <> 'manual' OR (manual_reason IS NOT NULL AND length(manual_reason) >= 5))
);
CREATE UNIQUE INDEX orders_tenant_id_id_key ON orders (tenant_id, id);

CREATE INDEX idx_orders_user ON orders (tenant_id, user_id, created_at DESC);
CREATE INDEX idx_orders_status ON orders (tenant_id, status, created_at DESC);
-- 未支付订单过期扫描
CREATE INDEX idx_orders_expiring ON orders (expires_at)
  WHERE status IN ('draft', 'pending_payment');

CREATE TRIGGER trg_orders_updated_at BEFORE UPDATE ON orders
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('orders');
-- +goose StatementEnd

-- +goose StatementBegin
-- 订单行：下单瞬间把商品名、套餐版本、价格全部快照进来。
-- 之后 products/prices/plan_versions 怎么改，这张单据都不会变形（SUB-001 验收）。
CREATE TABLE order_items (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  order_id          uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,

  product_id        uuid,
  price_id          uuid,
  plan_id           uuid,
  plan_version_id   uuid,

  -- ---- 快照字段，永不回查 ----
  snapshot_product_name text NOT NULL,
  snapshot_plan_name    text,
  snapshot_plan_version int,
  snapshot_interval     text,
  snapshot_interval_count smallint,
  -- 权益与配额的完整快照，用于「为什么我买到的是这些」的可解释性
  snapshot_entitlements jsonb NOT NULL DEFAULT '[]'::jsonb,
  snapshot_quotas       jsonb NOT NULL DEFAULT '[]'::jsonb,

  quantity          int NOT NULL DEFAULT 1 CHECK (quantity > 0),
  unit_amount       app.minor_amount NOT NULL CHECK (unit_amount >= 0),
  line_amount       app.minor_amount NOT NULL CHECK (line_amount >= 0),
  currency          app.currency_code NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT order_items_product_fk
    FOREIGN KEY (tenant_id, product_id) REFERENCES products (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT order_items_price_fk
    FOREIGN KEY (tenant_id, price_id) REFERENCES prices (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT order_items_plan_version_fk
    FOREIGN KEY (tenant_id, plan_version_id) REFERENCES plan_versions (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT order_items_line_identity CHECK (line_amount = unit_amount * quantity)
);

CREATE INDEX idx_order_items_order ON order_items (order_id);

SELECT app.enable_tenant_rls('order_items');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 支付意图与支付（PAY-001 / PAY-002）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE payment_providers (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code          text NOT NULL,
  adapter       text NOT NULL,
  display_name  text NOT NULL,
  -- 渠道密钥信封加密后存放（SEC-010：数据库备份泄漏时仍不可直接读取）
  credentials_encrypted bytea,
  key_version   int NOT NULL DEFAULT 1,
  supported_currencies app.currency_code[] NOT NULL DEFAULT '{}',
  config        jsonb NOT NULL DEFAULT '{}'::jsonb,
  enabled       boolean NOT NULL DEFAULT false,
  -- PAY-009 支付降级：故障时置 false，停止创建新支付但不猜测既有支付结果
  accepting_new boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT payment_providers_tenant_code_unique UNIQUE (tenant_id, code)
);
CREATE UNIQUE INDEX payment_providers_tenant_id_id_key ON payment_providers (tenant_id, id);

CREATE TRIGGER trg_payment_providers_updated_at BEFORE UPDATE ON payment_providers
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('payment_providers');
-- +goose StatementEnd

-- +goose StatementBegin
-- 支付意图：一次「我打算用某渠道付这笔单」的尝试。
-- 一个订单可以有多个意图（换渠道、超时重试），但同时只有一个活跃意图。
CREATE TABLE payment_intents (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  order_id          uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  provider_id       uuid NOT NULL,
  currency          app.currency_code NOT NULL,
  amount            app.minor_amount NOT NULL CHECK (amount > 0),
  status            text NOT NULL DEFAULT 'created'
                      CHECK (status IN ('created', 'requires_action', 'processing',
                                        'succeeded', 'failed', 'cancelled', 'expired')),
  -- 渠道侧单号，用于 PAY-007 对账与 PAY-009 主动查询补偿
  provider_ref      text,
  -- 客户端跳转/二维码信息，含时效
  action_payload    jsonb,
  failure_code      text,
  failure_message   text,
  expires_at        timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT payment_intents_provider_fk
    FOREIGN KEY (tenant_id, provider_id) REFERENCES payment_providers (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT payment_intents_provider_ref_unique UNIQUE (tenant_id, provider_id, provider_ref)
);
CREATE UNIQUE INDEX payment_intents_tenant_id_id_key ON payment_intents (tenant_id, id);

-- 一个订单同时只允许一个未终结的支付意图，避免用户重复付款
CREATE UNIQUE INDEX uq_payment_intents_active_per_order
  ON payment_intents (order_id)
  WHERE status IN ('created', 'requires_action', 'processing');

CREATE TRIGGER trg_payment_intents_updated_at BEFORE UPDATE ON payment_intents
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('payment_intents');

COMMENT ON INDEX uq_payment_intents_active_per_order IS
  'PAY-001 验收「支付处理中不会误把订单标记为完成」的配套：同一订单不允许并存多个在途支付。';
-- +goose StatementEnd

-- +goose StatementBegin
-- 支付：真实收到钱的记录。只有渠道确认成功才会有行。
CREATE TABLE payments (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  order_id          uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  payment_intent_id uuid REFERENCES payment_intents(id) ON DELETE SET NULL,
  provider_id       uuid NOT NULL,
  provider_payment_id text NOT NULL,
  currency          app.currency_code NOT NULL,
  amount            app.minor_amount NOT NULL CHECK (amount > 0),
  -- 渠道手续费，进费用科目
  fee_amount        app.minor_amount NOT NULL DEFAULT 0 CHECK (fee_amount >= 0),
  refunded_amount   app.minor_amount NOT NULL DEFAULT 0 CHECK (refunded_amount >= 0),
  status            text NOT NULL DEFAULT 'succeeded'
                      CHECK (status IN ('succeeded', 'refunded', 'partially_refunded', 'disputed', 'reversed')),
  method            text,
  paid_at           timestamptz NOT NULL DEFAULT now(),
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT payments_provider_fk
    FOREIGN KEY (tenant_id, provider_id) REFERENCES payment_providers (tenant_id, id) ON DELETE RESTRICT,
  -- 渠道支付单号全局唯一：同一笔钱不可能被记两次
  CONSTRAINT payments_provider_payment_unique UNIQUE (provider_id, provider_payment_id),
  CONSTRAINT payments_refund_not_exceed CHECK (refunded_amount <= amount)
);
CREATE UNIQUE INDEX payments_tenant_id_id_key ON payments (tenant_id, id);
CREATE INDEX idx_payments_order ON payments (order_id);
CREATE INDEX idx_payments_paid_at ON payments (tenant_id, paid_at DESC);

CREATE TRIGGER trg_payments_updated_at BEFORE UPDATE ON payments
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('payments');
-- +goose StatementEnd

-- +goose StatementBegin
-- PAY-003 幂等回调的核心表：渠道推来的每一个事件先在这里落唯一键。
-- 追加写 —— 原始回调报文是对账与纠纷时的证据，任何人不得篡改。
CREATE TABLE payment_events (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider_id        uuid NOT NULL,
  -- 渠道事件 ID：唯一约束在此，重复回调撞库即返回，不进业务逻辑
  provider_event_id  text NOT NULL,
  event_type         text NOT NULL,
  provider_payment_id text,
  -- 原始报文与签名头，原样保留
  raw_payload        jsonb NOT NULL,
  raw_headers        jsonb NOT NULL DEFAULT '{}'::jsonb,
  signature_verified boolean NOT NULL DEFAULT false,
  -- 处理结果；处理失败可重放但不会重复产生业务结果
  processing_status  text NOT NULL DEFAULT 'pending'
                       CHECK (processing_status IN ('pending', 'processed', 'ignored', 'failed')),
  processing_error   text,
  processed_at       timestamptz,
  received_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT payment_events_provider_event_unique UNIQUE (provider_id, provider_event_id),
  CONSTRAINT payment_events_provider_fk
    FOREIGN KEY (tenant_id, provider_id) REFERENCES payment_providers (tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX idx_payment_events_unprocessed ON payment_events (received_at)
  WHERE processing_status IN ('pending', 'failed');

SELECT app.enable_tenant_rls('payment_events');

COMMENT ON TABLE payment_events IS
  'PAY-003 验收「同一回调重复 100 次只产生一次业务结果」：唯一约束 (provider_id, provider_event_id) 是唯一防线。';
-- +goose StatementEnd

-- payment_events 的处理状态需要可更新，故只禁 DELETE 而非全禁。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.deny_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '表 %.% 的记录为证据链的一部分，不允许删除',
    TG_TABLE_SCHEMA, TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END $$;

CREATE TRIGGER trg_payment_events_no_delete BEFORE DELETE ON payment_events
  FOR EACH STATEMENT EXECUTE FUNCTION app.deny_delete();
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 退款（PAY-004）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE refunds (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  order_id          uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  payment_id        uuid NOT NULL REFERENCES payments(id) ON DELETE RESTRICT,
  provider_refund_id text,
  currency          app.currency_code NOT NULL,
  amount            app.minor_amount NOT NULL CHECK (amount > 0),
  reason            text NOT NULL,
  status            text NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'approved', 'processing',
                                        'succeeded', 'failed', 'rejected')),
  -- PAY-010 / SEC-013：超额或异常退款需双人审批，申请人不能审批自己
  requested_by      uuid REFERENCES users(id) ON DELETE SET NULL,
  approval_request_id uuid,
  -- 权益回收结果：退款成功后必须回收对应权益并冲销佣金
  entitlement_revoked boolean NOT NULL DEFAULT false,
  commission_reversed boolean NOT NULL DEFAULT false,
  failure_message   text,
  succeeded_at      timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT refunds_provider_refund_unique UNIQUE (tenant_id, provider_refund_id)
);
CREATE UNIQUE INDEX refunds_tenant_id_id_key ON refunds (tenant_id, id);
CREATE INDEX idx_refunds_payment ON refunds (payment_id);
CREATE INDEX idx_refunds_pending ON refunds (tenant_id, status)
  WHERE status IN ('pending', 'approved', 'processing');

CREATE TRIGGER trg_refunds_updated_at BEFORE UPDATE ON refunds
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('refunds');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 复式账本（PAY-005）—— 本项目的财务真相来源
--------------------------------------------------------------------------------

-- +goose StatementBegin
-- 会计科目。用户余额、佣金、渠道资金、平台收入各自成账户。
-- 「余额」不是 users 表的一个字段，而是这里一串不可变分录的和。
CREATE TABLE ledger_accounts (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 科目编码，如 user_balance / user_commission_pending / channel_cash / platform_revenue
  account_type   text NOT NULL
                   CHECK (account_type IN ('user_balance', 'user_commission_pending',
                                           'user_commission_available', 'channel_cash',
                                           'platform_revenue', 'platform_fee_expense',
                                           'refund_payable', 'dispute_hold', 'suspense')),
  -- 科目性质决定余额方向：asset/expense 借增，liability/equity/revenue 贷增
  normal_balance text NOT NULL CHECK (normal_balance IN ('debit', 'credit')),
  currency       app.currency_code NOT NULL,
  -- 归属主体：用户余额账户挂 user_id，渠道账户挂 provider_id，平台账户为 NULL
  owner_user_id  uuid,
  owner_ref      text,
  -- 缓存余额（有符号，借方为正）。真值仍是 ledger_entries 之和，
  -- 由触发器原子维护，并有 app.verify_ledger_account() 可随时校验一致性。
  balance_signed app.minor_amount NOT NULL DEFAULT 0,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'frozen', 'closed')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT ledger_accounts_owner_fk
    FOREIGN KEY (tenant_id, owner_user_id) REFERENCES users (tenant_id, id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX ledger_accounts_tenant_id_id_key ON ledger_accounts (tenant_id, id);

-- 每个用户每种币种每类账户唯一
CREATE UNIQUE INDEX uq_ledger_accounts_user
  ON ledger_accounts (tenant_id, account_type, owner_user_id, currency)
  WHERE owner_user_id IS NOT NULL;
CREATE UNIQUE INDEX uq_ledger_accounts_platform
  ON ledger_accounts (tenant_id, account_type, coalesce(owner_ref, ''), currency)
  WHERE owner_user_id IS NULL;

CREATE TRIGGER trg_ledger_accounts_updated_at BEFORE UPDATE ON ledger_accounts
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('ledger_accounts');
-- +goose StatementEnd

-- +goose StatementBegin
-- 账务交易：一组必须配平的分录。
CREATE TABLE ledger_transactions (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 业务语义：order_paid / refund_issued / balance_topup / commission_accrued /
  --          commission_settled / commission_reversed / manual_adjustment / dispute_hold
  kind           text NOT NULL,
  currency       app.currency_code NOT NULL,
  -- 溯源：这笔账是哪个业务动作产生的
  source_type    text,
  source_id      uuid,
  -- 人工调整必须有原因和审批（PAY-010）
  memo           text,
  actor_kind     text NOT NULL DEFAULT 'system'
                   CHECK (actor_kind IN ('system', 'user', 'admin', 'reconciliation')),
  actor_id       uuid,
  approval_request_id uuid,
  occurred_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT ledger_transactions_manual_needs_memo
    CHECK (kind <> 'manual_adjustment' OR (memo IS NOT NULL AND length(memo) >= 5))
);
CREATE UNIQUE INDEX ledger_transactions_tenant_id_id_key ON ledger_transactions (tenant_id, id);
CREATE INDEX idx_ledger_transactions_source ON ledger_transactions (tenant_id, source_type, source_id);
CREATE INDEX idx_ledger_transactions_time ON ledger_transactions (tenant_id, occurred_at DESC);

SELECT app.enable_tenant_rls('ledger_transactions');
SELECT app.make_append_only('ledger_transactions');
-- +goose StatementEnd

-- +goose StatementBegin
-- 分录：账本的原子单位，永久不可变。
CREATE TABLE ledger_entries (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  transaction_id uuid NOT NULL REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
  account_id     uuid NOT NULL,
  direction      text NOT NULL CHECK (direction IN ('debit', 'credit')),
  -- 金额恒为正，方向由 direction 表达
  amount         app.minor_amount NOT NULL CHECK (amount > 0),
  currency       app.currency_code NOT NULL,
  -- 有符号金额：借为正、贷为负。一笔交易所有分录之和必须为 0。
  signed_amount  app.minor_amount GENERATED ALWAYS AS
                   (CASE WHEN direction = 'debit' THEN amount ELSE -amount END) STORED,
  description    text,
  created_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT ledger_entries_account_fk
    FOREIGN KEY (tenant_id, account_id) REFERENCES ledger_accounts (tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX idx_ledger_entries_txn ON ledger_entries (transaction_id);
CREATE INDEX idx_ledger_entries_account ON ledger_entries (account_id, created_at DESC);

SELECT app.enable_tenant_rls('ledger_entries');
SELECT app.make_append_only('ledger_entries');

COMMENT ON TABLE ledger_entries IS
  'PAY-005 验收「任意余额可追溯到不可变分录」：本表只增不改不删，是全平台资金的唯一真相。';
-- +goose StatementEnd

-- +goose StatementBegin
-- 复式记账配平校验。
-- 用 DEFERRABLE INITIALLY DEFERRED 的约束触发器：允许事务中间态不平，
-- 但事务提交那一刻必须平 —— 否则整个事务回滚。
CREATE OR REPLACE FUNCTION app.assert_ledger_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_imbalance bigint;
  v_currencies int;
BEGIN
  SELECT coalesce(sum(signed_amount), 0), count(DISTINCT currency)
    INTO v_imbalance, v_currencies
  FROM ledger_entries
  WHERE transaction_id = NEW.transaction_id;

  IF v_currencies > 1 THEN
    RAISE EXCEPTION
      '账务交易 % 混用了 % 种币种（PAY-005）；跨币种必须通过兑换科目分两笔交易记账',
      NEW.transaction_id, v_currencies
      USING ERRCODE = 'check_violation';
  END IF;

  IF v_imbalance <> 0 THEN
    RAISE EXCEPTION
      '账务交易 % 借贷不平，差额 %（PAY-005 复式记账）',
      NEW.transaction_id, v_imbalance
      USING ERRCODE = 'check_violation';
  END IF;

  RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER trg_ledger_entries_balanced
  AFTER INSERT ON ledger_entries
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION app.assert_ledger_balanced();
-- +goose StatementEnd

-- +goose StatementBegin
-- 分录落库即原子更新账户缓存余额。
-- 因为分录不可变，缓存余额只会被 INSERT 推进，不存在漂移路径。
CREATE OR REPLACE FUNCTION app.apply_ledger_entry_to_account() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE ledger_accounts
     SET balance_signed = balance_signed + NEW.signed_amount
   WHERE id = NEW.account_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION '账户 % 不存在', NEW.account_id USING ERRCODE = 'foreign_key_violation';
  END IF;

  RETURN NULL;
END $$;

CREATE TRIGGER trg_ledger_entries_apply
  AFTER INSERT ON ledger_entries
  FOR EACH ROW EXECUTE FUNCTION app.apply_ledger_entry_to_account();
-- +goose StatementEnd

-- +goose StatementBegin
-- 一致性校验：缓存余额 vs 分录求和。对账任务与恢复演练后调用。
CREATE OR REPLACE FUNCTION app.verify_ledger_account(p_account_id uuid)
RETURNS TABLE (account_id uuid, cached bigint, computed bigint, drift bigint)
LANGUAGE sql STABLE AS $$
  SELECT a.id,
         a.balance_signed::bigint,
         coalesce(sum(e.signed_amount), 0)::bigint,
         a.balance_signed::bigint - coalesce(sum(e.signed_amount), 0)::bigint
  FROM ledger_accounts a
  LEFT JOIN ledger_entries e ON e.account_id = a.id
  WHERE a.id = p_account_id
  GROUP BY a.id, a.balance_signed
$$;

-- 全账户漂移扫描，供 NFR-003 恢复后一致性校验使用
CREATE OR REPLACE FUNCTION app.verify_ledger_all()
RETURNS TABLE (account_id uuid, cached bigint, computed bigint, drift bigint)
LANGUAGE sql STABLE AS $$
  SELECT a.id,
         a.balance_signed::bigint,
         coalesce(sum(e.signed_amount), 0)::bigint,
         a.balance_signed::bigint - coalesce(sum(e.signed_amount), 0)::bigint
  FROM ledger_accounts a
  LEFT JOIN ledger_entries e ON e.account_id = a.id
  GROUP BY a.id, a.balance_signed
  HAVING a.balance_signed::bigint <> coalesce(sum(e.signed_amount), 0)::bigint
$$;
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 发票（PAY-006）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE invoices (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  order_id       uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  invoice_no     text NOT NULL,
  -- PAY-006 验收「历史发票不随用户资料变化」：开票主体全部快照
  snapshot_buyer jsonb NOT NULL,
  snapshot_seller jsonb NOT NULL,
  snapshot_lines jsonb NOT NULL,
  currency       app.currency_code NOT NULL,
  subtotal_amount app.minor_amount NOT NULL,
  tax_amount     app.minor_amount NOT NULL DEFAULT 0,
  total_amount   app.minor_amount NOT NULL,
  tax_rate_bp    int NOT NULL DEFAULT 0,   -- 基点，2500 = 25%
  status         text NOT NULL DEFAULT 'issued'
                   CHECK (status IN ('draft', 'issued', 'void', 'credited')),
  issued_at      timestamptz NOT NULL DEFAULT now(),
  created_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT invoices_tenant_no_unique UNIQUE (tenant_id, invoice_no)
);

SELECT app.enable_tenant_rls('invoices');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 对账（PAY-007）与争议（PAY-008）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE reconciliation_runs (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider_id     uuid NOT NULL,
  -- 对账周期（渠道账单日）
  period_start    date NOT NULL,
  period_end      date NOT NULL,
  status          text NOT NULL DEFAULT 'running'
                    CHECK (status IN ('running', 'completed', 'failed')),
  channel_count   int NOT NULL DEFAULT 0,
  internal_count  int NOT NULL DEFAULT 0,
  matched_count   int NOT NULL DEFAULT 0,
  -- 四类差异（PAY-007 验收）
  missing_internal_count int NOT NULL DEFAULT 0,  -- 渠道有、我方无
  missing_channel_count  int NOT NULL DEFAULT 0,  -- 我方有、渠道无
  amount_mismatch_count  int NOT NULL DEFAULT 0,
  status_mismatch_count  int NOT NULL DEFAULT 0,
  started_at      timestamptz NOT NULL DEFAULT now(),
  completed_at    timestamptz,

  CONSTRAINT reconciliation_runs_period_unique UNIQUE (tenant_id, provider_id, period_start, period_end),
  CONSTRAINT reconciliation_runs_period_order CHECK (period_end >= period_start)
);

SELECT app.enable_tenant_rls('reconciliation_runs');

CREATE TABLE reconciliation_discrepancies (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  run_id          uuid NOT NULL REFERENCES reconciliation_runs(id) ON DELETE CASCADE,
  kind            text NOT NULL
                    CHECK (kind IN ('missing_internal', 'missing_channel',
                                    'amount_mismatch', 'status_mismatch', 'duplicate')),
  provider_payment_id text,
  payment_id      uuid REFERENCES payments(id) ON DELETE SET NULL,
  channel_amount  app.minor_amount,
  internal_amount app.minor_amount,
  currency        app.currency_code,
  detail          jsonb NOT NULL DEFAULT '{}'::jsonb,
  resolution      text NOT NULL DEFAULT 'open'
                    CHECK (resolution IN ('open', 'investigating', 'resolved', 'written_off')),
  resolved_by     uuid REFERENCES users(id) ON DELETE SET NULL,
  resolution_note text,
  resolved_at     timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_recon_discrepancies_open ON reconciliation_discrepancies (tenant_id, resolution)
  WHERE resolution IN ('open', 'investigating');

SELECT app.enable_tenant_rls('reconciliation_discrepancies');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE disputes (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  payment_id      uuid NOT NULL REFERENCES payments(id) ON DELETE RESTRICT,
  provider_dispute_id text,
  reason          text,
  currency        app.currency_code NOT NULL,
  amount          app.minor_amount NOT NULL CHECK (amount > 0),
  status          text NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open', 'evidence_submitted', 'won', 'lost', 'withdrawn')),
  -- PAY-008：争议期间冻结相关佣金与权益处置
  commission_frozen boolean NOT NULL DEFAULT false,
  entitlement_action text NOT NULL DEFAULT 'none'
                    CHECK (entitlement_action IN ('none', 'suspended', 'revoked')),
  evidence        jsonb NOT NULL DEFAULT '{}'::jsonb,
  conclusion      text,
  opened_at       timestamptz NOT NULL DEFAULT now(),
  closed_at       timestamptz,

  CONSTRAINT disputes_provider_unique UNIQUE (tenant_id, provider_dispute_id)
);

SELECT app.enable_tenant_rls('disputes');
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS disputes;
DROP TABLE IF EXISTS reconciliation_discrepancies;
DROP TABLE IF EXISTS reconciliation_runs;
DROP TABLE IF EXISTS invoices;
DROP FUNCTION IF EXISTS app.verify_ledger_all();
DROP FUNCTION IF EXISTS app.verify_ledger_account(uuid);
DROP TABLE IF EXISTS ledger_entries;
DROP FUNCTION IF EXISTS app.apply_ledger_entry_to_account();
DROP FUNCTION IF EXISTS app.assert_ledger_balanced();
DROP TABLE IF EXISTS ledger_transactions;
DROP TABLE IF EXISTS ledger_accounts;
DROP TABLE IF EXISTS refunds;
DROP TRIGGER IF EXISTS trg_payment_events_no_delete ON payment_events;
DROP TABLE IF EXISTS payment_events;
DROP FUNCTION IF EXISTS app.deny_delete();
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS payment_intents;
DROP TABLE IF EXISTS payment_providers;
DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;
-- +goose StatementEnd
