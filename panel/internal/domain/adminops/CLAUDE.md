# panel/internal/domain/adminops/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

管理后台的读写用例。与 billing / identity 分工：那两个包承载业务不变量（账本配平、会话吊销），这里负责把后台要看的数据拼好、把后台的写操作编排成带审计的事务。同一种行（订单行、用户行）只有一份查询形状，列表与详情复用它，避免「一处补了字段、另一处漏了」。

成员清单
service.go: Service 与构造；概览（含昨日收入、近 7 天新订阅、节点在线数）、改用户状态（revokeUserLogins 吊销会话与 refresh，与批量停用共用）、订单列表（orderRowSelectSQL / scanOrderRow 是 OrderRow 的唯一形状，带余额抵扣、收款渠道（入账优先、其次最近一次支付尝试）与人工单标识 manual；状态多值走 billing.ParseOrderStatuses 白名单，可按 user_id 精确筛）、套餐与渠道（渠道卡带租户时区今日分币种成交、近 24 小时成功率、最近回调时间）、降级开关
audit.go: 审计日志读模型与导出，auditRowSelect / auditCond 是列表、计数、导出共用的唯一形状；带对象可读名（含流量包名）、认证强度（00080）与来源 IP 密文，导出上限 50000 行并同事务记 audit.export
risk.go: 风控共享 IP 聚类：列聚类与成员、标记为正常（ip_cluster_reviews，30 天）、批量停用（suspended，跳过自己 / 持后台角色者 / 非成员 / 已停用，单事务、末尾核对有效管理员）
catalog.go: 套餐目录读写与上下架；套餐资料带卖点 highlights 与推荐 recommended（R100，新建可选、改资料整体覆盖）；版本语义里限速与超额策略解耦，新写入的策略只收 suspend（R99）；每个用例拆成「事务外校验（prepare*Input / validate*）+ *Tx 事务体」，事务体只假定输入已校验、在调用方事务里执行，所以向导能把多步编排进一个事务；版本行带建版本人邮箱
traffic_packs.go: 流量包目录管理（后台-04 流量包 tab）：列表（带已售单数）、新建、修改、上下架；表没有 row_version，乐观锁用触发器维护的 updated_at；新建、修改、上架过 P0B 销售闸门，下架不过；每个写操作同事务记 traffic_pack.* 审计
plan_highlights.go: 卖点规则（R100）唯一出处：去首尾空白、最多 5 条、每条 1–40 字、不空不重，字段键 highlights / highlights.{i}，与资料校验错误合并成一次 422；新建、改资料与两个向导共用，数据库 00088 只兜条数与 NULL
plan_wizard.go / plan_wizard_update.go: 一次建成 / 一次改完一个可售套餐，都是单事务（缺陷 12 及其同类）：任一步失败库里不留半成品，新建成功返回建成后的详情；改完的设备数与限速是三态 OptionalInt（缺省不动、null 不限），滚出的新版本经 inheritVersionSemantics 继承当前版本全部高级设置，资料写入保留上架时间窗（R92）；卖点与推荐在「一次改完」里缺省 = 不动（R100）
order_detail.go: 订单详情与商品快照；列表行部分复用 orderRowSelectSQL，另加开单人 created_by / created_by_email
revenue.go: 收入读模型与收入调整（列表、登记、冲销都带登记人邮箱）；趋势同时回紧邻前一个等长区间的合计
dashboard.go: 仪表盘读模型与流量排行、通知投递积压（scanNotificationBacklog 为唯一口径）
dashboard_tasks.go: 「需要处理」汇总 DashboardTasks：六项各挂原读权限（提现挂 marketing.commission.read），调用方没权限的项不查也不出现；工单等待从用户最后一次发言算，离线节点口径同 GET v1/nodes 的 stale
users.go: 用户列表与详情（从 service.go 拆出）：列表带组、当前订阅摘要（流量、生效设备上限、在线设备），状态多值 / 用户组 / 订阅状态 / q（邮箱、id、订阅令牌哈希反查）筛选；详情带配额、设备、统计（实收另按币种拆成 paid_totals，R80）、邀请人与 Telegram；currentSubscriptionSQL / subStateSQL 是「当前订阅」与订阅状态的唯一口径
bulk_users.go / bulk_mail.go: 用户批量筛选、导出、生成与群发；筛选含当前订阅的套餐、到期天数与订阅状态，预览带 sample_rows，群发正文 $email / $plan / $expire 逐人替换
*_test.go: 单元与契约测试；catalog_sales_pg18_test.go、plan_wizard_pg18_test.go、plan_wizard_update_pg18_test.go、finance_reads_pg18_test.go、users_pg18_test.go、users_filters_pg18_test.go、traffic_packs_pg18_test.go、plan_wizard_r92_pg18_test.go（向导继承与三态、限速解耦）与 plan_highlights_pg18_test.go（卖点与推荐）为 PG18 集成测试（run-pg18-gates.sh 的 catalog_sales 域，共用 openCatalogSalesPG18 夹具）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
