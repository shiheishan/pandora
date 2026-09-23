-- 运营域：coupons / referrals / commission_entries / settlements / tickets /
--          announcements / notification_deliveries
--
-- 对应 PRD 第 12 章 OPS-001..006、MKT-001..005，以及 XBD-017/019/020/022。
--
-- 关键不变量：
--   · MKT-001「并发兑换不超发」→ 计数器唯一约束 + 行锁，不靠应用判重。
--   · MKT-002「数据库泄漏不能直接兑换未使用礼品码」→ 只存哈希。
--   · MKT-003「用户不能反复更换邀请上级」→ 邀请关系一次写入即不可变。
--   · MKT-004「结算过程不直接修改佣金余额」→ 佣金落 ledger_entries，本表只是业务视图。

-- +goose Up

--------------------------------------------------------------------------------
-- 优惠券（MKT-001）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE coupons (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code            citext NOT NULL,
  name            text NOT NULL,
  discount_type   text NOT NULL CHECK (discount_type IN ('fixed', 'percent')),
  -- fixed 用最小单位金额，percent 用基点（1000 = 10%）
  discount_value  bigint NOT NULL CHECK (discount_value > 0),
  currency        app.currency_code,
  max_discount    app.minor_amount,
  min_order_amount app.minor_amount NOT NULL DEFAULT 0 CHECK (min_order_amount >= 0),
  -- 适用范围
  applicable_product_ids uuid[] NOT NULL DEFAULT '{}',
  applicable_plan_ids    uuid[] NOT NULL DEFAULT '{}',
  applicable_user_group_ids uuid[] NOT NULL DEFAULT '{}',
  applicable_order_kinds text[] NOT NULL DEFAULT '{}',
  -- 次数限制
  max_redemptions        int CHECK (max_redemptions IS NULL OR max_redemptions > 0),
  max_redemptions_per_user int NOT NULL DEFAULT 1 CHECK (max_redemptions_per_user > 0),
  redeemed_count         int NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
  -- 叠加规则
  stackable       boolean NOT NULL DEFAULT false,
  valid_from      timestamptz,
  valid_until     timestamptz,
  status          text NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'paused', 'expired', 'exhausted')),
  created_by      uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT coupons_tenant_code_unique UNIQUE (tenant_id, code),
  -- MKT-001「并发兑换不超发」的数据库级保证
  CONSTRAINT coupons_not_oversold
    CHECK (max_redemptions IS NULL OR redeemed_count <= max_redemptions),
  CONSTRAINT coupons_percent_bounds
    CHECK (discount_type <> 'percent' OR discount_value <= 10000),
  CONSTRAINT coupons_fixed_needs_currency
    CHECK (discount_type <> 'fixed' OR currency IS NOT NULL)
);
CREATE UNIQUE INDEX coupons_tenant_id_id_key ON coupons (tenant_id, id);

CREATE TRIGGER trg_coupons_updated_at BEFORE UPDATE ON coupons
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('coupons');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE coupon_redemptions (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  coupon_id     uuid NOT NULL REFERENCES coupons(id) ON DELETE CASCADE,
  user_id       uuid NOT NULL,
  order_id      uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  discount_amount app.minor_amount NOT NULL CHECK (discount_amount >= 0),
  currency      app.currency_code NOT NULL,
  -- MKT-001「退款按规则返还」：退款时置 reverted，兑换次数回滚
  reverted_at   timestamptz,
  revert_reason text,
  redeemed_at   timestamptz NOT NULL DEFAULT now(),

  -- 一个订单对同一张券只能用一次
  CONSTRAINT coupon_redemptions_order_unique UNIQUE (coupon_id, order_id)
);

CREATE INDEX idx_coupon_redemptions_user ON coupon_redemptions (coupon_id, user_id)
  WHERE reverted_at IS NULL;

