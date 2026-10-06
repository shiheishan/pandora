---
paths:
  - "panel/internal/domain/identity/**"
---

# 身份域

- 非测试代码只依赖 platform（crypto / db / httpx / audit / token / credentialrevocation）和 plugin 的事件发射；需要外部能力（注册验证码投递）时经本包定义的接口注入（`VerificationMailer`），不 import 其它业务域
- 可换取登录的凭据（refresh 令牌、注册令牌、验证码、快捷登录令牌）一律只存哈希，明文只在签发那一刻返回一次
- 注册不可借来探测邮箱（IAM-006）：`StartRegistration` 对已存在的邮箱返回与新邮箱完全一致的响应，但不写验证码、不入队任何邮件。验证码与入队在同一事务，提交后再 `Kick`
- 注册总开关 `feature_switches.auth.registration` 缺行或关闭都按关闭处理；邮箱验证缺行按 `EmailVerificationDefault`（= 迁移种子 false），后台邮件页也必须用这个值（守卫 `email_verify_default_test.go:TestEmailVerificationDefaultMatchesSeed`）
- 门户自助会话管理只能触及 audience=public 的会话，后台会话对门户不可见、不可踢；`last_seen_at` 在本包只读，写入点在认证中间件
- 改密：门户保留当前会话及其 refresh，admin 域吊销全部登录凭据（产品「保留规则 4」）；admin 新密码至少 12 个字符，门户仍是 8 个（`validatePasswordFor`）。旧密码错误的审计要先单独提交，再在事务外返回认证错误，不能随事务回滚
