-- 自助找回密码的邮件模板（w5account，用户 2026-10-07 定：邮件验证码）。
--
-- 找回密码第 1 步给账号邮箱发一个 6 位验证码（identity.StartPasswordReset），渲染按
-- code 找模板，所以种下 auth.password_reset。与 00074 一样归 transactional：验证码是
-- 重置的必要凭据，不能被偏好关掉；投递走按地址入队，payload 里的验证码明文在投递
-- 到终态时整个清空（notify.scrubAddressPayloadSQL）。
--
-- 老租户按现有租户逐个插入（已存在同键模板则跳过）；新租户由建租户触发器种下，
-- 所以这里同时 CREATE OR REPLACE app.seed_tenant_defaults：函数体逐字取自 00090，
-- 只在模板列表末尾加这一行（12 → 13 个）。notify 包的 TestTenantSeedTemplatesMatchDefaults
-- 按文件名取最后一个定义该函数的迁移核对，以后再加内置模板要在更新的迁移里整体重写它。

-- +goose Up
SET LOCAL lock_timeout = '5s';

INSERT INTO notification_templates
  (tenant_id, code, channel, locale, version, subject, body, allowed_variables, category, status)
SELECT t.id, 'auth.password_reset', 'email', 'zh-CN', 1,
       '【{{site}}】重置密码验证码 {{code}}',
       '你好，

你正在重置 {{site}} 的登录密码，验证码是：

{{code}}

验证码 {{minutes}} 分钟内有效。重置成功后，这个账号在所有设备上的登录都会失效，需要用新密码重新登录。

如果这不是你本人的操作，忽略这封邮件即可，你的密码不会改变。

{{site}}',
       ARRAY['site','code','minutes'], 'transactional', 'active'
  FROM tenants t
 WHERE NOT EXISTS (
   SELECT 1 FROM notification_templates x
    WHERE x.tenant_id = t.id AND x.code = 'auth.password_reset'
      AND x.channel = 'email' AND x.locale = 'zh-CN'
 );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.seed_tenant_defaults(p_tenant uuid) RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $fn$
DECLARE
  v_prev text := current_setting('app.tenant_id', true);
