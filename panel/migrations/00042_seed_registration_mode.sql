-- +goose Up

-- auth.registration_mode 是 registration_policy.go 的必读设置：读不到行就
-- 一律按 closed 处理。这个 fail-closed 本身是对的（宁可关掉也不要因为
-- 配置读了一半就把注册开到公网上），但在此之前没有任何迁移种过这一行，
-- 于是「设置缺失」和「管理员主动关闭」在数据上无法区分 ——
-- 代码一上线注册就静默关闭，管理员还得先摸到邮件设置表单才能打开。
--
-- 这里把状态显式写进库：新装默认 open，与 Xboard 的默认行为一致；
-- 要改成 invite_only 或 closed，走管理端「邮件设置」里的注册方式即可。
-- 已经有值的实例不动（ON CONFLICT DO NOTHING），避免把管理员
-- 主动设的 closed 又翻回 open。

-- +goose StatementBegin
INSERT INTO system_settings (tenant_id, key, value)
SELECT t.id, 'auth.registration_mode', to_jsonb('open'::text)
  FROM tenants t
ON CONFLICT (tenant_id, key) DO NOTHING;

-- 邮箱验证默认关闭：开启它需要先配好 SMTP，否则没人收得到验证码，
-- 等于把注册堵死。管理员配完 SMTP 再自行打开。
INSERT INTO system_settings (tenant_id, key, value)
SELECT t.id, 'auth.email_verification', to_jsonb(false)
  FROM tenants t
ON CONFLICT (tenant_id, key) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DELETE FROM system_settings
 WHERE key IN ('auth.registration_mode', 'auth.email_verification');
-- +goose StatementEnd
