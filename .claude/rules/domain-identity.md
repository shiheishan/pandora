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
- 改密：门户保留当前会话及其 refresh，admin 域吊销全部登录凭据（产品「保留规则 4」）；后台人员（有任何角色绑定，`iamguard.IsStaff`）不论从门户还是后台改密、或被别人重置，新密码都至少 12 个字符，普通用户 8 个；看账号不看入口（`validatePasswordFor(staff, …)`）。旧密码错误的审计要先单独提交，再在事务外返回认证错误，不能随事务回滚
- 越级：替人重置密码、改账号状态、批量停用同源账号前都过 `iamguard.CanManage`——操作者当前生效的租户级权限必须覆盖目标的全部生效权限，否则 403；批量停用一律跳过后台人员
- Argon2 一律经全局名额：本包先 `acquirePasswordSlot`（排不上回 503）再开事务，事务里用名额的 Hash/Verify；不调 crypto 的包级 HashPassword / VerifyPassword（守卫 `password_gate_test.go`）。改密失败计数复用审计记录：同一账号 15 分钟内旧口令错 5 次回 429
- 按邮箱找人（登录查口令、注册查重，`email_lookup.go`）写 `email_lower = lower($2::text) AND email = $2::citext`，不写裸的 `email = $2`：citext 等号不是 LEAKPROOF，RLS 下不能当索引条件，会把整个租户的用户逐行比一遍。`email_lower` 是 00119 的存储型生成列，索引 `(tenant_id, email_lower)`；附加的 `::citext` 不能省（citext = text 会被解析成区分大小写的 text 等号）。守卫 `email_lookup_pg18_test.go` 断言计划走这个索引