SELECT app.enable_tenant_rls('coupon_redemptions');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 礼品码（MKT-002）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE gift_codes (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  batch_id      uuid,
  -- 高熵随机码，服务端只保存哈希（MKT-002 验收）
  code_hash     bytea NOT NULL,
  code_prefix   text NOT NULL,
  -- 兑换后授予什么：余额充值、套餐、附加包、流量包
  grant_kind    text NOT NULL
                  CHECK (grant_kind IN ('balance', 'plan', 'addon', 'quota')),
  grant_ref_id  uuid,
  grant_amount  bigint CHECK (grant_amount IS NULL OR grant_amount > 0),
  grant_currency app.currency_code,
  status        text NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'redeemed', 'revoked', 'expired')),
  redeemed_by   uuid,
  redeemed_at   timestamptz,
  order_id      uuid REFERENCES orders(id) ON DELETE SET NULL,
  expires_at    timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT gift_codes_hash_unique UNIQUE (code_hash)
);

CREATE INDEX idx_gift_codes_batch ON gift_codes (tenant_id, batch_id) WHERE batch_id IS NOT NULL;

SELECT app.enable_tenant_rls('gift_codes');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 邀请与佣金（MKT-003 / MKT-004 / MKT-005 / XBD-017 / XBD-018）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE invite_codes (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code          citext NOT NULL,
  owner_user_id uuid,
  -- XBD-017：一次性或限次
  max_uses      int CHECK (max_uses IS NULL OR max_uses > 0),
  used_count    int NOT NULL DEFAULT 0 CHECK (used_count >= 0),
  -- 渠道归因参数由服务端签发，不可由前端伪造（XBD-017 验收）
  channel       text,
  campaign      text,
  attribution_signature bytea,
  expires_at    timestamptz,
  status        text NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'exhausted', 'expired', 'disabled')),
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT invite_codes_tenant_code_unique UNIQUE (tenant_id, code),
  CONSTRAINT invite_codes_not_oversold CHECK (max_uses IS NULL OR used_count <= max_uses)
);

SELECT app.enable_tenant_rls('invite_codes');
-- +goose StatementEnd

-- +goose StatementBegin
-- MKT-003：邀请关系首次固化后不可变更，防自邀与循环。
CREATE TABLE referrals (
  -- 被邀请人为主键 —— 一个用户只能有一个上级，天然杜绝「反复更换邀请上级」
  referee_user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  referrer_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  invite_code_id  uuid REFERENCES invite_codes(id) ON DELETE SET NULL,
  channel         text,
  campaign        text,
  -- MKT-005 反作弊：注册时的关联信号，供风控判定
  signals         jsonb NOT NULL DEFAULT '{}'::jsonb,
  risk_flag       text NOT NULL DEFAULT 'none'
                    CHECK (risk_flag IN ('none', 'suspicious', 'confirmed_fraud')),
  bound_at        timestamptz NOT NULL DEFAULT now(),

  -- 防自邀
  CONSTRAINT referrals_no_self CHECK (referee_user_id <> referrer_user_id)
);

CREATE INDEX idx_referrals_referrer ON referrals (tenant_id, referrer_user_id);

SELECT app.enable_tenant_rls('referrals');
SELECT app.make_append_only('referrals');

COMMENT ON TABLE referrals IS
  'MKT-003 验收「用户不能反复更换邀请上级」：以 referee 为主键 + 追加写，改都改不了。';
-- +goose StatementEnd

-- +goose StatementBegin
-- 佣金条目：业务视图。真正的资金变动在 ledger_entries，此表只描述「为什么有这笔佣金」。
CREATE TABLE commission_entries (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  referrer_user_id uuid NOT NULL,
  referee_user_id uuid NOT NULL,
  order_id        uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  payment_id      uuid REFERENCES payments(id) ON DELETE SET NULL,
  currency        app.currency_code NOT NULL,
  base_amount     app.minor_amount NOT NULL CHECK (base_amount >= 0),
  rate_bp         int NOT NULL CHECK (rate_bp >= 0),
  commission_amount app.minor_amount NOT NULL CHECK (commission_amount >= 0),
  status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'frozen', 'available', 'settled', 'reversed', 'rejected')),
  -- MKT-004 冻结期：期满且订单未退款才转为可用
  frozen_until    timestamptz,
  -- MKT-005：高风险佣金进入审核而非自动结算
  review_required boolean NOT NULL DEFAULT false,
  review_reason   text,
  -- 与账本的双向索引
  accrual_txn_id  uuid REFERENCES ledger_transactions(id) ON DELETE SET NULL,
  settle_txn_id   uuid REFERENCES ledger_transactions(id) ON DELETE SET NULL,
  reversal_txn_id uuid REFERENCES ledger_transactions(id) ON DELETE SET NULL,
  reversed_reason text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  -- 一个订单对同一推荐人只产生一条佣金
  CONSTRAINT commission_entries_order_referrer_unique UNIQUE (order_id, referrer_user_id)
);

