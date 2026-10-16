-- +goose Up
-- notify：删流量包订单的支付成功模板 order.pack_paid（00030 种的；流量包整体删除，用户 2026-10-16 定）。属 notify 模块。
-- notify 的 registry.go 同步去掉这个模板码（TestSeedTemplatesMatchRegistry 钉两边一致）。
-- 出站投递记录一并删掉（与 00030 的 Down 同口径）；站内信写入时已渲染好正文，用户的历史消息保留。
--
-- 锁与耗时：按模板码删几行，要 notification_templates、notification_deliveries 的行锁，瞬间完成。
-- 预检（可选）：SELECT count(*) FROM notification_deliveries WHERE template_code = 'order.pack_paid';
-- 失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在上一版，重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

DELETE FROM notification_deliveries WHERE template_code = 'order.pack_paid';
DELETE FROM notification_templates WHERE code = 'order.pack_paid';

-- +goose Down
-- 按 00030 原文把两条默认模板种回去（后台改过的文案与已删的投递记录不恢复）。没有放不下的数据，不需要守卫。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

INSERT INTO notification_templates (code, channel, subject, body) VALUES
('order.pack_paid', 'inapp', '支付成功',
'订单 {{order_no}} 已支付成功，{{traffic}} 流量包已加到「{{plan}}」。'),
('order.pack_paid', 'telegram', '支付成功',
'订单 {{order_no}} 已支付，{{traffic}} 流量包已加到「{{plan}}」。');
