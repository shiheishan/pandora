-- M10：四个新降级开关（契约后台-09 POST v1/switches/{code}），按租户各插一行。
--
--   billing.checkout           关 = 门户拒绝新建订单与发起支付（已发起的支付回调照常处理）
--   marketing.giftcard.redeem  关 = 拒绝兑换礼品卡
--   notify.email               关 = 派发跳过邮件渠道，队列保留，恢复后按序投递
--   admin.writes               关 = 管理端只读（切开关、登录、重认证、改密码除外）
--
-- 极性沿用「enabled = 可用」，默认全开、都不是 essential。00010 只给默认租户插过
-- 开关，这里对当时已有的每个租户都插；之后新建的租户没有这几行，读取方按
-- 「缺行视为开启」处理（middleware.FeatureSwitch、notify.Dispatch）。

-- +goose Up
INSERT INTO feature_switches (tenant_id, code, enabled, essential)
SELECT t.id, c.code, true, false
  FROM tenants t
 CROSS JOIN (VALUES ('billing.checkout'), ('marketing.giftcard.redeem'),
                    ('notify.email'), ('admin.writes')) AS c(code)
ON CONFLICT (tenant_id, code) DO NOTHING;

-- +goose Down
DELETE FROM feature_switches
 WHERE code IN ('billing.checkout', 'marketing.giftcard.redeem', 'notify.email', 'admin.writes');