CREATE INDEX idx_commission_entries_referrer ON commission_entries (tenant_id, referrer_user_id, status);
CREATE INDEX idx_commission_entries_unfreeze ON commission_entries (frozen_until)
  WHERE status = 'frozen';

CREATE TRIGGER trg_commission_entries_updated_at BEFORE UPDATE ON commission_entries
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('commission_entries');

COMMENT ON TABLE commission_entries IS
  'MKT-004 验收「退款、拒付自动冲销相关佣金」：reversal_txn_id 指向冲销分录，资金动作全在账本里。';
-- +goose StatementEnd

-- +goose StatementBegin
-- XBD-018 佣金提现
CREATE TABLE withdrawals (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id         uuid NOT NULL,
  currency        app.currency_code NOT NULL,
  amount          app.minor_amount NOT NULL CHECK (amount > 0),
  -- 收款信息为高敏数据，信封加密（SEC-010）
  payout_detail_encrypted bytea,
  key_version     int NOT NULL DEFAULT 1,
  status          text NOT NULL DEFAULT 'requested'
                    CHECK (status IN ('requested', 'reviewing', 'approved', 'rejected',
                                      'processing', 'paid', 'failed', 'returned')),
  reject_reason   text,
  approval_request_id uuid,
  -- 付款凭证
  payout_reference text,
  proof_object_key text,
  -- 失败退回：钱回到可用佣金账户，同样走账本
  hold_txn_id     uuid REFERENCES ledger_transactions(id) ON DELETE SET NULL,
  payout_txn_id   uuid REFERENCES ledger_transactions(id) ON DELETE SET NULL,
  return_txn_id   uuid REFERENCES ledger_transactions(id) ON DELETE SET NULL,
  requested_at    timestamptz NOT NULL DEFAULT now(),
  completed_at    timestamptz,
  updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_withdrawals_user ON withdrawals (tenant_id, user_id, requested_at DESC);
CREATE INDEX idx_withdrawals_pending ON withdrawals (tenant_id, status)
  WHERE status IN ('requested', 'reviewing', 'approved', 'processing');

CREATE TRIGGER trg_withdrawals_updated_at BEFORE UPDATE ON withdrawals
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('withdrawals');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 工单（OPS-001 / OPS-002）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE tickets (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  ticket_no       text NOT NULL,
  user_id         uuid NOT NULL,
  organization_id uuid,
  subject         text NOT NULL,
  category        text NOT NULL DEFAULT 'general',
  priority        text NOT NULL DEFAULT 'normal'
                    CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  status          text NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open', 'pending_user', 'pending_agent',
                                      'escalated', 'resolved', 'closed')),
  assigned_to     uuid REFERENCES users(id) ON DELETE SET NULL,
  -- OPS-001 SLA：首次响应与解决的截止时间，超时自动升级
  sla_first_response_due timestamptz,
  sla_resolution_due     timestamptz,
  first_responded_at     timestamptz,
  escalated_at    timestamptz,
  -- 关联对象，方便客服在不越权的前提下定位问题
  related_order_id uuid REFERENCES orders(id) ON DELETE SET NULL,
  related_subscription_id uuid REFERENCES subscriptions(id) ON DELETE SET NULL,
  related_node_id  uuid REFERENCES nodes(id) ON DELETE SET NULL,
  resolved_at     timestamptz,
  closed_at       timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT tickets_tenant_no_unique UNIQUE (tenant_id, ticket_no)
);

CREATE INDEX idx_tickets_user ON tickets (tenant_id, user_id, created_at DESC);
CREATE INDEX idx_tickets_queue ON tickets (tenant_id, status, priority, created_at)
  WHERE status IN ('open', 'pending_agent', 'escalated');
-- SLA 超时扫描
CREATE INDEX idx_tickets_sla ON tickets (sla_first_response_due)
  WHERE first_responded_at IS NULL AND status NOT IN ('resolved', 'closed');

