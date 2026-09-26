-- R102（D-A-3）：删掉三个没有接入任何代码的降级开关。
--
-- ops.bulk_export、ops.reports、node.autoscale 是 00010 按 NFR-008 预留的
-- 初始集合，但全仓没有一处读取它们：在后台切它们只会让管理员以为自己
-- 暂停了导出、报表或扩容，实际什么都没发生。与其留着一个假开关，不如删掉。
-- 「订阅下发使用缓存」同案定为不做，不补新开关。
--
-- 之后现有开关：auth.login、subscription.renewal、client.config_sync（essential）、
-- auth.registration，以及 R58 的 billing.checkout、marketing.giftcard.redeem、
-- notify.email、admin.writes。

-- +goose Up

-- +goose StatementBegin
DELETE FROM feature_switches
 WHERE code IN ('ops.bulk_export', 'ops.reports', 'node.autoscale');
-- +goose StatementEnd

-- +goose Down

-- 按 00010 原样插回：只有默认租户有这三行，node.autoscale 默认关闭并带原因。
-- +goose StatementBegin
INSERT INTO feature_switches (tenant_id, code, enabled, essential, reason)
SELECT t.id, v.code, v.enabled, false, v.reason
  FROM tenants t
 CROSS JOIN (VALUES
   ('ops.bulk_export', true,  NULL),
   ('ops.reports',     true,  NULL),
   ('node.autoscale',  false, '首版默认关闭，需人工审批扩容')
 ) AS v(code, enabled, reason)
 WHERE t.id = '00000000-0000-7000-8000-000000000001'
ON CONFLICT (tenant_id, code) DO NOTHING;
-- +goose StatementEnd
