-- 营销域补两个权限码，让优惠券与分销路由不再借用别的域的权限。
--
--   marketing.coupon.read      GET v1/coupons 此前只挂写权限，只想看券的人
--                              也得拿到造券的权力。授予所有已有
--                              marketing.coupon.write 的角色：会写的一定能读，
--                              没有人因此失去访问。
--   marketing.commission.write POST v1/commission/config（改返佣比例、冻结期、
--                              最低提现）此前挂的是 billing.provider.write
--                              「配置支付渠道与密钥」。新码授予当前持有
--                              billing.provider.write 的角色，受众不变。
--
-- 同一批路由改动里，分销的读与提现审批改挂 00010 已有、却一直没有路由使用的
-- marketing.commission.read / marketing.withdrawal.approve，不需要数据迁移。

-- +goose Up

-- +goose StatementBegin
INSERT INTO permissions (code, domain, description, high_risk)
VALUES ('marketing.coupon.read',      'marketing', '查看优惠券与兑换记录', false),
       ('marketing.commission.write', 'marketing', '修改分销返佣参数', true)
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT rp.role_id, 'marketing.coupon.read'
  FROM role_permissions rp
 WHERE rp.permission_code = 'marketing.coupon.write'
ON CONFLICT (role_id, permission_code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT rp.role_id, 'marketing.commission.write'
  FROM role_permissions rp
 WHERE rp.permission_code = 'billing.provider.write'
ON CONFLICT (role_id, permission_code) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DELETE FROM role_permissions
 WHERE permission_code IN ('marketing.coupon.read', 'marketing.commission.write');
DELETE FROM permissions
 WHERE code IN ('marketing.coupon.read', 'marketing.commission.write');
-- +goose StatementEnd
