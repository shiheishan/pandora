---
paths:
  - "panel/internal/module/report/**"
  - "panel/internal/module/audit/**"
---

# report 与 audit

## report 边界与读数

- `panel/internal/module/report/service/service.go` 包注释：`report` 只拥有 `revenue_report_adjustments`、`activity_daily`、`integrity_checks`；看板其余数字一律经 `Deps` 里各属主 `service` 的聚合读，**不读别人的表**。
- 切日、营收按日、行为趋势一律用 `Service.location` → `settings` 的 `site.timezone`（`time.LoadLocation`）；解析失败回 500，**不悄悄退回 UTC**（`service.go` 的 `location`）。
- 站点时区写入校验在 `panel/internal/module/settings/service/site.go` 的 `validTimezone`：拒空串、`Local`、无法 `LoadLocation` 的名字。
- 看板节点/用户流量排行：`Service.NodeTraffic` / `UserTraffic` 只调 `deps.Nodes.TrafficByNode`、`TrafficBySubscription`（小时汇总表，属主 `node`），不在请求里解析原始上报 payload（`service/traffic.go`）。

## 行为趋势（activity_daily）

- 表语义见 `panel/db/migrations/00032_report.sql`：按 `(tz, day)` 存派生读数；站点时区一改，旧 `tz` 行自动不用，读路径对没有行的日子现算。
- **定稿**：`service/activity.go` 的 `finalized`——`computed_at` 不早于该自然日结束后整一天（与迁移注释同口径）；只用于写路径判断跳过。
- **读路径**：`ActivityTimeseries` 用库里当前时区的全部行（定稿与否都用），请求里不现算；只有没有行的日子（刚装好、任务没跑过、站点时区刚改）才 `liveActivity`（audit `ActivityByDay` + subscription `ActiveUsersByDay`）。今天、昨天的数字最多晚一个任务周期（10 分钟）。
- **写路径**：`RefreshActivity` 补历史未定稿日（大前天及更早从新到旧最多 7 天）+ 重算前天、昨天与今天；已定稿跳过。
- 定时：`panel/internal/module/report/task/task.go`——`report:activity_daily` 每 10 分钟；`retention:activity_daily` 每天删 `ActivityRetentionDays`（400）以前，`PurgeActivity` 分批。
- 动作名：`activityActions` 用 `identitysvc.ActionRegistered`、`ordersvc.ActionOrderCreated`，登录成功用本包常量 `LoginAction`（`"user.login"`，与 auth 写入一致）（`service/activity.go`）。

## 数据巡检（integrity_checks，2026-10-20）

- 定时 `report:integrity`（`task/task.go`，cron `19,49 * * * *`，避开 `commission:settle` 的第 7 分；low 队列、不重试、5 分钟超时、Unique 29 分钟）调 `service/integrity.go` 的 `RunIntegrity`。
- 检查本身是各属主 `service` 的只读方法、SQL 在属主 `repo/integrity.go`，返回 `internal/pkg/integrity` 的 `Result`（违例数 + 至多 5 个样例 id，新的在前）；report 只经 `Deps` 调，不读别人的表。九项与属主：`ledger_drift`、`ledger_hold_mismatch`（ledger）；`order_paid_unfulfilled`、`order_reservation_mismatch`（order）；`payment_unsettled`（payment 给近 7 天没进挂账的收款 → order 的 `UnsettledAmong` 核状态）；`commission_unpaid_order`（commission 给近 7 天没作废的条目 → 同上）；`giftcard_redemption_mismatch`（giftcard）；`subscription_expire_overdue`、`subscription_close_overdue`（subscription，宽限 `IntegrityGrace` 10 分钟）。跨模块的两项回看 `IntegrityWindow`（7 天），订单 id 去重后每批 `unsettledChunk`（5000）个交给 order。
- 每项一个短事务：`LockIntegrityCheck`（FOR UPDATE）→ 决定 → 发告警 → `SaveIntegrityCheck`（upsert），告警的去重键与投递随同一事务提交。某项检查失败只记日志、跳过这一项（表里保留上次结果），其余照跑，最后 `errors.Join`；单条检查语句受应用账号 15 秒 statement_timeout 限制。失败不另发告警，靠 `checked_at` 落后：后台「要处理」在某项 2 小时没更新时标红（`screens-admin-dash.md`）。
- 告警规则：违例数 > 0 且大于这一段的 `alerted_count` 才经 notify 的 `NotifyAdminsIntegrity` 发（0→非 0 发、变多再发、不变或变少不发）；回到 0 清 `failing_since` 与 `alerted_count`、不发；notify 回 false（没配管理员群组）时不记 `alerted_count`，配好后下一轮照发。去重键 `integrity:<code>:<failing_since 秒>:<count>`，前后两段违例不会撞键。
- 表的形状由库约束钉住（迁移 00059）：代号格式、`(count = 0) = (failing_since IS NULL)`、0 时 `alerted_count = 0` 且样例为空、样例最多 5 个且不多于违例数、`failing_since <= checked_at`。
- 总览待办（`tasks.go`）的 `integrity` 条目只读这张表，只给 `integrityChecks()` 里还登记着的代号、按登记顺序；没跑过的检查不给。巡检告警的通知事件 `user_id` 为空，`writeAdminNotice` 只许 `admin.integrity_alert` 这样传，别的管理员提醒缺用户 id 报 Internal。后台概览的 `ledger_drift_accounts` 仍现算（`Ledger.CheckDrift`）。
- 加或改一项检查：属主 repo/service 写只读方法与 PG 用例（造一条违例计数 +1、样例里有它，正常数据不加），`integrityChecks()` 登记代号与中文名，前端 `screens/dash/model.ts` 的 `INTEGRITY_CHECKS` 同步，运维手册 `panel/deploy/RUNBOOK.md` 第 16 章补一节（是什么、只读确认、处理、修好之后）。代号一改等于换一项新检查。

