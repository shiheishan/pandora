---
paths:
  - "panel/internal/domain/adminops/**"
---

# 管理后台用例

- 分工：账本配平、会话吊销这类业务不变量留在 billing / identity，本包只拼后台读模型、把后台写操作编排成带审计的事务。本包不写账本分录：动钱的后台用例（提现打款、调账、人工单）放在 billing 包里
- 同一种行只有一份查询形状，列表、详情、导出复用它，免得一处补了字段另一处漏了：订单行 `orderRowSelectSQL` / `scanOrderRow`（详情 order_detail.go 也复用）、审计 `auditRowSelect` / `auditCond`、通知积压 `scanNotificationBacklog`。订单状态筛选走 `billing.ParseOrderStatuses` 白名单（精确匹配，不当 LIKE）
- 套餐目录用例一律拆成两段：事务外的 `prepare*Input` / `validate*` 负责规整和校验，`*Tx` 事务体假定输入已校验、在调用方事务里执行。新增目录用例照这个拆法，向导（plan_wizard.go、plan_wizard_update.go）才能把多步编排进同一个事务，任一步失败库里不留半成品
- `plan_node_pools` 有两个写入方：`SetPlanPools`（只许改未冻结草稿，`FOR UPDATE OF pv` 锁版本，按 id 顺序 `FOR KEY SHARE` 锁池防死锁，row_version 乐观锁，节点通知由 handler 在提交后发）与向导的 `bindPoolsTx`（只校验池存在且未停用，不锁池行）。改绑池逻辑时两处一起看
- 版本语义：新写入的超额策略只收 suspend（throttle / metered_billing 从没实现过），限速与策略解耦；版本更新请求里带旧的 `pool_ids` 一律拒绝，绑池只走专用端点
- 「一次改完」向导里，设备数与限速是三态 `OptionalInt`（缺省不动、null 不限）；卖点与推荐缺省等于不动；滚出的新版本经 `inheritVersionSemantics` 继承当前版本的全部高级设置；资料整体写入时要带上现有的上架时间窗，否则窗口会被清掉
- 卖点规则只有 plan_highlights.go 一处（去首尾空白、最多 5 条、每条 1–40 字、不空不重），新建、改资料、两个向导共用；数据库 00088 只兜条数与 NULL。切片永不为 nil（pgx 会把 nil 写成 NULL，撞上 NOT NULL）
- 流量包表没有 row_version，乐观锁用触发器维护的 `updated_at`
- 停用用户（单个改状态、风控批量停用）都经 `revokeUserLogins` 吊销会话与 refresh；事务末尾核对仍有有效管理员（守卫 `last_admin_contract_test.go:TestSetUserStatusPreservesLastAdministratorBeforeAudit`）
- 读模型里的来源 IP、收款信息等只给密文，明文由 handler 按表的 AAD 解开；审计表与订阅拉取日志的 IP 哈希盐不同，不能拿一边的哈希去查另一边
- 删用户组前逐项数引用，按 池名单 > 用户 > 套餐 > 价格 > 优惠券 的顺序给 409；查完到删之间被池名单引用的由外键兜住，同样回 409。组的 code 建后不可改（套餐、价格、优惠券按 ID 引用它）
- 后台列表先在主表上按排序键取一页 id（`(tenant_id, created_at DESC, id DESC)` 索引），再只对这一页拼当前订阅、配额、在线设备等读模型；不要把 LATERAL 放在 LIMIT 之前
- 看板流量与节点列表的流量只读小时汇总表（`node_traffic_hourly`、`node_user_traffic_hourly`，入库时在 `ReportTraffic` 同一事务里累加，00099），不在请求时解析 `node_traffic_reports.raw_payload`
- 批量生成用户是后台任务（用户 2026-10-07 定，方案 A；user_generation_jobs.go / user_generation_worker.go，表 00130）：请求只登记任务并写审计，aegis-admin 的 worker 每批 10 个、Argon2 一次只占 1 个全局名额（排不上就等，不让任务失败），事务里只写库
  - 每批的用户、口令、进度与结果密文在一个事务里写，并先按 `attempts`（认领代数）与 `completed` 确认租约还是自己的；崩溃后租约过期由下一次认领从 completed 接着做，不重复建号；认领超过 5 次判失败
  - 结果含明文初始口令：只存信封密文（AAD `UserGenerationResultAAD`），handler 解开写 CSV；只有提交人能下、要近期重认证、每次下载审计；任务结束 24 小时后 worker 清密文，任务行保留
- 没有业务写入的后台敏感动作走 admin_audit.go 先单独写审计再动作（写不进去就不做）：测试发送（`notify.test_sent`）、解开明文来源 IP 的读取（`security.source_ip_viewed`）；用户导出在读名单的同一事务里写 `user.bulk_exported`
