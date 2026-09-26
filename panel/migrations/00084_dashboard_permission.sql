-- M9：仪表盘「需要处理」的路由权限 ops.dashboard.read（契约后台-01 GET v1/dashboard/tasks）。
--
-- 路由层要一个明确的权限码（IAM-009：每条业务路由都得声明权限）；卡片里每一项
-- 再由处理器按各自原有的读权限逐条过滤，没有对应权限的项不出现。所以这个码
-- 本身只放行「看这张汇总卡」，不带出任何额外数据，授予全部内置角色：
-- platform_admin 的「全部权限」是 00010 执行那一刻展开的，不会自动带上新码，
-- 其余内置角色本来就各有一两项可看。自建角色不动，由租户管理员自己加。

-- +goose Up

-- +goose StatementBegin
INSERT INTO permissions (code, domain, description, high_risk)
VALUES ('ops.dashboard.read', 'ops', '查看仪表盘待办汇总', false)
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, 'ops.dashboard.read'
  FROM roles r
 WHERE r.is_system
ON CONFLICT (role_id, permission_code) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DELETE FROM role_permissions WHERE permission_code = 'ops.dashboard.read';
DELETE FROM permissions WHERE code = 'ops.dashboard.read';
-- +goose StatementEnd