## 营收报表调整

- `revenue_report_adjustments` 只追加；冲正 = 新插一行、金额取反、生效日同原行；表上 `reversal_of` 唯一。业务规则在 `service/adjustments.go`（`LockAdjustment` 咨询锁、`HasReversal`）；幂等在 HTTP 中间件，表上不存幂等键（迁移 00032）。
- 登记/冲正与审计同事务：`audit.Write` 动作为 `revenue.adjustment.create` / `revenue.adjustment.reverse`。

## audit 写入与表

- **动作代号与前端中文名对照**：守卫 `service/actions_frontend_test.go` 的 `TestAuditActionsMatchFrontend`——`service/actions_scan_test.go` 用 go/ast 扫 `panel/internal` 全部非测试文件里写进 audit `Event.Action` 的代号（字面量、本包或别包常量、包装函数参数如 `nodeAudit(ctx, "node.create", …)`、局部变量、结构体字段如 order 的 `AuditAction`），与前端 `screens/security/logic.ts` 的 `ACTION_NAMES` 双向比对（前端多出的只许 `frontendOnlyActions` 里的访问日志伪动作 `subscription.fetch`）。**加、改名、删审计动作要同改前端表**；守卫解析不了的写法（多返回值赋给动作变量、从 map 或函数返回值取动作）直接判红，改成常量 / 字面量，或扩展扫描器。
- 业务模块只经 `panel/internal/module/audit/service` 的 `Service.Write`（→ `repo.Insert`）写 `audit_events`；无哈希链（设计见 `docs/architecture.md` §1 决定 12）。
- 表只追加：`panel/db/migrations/00003_audit.sql` 的 `audit_events_append_only`；清理只走 `purge_audit_events`，worker 任务 `retention:audit`（`audit/task/task.go`，保留 400 天）。
- `Event.Data` 不放秘密；`ActorID` 与 ctx 登录主体不一致直接报错，操作对象放 `ResourceID` / `Data`（`service/service.go`）。
- ctx 有主体时一律记主体；`auth_context` 在 `RecentlyReauthed` 时为 `reauth`（`actorFromPrincipal`）。

## 来源 IP：哈希、密文、AAD

- 写入：`reqmeta` 的 IP → `keys.HMAC(crypto.InfoHMACIP, …)` 进 `ip_hash`；`sealer.Seal(..., ipAAD)` 进 `ip_enc`，`ipAAD` = `"audit_events.ip"`（`service/service.go`）。
- 展示/导出：`enrich` / `openIP` 用同一 `ipAAD` 解 `ip_enc`；解不开则列表里来源 IP 为空。
- 按 IP 筛选访问明细：`AccessEvents` 对查询 IP 做 `netip` 规范写法再算 `InfoHMACIP`（`service/ops.go`），与写入一致。
- **不要**用审计侧的 `ip_hash` 去查订阅拉取日志：拉取日志 HMAC 盐是 `crypto.InfoHMACSubAudit`，封存 AAD 是 `subscription/service/fetchlog.go` 的 `fetchLogAAD`（`"sub-fetch-log"`），与审计不是同一用途。
- `SharesSource`（commission 同源）只比 `audit_events` 里两人的 `ip_hash`（`service/sources.go`）。

## 查询、导出与敏感查看

