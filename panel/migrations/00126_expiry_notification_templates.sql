-- 到期当时与过期后召回的通知模板（用户 2026-10-07，w5expiry）。
--
-- 新增两个模板 code，各三个渠道（站内信、邮件、Telegram），类别 service：
--   subscription.expired  到期当时一条（变量 plan、expired_at）
--   subscription.recall   过期后第 1 天、第 7 天各一条召回（变量 plan、expired_at、days）
-- 由 notify 的扫描按订阅 status = 'expired' 排队（notify/scan.go）。
--
-- 做法照 00090：CREATE OR REPLACE app.seed_tenant_defaults，在它的模板清单末尾加这 6 行，
-- 其余逐字不变；再对已有租户补种（缺哪行补哪行，已有同 code、渠道、语言的任何版本就跳过）。
-- notify 包的单测核对这份种子与 defaultTemplates（「恢复默认」用的那份）一致。

-- +goose Up
SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
-- 调用者权限、只给属主用：它会把会话租户切到 p_tenant 再写，谁能调它谁就能
-- 往任意租户里插行，所以下面收回 PUBLIC 与 aegis_app 的执行权，只经触发器进来。
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

  -- 内置通知模板（18 个：00090 的 12 个 + 00126 的到期与召回 6 个）。同 00023：已有同 code、渠道、语言的任何版本就跳过。
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

    -- 00126 到期与召回（w5expiry）
    ('subscription.expired', 'inapp', '套餐已到期',
     '你的「{{plan}}」已于 {{expired_at}} 到期，节点已停止服务。续费后在客户端里更新订阅即可恢复，订阅链接不变。',
     ARRAY['plan','expired_at'], 'service'),

    ('subscription.expired', 'email', '【{{site}}】你的套餐已到期',
     '你好，

你的「{{plan}}」已于 {{expired_at}} 到期，节点已停止服务。
续费后在客户端里更新一次订阅即可恢复，订阅链接不变，无需重新导入。
过期满 30 天后将不能再原地续费，只能重新购买并更换订阅链接。

{{site}}',
     ARRAY['site','plan','expired_at'], 'service'),

    ('subscription.expired', 'telegram', '套餐已到期',
     '你的「{{plan}}」已于 {{expired_at}} 到期，续费后更新订阅即可恢复，链接不变。',
     ARRAY['plan','expired_at'], 'service'),

    ('subscription.recall', 'inapp', '套餐已过期 {{days}} 天',
     '你的「{{plan}}」已于 {{expired_at}} 到期。现在续费，原订阅链接自动恢复，无需重新导入；过期满 30 天后只能重新购买。',
     ARRAY['plan','expired_at','days'], 'service'),

    ('subscription.recall', 'email', '【{{site}}】你的套餐已过期 {{days}} 天',
     '你好，

你的「{{plan}}」已于 {{expired_at}} 到期，至今已 {{days}} 天。
现在续费，原订阅链接自动恢复，无需重新导入；过期满 30 天后只能重新购买，并需要更换订阅链接。

{{site}}',
     ARRAY['site','plan','expired_at','days'], 'service'),

    ('subscription.recall', 'telegram', '套餐已过期 {{days}} 天',
     '你的「{{plan}}」已于 {{expired_at}} 到期，现在续费原链接自动恢复。',
     ARRAY['plan','expired_at','days'], 'service')
    ) AS v(code, channel, subject, body, vars, category)
   WHERE NOT EXISTS (
     SELECT 1 FROM public.notification_templates x
      WHERE x.tenant_id = p_tenant AND x.code = v.code AND x.channel = v.channel
        AND x.locale = 'zh-CN');

  PERFORM set_config('app.tenant_id', coalesce(v_prev, ''), true);
END
$fn$;
-- +goose StatementEnd

-- 补种：已存在的租户只会补上这 6 行（其余早已齐全）。
-- +goose StatementBegin
DO $$
DECLARE
  r record;
BEGIN
  FOR r IN SELECT id FROM tenants ORDER BY id LOOP
    PERFORM app.seed_tenant_defaults(r.id);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- 还原 00090 的函数体（逐字），再删掉这两个 code：它们在本迁移之前不存在，删掉全部
-- 版本即回到原状。还没发出的这两类投递一并删掉，模板没了它们只会一直停在 queued。
-- +goose StatementBegin
-- 调用者权限、只给属主用：它会把会话租户切到 p_tenant 再写，谁能调它谁就能
-- 往任意租户里插行，所以下面收回 PUBLIC 与 aegis_app 的执行权，只经触发器进来。
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

DELETE FROM notification_deliveries
 WHERE template_code IN ('subscription.expired', 'subscription.recall') AND status = 'queued';
DELETE FROM notification_templates
 WHERE code IN ('subscription.expired', 'subscription.recall');
