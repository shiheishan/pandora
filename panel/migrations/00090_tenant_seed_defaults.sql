-- 新租户和老租户一样能用（R94、R97 与线下渠道、通知模板）。
--
-- 谁建租户：只有迁移 00010 种下的默认租户。Go 里没有 INSERT INTO tenants，
-- adminctl、install.sh 也不建租户，middleware.Tenant 把每个请求都钉在默认租户
-- 上（附录 A：首版单租户运行）。所以「缺种子」今天只会出现在两处：手工 SQL
-- 建的租户，以及 PG18 夹具——夹具里已经有人「照默认租户的种子抄一份」。
--
-- 病根在于每条种子迁移都写成 `SELECT … FROM tenants t`：只照顾执行那一刻已
-- 存在的租户，之后建的租户什么都拿不到。缺了线下渠道，标记已支付与线下已
-- 收款会 404；缺了模板，通知静默不发、后台模板页是空的；缺了开关行，后台
-- 列表不显示、POST v1/switches/{code} 回 404（R97）。
--
-- 做法：把「一个租户出生时必须带的行」收进 app.seed_tenant_defaults，挂在
-- tenants 的 AFTER INSERT 上。放在数据库而不是 Go：租户没有 Go 创建入口，
-- 将来的建租户接口、adminctl 子命令还是手工 SQL，都一定经过这张表。
-- 降级开关因此选「建租户时补种」而不是「POST 改 upsert」：列表与切换都继续以
-- 行为准，essential 位留在数据里由 CHECK 守着；upsert 要在 Go 里再抄一份开关
-- 目录与 essential 位，而且列表照样缺行。
--
-- 范围只到这三类：线下渠道、12 个通知模板、8 个降级开关。SMTP 密码行由写接口
-- 改 upsert 兜住（R94）；系统角色、主题、其余 system_settings 等租户级种子不在
-- 本次范围，报告里列为遗留。
--
-- 模板正文逐字取自 00023 / 00049 / 00050 / 00074，notify 包的单元测试核对它与
-- defaultTemplates（「恢复默认」用的那份）一致。以后新增内置模板，要同时改这里。

-- +goose Up

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

-- +goose StatementBegin
REVOKE ALL ON FUNCTION app.seed_tenant_defaults(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.seed_tenant_defaults(uuid) FROM aegis_app;
-- +goose StatementEnd

-- +goose StatementBegin
-- 触发器函数以属主身份执行：aegis_app 对 tenants 有 INSERT 权，将来若经应用
-- 建租户，也能拿到种子，但它自己调不到 seed_tenant_defaults。
CREATE OR REPLACE FUNCTION app.seed_new_tenant() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog
AS $fn$
BEGIN
  PERFORM app.seed_tenant_defaults(NEW.id);
  RETURN NULL;
END
$fn$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_tenants_seed_defaults
  AFTER INSERT ON tenants
  FOR EACH ROW EXECUTE FUNCTION app.seed_new_tenant();
-- +goose StatementEnd

-- 补种：已存在的租户缺哪行补哪行（默认租户本来就齐，这里对它是空操作）。
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

-- 只拆机制；补种出来的行与之后新租户拿到的行都是合法的业务数据，留着。
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_tenants_seed_defaults ON tenants;
DROP FUNCTION IF EXISTS app.seed_new_tenant();
DROP FUNCTION IF EXISTS app.seed_tenant_defaults(uuid);
-- +goose StatementEnd