CREATE TRIGGER trg_tickets_updated_at BEFORE UPDATE ON tickets
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('tickets');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE ticket_messages (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  ticket_id     uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  author_id     uuid REFERENCES users(id) ON DELETE SET NULL,
  author_kind   text NOT NULL CHECK (author_kind IN ('user', 'agent', 'system')),
  body          text NOT NULL,
  -- OPS-001 验收「用户不可见内部备注」：查询层按此位过滤，用户 API 永不返回 true 的行
  internal_note boolean NOT NULL DEFAULT false,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT ticket_messages_internal_only_from_staff
    CHECK (NOT internal_note OR author_kind IN ('agent', 'system'))
);

CREATE INDEX idx_ticket_messages_ticket ON ticket_messages (ticket_id, created_at);

SELECT app.enable_tenant_rls('ticket_messages');
-- +goose StatementEnd

-- +goose StatementBegin
-- OPS-002 附件安全
CREATE TABLE ticket_attachments (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  ticket_id     uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  message_id    uuid REFERENCES ticket_messages(id) ON DELETE CASCADE,
  -- 文件只落对象存储，应用服务器永不执行它（OPS-002 验收）
  object_key    text NOT NULL,
  filename      text NOT NULL,
  size_bytes    bigint NOT NULL CHECK (size_bytes > 0),
  -- 声明的与嗅探出的内容类型都记录，不一致本身就是风险信号
  declared_mime text,
  detected_mime text,
  sha256        bytea NOT NULL,
  scan_status   text NOT NULL DEFAULT 'pending'
                  CHECK (scan_status IN ('pending', 'clean', 'infected', 'failed', 'quarantined')),
  scan_result   jsonb,
  uploaded_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT ticket_attachments_object_unique UNIQUE (tenant_id, object_key)
);

CREATE INDEX idx_ticket_attachments_scan ON ticket_attachments (scan_status)
  WHERE scan_status = 'pending';

