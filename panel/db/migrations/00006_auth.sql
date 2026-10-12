-- +goose Up
-- auth 模块：后台权限字典、角色、角色权限与用户角色绑定。
-- 会话、refresh、快捷登录、凭据纪元、登录失败计数都在 Redis，库里不留副本。
-- 权限码与 Go 常量（module/auth/service/permissions.go）由测试对齐；旧库里 28 个从不检查的码不再种。

CREATE TABLE permissions (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  code        text NOT NULL,
  domain      text NOT NULL CHECK (domain ~ '^[a-z]{1,32}$'),
  description text NOT NULL CHECK (btrim(description) <> '' AND char_length(description) <= 200),
  high_risk   boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT permissions_code_unique UNIQUE (code),
  -- 形如 iam.user.read：小写点分，至少两段
  CONSTRAINT permissions_code_format CHECK (code ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+$' AND char_length(code) <= 64)
);

CREATE TABLE roles (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  code        text NOT NULL,
  name        text NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 64),
  description text CHECK (description IS NULL OR char_length(description) <= 500),
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT roles_code_unique UNIQUE (code),
  CONSTRAINT roles_code_format CHECK (code ~ '^[a-z][a-z0-9_]{0,63}$')
);

CREATE TABLE role_permissions (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  role_id         uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
  permission_code text NOT NULL REFERENCES permissions (code) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT role_permissions_unique UNIQUE (role_id, permission_code)
);
CREATE INDEX role_permissions_code_idx ON role_permissions (permission_code);

