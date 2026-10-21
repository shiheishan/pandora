-- +goose Up
-- notify：加两个后台操作通知模板的种子（用户 2026-10-21 定）：后台换链接 subscription.link_rotated、
-- 后台开待付单 order.pending_created，各邮件 / 站内信 / Telegram 三个渠道；与 registry_admin_ops.go 逐字一致
-- （TestSeedTemplatesMatchRegistry 对照）。
-- 锁与耗时：插 6 行模板，瞬间完成。
-- 预检：无（新模板码）。
-- 失败回滚：整笔回滚，库停在 00062，重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

INSERT INTO notification_templates (code, channel, subject, body) VALUES
('subscription.link_rotated', 'inapp', '链接已更换',
'管理员给你的「{{plan}}」换了新链接，旧链接已失效，用它的设备会连不上。请到「我的套餐」复制新链接，在 App 里重新添加；原来那条可以删掉。到期日和流量不变。'),
('subscription.link_rotated', 'email', '【{{site}}】你的套餐链接已更换',
'你好，

管理员给你的「{{plan}}」换了新链接，旧链接已失效，用它的设备会连不上。
请登录后到「我的套餐」复制新链接，在 App 里重新添加；原来那条可以删掉。到期日和流量不变。

{{subs_url}}

{{site}}'),
('subscription.link_rotated', 'telegram', '链接已更换',
'管理员给你的「{{plan}}」换了新链接，旧链接已失效。请到「我的套餐」复制新链接，在 App 里重新添加：{{subs_url}}'),
('order.pending_created', 'inapp', '有一张待付款订单',
'管理员给你开了一张订单 {{order_no}}（「{{plan}}」），应付 {{amount}}。请在 {{expires_at}} 前到「订单」页付款，过期自动取消。'),
('order.pending_created', 'email', '【{{site}}】你有一张待付款订单 {{order_no}}',
'你好，

管理员给你开了一张订单，等你付款：
订单号：{{order_no}}
套餐：{{plan}}
应付：{{amount}}
付款期限：{{expires_at}}

过期自动取消。登录后到「订单」页付款：
{{order_url}}

{{site}}'),
('order.pending_created', 'telegram', '有一张待付款订单',
'管理员给你开了一张订单 {{order_no}}（「{{plan}}」），应付 {{amount}}，请在 {{expires_at}} 前付款，过期自动取消。去付款：{{order_url}}');

-- +goose Down
-- 删掉这两个模板码的投递记录与模板（站内信写入时已渲染好，用户的历史消息保留；去重键留着无害）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

DELETE FROM notification_deliveries WHERE template_code IN ('subscription.link_rotated', 'order.pending_created');
DELETE FROM notification_templates WHERE code IN ('subscription.link_rotated', 'order.pending_created');
