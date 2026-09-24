# panel/internal/domain/adminops/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

管理后台的读写用例。与 billing / identity 分工：那两个包承载业务不变量（账本配平、会话吊销），这里负责把后台要看的数据拼好、把后台的写操作编排成带审计的事务。同一种行（订单行、用户行）只有一份查询形状，列表与详情复用它，避免「一处补了字段、另一处漏了」。

成员清单
service.go: Service 与构造；概览、用户列表与详情（组名、最近订单）、改用户状态（revokeUserLogins 吊销会话与 refresh，与批量停用共用）、订单列表（orderRowSelectSQL / scanOrderRow 是 OrderRow 的唯一形状，带余额抵扣与收款渠道：入账优先、其次最近一次支付尝试；状态多值走 billing.ParseOrderStatuses 白名单，可按 user_id 精确筛）、套餐与渠道（渠道卡带租户时区今日分币种成交、近 24 小时成功率、最近回调时间）、降级开关
audit.go: 审计日志读模型与导出，auditRowSelect / auditCond 是列表、计数、导出共用的唯一形状；带对象可读名、认证强度（00080）与来源 IP 密文，导出上限 50000 行并同事务记 audit.export
risk.go: 风控共享 IP 聚类：列聚类与成员、标记为正常（ip_cluster_reviews，30 天）、批量停用（suspended，跳过自己 / 持后台角色者 / 非成员 / 已停用，单事务、末尾核对有效管理员）
catalog.go: 套餐目录读写与上下架；每个用例拆成「事务外校验（prepare*Input / validate*）+ *Tx 事务体」，事务体只假定输入已校验、在调用方事务里执行，所以向导能把多步编排进一个事务；版本行带建版本人邮箱
plan_wizard.go / plan_wizard_update.go: 一次建成 / 一次改完一个可售套餐，都是单事务（缺陷 12 及其同类）：任一步失败库里不留半成品，新建成功返回建成后的详情
order_detail.go: 订单详情与商品快照；列表行部分复用 orderRowSelectSQL，另加开单人 created_by / created_by_email
revenue.go: 收入读模型与收入调整（列表、登记、冲销都带登记人邮箱）
dashboard.go: 仪表盘读模型与流量排行
bulk_users.go / bulk_mail.go: 用户批量筛选、导出、生成与群发
*_test.go: 单元与契约测试；catalog_sales_pg18_test.go、plan_wizard_pg18_test.go、plan_wizard_update_pg18_test.go、finance_reads_pg18_test.go 与 users_pg18_test.go 为 PG18 集成测试（run-pg18-gates.sh 的 catalog_sales 域，共用 openCatalogSalesPG18 夹具）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
