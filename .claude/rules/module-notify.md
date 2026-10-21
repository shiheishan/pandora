---
paths:
  - "panel/internal/module/notify/**"
---

# 通知、公告与 Telegram

- 「决定要发」与「实际送出」分两步，中间隔着投递表：业务事务里只写一行 queued 投递（`SendToAddress`、`NotifyUser`），提交后入队 `notify:dispatch`；派发失败按退避重试（1、2、4…分钟，封顶 32 分钟）。业务路径不要直接调 SMTP 或 Telegram，发信故障不能让业务回滚。
- 补派用固定 TaskID（`kickTaskID`）：已在排队或运行就不再入队，另有每 30 秒的周期派发兜底；入队失败只记日志。
- 派发认领 `ClaimDeliveries`（`panel/internal/module/notify/repo/deliveries.go`）用 `FOR UPDATE SKIP LOCKED` 把 `next_retry_at` 推到租约之后、`attempts` 加 1：认领即算一次尝试，处理中途崩溃的行也会走到上限，由 `FailExhausted` 收尾。
- `notify.email` 降级开关关闭时派发不认领邮件，记录留在队列里，恢复后按原顺序发。
  - 发信入口也看这个开关（报告 2.1-8，各模块经 `settings.SwitchEnabled(SwitchNotifyEmail)` 读，3 秒缓存）：关闭时要验证邮箱的注册第 1 步、找回密码第 1 步、邮件链接登录第 1 步直接回 503（「邮件发送暂停，暂时无法注册 / 找回密码 / 用邮件登录，请稍后再试」，只看设置、不看邮箱存不存在，不写码不发信）；site-config 的 `password_reset`、signup-config 的 `mail_link_login` 为 false，门户不给这两个入口。找回密码第 2 步不看开关，暂停前发出的验证码照常可用。
- 投递表只存收件地址的 HMAC（`recipientHash`）；地址与变量封存在 `payload_enc`，AAD 绑定投递 id（`deliveryAAD`），防止密文被挪到别的行。日志不带收件地址。
- 每件事的通知都要带去重键（`NotifyUser` 的 `dedupeKey` 必填、不超过 200 字）：先插去重键，撞了就什么都不写。定时扫描每轮都会扫到同一批订阅，靠它保证只通知一次。
  - 到期类的键带周期末 Unix 秒（`expiring:`、`expired:`、`recall:`），流量预警带档位与配额当期起点（`quota:<订阅>:<档>:<起点>`）；不带周期的话续费后的下一期再也发不出。
- 内置模板登记在 `registry.go`，后台只能改内容不能新建；默认内容与迁移 `00007_notify.sql`、`00026_notify_inbox.sql` 的种子逐字一致，「恢复默认」回到代码里的值，改一处必须同改另一处（PG 测试对照）。模板变量走白名单，主题去换行防邮件头注入（`headerSafe`）。
- 模板类别：transactional 不可退订；service 用户可按渠道关；marketing 是营销与群发。
- 扫描 `notify:scan` 约每 5 分钟：先按游标写支付成功通知（`scan_paid.go`，停机多久都不漏，第一次只回看 `paidLookback`），再扫到期提醒、过期召回与流量预警。每轮最多写 `scanBatch` 件，余下的下一轮接着写（一轮写完会超任务时限）。时刻按站点时区显示到分钟；永久的一次性套餐（`subsvc.IsPermanent`）的到期写「永久」（`paidExpiry`）。
  支付成功只有 `order.paid` 一套模板（新购、续费、叠加、换套餐共用）；剩余流量取配额行的 `Remaining`，已含本期赠送（`bonus_value`），TG `/traffic` 的「共」按已用 + 剩余算。
