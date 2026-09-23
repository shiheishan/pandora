# 迁移建了、Go 代码从不引用的表

> 登记簿 | 父级: /panel/CLAUDE.md | 守卫: internal/platform/db/schema_registry_test.go

这些表由迁移创建并保留至今，非测试 Go 源码（internal/、cmd/、web/）里没有任何一处按名字引用它们。
契约测试把三者绑在一起：按序号重放全部迁移的 goose Up 段（CREATE TABLE 加入、DROP TABLE 移出）之后
仍存在的表，要么被 Go 引用，要么登记在下表；登记了却被 Go 引用、或已被后续迁移删除的，必须从这里删掉。
这样“建了表没有代码”永远是显式的事实，不会被当成已实现的功能。

00067_drop_orphan_tables.sql 删掉了其余 21 张真正无依赖的孤儿表（身份扩展、对账、旧计量、旧 webhook 等）。
下面 16 张留着，每张都有一个删除前必须先解开的锁，写在“保留原因”列。解锁（改 invariants.sql、
configure-app-role.sql、在用表外键或冻结契约）本身就是需要用户授权的独立变更。

另有两张表被 Go 引用但从未写入，只做只读依赖计数：`node_templates`（pools.go 删除资源池前计数）
与 `provisioning_runs`（node_admin.go 迁移节点前计数）。它们有引用，不在下表；计数恒为 0。

状态：`孤儿` = 无 Go 引用、也没有迁移内 SQL 函数或触发器读写；`库内使用` = 无 Go 引用，但迁移内的
种子、函数或触发器读写它。

| 表 | 建表迁移 | 状态 | 保留原因 |
| --- | --- | --- | --- |
| approval_decisions | 00009_security_audit.sql | 孤儿 | tests/invariants.sql 用它验证 SEC-013 申请人不能自批；configure-app-role.sql 与 00011 授权 |
| approval_requests | 00009_security_audit.sql | 孤儿 | 00009 三张表外键指向它；tests/invariants.sql 引用 |
| client_releases | 00007_client_delivery.sql | 孤儿 | CLIENT-AUTH-01 冻结契约列出其外键为保留项；CA42 冻结期间不动 |
| cloud_providers | 00005_node_fabric.sql | 孤儿 | 00005 四张在用表外键指向它 |
| invoices | 00004_billing_ledger.sql | 孤儿 | 00036 加了证据守卫触发器与 orders 外键；configure-app-role.sql 授权 |
| order_transitions | 00036_order_reservations.sql | 库内使用 | 订单状态机合法转换表：迁移种子写入，BEFORE UPDATE 守卫函数读取；configure-app-role.sql 授权 |
| organizations | 00002_identity.sql | 孤儿 | 00002、00003、00004 的在用表外键指向它 |
| payment_webhook_receipts | 00036_order_reservations.sql | 孤儿 | configure-app-role.sql 授权；Go 侧回调收据走 payment_events |
| plugins | 00009_security_audit.sql | 孤儿 | tests/invariants.sql 引用；真实钩子用 00051 的 plugin_hooks |
| provisioning_steps | 00005_node_fabric.sql | 孤儿 | configure-app-role.sql 与 00011 授权 |
| quota_adjustments | 00006_metering.sql | 孤儿 | configure-app-role.sql 与 00011 授权；配额调整走人工订单与流量重置 |
| refund_requests | 00036_order_reservations.sql | 孤儿 | refunds.refund_request_id 的 NOT NULL 外键指向它；configure-app-role.sql 授权 |
| risk_events | 00009_security_audit.sql | 孤儿 | CLIENT-AUTH-01 冻结契约规定设备授权各结果与 audit_events 同事务写它 |
| subscription_transitions | 00003_catalog_subscription.sql | 库内使用 | 订阅状态机合法转换表：迁移种子写入，守卫函数读取；tests/invariants.sql 引用；configure-app-role.sql 授权 |
| system_setting_revisions | 00009_security_audit.sql | 孤儿 | configure-app-role.sql 与 00011 授权；系统设置无修订历史 |
| trial_grants | 00003_catalog_subscription.sql | 孤儿 | tests/invariants.sql 引用；试用套餐未实现 |

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
