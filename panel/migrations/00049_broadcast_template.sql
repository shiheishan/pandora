-- +goose Up

-- 群发邮件的「直通」模板（对标 Xboard user/sendMail）。
--
-- 派发器是按 (template_code, channel) 查模板、再用 payload 里的变量渲染的。
-- 群发的内容是管理员当场写的，不属于任何预置模板，所以需要一个
-- 主题和正文都只有一个变量的壳：真正的内容随 payload 走。
--
-- category 定为 marketing 而不是 transactional：群发是运营内容，
-- 用户有权在通知偏好里关掉它。用 transactional 能绕过偏好检查，
-- 但那是给「你的密码被修改了」这类必须送达的通知准备的，
-- 拿来发促销是把用户的退订当没看见。

-- +goose StatementBegin
INSERT INTO notification_templates
  (tenant_id, code, channel, locale, version, subject, body,
   allowed_variables, category, status)
SELECT t.id, 'admin.broadcast', 'email', 'zh-CN', 1,
       '{{subject}}', '{{body}}',
       ARRAY['subject','body'], 'marketing', 'active'
  FROM tenants t
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down

DELETE FROM notification_templates WHERE code = 'admin.broadcast';
