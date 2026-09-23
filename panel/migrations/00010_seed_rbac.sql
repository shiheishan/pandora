-- 权限字典与系统角色种子。
--
-- 对应 PRD 2.3 用户角色矩阵与 IAM-009「默认拒绝；新增接口必须声明权限」。
-- 权限码是代码里的常量来源：网关在启动时会核对路由声明的权限码是否都在此表中，
-- 少一个就拒绝启动 —— 这样「忘了声明权限」会在部署时暴露，而不是在生产被绕过。

-- +goose Up

-- +goose StatementBegin
INSERT INTO permissions (code, domain, description, high_risk) VALUES
  -- 身份
  ('iam.user.read',            'iam',      '查看用户资料（默认脱敏）', false),
  ('iam.user.read_sensitive',  'iam',      '查看用户敏感字段', true),
  ('iam.user.write',           'iam',      '修改用户资料与状态', true),
  ('iam.user.impersonate_readonly', 'iam', '只读代用户查看（XBD-027）', true),
  ('iam.session.revoke',       'iam',      '远程注销用户会话与设备', true),
  ('iam.role.read',            'iam',      '查看角色与权限', false),
  ('iam.role.write',           'iam',      '变更角色绑定与权限', true),
  ('iam.org.read',             'iam',      '查看组织', false),
  ('iam.org.write',            'iam',      '管理组织与成员', false),
  ('iam.sso.write',            'iam',      '配置企业身份源', true),
  ('iam.token.write',          'iam',      '签发与吊销 API Token', true),

  -- 商品与订阅
  ('catalog.read',             'catalog',  '查看商品、价格与套餐', false),
  ('catalog.write',            'catalog',  '编辑商品、价格与套餐版本', false),
  ('catalog.publish',          'catalog',  '发布套餐版本与调价', true),
  ('subscription.read',        'catalog',  '查看订阅', false),
  ('subscription.write',       'catalog',  '调整订阅状态与周期', true),
  ('subscription.migrate',     'catalog',  '批量迁移订阅（SUB-009）', true),

  -- 交易与财务
  ('billing.order.read',       'billing',  '查看订单', false),
  ('billing.order.write',      'billing',  '创建人工订单（XBD-015）', true),
  ('billing.payment.read',     'billing',  '查看支付记录', false),
  ('billing.refund.request',   'billing',  '发起退款申请', false),
  ('billing.refund.approve',   'billing',  '审批退款', true),
  ('billing.ledger.read',      'billing',  '查看账本分录', false),
  ('billing.adjustment.write', 'billing',  '发起余额/佣金人工调整', true),
  ('billing.reconciliation.read',  'billing', '查看对账结果', false),
  ('billing.reconciliation.write', 'billing', '处理对账差异', true),
  ('billing.provider.write',   'billing',  '配置支付渠道与密钥', true),

  -- 节点
  ('node.read',                'node',     '查看节点与资源池', false),
  ('node.provision',           'node',     '创建/导入节点', true),
  ('node.write',               'node',     '修改节点配置与调度权重', false),
  ('node.lifecycle',           'node',     '排空、隔离、退役与销毁节点', true),
  ('node.identity.revoke',     'node',     '吊销节点身份', true),
  ('node.config.publish',      'node',     '发布节点配置', true),
  ('node.task.dispatch',       'node',     '下发 Agent 白名单任务', true),
  ('node.provider.write',      'node',     '配置云厂商与凭据', true),

  -- 计量
  ('metering.read',            'metering', '查看用量与配额', false),
  ('metering.adjust',          'metering', '人工调整配额', true),

  -- 运营
  ('ops.ticket.read',          'ops',      '查看工单', false),
  ('ops.ticket.write',         'ops',      '处理工单与内部备注', false),
  ('ops.announcement.write',   'ops',      '发布与撤回公告', false),
  ('ops.notification.write',   'ops',      '管理通知模板与发送', false),
  ('ops.content.write',        'ops',      '编辑自定义页面与知识库', false),
  ('ops.bulk_operation',       'ops',      '批量用户运营（XBD-016）', true),
  ('ops.export',               'ops',      '导出用户数据', true),
  ('ops.import',               'ops',      '导入用户数据', true),

  -- 营销
  ('marketing.coupon.write',   'marketing','管理优惠券与礼品码', false),
  ('marketing.commission.read','marketing','查看佣金', false),
  ('marketing.commission.review', 'marketing', '复核高风险佣金', true),
  ('marketing.withdrawal.approve','marketing','审批提现', true),

  -- 安全
  ('security.audit.read',      'security', '查看审计记录', false),
  ('security.risk.read',       'security', '查看风险事件', false),
  ('security.risk.review',     'security', '复核风险处置', true),
  ('security.alert.write',     'security', '处置安全事件', true),
  ('security.approval.decide', 'security', '审批高风险动作', true),
  ('security.settings.write',  'security', '修改安全参数与降级开关', true),

  -- 平台
  ('platform.settings.write',  'platform', '修改系统参数', true),
  ('platform.plugin.write',    'platform', '安装与启用插件', true),
  ('platform.tenant.write',    'platform', '管理租户', true),
  ('platform.webhook.write',   'platform', '配置 Webhook', false)
ON CONFLICT (code) DO NOTHING;
-- +goose StatementEnd