SELECT app.enable_tenant_rls('ticket_attachments');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 公告与通知（OPS-003 / OPS-004 / OPS-005 / XBD-019）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE announcements (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 多语言正文：{"zh-CN": {...}, "en": {...}}
  content       jsonb NOT NULL,
  severity      text NOT NULL DEFAULT 'info'
                  CHECK (severity IN ('info', 'notice', 'warning', 'critical')),
  -- OPS-003 分群：为空表示全体
  target_user_group_ids uuid[] NOT NULL DEFAULT '{}',
  target_plan_ids       uuid[] NOT NULL DEFAULT '{}',
  pinned        boolean NOT NULL DEFAULT false,
  version       int NOT NULL DEFAULT 1,
  status        text NOT NULL DEFAULT 'draft'
                  CHECK (status IN ('draft', 'scheduled', 'published', 'withdrawn')),
  publish_at    timestamptz,
  expires_at    timestamptz,
  published_at  timestamptz,
  -- OPS-003 验收「撤回后仍保留审计」：不删行，只置状态
  withdrawn_at  timestamptz,
  withdrawn_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_announcements_live ON announcements (tenant_id, publish_at DESC)
  WHERE status = 'published';

CREATE TRIGGER trg_announcements_updated_at BEFORE UPDATE ON announcements
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('announcements');
-- +goose StatementEnd

-- +goose StatementBegin
-- XBD-019 消息模板：版本化、多语言、变量白名单
CREATE TABLE notification_templates (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code          text NOT NULL,
  channel       text NOT NULL CHECK (channel IN ('email', 'sms', 'inapp', 'telegram', 'webhook', 'push')),
  locale        text NOT NULL DEFAULT 'zh-CN',
  version       int NOT NULL DEFAULT 1,
  subject       text,
  body          text NOT NULL,
  -- 变量白名单：渲染时超出白名单的占位符一律报错，杜绝模板注入取数
  allowed_variables text[] NOT NULL DEFAULT '{}',
  -- OPS-005 通知分类：事务类不可退订，营销类可退订
  category      text NOT NULL DEFAULT 'transactional'
                  CHECK (category IN ('transactional', 'service', 'marketing')),
  status        text NOT NULL DEFAULT 'draft'
                  CHECK (status IN ('draft', 'active', 'archived')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT notification_templates_unique UNIQUE (tenant_id, code, channel, locale, version)
);

CREATE TRIGGER trg_notification_templates_updated_at BEFORE UPDATE ON notification_templates
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('notification_templates');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE notification_preferences (
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  category    text NOT NULL CHECK (category IN ('transactional', 'service', 'marketing')),
  channel     text NOT NULL,
  enabled     boolean NOT NULL DEFAULT true,
  updated_at  timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (user_id, category, channel),
  -- OPS-005 验收「不影响安全通知」：事务类不允许关闭
  CONSTRAINT notification_preferences_transactional_always_on
    CHECK (category <> 'transactional' OR enabled)
);

SELECT app.enable_tenant_rls('notification_preferences');
-- +goose StatementEnd

-- +goose StatementBegin
-- OPS-004 通知中心：异步发送、重试、限频、去重
CREATE TABLE notification_deliveries (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
  template_code text NOT NULL,
  channel       text NOT NULL,
  -- 去重键：同一事件对同一用户同一渠道只发一次
  dedupe_key    text NOT NULL,
  recipient_hash bytea,
  payload       jsonb NOT NULL DEFAULT '{}'::jsonb,
  status        text NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued', 'sending', 'sent', 'failed', 'suppressed', 'bounced')),
  attempts      smallint NOT NULL DEFAULT 0,
  max_attempts  smallint NOT NULL DEFAULT 5,
  next_retry_at timestamptz,
  provider_message_id text,
  error_message text,
  sent_at       timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT notification_deliveries_dedupe_unique UNIQUE (tenant_id, dedupe_key)
);

CREATE INDEX idx_notification_deliveries_queue ON notification_deliveries (next_retry_at)
  WHERE status IN ('queued', 'failed');

SELECT app.enable_tenant_rls('notification_deliveries');

COMMENT ON CONSTRAINT notification_deliveries_dedupe_unique ON notification_deliveries IS
  'OPS-004「去重」与 USE-006「同一阈值不重复轰炸」共用的落点。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 知识库与自定义页面（OPS-006 / XBD-022）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE content_pages (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  slug          citext NOT NULL,
  kind          text NOT NULL DEFAULT 'page'
                  CHECK (kind IN ('page', 'kb_article', 'tutorial', 'legal')),
  category      text,
  version       int NOT NULL DEFAULT 1,
  -- 多语言标题与正文
  content       jsonb NOT NULL,
  -- XBD-022 验收「富文本经过清洗」：记录清洗器版本，便于策略升级后重扫历史内容
  sanitizer_version text NOT NULL DEFAULT 'v1',
  -- XBD-028 内容匹配：按系统、客户端版本、语言、套餐展示
  target_platforms text[] NOT NULL DEFAULT '{}',
  min_client_version text,
  max_client_version text,
  target_plan_ids  uuid[] NOT NULL DEFAULT '{}',
  visibility    text NOT NULL DEFAULT 'public'
                  CHECK (visibility IN ('public', 'authenticated', 'group', 'internal')),
  status        text NOT NULL DEFAULT 'draft'
                  CHECK (status IN ('draft', 'published', 'archived', 'needs_review')),
  -- OPS-006「过期文章可标记并进入复审」
  review_due_at timestamptz,
  published_at  timestamptz,
  created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT content_pages_slug_version_unique UNIQUE (tenant_id, slug, version)
);

CREATE INDEX idx_content_pages_published ON content_pages (tenant_id, kind, slug)
  WHERE status = 'published';
CREATE INDEX idx_content_pages_review ON content_pages (review_due_at)
  WHERE status = 'published' AND review_due_at IS NOT NULL;

CREATE TRIGGER trg_content_pages_updated_at BEFORE UPDATE ON content_pages
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('content_pages');
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS content_pages;
DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS notification_preferences;
DROP TABLE IF EXISTS notification_templates;
DROP TABLE IF EXISTS announcements;
DROP TABLE IF EXISTS ticket_attachments;
DROP TABLE IF EXISTS ticket_messages;
DROP TABLE IF EXISTS tickets;
DROP TABLE IF EXISTS withdrawals;
DROP TABLE IF EXISTS commission_entries;
DROP TABLE IF EXISTS referrals;
DROP TABLE IF EXISTS invite_codes;
DROP TABLE IF EXISTS gift_codes;
DROP TABLE IF EXISTS coupon_redemptions;
DROP TABLE IF EXISTS coupons;
-- +goose StatementEnd