- `SaveMailSettings` 在一个事务里先写 SMTP 设置再写注册设置：开启邮箱验证的校验要读到新的 SMTP 值。
- 公告：已撤回不可再编辑，已发布只能保持发布（`canSaveAs`）；定时公告由 `PublishDueAnnouncements` 每分钟转为已发布。
- Telegram bot（`telegram.go`、`telegram_bot.go`）只认私聊、管理员群组里的回复、用户群的入群申请三种更新，其余一律忽略、不回话：
  - 私聊命令：`/start`（不带参数回后台欢迎文案，空则内置「请发送面板上显示的 8 位绑定码。」；带参数走绑定）、`/traffic`、`/sub`（开关 `telegram.sub_command_enabled`，缺省关：订阅链接等同账号凭据）、`/unbind`（与门户解绑同一个 `unbindTelegram`，审计记本人、via=telegram；本人此刻是管理员时 audience 记 admin）。`/traffic`、`/sub` 要求账号 active（`Directory.ActiveUserGroups`），停用的回「账号已停用」。命令菜单（setMyCommands）在保存 Telegram 设置或扩展设置、webhook 已注册时同步。 webhook 地址 = settings 的 `SiteURL`（主域名）+ `/v1/webhooks/telegram`，不是 https 不注册；后台改了主域名，notify 作为 settings 的 `SiteURLChangeListener` 在提交后用现有 secret 重新 setWebhook（`drop_pending_updates=false`）并同步命令菜单，失败只记日志、不回滚设置（`telegram_settings.go` 的 `SiteURLChanged`）。
  - 入群审核（`telegram.join_review_enabled`，缺省关）：只处理 `telegram.user_group_chat_id` 那个群；已绑定、账号 active 且有生效订阅（`LivePlanIDs`）批准，否则拒绝；读库出错不批也不拒。开关一变就用现有 secret 重设 webhook 的 `allowed_updates`（加或去 `chat_join_request`），这时 `drop_pending_updates=false`，不丢 Telegram 那边排队的更新；只有换 token、换 secret 时才丢。
  - 管理员群组里回复工单提醒：chat.id 必须是管理员群组、发消息的人经 `telegram_bindings.chat_id`（私聊 chat id 就是 TG 用户 id）对到用户、`Admins.IsActiveAdmin` 为真、被回复的消息在 `telegram_ticket_messages` 里找得到工单，任何一步不过都静默忽略；然后经 `TicketReplier`（app 适配器，以该管理员的后台主体调 `support.AgentReply`，后台只读时拒绝）回复。回复与去重键 `tg-admin-reply:<chat_id>:<message_id>`（`notification_events`）在同一事务里：Telegram 重推同一条消息时撞键直接成功，回复失败去重键一起回滚。管理员的绑定与门户同表同流程（后台 `/v1/me/telegram`，发码要重认证）。
  - 管理员吊销会话、改密码都不影响已绑定的 Telegram：TG 回复只看绑定与 `IsActiveAdmin`（在名单里且账号 active），要让某个 TG 账号失效，得解绑、取消管理员或停用账号。
  - 管理员群组、用户群的 chat id 必须是负数（群组与超级群都是负数），后台与 settings 两层校验；正数是私聊，在私聊里回复会被当成绑定码。
- 管理员群组提醒（模板 `admin.order_paid`、`admin.ticket_opened`、`admin.ticket_replied`，开关 `telegram.admin_paid_notify` / `telegram.admin_ticket_notify`，缺省关）同样走投递表：`writeAdminNotice` 写一行 `user_id` 为空、收件地址为群 chat id 的投递（为空才不会被用户解绑时的 `SuppressQueuedTelegram` 压掉），去重键 `admin-paid:<订单>` / `admin-ticket:<消息>`，`notification_events.user_id` 记事件涉及的用户。
  - 收款提醒随 `scan_paid.go` 的支付成功通知在同一事务里写（游标保证不漏不重）；金额 `paidAmount` 是订单总额两位小数加「 元」（order 的 `PaidNotice.Total`，如 `29.90 元`）。
  - 流量重置单（用户 10-14 定）：不给用户写支付成功通知（没有专门模板，门户下单页当场显示结果），收款提醒照发，「套餐」一栏写 `流量重置 · <套餐名>`（模板正文不改，与 00044 种子逐字一致）。实现是只写管理员提醒的扫描条目（`scanItem.Code` 为空、`Key` 用 `admin-paid:<订单>`，`writeScanItems` 只调 `writeAdminNotice`）；收款提醒关着时跳过、游标照样推进。
  - 工单提醒由 support 在建单、用户回复的事务里经 `AdminTicketNotifier` → `NotifyAdminsTicket` 写，附提单人的套餐、剩余流量、到期、余额（经 `SubscriptionSummaries`、`Balances` 批量读这一个人），邮箱打码，正文截到 `adminTicketBodyMax`。这些上下文读失败只记 Warn、写「—」，不拖垮建单与回复；只有写去重键与投递失败才随调用方事务回滚。
  - Bot API 回包最多读 64 KiB（`tgResponseMax`：sendMessage 成功时整条消息按 `\uXXXX` 转义回来，4096 字的中文约 24 KB）；2xx 却解析不了或缺 message_id 记永久失败，不重发，免得群里重复。
  - 派发把工单提醒发出去后，按 Telegram 回的 message_id 写 `telegram_ticket_messages`（payload 里的 `ticket_id`）；映射保留 `TicketMessageRetentionDays`，由 `retention:notify` 清理。
- 数据巡检告警（模板 `admin.integrity_alert`，迁移 00059 种子与 `registry.go` 逐字一致）：report 的 `RunIntegrity` 在自己的事务里调 `NotifyAdminsIntegrity`，变量只有 `check`（中文名）、`code`、`count`，不带样例与业务数据；没有后台开关，`adminChat` 不可发时什么都不写、回 false（report 据此不记告警时的数）。去重键 `integrity:<code>:<这一段违例起点的秒>:<count>`，撞键也回 true。这条去重键不关某个用户：`notification_events.user_id` 自 00059 起可空（`model.Event.UserID` 是指针，`adminNotice.AboutID` 为 `uuid.Nil` 时写空）。
