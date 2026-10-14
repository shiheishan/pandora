---
paths:
  - "panel/internal/module/identity/**"
  - "panel/internal/module/auth/**"
---

# 身份（identity）与登录会话（auth）

## 分工
- identity 管用户、口令、注册、找回密码、改密、邀请码、用户组与后台用户管理；auth 管登录、refresh、登出、重认证、快捷登录、管理员名单。会话状态全在 Redis（`panel/internal/module/auth/service/store.go`），PG 里 auth 只有管理员名单。
- 两边互相要的能力走 `panel/internal/module/identity/service/ports.go` 的接口：`SessionRevoker`、`AdminGuard`、`StaffGuard` 由 auth 实现，`VerificationMailer` 由 notify 实现，启动时 `Use*` 注入。改这些接口要同时改实现方。
- 后台权限只分有和没有：在管理员名单里且账号 active 就能做全部后台操作，没有角色和权限码（用户 2026-10-12 定）。

## 账号枚举防护（IAM-006）
- 注册、登录、找回密码三个入口，账号存不存在对外的状态码、正文和耗时都一样：邮箱不存在时 `dummyVerify` 付一次真实校验的代价；找回密码第 1 步对不存在、非 active、管理员的邮箱回同样的响应，但不写码不发信。
- 口令错、用户不存在、停用、登后台的不是管理员，auth 一律回同一个 `errInvalidCredentials`。管理员登门户回中性的 `ErrStaffPortalLogin`，不带后台路径。
- 找回密码第 2 步一切「对不上」（没有码、过期、次数用完、账号不可用）统一回 `errCodeInvalid`。

## 口令与 Argon2id
- Argon2id 走 `passwordHasher` 的两道并发闸（public、admin 各一），排不上回 503；一律在事务外算好再开事务（`panel/internal/module/identity/service/password.go`）。参数内嵌在 PHC 串里，调强后随登录静默重算。
- 口令策略只有长度与「同时含字母和数字」，不加大写、符号组合规则。管理员（`StaffGuard.IsStaff`）至少 12 位：看账号不看入口，从门户改密也一样（`validatePassword`）。
- 改密：15 分钟错 5 次后一律 429；成功后门户保留当前会话、吊销其余，后台连当前会话一起吊销（`ChangePassword`）。

## 会话（Redis）
- refresh cookie 是 `<sid>.<随机串>`，Redis 只存哈希。轮换用 Lua 原子 CAS；`refreshGrace` 内拿上一代 refresh 来只发 access 不轮换，超出宽限再用就是重放，整条会话吊销并写 `session.refresh_reuse` 审计（`refreshReused`）。
- 每个用户有凭据纪元（`uepoch:`）：登录在验口令之前读纪元，建会话时纪元变了（期间改密、被停用）就不建。吊销用户会话一律经 `SessionRevoker.RevokeUser`：事务里先删 Redis 会话，Redis 失败回 `errs.Unavailable` 让业务事务回滚，提交后再扫一遍并推进纪元。
- 后台会话有绝对寿命与空闲超时（`AdminSessionTTL`、`AdminIdleTimeout`），门户只有绝对寿命。`last_seen` 按 `lastSeenEvery` 节流写。
- Lua 脚本会拼出不在 KEYS 里的键，只适用于单实例 Redis（部署就是单实例）；要上集群先改脚本。
- 登录失败计数按账号跨门户与后台共用（邮箱不存在时用邮箱哈希）：先原子占名额再验口令，窗口内第 11 次起口令对也 429；成功清零，没有结论的结果退回名额（`panel/internal/module/auth/service/login.go`）。`pandora admin reset-password` 成功后调 `ClearLoginFailures`。
- 登录失败审计记 anonymous，被撞账号只作 ResourceID；同一邮箱或同一 IP 在窗口内只记一条（`loginFailed`）。

## 管理员与用户管理
- 停用、封禁用户前在同一事务里过 `AdminGuard`：是最后一个有效管理员就 409。批量停用同源账号跳过管理员（`DisableIPClusterAccounts`）。
- 管理员不能走门户自助找回密码，只能用 `pandora admin reset-password`。
- 删用户组先锁组、数组内用户（有就 409），再删；被价格、优惠券、节点池引用时由外键兜住回 409（`DeleteGroup`）。组的 code 建后不可改。
- 批量生成账号是 asynq 任务 `identity:generate_users`：请求只登记任务、写审计、提交后入队；任务 ID 带认领代数 `attempts`，`reconcile:user_generation` 每分钟补排卡住的任务并清过期结果。结果含明文初始口令，只存密文（`generationResultAAD`），只有提交人能下载、要近期重认证、每次下载记审计，24 小时后清密文。
- 用户导出、按条件预览、群发收件人共用同一套 `BulkFilter`；导出审计写不进去就整单失败，不交出名单（`ExportUsers`）。CSV 单元格过 `CSVCell`。
