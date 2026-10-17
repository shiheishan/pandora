---
paths:
  - "panel/internal/module/report/**"
  - "panel/internal/module/audit/**"
---

# report 与 audit

## report 边界与读数

- `panel/internal/module/report/service/service.go` 包注释：`report` 只拥有 `revenue_report_adjustments`、`activity_daily`；看板其余数字一律经 `Deps` 里各属主 `service` 的聚合读，**不读别人的表**。
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

## 营收报表调整

- `revenue_report_adjustments` 只追加；冲正 = 新插一行、金额取反、生效日同原行；表上 `reversal_of` 唯一。业务规则在 `service/adjustments.go`（`LockAdjustment` 咨询锁、`HasReversal`）；幂等在 HTTP 中间件，表上不存幂等键（迁移 00032）。
- 登记/冲正与审计同事务：`audit.Write` 动作为 `revenue.adjustment.create` / `revenue.adjustment.reverse`。

## audit 写入与表

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

- 列表/导出共用 `repo.Query` 形状（`repo/repo.go`）；导出先 `Count`，超过 `ExportMax`（50000）回 422，再 `Write` 动作 `audit.export`，再写 CSV（`service/query.go`）。
- 自由文本列（邮箱、标签、reason 等）过 `csvSafe`；对外复用 `CSVSafe`（identity 用户导出）。单测 `service/csv_test.go` 的 `TestCsvSafe`。
- 后台查看明文来源 IP 前记审计：`RecordSourceIPView` → `security.source_ip_viewed`，`data.view` 用 `ViewUserProfile` / `ViewAccessLog` / `ViewIPClusters`（`service/ops_types.go`）；例如 `panel/internal/module/identity/service/ops_access.go` 的 `AccessLog` 在返回列表前先记一条，`data` 里带查询条件。
- 路由：`audit/handler/handler.go` 导出要 `k.RecentReauth`。

## audit 给 report 的读模型

- `audit/service/report_reads.go` 的 `ActivityByDay`：按站点时区切日统计注册/登录/下单与 `unique_ips`（`ip_hash` 去重）；report 的 `liveActivity` / `RefreshActivity` 只调此端口，不在 report repo 里拼 audit SQL。
