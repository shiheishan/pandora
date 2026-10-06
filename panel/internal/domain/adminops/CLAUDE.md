# panel/internal/domain/adminops/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

管理后台的读写用例
  - 与 billing / identity 分工：那两个包承载业务不变量（账本配平、会话吊销），这里负责把后台要看的数据拼好、把后台的写操作编排成带审计的事务
  - 同一种行（订单行、用户行）只有一份查询形状，列表与详情复用它，避免「一处补了字段、另一处漏了」。

成员清单
service.go: Service 与构造 NewService(pool)
  - 概览（含昨日收入、近 7 天新订阅、节点在线数）、改用户状态（revokeUserLogins 吊销会话与 refresh，与批量停用共用）、套餐列表（active_subscriptions 按 subscription.LiveStatusesSQL 在用计）
orders.go: 订单列表（从 service.go 拆出）：orderRowSelectSQL / scanOrderRow 是 OrderRow 的唯一形状，带余额抵扣、收款渠道（入账优先、其次最近一次支付尝试）与人工单标识 manual
  - 状态多值走 billing.ParseOrderStatuses 白名单，可按 user_id 精确筛
providers.go: 支付渠道卡（从 service.go 拆出）：租户时区今日分币种成交、近 24 小时成功率、最近回调时间，启停带审计
switches.go: 降级开关读写（从 service.go 拆出），数据库拒绝切换时按约束名给中文原因，PG 原句只进日志
access_log.go: 全站访问明细读模型 ListAccessLog（从 api/admin 的 access_log.go 下沉）：同一事务里审计表与订阅拉取日志各取 limit+offset 条，分类前缀、IP 哈希（两表盐不同）、账号与 outcome 筛选由 handler 算好传入
  - 归并、切页、解密、归属地在 handler
audit.go: 审计日志读模型与导出，auditRowSelect / auditCond 是列表、计数、导出共用的唯一形状；带对象可读名（含流量包名）、认证强度（00080）与来源 IP 密文，导出上限 50000 行并同事务记 audit.export
plan_pools.go: 套餐版本 ↔ 节点分组绑定（从 api/admin 的 pools.go 下沉，与 plan_wizard 同为 plan_node_pools 的写入方）：PlanPools 有草稿给草稿、没有给当前发布版且 editable=false
  - SetPlanPools 只许改未冻结草稿（ValidateEditablePlanPoolVersion），FOR UPDATE OF pv 锁版本、按 id 顺序 FOR KEY SHARE 锁池防死锁、row_version 乐观锁，同事务写 plan_version.pools_changed 审计
  - 节点通知由 handler 提交后发
user_profile.go: 用户风控画像与行为时序读模型（从 api/admin 的 profile.go 下沉）：
  - UserActivity（注册 IP、最近 80 条行为、按来源哈希归并的前 20 个 IP 与同 IP 账号）、UserPeers（关联账号邮箱）、UserFetches（最近 50 次订阅拉取与 7 天不同来源数）三次事务与原先一致，后两者出错时返回已读部分
  - ActivityTimeseries 补齐日期、活跃用户两路去重
  - 只给密文，明文由 handler 按表 AAD 解
system_status.go: 系统状态读模型（从 api/admin 的 system_status.go / system_components.go 下沉，契约后台-01）：
  - PingDatabase（SELECT 1，不进租户事务）、DatabaseStats（体积 / 连接数 / 上限同一事务，要么全有要么报错）、SystemCounts（节点、卡住的支付回调、按渠道通知投递同一事务逐项读，失败项为 nil）
  - 组件 ok / warn / down 判定与备份目录探测在 handler
risk.go: 风控共享 IP 聚类：
  - 列聚类与成员（成员 active_plan 取 subscription.ActivePlanNameSQL）、标记为正常（ip_cluster_reviews，30 天）、批量停用（suspended，跳过自己 / 持后台角色者 / 非成员 / 已停用，单事务、末尾核对有效管理员）
catalog.go: 套餐目录读写：套餐资料带卖点 highlights 与推荐 recommended（新建可选、改资料整体覆盖）、归档套餐，以及目录共用的输入输出类型与助手
  - 每个用例拆成「事务外校验（prepare*Input / validate*）+ *Tx 事务体」，事务体只假定输入已校验、在调用方事务里执行，所以向导能把多步编排进一个事务
  - 版本行带建版本人邮箱
