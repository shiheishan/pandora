---
paths:
  - "panel/internal/domain/notify/**"
---

# 通知域

- 「决定要通知」与「实际送出去」是两步：业务事务里只往 `notification_deliveries` 入队（同步、可回滚），`Dispatch` 在后台投递（重试、退避、去重）。业务路径不要直接调渠道发信，SMTP 故障不能让下单回滚
- 每次入队都要带 dedupeKey（投递表的唯一键），定时扫描每轮都会扫到同一批订阅，靠它保证同一窗口只通知一次；`Enqueue` 返回的是真正新插入的行数
- 投递表只存收件人哈希，收件人由 user_id 反查。注册验证码这类收件人还不是用户的走 `EnqueueToAddress`：只排邮件、不查退订偏好、dedupeKey 必填，地址与变量随行存在 payload，投递到终态时用 `scrubAddressPayloadSQL` 整个清空
- `{{site}}` 在入队时由 `withSite` 统一补（生效主题站点名，经 `appearance.SiteNameTx`），调用方不必各自查；未设发件人名时也用站点名
- `defaultTemplates` 必须与迁移种子、建租户触发器种下的模板逐字一致（守卫 `tenant_seed_test.go:TestTenantSeedTemplatesMatchDefaults`、`address_test.go:TestEmailVerifySeedMatchesDefaultTemplate`）。改模板文案要连迁移一起改
- `notify.email` 降级开关关闭时，Dispatch 跳过邮件渠道、留在队列里，恢复后按原顺序投递；缺行视为开启
- `SaveMailSettings` 先写 `auth.registration_mode` 再写其它键，与注册流程读设置的加锁顺序一致，避免死锁
- 公告的版本状态机：已撤回不可再编辑，已发布只能保持发布；写入按 version CAS，审计里正文只记 sha256（守卫 `announce_admin_test.go:TestAnnouncementLifecycleCannotBypassWithdrawal`）
- 派发认领带租约：`Dispatch` 用一条 `WITH due AS (… FOR UPDATE SKIP LOCKED) UPDATE … SET next_retry_at = now() + 租约 RETURNING` 认领，状态仍是 queued；发完改 sent 或按退避排下次，中途挂了租约一过任一实例重领。插件投递（`plugin.Dispatch`）同一写法。租约（10 分钟）必须长于一轮派发的上限（`dispatchRoundTimeout` 4 分钟）
- 扫描与派发是两个 goroutine（`loops.go`）：扫描每 5 分钟、派发每 30 秒一轮并循环到队列空（有批数上限），`Kick` 只催派发。`StartScanner` 返回 join 函数，public 网关停机时在关资源之前调用
