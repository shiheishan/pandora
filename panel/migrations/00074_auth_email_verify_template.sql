-- 注册验证码邮件模板（契约 M11，缺陷 1）。
--
-- 注册第 1 步一直只把验证码的哈希写进 verification_codes，全仓没有任何
-- 代码把验证码发出去：生产环境一开邮箱验证，新用户就注册不了。
-- 投递走 notify 的按地址入队（此时用户还不存在，收件人是注册时填的邮箱），
-- 渲染按 code 找模板，所以这里补上这一行种子。
--
-- 归 transactional：验证码是完成注册的必要凭据，不能被偏好关掉
-- （也没有偏好可查 —— 收件人还不是用户）。
-- 与 00023 同样按现有租户逐个插入，已存在同键模板则跳过。

-- +goose Up
INSERT INTO notification_templates
  (tenant_id, code, channel, locale, version, subject, body, allowed_variables, category, status)
SELECT t.id, 'auth.email_verify', 'email', 'zh-CN', 1,
       '【{{site}}】注册验证码 {{code}}',
       '你好，

你正在注册 {{site}}，验证码是：

{{code}}

验证码 {{minutes}} 分钟内有效。如果这不是你本人的操作，忽略这封邮件即可。

{{site}}',
       ARRAY['site','code','minutes'], 'transactional', 'active'
  FROM tenants t
 WHERE NOT EXISTS (
   SELECT 1 FROM notification_templates x
    WHERE x.tenant_id = t.id AND x.code = 'auth.email_verify'
      AND x.channel = 'email' AND x.locale = 'zh-CN'
 );

-- +goose Down
-- 这个 code 在本迁移之前不存在，删掉它的全部版本即回到原状。
-- 还没发出的验证码投递一并删掉：模板没了它们只会一直停在 queued，
-- 而 payload 里带着验证码明文与收件邮箱（发出后才会被清空）。
DELETE FROM notification_deliveries
 WHERE template_code = 'auth.email_verify' AND status = 'queued';
DELETE FROM notification_templates
 WHERE code = 'auth.email_verify' AND channel = 'email';