-- 用户角色绑定。expires_at 为空是长期绑定；有值的是临时提权，过期即不算数（不需要清理任务）。
-- 只有长期绑定能让一个人算作「有效管理员」（最后一个管理员保护）。
CREATE TABLE user_roles (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  role_id    uuid NOT NULL REFERENCES roles (id) ON DELETE RESTRICT,
  expires_at timestamptz,
  granted_by uuid REFERENCES users (id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT user_roles_unique UNIQUE (user_id, role_id),
  CONSTRAINT user_roles_expiry_after_grant CHECK (expires_at IS NULL OR expires_at > created_at)
);
CREATE INDEX user_roles_role_idx ON user_roles (role_id);

-- 权限字典（44 个）：沿用旧码与说明。
INSERT INTO permissions (code, domain, description, high_risk) VALUES
  ('iam.user.read',              'iam',       '查看用户资料（默认脱敏）', false),
  ('iam.user.write',             'iam',       '修改用户资料与状态', true),
  ('catalog.read',               'catalog',   '查看商品、价格与套餐', false),
  ('catalog.write',              'catalog',   '编辑商品、价格与套餐版本', false),
  ('catalog.publish',            'catalog',   '发布套餐版本与调价', true),
  ('billing.order.read',         'billing',   '查看订单', false),
  ('billing.order.write',        'billing',   '创建人工订单', true),
  ('billing.payment.read',       'billing',   '查看支付记录', false),
  ('billing.ledger.read',        'billing',   '查看账本分录', false),
  ('billing.adjustment.write',   'billing',   '发起余额/佣金人工调整', true),
  ('billing.provider.write',     'billing',   '配置支付渠道与密钥', true),
  ('node.read',                  'node',      '查看节点与资源池', false),
  ('node.provision',             'node',      '创建/导入节点', true),
  ('node.write',                 'node',      '修改节点配置与调度权重', false),
  ('node.lifecycle',             'node',      '排空、隔离、退役与销毁节点', true),
  ('node.identity.revoke',       'node',      '吊销节点身份', true),
  ('node.config.publish',        'node',      '发布节点配置', true),
  ('node.certificate.read',      'node',      '查看节点证书、DNS 凭据与 ACME 设置', false),
  ('node.certificate.write',     'node',      '管理节点证书、DNS 凭据与 ACME 设置', true),
  ('metering.read',              'metering',  '查看用量与配额', false),
  ('metering.reset.read',        'metering',  '查看流量重置记录与统计', false),
  ('metering.reset.write',       'metering',  '手动重置用户流量', true),
  ('ops.ticket.read',            'ops',       '查看工单', false),
  ('ops.ticket.write',           'ops',       '处理工单与内部备注', false),
  ('ops.announcement.write',     'ops',       '发布与撤回公告', false),
  ('ops.notification.read',      'ops',       '查看通知投递积压与健康聚合', false),
  ('ops.notification.write',     'ops',       '管理通知模板与发送', false),
  ('ops.content.write',          'ops',       '编辑自定义页面与知识库', false),
  ('ops.export',                 'ops',       '导出用户数据', true),
  ('ops.dashboard.read',         'ops',       '查看仪表盘待办汇总', false),
  ('marketing.coupon.read',      'marketing', '查看优惠券与兑换记录', false),
  ('marketing.coupon.write',     'marketing', '管理优惠券与礼品码', false),
  ('marketing.giftcard.read',    'marketing', '查看礼品卡模板与卡密', false),
  ('marketing.giftcard.write',   'marketing', '创建礼品卡、批量生码、启停卡密', true),
  ('marketing.commission.read',  'marketing', '查看佣金', false),
  ('marketing.commission.write', 'marketing', '修改分销返佣参数', true),
  ('marketing.withdrawal.approve', 'marketing', '审批提现', true),
  ('security.audit.read',        'security',  '查看审计记录', false),
  ('security.risk.review',       'security',  '复核风险处置', true),
  ('platform.settings.write',    'platform',  '修改系统参数', true),
  ('platform.appearance.read',   'platform',  '查看主题与插槽配置', false),
  ('platform.appearance.write',  'platform',  '修改主题、自定义 CSS 与插槽内容', true),
  ('platform.plugin.read',       'platform',  '查看插件钩子与投递记录', false),
  ('platform.plugin.write',      'platform',  '增删改插件钩子、重发投递', true);

-- 内置角色：沿用旧角色码；授予与收回只经 pandora admin grant / revoke。
INSERT INTO roles (code, name, description) VALUES
  ('platform_admin',    '平台管理员', '拥有全部权限'),
  ('tenant_admin',      '运营管理员', '用户、商品、运营与营销的日常管理'),
  ('finance',           '财务', '订单、账本、调账与提现'),
  ('customer_support',  '客服', '用户查看、工单与公告'),
  ('resource_operator', '资源运维', '节点、配置与证书'),
  ('security_auditor',  '安全审计员', '审计记录与只读核查');

-- 平台管理员拥有全部权限
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
  FROM roles r CROSS JOIN permissions p
 WHERE r.code = 'platform_admin';

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, v.code
  FROM (VALUES
    ('tenant_admin', 'iam.user.read'), ('tenant_admin', 'iam.user.write'),
    ('tenant_admin', 'catalog.read'), ('tenant_admin', 'catalog.write'), ('tenant_admin', 'catalog.publish'),
    ('tenant_admin', 'billing.order.read'), ('tenant_admin', 'billing.payment.read'), ('tenant_admin', 'billing.ledger.read'),
    ('tenant_admin', 'node.read'), ('tenant_admin', 'node.write'), ('tenant_admin', 'node.certificate.read'),
    ('tenant_admin', 'metering.read'), ('tenant_admin', 'metering.reset.read'), ('tenant_admin', 'metering.reset.write'),
    ('tenant_admin', 'ops.ticket.read'), ('tenant_admin', 'ops.ticket.write'), ('tenant_admin', 'ops.announcement.write'),
    ('tenant_admin', 'ops.notification.read'), ('tenant_admin', 'ops.notification.write'), ('tenant_admin', 'ops.content.write'),
    ('tenant_admin', 'ops.dashboard.read'),
    ('tenant_admin', 'marketing.coupon.read'), ('tenant_admin', 'marketing.coupon.write'),
    ('tenant_admin', 'marketing.giftcard.read'), ('tenant_admin', 'marketing.giftcard.write'),
    ('tenant_admin', 'marketing.commission.read'),
    ('tenant_admin', 'security.audit.read'),
    ('tenant_admin', 'platform.appearance.read'), ('tenant_admin', 'platform.appearance.write'),
    ('tenant_admin', 'platform.plugin.read'), ('tenant_admin', 'platform.plugin.write'),

    ('finance', 'iam.user.read'),
    ('finance', 'billing.order.read'), ('finance', 'billing.order.write'), ('finance', 'billing.payment.read'),
    ('finance', 'billing.ledger.read'), ('finance', 'billing.adjustment.write'),
    ('finance', 'marketing.commission.read'), ('finance', 'marketing.withdrawal.approve'),
    ('finance', 'ops.dashboard.read'),

    ('customer_support', 'iam.user.read'),
    ('customer_support', 'billing.order.read'), ('customer_support', 'billing.payment.read'),
    ('customer_support', 'metering.read'), ('customer_support', 'node.read'), ('customer_support', 'node.certificate.read'),
    ('customer_support', 'ops.ticket.read'), ('customer_support', 'ops.ticket.write'), ('customer_support', 'ops.announcement.write'),
    ('customer_support', 'ops.dashboard.read'),

    ('resource_operator', 'node.read'), ('resource_operator', 'node.provision'), ('resource_operator', 'node.write'),
    ('resource_operator', 'node.lifecycle'), ('resource_operator', 'node.identity.revoke'), ('resource_operator', 'node.config.publish'),
    ('resource_operator', 'node.certificate.read'), ('resource_operator', 'node.certificate.write'),
    ('resource_operator', 'metering.read'),
    ('resource_operator', 'ops.dashboard.read'),

    ('security_auditor', 'security.audit.read'), ('security_auditor', 'iam.user.read'),
    ('security_auditor', 'billing.ledger.read'), ('security_auditor', 'node.read'), ('security_auditor', 'node.certificate.read'),
    ('security_auditor', 'ops.dashboard.read')
  ) AS v(role_code, code)
  JOIN roles r ON r.code = v.role_code;

-- +goose Down
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS permissions;
