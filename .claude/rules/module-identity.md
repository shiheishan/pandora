---
paths:
  - "panel/internal/module/identity/**"
  - "panel/internal/module/auth/**"
---

# 身份（identity）与登录会话（auth）

## 分工
- identity 管用户、口令、注册、找回密码、改密、邀请码、用户组与后台用户管理；auth 管登录、refresh、登出、重认证、快捷登录、管理员名单。会话状态全在 Redis（`panel/internal/module/auth/service/store.go`），PG 里 auth 只有管理员名单。
- 两边互相要的能力走 `panel/internal/module/identity/service/ports.go` 的接口：`SessionRevoker`、`AdminGuard`、`StaffGuard` 由 auth 实现，`VerificationMailer` 由 notify 实现，启动时 `Use*` 注入。改这些接口要同时改实现方。
- 注册试用、注销与解绑走 `ports_xb.go`：`TrialGranter`、`AccountCloser` 由 subscription 实现，`OrderCanceller` 由 order 实现，`CommissionVoider`（注销时锁佣金、作废未打款的佣金与申请中的提现，有已批准未打款的提现时拒绝）由 commission 实现，`TelegramUnbinder` 由 notify 实现；没注入时调用处回 Internal，`MissingPorts` 会列出来（wire.go 启动时检查）。
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
- 邮件链接免密登录（`panel/internal/module/auth/service/mail_link.go`，`signup.mail_link_login` 开且配好发信才可用，否则 403）：第 1 步读开关用 `SignupWithoutSecret`（不解封 secret）；令牌一次性、15 分钟、Redis 只存哈希、同一用户只留最新一条、签发时记凭据纪元（之后改密、停用、后台改邮箱、注销即失效）；第 1 步开了人机验证时先过人机验证（auth 经 `MailLinkCaptcha` 端口调 identity 的 `VerifyCaptcha`，action `mail_login`，放在限频之前），再按邮箱 15 分钟 3 次限频，邮箱存不存在回同样的响应；第 2 步与快捷登录一样建门户会话（`amr` 记 `mail_link`），管理员不能用。

## 管理员与用户管理
- 停用、封禁用户前在同一事务里过 `AdminGuard`：是最后一个有效管理员就 409。批量停用同源账号跳过管理员（`DisableIPClusterAccounts`）。
- 管理员不能走门户自助找回密码，只能用 `pandora admin reset-password`。
- 删用户组先锁组、数组内用户（有就 409），再按引用统计拦节点池名单、套餐可见名单、优惠券（409，`ensureGroupUnused`），再删（`DeleteGroup`）。用户组专属价已删（10-16，00056 删 `prices.user_group_id`），组的引用统计只剩套餐可见、优惠券、节点池。组的 code 建后不可改。
- 用户组停用（用户 10-14 定 B，`user_groups.status` active / disabled，`SetGroupStatus`，`POST /v1/user-groups/:id/status`，幂等、写审计 `user_group.status_changed`，可再启用）：只挡新成员——`AssignGroup` 对不在该组的用户回 409（本来就在组里再提交同组不拦），批量生成入队回 422 `fields.group_id`「分组已停用」、执行时复查（`errGenerationGroupDisabled`，任务失败）；读组状态用 `repo.GroupStatus`（FOR SHARE，与停用、删组互斥）。已有成员完全不受影响：门户、订阅拉取、节点名单、订阅域名都不看 status。后台各处的分配下拉隐藏停用组（前端，见 screens-admin-users）。
- 批量生成账号是 asynq 任务 `identity:generate_users`：请求只登记任务、写审计、提交后入队；任务 ID 带认领代数 `attempts`，`reconcile:user_generation` 每分钟补排卡住的任务并清过期结果。结果含明文初始口令，只存密文（`generationResultAAD`），只有提交人能下载、要近期重认证、每次下载记审计，24 小时后清密文。
- 用户导出、按条件预览、群发收件人共用同一套 `BulkFilter`；导出审计写不进去就整单失败，不交出名单（`ExportUsers`）。CSV 单元格过 `CSVCell`。

