---
paths:
  - "panel/internal/module/notify/**"
---

# 通知、公告与 Telegram

- 「决定要发」与「实际送出」分两步，中间隔着投递表：业务事务里只写一行 queued 投递（`SendToAddress`、`NotifyUser`），提交后入队 `notify:dispatch`；派发失败按退避重试（1、2、4…分钟，封顶 32 分钟）。业务路径不要直接调 SMTP 或 Telegram，发信故障不能让业务回滚。
- 补派用固定 TaskID（`kickTaskID`）：已在排队或运行就不再入队，另有每 30 秒的周期派发兜底；入队失败只记日志。
- 派发认领 `ClaimDeliveries`（`panel/internal/module/notify/repo/deliveries.go`）用 `FOR UPDATE SKIP LOCKED` 把 `next_retry_at` 推到租约之后、`attempts` 加 1：认领即算一次尝试，处理中途崩溃的行也会走到上限，由 `FailExhausted` 收尾。
- `notify.email` 降级开关关闭时派发不认领邮件，记录留在队列里，恢复后按原顺序发。
- 投递表只存收件地址的 HMAC（`recipientHash`）；地址与变量封存在 `payload_enc`，AAD 绑定投递 id（`deliveryAAD`），防止密文被挪到别的行。日志不带收件地址。
- 每件事的通知都要带去重键（`NotifyUser` 的 `dedupeKey` 必填、不超过 200 字）：先插去重键，撞了就什么都不写。定时扫描每轮都会扫到同一批订阅，靠它保证只通知一次。
  - 到期类的键带周期末 Unix 秒（`expiring:`、`expired:`、`recall:`），流量预警带档位与配额当期起点（`quota:<订阅>:<档>:<起点>`）；不带周期的话续费后的下一期再也发不出。
- 内置模板登记在 `registry.go`，后台只能改内容不能新建；默认内容与迁移 `00007_notify.sql`、`00026_notify_inbox.sql` 的种子逐字一致，「恢复默认」回到代码里的值，改一处必须同改另一处（PG 测试对照）。模板变量走白名单，主题去换行防邮件头注入（`headerSafe`）。
- 模板类别：transactional 不可退订；service 用户可按渠道关；marketing 是营销与群发。
- 扫描 `notify:scan` 约每 5 分钟：先按游标写支付成功通知（`scan_paid.go`，停机多久都不漏，第一次只回看 `paidLookback`），再扫到期提醒、过期召回与流量预警。每轮最多写 `scanBatch` 件，余下的下一轮接着写（一轮写完会超任务时限）。时刻按站点时区显示到分钟。
- `SaveMailSettings` 在一个事务里先写 SMTP 设置再写注册设置：开启邮箱验证的校验要读到新的 SMTP 值。
- 公告：已撤回不可再编辑，已发布只能保持发布（`canSaveAs`）；定时公告由 `PublishDueAnnouncements` 每分钟转为已发布。