BEGIN
  -- 这几张表都是 FORCE RLS，写入要求会话租户就是目标租户；写完还原调用方的设置。
  PERFORM set_config('app.tenant_id', p_tenant::text, true);

  -- 线下收款渠道（同 00043）：收银台选不到，只承载「标记已支付 / 线下已收款」。
  INSERT INTO public.payment_providers
    (tenant_id, code, adapter, display_name, credentials_encrypted,
     supported_currencies, config, enabled, accepting_new)
  VALUES (p_tenant, 'offline', 'offline', '线下收款', ''::bytea,
          ARRAY['CNY','USD']::text[], '{}'::jsonb, true, false)
  ON CONFLICT (tenant_id, code) DO NOTHING;

  -- 降级开关（00010 的保留四项 + 00085 的四项，R102 之后的全集），全部开启。
  INSERT INTO public.feature_switches (tenant_id, code, enabled, essential)
  SELECT p_tenant, c.code, true, c.essential
    FROM (VALUES
      ('auth.login', true), ('subscription.renewal', true), ('client.config_sync', true),
      ('auth.registration', false),
      ('billing.checkout', false), ('marketing.giftcard.redeem', false),
      ('notify.email', false), ('admin.writes', false)
    ) AS c(code, essential)
  ON CONFLICT (tenant_id, code) DO NOTHING;

  -- 内置通知模板（13 个）。同 00023：已有同 code、渠道、语言的任何版本就跳过。
  INSERT INTO public.notification_templates
    (tenant_id, code, channel, locale, version, subject, body, allowed_variables, category, status)
  SELECT p_tenant, v.code, v.channel, 'zh-CN', 1, v.subject, v.body, v.vars, v.category, 'active'
    FROM (VALUES
    -- 00023 站内信与邮件
    ('subscription.expiring', 'inapp', '套餐即将到期',
     '你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期。到期后节点会停止服务，记得及时续费。',
     ARRAY['plan','days','expires_at'], 'service'),

    ('subscription.expiring', 'email', '【{{site}}】你的套餐 {{days}} 天后到期',
     '你好，

你的「{{plan}}」将在 {{days}} 天后到期（{{expires_at}}）。
到期后节点将停止服务，请及时续费以免影响使用。

{{site}}',
     ARRAY['site','plan','days','expires_at'], 'service'),

    ('quota.warning', 'inapp', '流量即将用尽',
     '你的「{{plan}}」已使用 {{percent}}% 流量（剩余 {{remaining}}）。用尽后将无法连接节点。',
     ARRAY['plan','percent','remaining'], 'service'),

    ('quota.warning', 'email', '【{{site}}】流量已使用 {{percent}}%',
     '你好，

你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。
流量用尽后将无法连接节点，可在面板购买流量包或升级套餐。

{{site}}',
     ARRAY['site','plan','percent','remaining'], 'service'),

    ('order.paid', 'inapp', '支付成功',
     '订单 {{order_no}} 已支付成功，「{{plan}}」已开通，有效期至 {{expires_at}}。',
     ARRAY['order_no','plan','expires_at'], 'transactional'),

    ('ticket.replied', 'inapp', '工单有新回复',
     '你的工单「{{subject}}」有新回复，点击查看。',
     ARRAY['subject'], 'service'),

    -- 00049 群发
    ('admin.broadcast', 'email', '{{subject}}', '{{body}}',
     ARRAY['subject','body'], 'marketing'),

    -- 00050 Telegram 渠道
    ('subscription.expiring', 'telegram', '套餐即将到期',
     '你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期，记得续费。',
     ARRAY['plan','days','expires_at'], 'service'),
    ('quota.warning', 'telegram', '流量预警',
     '你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。',
     ARRAY['plan','percent','remaining'], 'service'),
    ('order.paid', 'telegram', '支付成功',
     '订单 {{order_no}} 已支付，「{{plan}}」已开通，有效期至 {{expires_at}}。',
     ARRAY['order_no','plan','expires_at'], 'transactional'),
    ('ticket.replied', 'telegram', '工单有新回复',
     '你的工单「{{subject}}」有新回复。',
     ARRAY['subject'], 'service'),

    -- 00074 注册验证码
    ('auth.email_verify', 'email',
     '【{{site}}】注册验证码 {{code}}',
     '你好，

你正在注册 {{site}}，验证码是：

{{code}}

验证码 {{minutes}} 分钟内有效。如果这不是你本人的操作，忽略这封邮件即可。

{{site}}',
     ARRAY['site','code','minutes'], 'transactional'),

    -- 00128 找回密码验证码
    ('auth.password_reset', 'email',
     '【{{site}}】重置密码验证码 {{code}}',
     '你好，

你正在重置 {{site}} 的登录密码，验证码是：

{{code}}

验证码 {{minutes}} 分钟内有效。重置成功后，这个账号在所有设备上的登录都会失效，需要用新密码重新登录。

如果这不是你本人的操作，忽略这封邮件即可，你的密码不会改变。

{{site}}',
     ARRAY['site','code','minutes'], 'transactional')
    ) AS v(code, channel, subject, body, vars, category)
   WHERE NOT EXISTS (
     SELECT 1 FROM public.notification_templates x
      WHERE x.tenant_id = p_tenant AND x.code = v.code AND x.channel = v.channel
        AND x.locale = 'zh-CN');

  PERFORM set_config('app.tenant_id', coalesce(v_prev, ''), true);
END
$fn$;
-- +goose StatementEnd

