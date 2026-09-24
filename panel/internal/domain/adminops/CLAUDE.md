# panel/internal/domain/adminops/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

管理后台的读写用例。与 billing / identity 分工：那两个包承载业务不变量（账本配平、会话吊销），这里负责把后台要看的数据拼好、把后台的写操作编排成带审计的事务。同一种行（订单行、用户行）只有一份查询形状，列表与详情复用它，避免「一处补了字段、另一处漏了」。

成员清单
service.go: Service 与构造；概览、用户列表与详情（组名、最近订单）、改用户状态、订单列表（orderRowSelectSQL / scanOrderRow 是 OrderRow 的唯一形状）、套餐与渠道、审计、降级开关
catalog.go: 套餐目录读写与上下架
plan_wizard.go / plan_wizard_update.go: 一次建成 / 一次改完一个可售套餐
order_detail.go: 订单详情与商品快照
revenue.go: 收入读模型
dashboard.go: 仪表盘读模型与流量排行
bulk_users.go / bulk_mail.go: 用户批量筛选、导出、生成与群发
*_test.go: 单元与契约测试；catalog_sales_pg18_test.go 与 users_pg18_test.go 为 PG18 集成测试（run-pg18-gates.sh 的 catalog_sales 域）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
