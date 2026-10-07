-- irreversible: 纯种子数据。这一版只插入邮件与注册验证的设置行，没有结构可回退；删掉这些行会连带删掉管理员已填的 SMTP 配置与加密后的发信密码，不删则 Down 什么也没做，两者都不是真正的回退。
-- forward-fix: 这些设置行对 00029 的代码无害，回到 00029 不需要处理它们；某项设置有误就在后台「系统设置」里改。要整体回到 00030 之前，走升级前备份恢复（panel/deploy/MIGRATION-RUNBOOK.md 第 3 节）。
-- +goose Up
-- 邮件与注册验证的设置项。
--
-- SMTP 参数从环境变量搬到数据库：换一个发信服务商不该需要改 .env 再重启进程。
-- 这也是 xboard 那类面板的通行做法 —— 运营的人未必有服务器权限。
--
-- 密码走 is_secret + secret_encrypted：明文躺在 value 里的话，
-- 任何一次数据库导出都会把发信账号一起带走，而 SMTP 凭据被盗用的
-- 直接后果是自己的域名进垃圾邮件黑名单。

-- 密文项给一个空字节串而不是 NULL：约束要求 is_secret 为真时
-- secret_encrypted 必须存在，而这一行在管理员填密码之前就得先占好位置。
INSERT INTO system_settings (tenant_id, key, value, value_schema, is_secret, secret_encrypted)
SELECT t.id, v.key, v.value, v.schema, v.secret,
       CASE WHEN v.secret THEN ''::bytea ELSE NULL END
  FROM tenants t
  CROSS JOIN (VALUES
    -- 邮箱验证开关。默认关：没配 SMTP 就开验证，等于把注册入口焊死。
    ('auth.email_verification', 'false'::jsonb,
     '{"type":"boolean","title":"注册时验证邮箱"}'::jsonb, false),

    ('mail.smtp_host', '""'::jsonb,
     '{"type":"string","title":"SMTP 服务器"}'::jsonb, false),
    ('mail.smtp_port', '465'::jsonb,
     '{"type":"integer","minimum":1,"maximum":65535,"title":"端口"}'::jsonb, false),
    -- ssl 走 465 直连 TLS，tls 走 587 先明文再 STARTTLS，none 不加密
    ('mail.encryption', '"ssl"'::jsonb,
     '{"type":"string","enum":["ssl","tls","none"],"title":"加密方式"}'::jsonb, false),
    ('mail.smtp_username', '""'::jsonb,
     '{"type":"string","title":"用户名"}'::jsonb, false),
    ('mail.smtp_password', '""'::jsonb,
     '{"type":"string","title":"密码"}'::jsonb, true),
    ('mail.from_address', '""'::jsonb,
     '{"type":"string","title":"发件人地址"}'::jsonb, false),
    ('mail.from_name', '"AegisPanel"'::jsonb,
     '{"type":"string","title":"发件人名称"}'::jsonb, false)
  ) AS v(key, value, schema, secret)
 WHERE NOT EXISTS (
   SELECT 1 FROM system_settings s WHERE s.tenant_id = t.id AND s.key = v.key);

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  RAISE EXCEPTION
    'rollback refused (00030 mail settings seed): deleting the seeded rows would also delete operator-entered SMTP settings and the encrypted password; fix settings forward in the admin panel, or restore the pre-upgrade backup';
END
$$;
-- +goose StatementEnd