-- +goose StatementBegin
REVOKE ALL ON FUNCTION app.seed_tenant_defaults(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.seed_tenant_defaults(uuid) FROM aegis_app;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- 函数恢复成 00090 的原文（12 个模板）。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.seed_tenant_defaults(p_tenant uuid) RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $fn$
DECLARE
  v_prev text := current_setting('app.tenant_id', true);
BEGIN
  -- 这几张表都是 FORCE RLS，写入要求会话租户就是目标租户；写完还原调用方的设置。
  PERFORM set_config('app.tenant_id', p_tenant::text, true);

  -- 线下收款渠道（同 00043）：收银台选不到，只承载「标记已支付 / 线下已收款」。
  INSERT INTO public.payment_providers
    (tenant_id, code, adapter, display_name, credentials_encrypted,
     supported_currencies, config, enabled, accepting_new)
  VALUES (p_tenant, 'offline', 'offline', '线下收款', ''::bytea,
          ARRAY['CNY','USD']::text[], '{}'::jsonb, true, false)
  ON CONFLICT (tenant_id, code) DO NOTHING;

  -- 降级开关（00010 的保留四项 + 00085 的四项，R102 之后的全集），全部开启。
  INSERT INTO public.feature_switches (tenant_id, code, enabled, essential)
  SELECT p_tenant, c.code, true, c.essential
    FROM (VALUES
      ('auth.login', true), ('subscription.renewal', true), ('client.config_sync', true),
      ('auth.registration', false),
      ('billing.checkout', false), ('marketing.giftcard.redeem', false),
      ('notify.email', false), ('admin.writes', false)
    ) AS c(code, essential)
  ON CONFLICT (tenant_id, code) DO NOTHING;

  -- 内置通知模板（12 个）。同 00023：已有同 code、渠道、语言的任何版本就跳过。
  INSERT INTO public.notification_templates
    (tenant_id, code, channel, locale, version, subject, body, allowed_variables, category, status)
  SELECT p_tenant, v.code, v.channel, 'zh-CN', 1, v.subject, v.body, v.vars, v.category, 'active'
    FROM (VALUES
    -- 00023 站内信与邮件
    ('subscription.expiring', 'inapp', '套餐即将到期',
     '你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期。到期后节点会停止服务，记得及时续费。',
     ARRAY['plan','days','expires_at'], 'service'),

    ('subscription.expiring', 'email', '【{{site}}】你的套餐 {{days}} 天后到期',
     '你好，

你的「{{plan}}」将在 {{days}} 天后到期（{{expires_at}}）。
到期后节点将停止服务，请及时续费以免影响使用。

{{site}}',
     ARRAY['site','plan','days','expires_at'], 'service'),

    ('quota.warning', 'inapp', '流量即将用尽',
     '你的「{{plan}}」已使用 {{percent}}% 流量（剩余 {{remaining}}）。用尽后将无法连接节点。',
     ARRAY['plan','percent','remaining'], 'service'),

    ('quota.warning', 'email', '【{{site}}】流量已使用 {{percent}}%',
     '你好，

你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。
流量用尽后将无法连接节点，可在面板购买流量包或升级套餐。

{{site}}',
     ARRAY['site','plan','percent','remaining'], 'service'),

    ('order.paid', 'inapp', '支付成功',
     '订单 {{order_no}} 已支付成功，「{{plan}}」已开通，有效期至 {{expires_at}}。',
     ARRAY['order_no','plan','expires_at'], 'transactional'),

    ('ticket.replied', 'inapp', '工单有新回复',
     '你的工单「{{subject}}」有新回复，点击查看。',
     ARRAY['subject'], 'service'),

    -- 00049 群发
    ('admin.broadcast', 'email', '{{subject}}', '{{body}}',
     ARRAY['subject','body'], 'marketing'),

    -- 00050 Telegram 渠道
    ('subscription.expiring', 'telegram', '套餐即将到期',
     '你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期，记得续费。',
     ARRAY['plan','days','expires_at'], 'service'),
    ('quota.warning', 'telegram', '流量预警',
     '你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。',
     ARRAY['plan','percent','remaining'], 'service'),
    ('order.paid', 'telegram', '支付成功',
     '订单 {{order_no}} 已支付，「{{plan}}」已开通，有效期至 {{expires_at}}。',
     ARRAY['order_no','plan','expires_at'], 'transactional'),
    ('ticket.replied', 'telegram', '工单有新回复',
     '你的工单「{{subject}}」有新回复。',
     ARRAY['subject'], 'service'),

    -- 00074 注册验证码
    ('auth.email_verify', 'email',
     '【{{site}}】注册验证码 {{code}}',
     '你好，

你正在注册 {{site}}，验证码是：

{{code}}

验证码 {{minutes}} 分钟内有效。如果这不是你本人的操作，忽略这封邮件即可。

{{site}}',
     ARRAY['site','code','minutes'], 'transactional')
    ) AS v(code, channel, subject, body, vars, category)
   WHERE NOT EXISTS (
     SELECT 1 FROM public.notification_templates x
      WHERE x.tenant_id = p_tenant AND x.code = v.code AND x.channel = v.channel
        AND x.locale = 'zh-CN');

  PERFORM set_config('app.tenant_id', coalesce(v_prev, ''), true);
END
$fn$;
-- +goose StatementEnd

-- +goose StatementBegin
REVOKE ALL ON FUNCTION app.seed_tenant_defaults(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.seed_tenant_defaults(uuid) FROM aegis_app;
-- +goose StatementEnd

-- 这个 code 在本迁移之前不存在，删掉它的全部版本即回到原状。还没发出的投递一并删掉：
-- 模板没了它们只会一直停在 queued，而 payload 里带着验证码明文与收件邮箱。
-- 找回密码写下的验证码行（purpose = password_reset）只存哈希，留着无害，到期即失效。
DELETE FROM notification_deliveries
 WHERE template_code = 'auth.password_reset' AND status = 'queued';
DELETE FROM notification_templates
 WHERE code = 'auth.password_reset' AND channel = 'email';
