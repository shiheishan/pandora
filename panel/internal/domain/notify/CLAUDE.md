# panel/internal/domain/notify/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

通知域：把「决定要通知」与「实际送出去」拆成两步，中间隔着 notification_deliveries。业务事务里只入队（同步、可回滚），Dispatch 在后台取出投递（异步、重试、退避、去重）。收件人通常由 user_id 反查，投递表只存收件人哈希；收件人还不是用户时（注册验证码）走按地址入队，地址与变量随行存在 payload，投递到终态即清空。

成员清单
notify.go: Channel/Sender 抽象、Service 与构造、按用户入队 Enqueue（查退订偏好，返回实际插入的行数）、Render、Dispatch/deliver/markFailed 的派发生命周期；notify.email 降级开关关闭时 Dispatch 跳过邮件渠道（留在队列）
address.go: 按地址入队 EnqueueToAddress（只排邮件、不查偏好、dedupeKey 必填）、withSite 在入队时统一补 {{site}}（生效主题站点名，经 appearance.SiteNameTx）、Kick 让派发循环立刻跑一轮、终态清空 payload 的 SQL 片段
scan.go: 到期 / 流量 / 支付成功三类扫描入队，StartScanner 循环（Kick 只派发不扫描）；返回与日志的条数只数新排的行，重扫为 0；流量预警的可用量 = 套餐本期额度 + 用户流量包剩余（traffic_pack_grants）
template_admin.go: 模板管理端读写与预览、变量白名单，defaultTemplates 与迁移种子逐字一致；草稿预览 PreviewDraft（列出白名单外变量）、草稿实发前校验 RenderDraftForTest、HasDefaultTemplate
announce.go: 用户可见公告与定时发布
mailcfg.go: 数据库里的 SMTP 设置（信封加密口令、短缓存）与按租户动态发信器；未设发件人名时用站点名
smtp.go: 标准库 net/smtp 的邮件渠道
telegram.go: Telegram 渠道与双向验证的账号绑定
*_test.go: 单元测试（address_test.go 守验证码模板种子与 Kick 不阻塞；tenant_seed_test.go 守建租户触发器种下的 12 个模板与 defaultTemplates 逐字一致）；scan_pg18_test.go、dispatch_pg18_test.go 由 run-pg18-gates.sh 的 notify 域跑

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