## 注册与登录设置（settings 的 signup 组，2026-10-14）
- 人机验证（reCAPTCHA v2 / v3、Turnstile）在注册第 1 步、找回密码第 1 步、邮件链接登录第 1 步校验（`captcha.go`），没开时不查；第三方校验接口连不上回 422 `fields.captcha`，不放行。服务商回的 `hostname` 必须等于 `public_base_url` 的主机名（取不到主机名时按不可用处理）；v3 的 `action` 必须与入口一致（`register`、`password_reset`、`mail_login`，前端 `getToken` 传同一个值）。注册第 1 步先做 IP 限额再调第三方（少打一次出站请求；代价是共用出口 IP 时不过人机验证的脚本也能耗光这个 IP 的注册额度，影响只限这个 IP）；找回与邮件链接第 1 步先过人机验证，再按邮箱限频（按邮箱的额度不能让没过人机验证的请求占用，否则换 IP 就能锁住别人的找回）。secret 只经 Sealer 封存，后台只回 `has_captcha_secret`。门户入口页 CSP 的放行来源在 `panel/internal/app/portal_csp.go`。
- 邮箱规则在 `signup_policy.go`：后缀白名单开时域名必须在名单里；Gmail 别名限制只管 gmail.com / googlemail.com 的 `.` 与 `+`。注册第 1 步按 IP 限额（缺省开、10 次 / 1 分钟、可关），路由上的 `reg_start` 只留全局维度。
- 注册试用在 `CompleteRegistration` 的同一事务里调 `TrialGranter`，每人终身一次（`subscription_trials` 主键）；套餐不可用时 subscription 记日志跳过，不挡注册。
- `signup.invite_never_expire` 关时个人邀请码用一次就失效（`ConsumeInvite(id, singleUse)`）；后台发的码不受影响。

## 邀请（2026-10-14）
- 一人可同时持有多个 active 个人码，上限 `signup.invite_max_codes`（缺省 5）；`MyInvite` 懒生成第一个码前先锁用户行，`CreateInviteCode` 在锁内数 active 再插。
- 邀请访问量 `RecordInviteVisit`：同一 IP 对同一码 24 小时只计一次，Redis 键是 IP 与码的 HMAC，不存 IP 原文；任何失败都静默返回（访问量只是参考），路由限流不加 StrictMode。
- referrals 只追加：后台改邀请人写 `referral_changes`（只追加），「当前邀请人」一律读视图 `effective_referrals`（`Referrer`、`ReferralCount`、`Invitees` 都已改读它）。改邀请人先锁全局邀请图（`LockReferralGraph`），沿视图往上查 100 层防环，不能设成自己或自己的下线；只影响以后订单的计佣。