catalog_version.go: 版本生命周期（从 catalog.go 拆出）：建草稿、改版本语义（限速与超额策略解耦，新写入的策略只收 suspend；旧 pool_ids 一律拒绝）、发布（套餐与版本双令牌、价格覆盖可见用户组、有池且有可服务节点）
catalog_price.go: 价格（从 catalog.go 拆出）：只有新建与归档，归档带乐观锁；createPlanPriceTx 供向导编排
traffic_packs.go: 流量包目录管理（后台-04 流量包 tab）：列表（带已售单数）、新建、修改、上下架
  - 表没有 row_version，乐观锁用触发器维护的 updated_at
  - 每个写操作同事务记 traffic_pack.* 审计
plan_highlights.go: 卖点规则唯一出处：去首尾空白、最多 5 条、每条 1–40 字、不空不重，字段键 highlights / highlights.{i}，与资料校验错误合并成一次 422；新建、改资料与两个向导共用，数据库 00088 只兜条数与 NULL
plan_wizard.go / plan_wizard_update.go: 一次建成 / 一次改完一个可售套餐，都是单事务（缺陷 12 及其同类）：任一步失败库里不留半成品，新建成功返回建成后的详情
  - 改完的设备数与限速是三态 OptionalInt（缺省不动、null 不限），滚出的新版本经 inheritVersionSemantics 继承当前版本全部高级设置，资料写入保留上架时间窗
  - 卖点与推荐在「一次改完」里缺省 = 不动
order_detail.go: 订单详情与商品快照；列表行部分复用 orderRowSelectSQL，另加开单人 created_by / created_by_email
revenue.go: 收入读模型与收入调整（列表、登记、冲销都带登记人邮箱）；趋势同时回紧邻前一个等长区间的合计
dashboard.go: 仪表盘读模型与流量排行、通知投递积压（scanNotificationBacklog 为唯一口径）
dashboard_tasks.go: 「需要处理」汇总 DashboardTasks：六项各挂原读权限（提现挂 marketing.commission.read），调用方没权限的项不查也不出现；工单等待从用户最后一次发言算，离线节点口径同 GET v1/nodes 的 stale
users.go: 用户列表与详情（从 service.go 拆出）：列表带组、当前订阅摘要（流量、生效设备上限、在线设备），状态多值 / 用户组 / 订阅状态 / q（邮箱、id、订阅令牌哈希反查）筛选
  - 详情带配额、设备、统计（实收另按币种拆成 paid_totals）、邀请人与 Telegram
  - currentSubscriptionSQL / hasLiveSubscriptionSQL / subStateSQL 把 subscription 包的订阅态口径套到别名 u 上，active_plan 从同一行当前订阅派生（在用时取其套餐名，否则 null）
site_settings_store.go: 站点时区读写 SiteTimezone / SetSiteTimezone（即 tenants.timezone，从 api/admin/site_settings.go 下沉）：
  - 改时区先 FOR UPDATE 锁租户行、同事务写 site.timezone_changed 前后对照审计
  - 租户行不可见回中性 404，时区名校验留在 handler
user_groups.go: 用户分组存取（从 api/admin/usergroup.go 下沉）：列表带用户 / 套餐 / 价格 / 优惠券引用计数与限定该组的节点池 exclusive_pools
  - 新建或改名（code 不给改，重复回 409）
  - 删组前逐项数引用，按池名单 > 用户 > 套餐 > 价格 > 优惠券给 409，查删之间被池名单引用由外键兜住同样回 409
  - 换组只认本租户的组
  - 每个写操作同事务写审计，node.users.changed 由 handler 提交后发
bulk_users.go / bulk_mail.go: 用户批量筛选、导出、生成与群发
  - 筛选含当前订阅的套餐、到期天数与订阅状态，has_active_sub 即存在在用订阅（与 sub_state=active 同义），导出的订阅数列按在用计，预览带 sample_rows，群发正文 $email / $plan / $expire 逐人替换
*_test.go: 单元与契约测试
  - catalog_test.go 的 expectHTTPCode 是包内测试共用的错误码断言
  - catalog_sales_pg18_test.go（夹具 openCatalogSalesPG18 与过期 row_version 改资料回 409 不留痕）、plan_wizard_pg18_test.go、plan_wizard_update_pg18_test.go、finance_reads_pg18_test.go、users_pg18_test.go、users_filters_pg18_test.go、users_current_sub_pg18_test.go（订阅态口径：宽限期、欠费、只有过期、两条在用、别的租户，及套餐列表的在用订阅数）、traffic_packs_pg18_test.go、plan_wizard_r92_pg18_test.go（向导继承与三态、限速解耦）与 plan_highlights_pg18_test.go（卖点与推荐）为 PG18 集成测试（run-pg18-gates.sh 的 catalog_sales 域，共用 openCatalogSalesPG18 夹具）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
