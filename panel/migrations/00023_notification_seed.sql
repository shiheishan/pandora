-- +goose Up
-- 通知系统：站内信已读标记与内置模板
--
-- 三张表的结构已经齐了（模板 / 偏好 / 投递记录，含重试与去重），
-- 缺的是「已读」这个状态和一批可用的模板。
--
-- 为什么站内信不另建一张表：它本质就是一条投递记录，只不过渠道是
-- 「站内」而不是邮件。共用一张表意味着重试、去重、偏好这些逻辑
-- 只写一次；分开建则要在两处各维护一遍，迟早不一致。

ALTER TABLE notification_deliveries
  ADD COLUMN IF NOT EXISTS read_at timestamptz;

-- 未读列表要按用户查且量会不断增长，没有索引会随着历史累积越来越慢
CREATE INDEX IF NOT EXISTS notification_deliveries_inbox_idx
  ON notification_deliveries (tenant_id, user_id, created_at DESC)
  WHERE channel = 'inapp';

-- 待投递队列的扫描索引。只索引真正待处理的行 ——
-- 已发送的记录会累积到几十万条，全部进索引纯属浪费
CREATE INDEX IF NOT EXISTS notification_deliveries_pending_idx
  ON notification_deliveries (tenant_id, next_retry_at)
  WHERE status IN ('queued', 'sending');

COMMENT ON COLUMN notification_deliveries.read_at IS
  '站内信已读时间。其它渠道恒为 NULL —— 邮件是否被读我们无从得知，
   留空比编一个值诚实。';

-- 内置模板
--
-- category 取的是「用户能不能退订」而不是业务域：
--   transactional 必须送达，用户关不掉（表上有 transactional_always_on 约束保证）
--   service       服务提醒，用户可以关
--   marketing     营销，默认就该是关的
-- 支付成功属于交易凭据，归 transactional；到期与流量提醒归 service。
--
-- body 里用 {{name}} 占位。刻意不引模板引擎：通知文案里出现循环和条件
-- 意味着这条通知在试图表达太多东西，那是文案该拆开的信号，不是引擎该解决的问题。
INSERT INTO notification_templates
  (tenant_id, code, channel, locale, version, subject, body, allowed_variables, category, status)
SELECT t.id, v.code, v.channel, 'zh-CN', 1, v.subject, v.body, v.vars, v.category, 'active'
  FROM tenants t
 CROSS JOIN (VALUES
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
   ARRAY['subject'], 'service')
 ) AS v(code, channel, subject, body, vars, category)
 WHERE NOT EXISTS (
   SELECT 1 FROM notification_templates x
    WHERE x.tenant_id = t.id AND x.code = v.code AND x.channel = v.channel
      AND x.locale = 'zh-CN'
 );
