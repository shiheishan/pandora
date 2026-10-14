---
paths:
  - "panel/internal/module/node/**"
---

# 节点模块：编排、下发与会话

## 通道与装配
- 节点只走 gRPC：`EnrollmentService.Enroll`（首次接入）与 `ControlService.Session`（mTLS 双向流），实现见 `panel/internal/module/node/grpc/`；业务在 `service/session*.go`、`session_store.go`、`session_inbound.go`。不再有 UniProxy HTTP、Ed25519 签名心跳、节点 SSE。
- 会话管理器在启动时装配：`NewSessions` → `UseSessions`；`serve` 里 `go m.Run(ctx)`，gRPC `GracefulStop` 前先 `Sessions.Shutdown`（`service/session.go` 文件头注释）。

## 下发纪元（Redis）
- 会影响名单的写：事务里 `MarkUsersChanged`；会影响节点配置文档的写：`MarkConfigChanged`（`service/epoch.go`）。提交后 `INCR node:users_epoch` / `node:config_epoch` 并 `PUBLISH node:changed`；丢通知时 `EpochPoll`（缺省 5s）与 `FullResync`（缺省 15s）兜底（`service/session.go`）。
- 订阅侧影响名单的事实经 `SubscriptionsChanged` 直接 bump，不经过 `MarkUsersChanged` 登记（`epoch.go`）。
- 改协议/端口/路由/退役等具体入口以代码里是否调 `MarkConfigChanged` / `MarkUsersChanged` 为准；`PatchNode` 只在 `protocolChanged` 时推配置纪元、换池推名单纪元（`service/nodes.go`）。

## 名单与线路资格
- 名单 `Rosters`、订阅线路 `node_source.go` 共用 `poolAdmits`（`service/delivery.go`）；池限组时空名单 = 不限，默认组用户进不了限组池。
- 「节点自己能不能下发」只有 `repo.DeliverableNodeCond`；「服务器上该跑哪些节点」只有 `repo.ServedOnServerCond`（`repo/repo.go`）。订阅拉取、后台列表「是否下发」都引用前者，不另写 SQL 变体。
- 心跳超时不写进 `DeliverableNodeCond`；从没心跳过的服务器不满足条件。运行异常只降级：`repo.NodeDegradedCond`，`preferFresh` 先摘降级节点，全是降级时仍下发（`repo/delivery.go`、`service/delivery.go` 的 `HeartbeatFreshWindow`）。
- 名单版本 `rosterVersion` 前缀 `u1-`（`service/delivery.go`）；同池节点共用一份 `Users` 切片，不要改切片。
- 账号须 `identity` 的 `ActiveUserGroups`（封禁即断）；可服务订阅来自 `subscription.ServingSubscriptions`。

## 订阅线路缓存
- `DeliverableNodes` 的结果按（套餐版本, 用户组）在进程内缓存 5 秒（`service/delivery_cache.go`：expirable LRU 1024 个键 + singleflight 合并同键并发未命中，出错不缓存）。订阅 id、用户 id 不进键，现算也不能依赖它们；要加新输入就得同时改键。
- 只靠 TTL 失效，不接纪元：线路变化最多晚 5 秒进订阅。不是安全边界：能不能连看节点名单（纪元推送），订阅与账号资格由拉取路径每次现查。
- 返回的切片与 `render.Node.Config` 被所有命中者共享：调用方只读不改；不要在事务里调（事务内的读会经缓存给到别人）。测分层、过滤逻辑用不经缓存的 `loadDeliverableNodes` / `deliverableFromRows`（`service/node_source.go`）。

## 生效路由与配置文档
- 合并只有 `mergeRouting`（`service/routing.go`）：层序节点私有 → 各路由组（`sort_order, id`）→ 全局；规则按层拼接，出站从低到高同 tag 覆盖且保留首次位置。读层用 `nodeRoutingLayers`（`service/routing_layers.go`）。下发配置用 `ServerConfigs`（`service/config_docs.go`），不要另写合并。
- 内置出站 `direct` / `block` 保存时经 `canonicalRouteTag` 规范成小写；自定义 tag 大小写敏感。
- 写路由用 `refuseNewDangling`：只拒本次新造成的悬空引用（`service/routing_write.go`）。

## 同机端口（先到先得）
- L4 唯一口径：`protocol.ListenL4` = 写入 `nodes.listen_l4` = pdnd `kernel/port_claims.go`（`protocol/port_l4.go`、`protocol/port_l4_test.go`）。
- 建、改、克隆、迁服务器在服务器行锁内 `claimNodePort`；冲突 409。锁序：改节点为节点行 → 服务器行（`service/nodes.go` `PatchNode`）。

## 服务状态与运行态
- 只有 `serving_status`（`draft` / `active` / `disabled` / `retired`），边表在 `nodeServingTransitions`（`service/node_status.go`）；无旧版 17 态生命周期触发器。
- 服务器心跳在 `servers.last_heartbeat_at`；后台失联 `NodeStaleAfter`（90s，`service/node_types.go`）。`RecordHeartbeat` 每拍写服务器心跳，节点 `runtime_status` 只在变化时写（`service/session_store.go`）。
- 配置回执 `RecordConfigAck`：`switched`/`health_passed` 写 `applied_config_*` 并清 `apply_failed_*`；`failed` 只写失败台账，已应用版本不动。

## 流量上报
- 入口 `IngestTraffic`（`service/traffic_ingest.go`）：`report_id` 必填，`UNIQUE(server_id, report_id)` 去重；同 id 不同摘要不入账并 `ErrTrafficReportConflict`（仍 ack，防死循环）。扣量在同一事务调 `subscription.ChargeTraffic`（先配额、再流量包、按日用量；切日与 `dayOf`+`siteLocation` 同口径）。
- 只扣当前名单成员（`RosterMembership`）；名单外与不合规条目留档、计 `Invalid`/`Unlisted`，不拒整份。遇 `40P01`/`40001` 整笔重试最多 `trafficRetryAttempts`（3）次。

## 设备窗口与 strict 名单
- 在线统计窗口只读 `settings` 的 `DevicePolicy` / `ValidDeviceWindow`（`service/devices.go`）；strict 超限集合只由任务 `RecheckStrictDevices` 写 Redis `node:devices_strict_set`，`poolRosters` 只读，不在会话里按在线表现算。

## 并发与单进程假设
- `Sessions` 的纪元轮次、`MinRoundGap`（缺省 1s）合并推送；发送队列满则 `ErrSessionOverflow` 断流重连。心跳合写、PG LISTEN 纪元、多实例分流尚未做：会话状态在进程内，按单 `serve` 实例设计。
