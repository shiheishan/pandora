# panel/internal/domain/notify/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

通知域：把「决定要通知」与「实际送出去」拆成两步，中间隔着 notification_deliveries。业务事务里只入队（同步、可回滚），Dispatch 在后台取出投递（异步、重试、退避、去重）。收件人通常由 user_id 反查，投递表只存收件人哈希；收件人还不是用户时（注册验证码）走按地址入队，地址与变量随行存在 payload，投递到终态即清空。

成员清单
notify.go: Channel/Sender 抽象、Service 与构造、按用户入队 Enqueue（查退订偏好，返回实际插入的行数）、Render、Dispatch/deliver/markFailed 的派发生命周期；notify.email 降级开关关闭时 Dispatch 跳过邮件渠道（留在队列）
address.go: 按地址入队 EnqueueToAddress（只排邮件、不查偏好、dedupeKey 必填）、withSite 在入队时统一补 {{site}}（生效主题站点名，经 appearance.SiteNameTx）、Kick 让派发循环立刻跑一轮、终态清空 payload 的 SQL 片段
scan.go: 到期 / 流量 / 支付成功三类扫描入队，StartScanner 循环（Kick 只派发不扫描）；返回与日志的条数只数新排的行，重扫为 0；流量预警的可用量 = 套餐本期额度 + 用户流量包剩余（traffic_pack_grants）
template_admin.go: 模板管理端读写与预览、变量白名单，defaultTemplates 与迁移种子逐字一致；草稿预览 PreviewDraft（列出白名单外变量）、草稿实发前校验 RenderDraftForTest、HasDefaultTemplate
announce.go: 用户可见公告与定时发布
announce_admin.go: 公告后台读写（从 api/admin/announce.go 下沉）：ListAdminAnnouncements 带定向套餐 / 用户组的可读名与选择器数据；SaveAdminAnnouncement / WithdrawAdminAnnouncement 在带操作人的租户事务里校验定向属于本租户、FOR UPDATE 锁行、核对期望版本与状态机（已撤回不可再编辑、已发布只能保持发布）、按 version CAS 更新，同事务写 announcement.saved / announcement.withdrawn 审计（正文只记 sha256）
mailcfg.go: 数据库里的 SMTP 设置（信封加密口令、短缓存）与按租户动态发信器；未设发件人名时用站点名
smtp.go: 标准库 net/smtp 的邮件渠道
telegram.go: Telegram 渠道与双向验证的账号绑定
mail_settings.go: 邮件与注册设置后台存取（从 api/admin/mail.go 下沉）：MailSettings 读 mail.* 与 auth.registration_mode / auth.email_verification（发件人名缺省回退 appearance.SiteNameTx，邮箱验证缺行回退值由调用方传入 identity.EmailVerificationDefault——identity 依赖 notify，反向会成环）；SaveMailSettings 单事务全部 upsert（注册模式先于其他键，与注册流程同一加锁顺序；SMTP 密码行缺失也写得进，R94），同事务写不含凭据的 mail.settings_changed 审计
telegram_settings.go: Telegram 后台配置存取（从 api/admin/telegram.go 下沉）：TelegramAdminChat 读管理员群组 chat id（缺行 nil）；SaveTelegramSettings 单事务 upsert telegram.* 键，admin_chat_id 按「缺省不动 / null 清空」三态写，Bot Token 非空才经信封密封写入
*_test.go: 单元测试（announce_admin_test.go 守公告状态机；address_test.go 守验证码模板种子与 Kick 不阻塞；tenant_seed_test.go 守建租户触发器种下的 12 个模板与 defaultTemplates 逐字一致）；scan_pg18_test.go、dispatch_pg18_test.go 由 run-pg18-gates.sh 的 notify 域跑

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