## 后台改用户与注销（2026-10-14）
- 改邮箱（要原因、撞号 409、改后未验证；同一事务里作废发往旧邮箱的找回码、吊销全部会话并推进纪元，邮件登录链接随之失效；找回第 2 步另核码对应的邮箱仍是用户现在的邮箱）、改显示名与备注（备注只在后台详情出现，审计只记字段与长度）、改邀请人、注销都在 `admin_edit.go` / `referrer.go`，各写一条审计。审计不记明文邮箱：改邮箱只记打码值（`maskEmail`）与 `auditEmailHMAC`（`InfoHMACIdentity`，前缀 `audit-email:`）。
- 占位邮箱的域名 `deleted.invalid`（任何大小写）在注册、命令行建号、批量生成、后台改邮箱一律拒（`reservedEmail`），否则有人抢注 `DeletedEmail(某人)` 会让那个人注销时撞唯一约束。
- 注销 `DeleteUser` 不可撤销：同一事务里锁用户 → `CommissionVoider.LockForDeletion`（拿 commission 的用户咨询锁）→ `OrderCanceller` 取消全部待付订单（order 释放资源、作废在途支付意图，原因 `account_deleted`；之后到的付款回调走迟到付款挂账，不开订阅、不计佣金）→ `AccountCloser` 停订阅 → `CommissionVoider.VoidForDeletedUser` 作废名下未打款的佣金（用户 10-14 定：冻结中、待复核的条目逐条作废；有已批准待打款的提现就拒绝注销（409），申请中的驳回；驳回后剩下的可提现佣金作废；原因都是 `account_deleted`；已打款、已转余额的不动，口径与锁序见 module-order）→ 解绑 Telegram → 吊销会话 → 作废找回码 → 匿名化（占位邮箱 `DeletedEmail`、清显示名与备注、`status='deleted'`、`deleted_at`）→ 删口令 → 停用他的邀请码 → 插件事件 `user.deleted`（只带 `user_id` 与 `deleted_at`）；审计 `user.deleted` 另记作废条目数 `voided_commission_entries`、条目金额 `voided_commission_pending`、作废的可提现 `forfeited_commission`、驳回提现数 `rejected_withdrawals` 与金额 `rejected_withdrawal_amount`（金额都是 "0.00" 形式的字符串）；订单、账本、佣金与提现记录、审计、邀请关系保留（作废只追加冲回分录、改状态）。不能注销自己与管理员。锁序与别处一致：先用户行（FOR NO KEY UPDATE），再佣金咨询锁（取消待付单会锁他的余额科目，转余额是拿着这把锁再锁余额，所以要先拿），再订单行（与付款结算「先订单、后订阅」同序），再按 id 升序锁订阅行；作废佣金要锁平台收入，放在停完订阅之后（结算先锁库存、券、订阅再锁平台收入）、改占位邮箱之前（那时用户行只是 NO KEY UPDATE，与计提插条目时外键的 KEY SHARE 相容）；改占位邮箱（唯一列）的 UPDATE 放在订阅都停完之后，此时锁才升到 FOR UPDATE，用户自己操作订阅（先锁订阅、再插 actor 指向他的事件行）不会与之互等。界面显示「已注销 #<id 前 8 位>」（`DeletedShortID`，与前端 `shortId` 同口径）。
- 已注销账号：`SetStatus`、`AdminResetPassword`、`AssignGroup`、改邮箱、改备注、改邀请人一律 409「账号已注销」；别的模块对用户的写入口（order 下单与后台开单、ledger 后台加钱、giftcard 兑换、subscription 后台改订阅）在各自 service 里经 `IsDeleted` 判断、同码同文案 409；ledger 后台扣减不拒（注销账号只能扣减，用于线下退款后冲账）。`IsDeleted` 读时给用户行加共享锁（FOR SHARE）并持有到调用方提交，与注销互斥；调用方必须在事务里第一个调（先于锁自己的行），锁序才与注销一致。commission 对已注销账号：门户转余额、提现申请，自动转余额，后台批准提现、标记已打款、通过待复核条目一律 409「账号已注销」（无锁的 `AccountDeleted`，各自先锁住会与注销作废冲突的行再读）；后台驳回提现、作废条目照常。计提时已注销的邀请人等同比例 0（跳过、仍占那一级），读的是无锁的 `AccountDeleted`：结算事务那时已持有库存、券等行锁，不能再等用户行锁（理由见 module-order）；批量筛选不带状态时排除已注销，IP 聚类停用跳过已注销。
- 批量封禁 `BulkBan` 复用 `BulkFilter`：至少一个条件；命中人数必须等于预览时的 `expected_total`；一次最多 `BulkBanMax`（500）人，同步执行；自己、管理员、已封禁、已注销跳过并计数；路由要 reauth 与幂等键 `user_bulk_ban`。

## 溯源总览（2026-10-20）
- 读服务在 `service/trace.go`（编排）与 `trace_merge.go`（纯函数），类型在 `trace_types.go`；两路数据：审计经 `auditOps.TraceIPs` / `TraceUsers`，拉取日志经端口 `TraceFetches`（subscription 实现，`UseTraceFetches` 注入，`MissingPorts` 会列）。两路 HMAC 盐不同，按解封后的明文（`normTraceIP`）合并，不比哈希。
- IP 排行：两路各取头部 `TraceScanIPs`（5000），拉取一路额外带回审计头部 200 个 IP，审计一路再补拉取头部 200 个里缺的（`Limit 0` + `ExtraIPs`）；任一路独立 IP 数超过 5000 时概览的独立 IP 数是估算（`DistinctIPsApprox`）。UA 种类两路相加不去重；关联账号每路明细最多 20 个，超了 `AccountsCapped`。
- 账号排行：缺省按关联 IP（两路 IP 合并去重）；共用 IP 只在各自一路内判断再按明文去重；按「机房命中」排时只在关联 IP 选出的候选里重排。
- IP 只给打码串（`maskIP`：IPv4 `a.b.*.*`，IPv6 前两段加 `:*`，前端 `users/RiskTab.tsx` 的 `maskIp` 同口径）与「显示」令牌：`base64url(Seal("unix|ip", AAD "identity.trace_reveal:"+管理员 id))`，2 小时有效，换管理员解不开。`TraceReveal` 先写 `security.source_ip_viewed`（`data.view = trace`、`data.masked`）再返回明文；令牌失效回 422 `fields.token`。
- 路由 `GET /v1/trace/{ips,subscribers,accounts,reveal}` 挂在后台安全页同组，只要 `RequireAuth`；window / sort 的取值在 service 校验（422 `fields.window` / `fields.sort`）。