- 列表多取整份 `data`（`selectRecords(..., "data")` → `Row.Data` → DTO `data`，空时回 `{}`），后台展开一行看细节用；导出不取。所以 **`Event.Data` 里的东西后台页面都看得到**：秘密只记 `*_changed` 布尔，不放令牌、口令、私钥、整份请求体或带秘密字段的结构体。单条 data 上限 16KB（`maxDataBytes`，超了整笔写失败），长文本只记条数与摘要（首页 FAQ 记 `home.faq_count` / `home.faq_sha256`，通知模板记 `body_sha256_*`）。节点注册 `server.enrollment.commit` 的 `remote_ip` 是服务器自己的公网地址（后台本来就显示），不属于「来源 IP 只进封存列」的范围。
- 列表/导出共用 `repo.Query` 形状（`repo/repo.go`）；导出先 `Count`，超过 `ExportMax`（50000）回 422，再 `Write` 动作 `audit.export`，再写 CSV（`service/query.go`）。
- 搜索框（`Filter.Query` → `repo.Query.Text`）：动作 ILIKE 包含；邮箱包含反查出的用户 id（`ActorDirectory.IDsByEmail`，存 `TextUserIDs`）命中 `actor_id IN` 或 `resource_type = 'user' AND resource_id IN`（别人对他的操作、冲着他的匿名登录失败都能搜到）；整串是 uuid 时 `resource_id =` 或 `actor_id =`（用户详情「看审计记录」传用户 id 跳过来，只搜这一个人，不走邮箱包含匹配混进别人）。对象名（套餐名、节点名）不能搜：可读名只有登记了 labeler 的类型（`wire.go` 的 `UseResourceLabeler`：user → 邮箱、ticket → 工单号、plan → 套餐名、plan_version → 「套餐名 · v版本号」），查不到的 id 与其余类型前端显示「类型 · id 前 8 位」。守卫 `service/query_pg_test.go` 的 `TestListQueryMatchesUserAsResource`。
- 访问明细 `AccessEvents`（`service/ops.go`、`repo/ops.go`）：`FilterActors` 时只看 `ActorIDs` 这些操作者；再带 `TargetActions` 时这些动作另按对象匹配（`resource_type = 'user' AND resource_id = ANY(ActorIDs)`，identity 用它把匿名的 `user.login_failed` 挂到被试账号上）；行里带 `ResourceType`（没有为空串）与 `ResourceID`。守卫 `service/ops_pg_test.go` 的 `TestAccessEventsTargetActionsPG`。
- 自由文本列（邮箱、标签、reason 等）过 `csvSafe`；对外复用 `CSVSafe`（identity 用户导出）。单测 `service/csv_test.go` 的 `TestCsvSafe`。
- `RecordSourceIPView` → `security.source_ip_viewed` 只给溯源总览「显示」明文 IP 用（`data.view` = `ViewTrace`，见下一节）。访问日志、聚类列表、用户画像的读取不记（用户 10-21 定）。
- 路由：`audit/handler/handler.go` 导出要 `k.RecentReauth`。

## audit 给 report 的读模型

- `audit/service/report_reads.go` 的 `ActivityByDay`：按站点时区切日统计注册/登录/下单与 `unique_ips`（`ip_hash` 去重）；report 的 `liveActivity` / `RefreshActivity` 只调此端口，不在 report repo 里拼 audit SQL。

## audit 给 identity 溯源总览的读（`service/trace_reads.go`，2026-10-20）
- `TraceIPs`（按来源 IP 聚合，只算 actor_kind 为 user / anonymous、带 ip_hash 的事件；窗口总数用窗口函数随行带回）与 `TraceUsers`（按用户聚合，带每人最多 100 个 IP 与是否和别人共用）。取头部（Limit > 0）各一条 SQL，走 `audit_events_time_idx`；按 IP 的那条先窄聚合到 (ip_hash, user_agent, actor_kind, actor_id) 再按 IP 合并（直接在原表上 count(DISTINCT) 会让规划器顺着 `audit_events_ip_idx` 逐行回表，30 天 113 万行 4.4 秒 → 1.1 秒）；按用户的那条先在原表窄聚合到 (actor_id, ip_hash, user_agent)，后面几个 CTE 都从它派生，不让整窗原始行因被引用两次而物化。只补查（Limit = 0）走另一条窄 SQL：按 ip_hash 过滤（`audit_events_ip_idx`）或按操作者过滤（`audit_events_actor_idx`，共用 IP 用 EXISTS 回查），不算全窗总数（为 0）。排序列只从 repo 的白名单取（`traceUserSort`），拼进 SQL 的不是用户输入。
- 按明文补查的 IP 在这里做 `netip` 规范写法再算 `InfoHMACIP`；数组参数以文本字面量传（`CAST(? AS text[])`），GORM 会把切片展开成列表。
- 「显示」明文 IP 记 `security.source_ip_viewed`，`data.view` = `ViewTrace`（`"trace"`），`data.filter` 里是打码串 `masked` 与 `ip_hash`（`InfoHMACIP` 的小写 hex，与 `audit_events.ip_hash` 同口径，可按哈希对上是哪个 IP），不记明文。