-- +goose StatementBegin
-- 默认租户。附录 A：首个商用版本单租户运行，但全表已预留 tenant_id。
INSERT INTO tenants (id, slug, display_name, timezone, default_currency)
VALUES ('00000000-0000-7000-8000-000000000001', 'default', 'AegisPanel', 'UTC', 'USD')
ON CONFLICT (slug) DO NOTHING;
-- +goose StatementEnd

-- +goose StatementBegin
-- 系统角色，对应 PRD 2.3 角色矩阵。is_system = true，租户管理员不可删改。
DO $$
DECLARE
  v_tenant  uuid := '00000000-0000-7000-8000-000000000001';
  r         record;
  v_role_id uuid;
BEGIN
  CREATE TEMP TABLE _seed_roles (code text, name text, perms text[]) ON COMMIT DROP;

  INSERT INTO _seed_roles VALUES
    ('customer_support', '客服', ARRAY[
      'iam.user.read', 'iam.user.impersonate_readonly',
      'subscription.read', 'billing.order.read', 'billing.payment.read',
      'billing.refund.request',
      'metering.read', 'node.read',
      'ops.ticket.read', 'ops.ticket.write', 'ops.announcement.write'
    ]),
    ('finance', '财务', ARRAY[
      'iam.user.read',
      'billing.order.read', 'billing.order.write', 'billing.payment.read',
      'billing.refund.request', 'billing.refund.approve', 'billing.ledger.read',
      'billing.adjustment.write', 'billing.reconciliation.read',
      'billing.reconciliation.write',
      'marketing.commission.read', 'marketing.withdrawal.approve',
      'subscription.read'
    ]),
    ('resource_operator', '资源运维', ARRAY[
      'node.read', 'node.provision', 'node.write', 'node.lifecycle',
      'node.identity.revoke', 'node.config.publish', 'node.task.dispatch',
      'metering.read'
    ]),
    ('security_auditor', '安全审计员', ARRAY[
      'security.audit.read', 'security.risk.read', 'security.alert.write',
      'iam.user.read', 'billing.ledger.read', 'node.read'
    ]),
    ('tenant_admin', '租户管理员', ARRAY[
      'iam.user.read', 'iam.user.write', 'iam.role.read', 'iam.role.write',
      'iam.org.read', 'iam.org.write', 'iam.session.revoke', 'iam.token.write',
      'catalog.read', 'catalog.write', 'catalog.publish',
      'subscription.read', 'subscription.write',
      'billing.order.read', 'billing.payment.read', 'billing.ledger.read',
      'billing.reconciliation.read',
      'node.read', 'node.write', 'metering.read', 'metering.adjust',
      'ops.ticket.read', 'ops.ticket.write', 'ops.announcement.write',
      'ops.notification.write', 'ops.content.write',
      'marketing.coupon.write', 'marketing.commission.read',
      'security.audit.read', 'security.risk.read',
      'platform.webhook.write'
    ]),
    ('platform_admin', '平台管理员', ARRAY[]::text[]);

  FOR r IN SELECT * FROM _seed_roles LOOP
    INSERT INTO roles (tenant_id, code, name, is_system, description)
    VALUES (v_tenant, r.code, r.name, true, 'PRD 2.3 角色矩阵内置角色')
    ON CONFLICT (tenant_id, code) DO UPDATE SET name = EXCLUDED.name
    RETURNING id INTO v_role_id;

    IF r.code = 'platform_admin' THEN
      -- 平台管理员拥有全部权限；高风险动作仍需重认证与审批，
      -- 那是网关与审批流的职责，不在权限位上打折。
      INSERT INTO role_permissions (role_id, permission_code)
      SELECT v_role_id, code FROM permissions
      ON CONFLICT DO NOTHING;
    ELSE
      INSERT INTO role_permissions (role_id, permission_code)
      SELECT v_role_id, unnest(r.perms)
      ON CONFLICT DO NOTHING;
    END IF;
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
-- NFR-008 降级开关的初始集合。essential 的三项永远不可关闭。
INSERT INTO feature_switches (tenant_id, code, enabled, essential, reason) VALUES
  ('00000000-0000-7000-8000-000000000001', 'auth.login',            true,  true,  NULL),
  ('00000000-0000-7000-8000-000000000001', 'subscription.renewal',  true,  true,  NULL),
  ('00000000-0000-7000-8000-000000000001', 'client.config_sync',    true,  true,  NULL),
  ('00000000-0000-7000-8000-000000000001', 'auth.registration',     true,  false, NULL),
  ('00000000-0000-7000-8000-000000000001', 'ops.bulk_export',       true,  false, NULL),
  ('00000000-0000-7000-8000-000000000001', 'ops.reports',           true,  false, NULL),
  ('00000000-0000-7000-8000-000000000001', 'node.autoscale',        false, false, '首版默认关闭，需人工审批扩容')
ON CONFLICT (tenant_id, code) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DELETE FROM feature_switches WHERE tenant_id = '00000000-0000-7000-8000-000000000001';
DELETE FROM role_permissions WHERE role_id IN (
  SELECT id FROM roles WHERE tenant_id = '00000000-0000-7000-8000-000000000001' AND is_system
);
DELETE FROM roles WHERE tenant_id = '00000000-0000-7000-8000-000000000001' AND is_system;
DELETE FROM tenants WHERE id = '00000000-0000-7000-8000-000000000001';
DELETE FROM permissions;
-- +goose StatementEnd
