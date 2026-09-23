-- +goose Up

-- Telegram 通知（对标 Xboard config/setTelegramWebhook + user/telegram）。
--
-- 两部分：站点级的 Bot 配置，和用户级的账号绑定。

-- Bot Token 是敏感值，走 system_settings 的信封加密通道（is_secret = true），
-- 与 SMTP 口令同一套机制：明文不落库，只有 secret_encrypted。
-- 空 bytea 是因为列上有 CHECK 要求 is_secret 时非空。
-- +goose StatementBegin
INSERT INTO system_settings (tenant_id, key, value, is_secret, secret_encrypted)
SELECT t.id, 'telegram.bot_token', to_jsonb(''::text), true, ''::bytea
  FROM tenants t
ON CONFLICT (tenant_id, key) DO NOTHING;

INSERT INTO system_settings (tenant_id, key, value)
SELECT t.id, k.key, k.val
  FROM tenants t
 CROSS JOIN (VALUES
   ('telegram.bot_username', to_jsonb(''::text)),
   ('telegram.enabled',      to_jsonb(false))
 ) AS k(key, val)
ON CONFLICT (tenant_id, key) DO NOTHING;
-- +goose StatementEnd

-- 用户绑定。
--
-- 绑定必须双向验证：面板发一个一次性验证码，用户在 Telegram 里把它发给 bot，
-- bot 的 webhook 带着 chat_id 回来核对。只让用户在面板里填 chat_id 是不够的 ——
-- 填别人的 chat_id 就能把别人的账号通知劫持到自己那里。
CREATE TABLE telegram_bindings (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  chat_id     bigint NOT NULL,
  username    text NOT NULL DEFAULT '',
  bound_at    timestamptz(6) NOT NULL DEFAULT now(),
  created_at  timestamptz(6) NOT NULL DEFAULT now(),
  -- 一个用户只绑一个 Telegram；一个 Telegram 也只能绑一个用户。
  -- 后者防的是「同一个人注册多个账号，用一个 TG 收所有通知」——
  -- 那会让「按用户发通知」的口径失真。
  UNIQUE (tenant_id, user_id),
  UNIQUE (tenant_id, chat_id)
);

-- 绑定验证码。短时效、一次性。
CREATE TABLE telegram_bind_codes (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code       text NOT NULL CHECK (code = upper(code) AND length(code) BETWEEN 6 AND 12),
  used_at    timestamptz(6),
  expires_at timestamptz(6) NOT NULL,
  created_at timestamptz(6) NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code)
);

CREATE INDEX idx_telegram_bind_codes_live
  ON telegram_bind_codes (tenant_id, code) WHERE used_at IS NULL;

ALTER TABLE telegram_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE telegram_bind_codes ENABLE ROW LEVEL SECURITY;
CREATE POLICY telegram_bindings_tenant ON telegram_bindings
  USING (tenant_id = app.current_tenant_id());
CREATE POLICY telegram_bind_codes_tenant ON telegram_bind_codes
  USING (tenant_id = app.current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON telegram_bindings TO aegis_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON telegram_bind_codes TO aegis_app;

-- 通知模板：Telegram 也是一个投递渠道，走同一套模板机制。
-- +goose StatementBegin
INSERT INTO notification_templates
  (tenant_id, code, channel, locale, version, subject, body,
   allowed_variables, category, status)
SELECT t.id, v.code, 'telegram', 'zh-CN', 1, v.subject, v.body, v.vars, v.category, 'active'
  FROM tenants t
 CROSS JOIN (VALUES
  ('subscription.expiring', '套餐即将到期',
   '你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期，记得续费。',
   ARRAY['plan','days','expires_at'], 'service'),
  ('quota.warning', '流量预警',
   '你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。',
   ARRAY['plan','percent','remaining'], 'service'),
  ('order.paid', '支付成功',
   '订单 {{order_no}} 已支付，「{{plan}}」已开通，有效期至 {{expires_at}}。',
   ARRAY['order_no','plan','expires_at'], 'transactional'),
  ('ticket.replied', '工单有新回复',
   '你的工单「{{subject}}」有新回复。',
   ARRAY['subject'], 'service')
 ) AS v(code, subject, body, vars, category)
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS telegram_bind_codes;
DROP TABLE IF EXISTS telegram_bindings;
DELETE FROM notification_templates WHERE channel = 'telegram';
DELETE FROM system_settings WHERE key LIKE 'telegram.%';
