---
paths:
  - "panel/internal/module/node/**"
---

# 节点模块：编排、下发与会话

## 通道与装配
- 签安装令牌（`IssueBootstrapToken`）带幂等认领时在业务事务里 `CompleteUnreplayable(201)`：响应含令牌明文，不许落进幂等表；端口经 `UseIdempotency` 装配，没装配时带认领的签发失败关闭。
- 节点只走 gRPC：`EnrollmentService.Enroll`（首次接入）与 `ControlService.Session`（mTLS 双向流），实现见 `panel/internal/module/node/grpc/`；业务在 `service/session*.go`、`session_store.go`、`session_inbound.go`。不再有 UniProxy HTTP、Ed25519 签名心跳、节点 SSE。
- 会话管理器在启动时装配：`NewSessions` → `UseSessions`；`serve` 里 `go m.Run(ctx)`，gRPC `GracefulStop` 前先 `Sessions.Shutdown`（`service/session.go` 文件头注释）。
- 节点日志的 `server_id` 只由鉴权拦截器挂到流 ctx 的 logger 上（`grpc/interceptors.go` 的 `authorize`）；`control.go` 的 `withStreamLogger` 只管 req_id，会话层只加 `session_id`，不要再 `With` 一次 server_id。

## 下发纪元（Redis）
- 会影响名单的写：事务里 `MarkUsersChanged`；会影响节点配置文档的写：`MarkConfigChanged`（`service/epoch.go`）。提交后 `INCR node:users_epoch` / `node:config_epoch` 并 `PUBLISH node:changed`；丢通知时 `EpochPoll`（缺省 5s）与 `FullResync`（缺省 15s）兜底（`service/session.go`）。
- 订阅侧影响名单的事实经 `SubscriptionsChanged` 直接 bump，不经过 `MarkUsersChanged` 登记（`epoch.go`）。
- 改协议/端口/路由/退役等具体入口以代码里是否调 `MarkConfigChanged` / `MarkUsersChanged` 为准；`PatchNode`（事务内主体是 `patchNodeTx`，批量修改复用它）只在 `protocolChanged` 时推配置纪元、换池推名单纪元、超限标记变了两个都推，每次都 `MarkLinesChanged`（`service/nodes.go`）。
- 只影响订阅线路、不影响节点端配置与名单的写（中转、标签、倍率）用 `MarkLinesChanged`：提交后只清本进程的线路缓存，不推纪元（`epoch.go`）。

## 名单与线路资格
- 名单 `Rosters`、订阅线路 `node_source.go` 共用 `poolAdmits`（`service/delivery.go`）；池限组时空名单 = 不限，默认组用户进不了限组池。
- 「节点自己能不能下发」只有 `repo.DeliverableNodeCond`；「服务器上该跑哪些节点」只有 `repo.ServedOnServerCond`（`repo/repo.go`）。订阅拉取、后台列表「是否下发」都引用前者，不另写 SQL 变体。
- 心跳超时不写进 `DeliverableNodeCond`；从没心跳过的服务器不满足条件。运行异常只降级：`repo.NodeDegradedCond`，`preferFresh` 先摘降级节点，全是降级时仍下发（`repo/delivery.go`、`service/delivery.go` 的 `HeartbeatFreshWindow`）。
- 名单版本 `rosterVersion` 前缀 `u1-`（`service/delivery.go`）；同池节点共用一份 `Users` 切片，不要改切片。
- 账号须 `identity` 的 `ActiveUserGroups`（封禁即断）；可服务订阅来自 `subscription.ServingSubscriptions`。

## 订阅线路缓存
- `DeliverableNodes` 的结果按（套餐版本, 用户组）在进程内缓存 5 秒（`service/delivery_cache.go`：expirable LRU 1024 个键 + singleflight 合并同键并发未命中，出错不缓存）。订阅 id、用户 id 不进键，现算也不能依赖它们；要加新输入就得同时改键。
- 失效：本进程推进下发纪元时（`epoch.go` 的 `bumpErr`）整份清空，后台改了线路下一次拉取就是新的；心跳失联等别的变化最多晚 5 秒进订阅。缓存第一次用到时才建（库的清理 goroutine 停不掉）。不是安全边界：能不能连看节点名单（纪元推送），订阅与账号资格由拉取路径每次现查。
- 返回的切片与 `render.Node.Config` 被所有命中者共享：调用方只读不改；不要在事务里调（事务内的读会经缓存给到别人）。测分层、过滤逻辑用不经缓存的 `loadDeliverableNodes` / `deliverableFromRows`（`service/node_source.go`）。

## 生效路由与配置文档
- 合并只有 `mergeRouting`（`service/routing.go`）：层序节点私有 → 各路由组（`sort_order, id`）→ 全局；规则按层拼接，出站从低到高同 tag 覆盖且保留首次位置。读层用 `nodeRoutingLayers`（`service/routing_layers.go`）。下发配置用 `ServerConfigs`（`service/config_docs.go`），不要另写合并。
- 内置出站 `direct` / `block` 保存时经 `canonicalRouteTag` 规范成小写；自定义 tag 大小写敏感。
- 写路由用 `refuseNewDangling`：只拒本次新造成的悬空引用（`service/routing_write.go`）。
- 复制节点勾选复制路由（`CopyNode` 的 `CopyRouting`）时，在同一事务里经 `copyNodeRouting`（`service/nodes_placement.go`）把私有出站、规则和路由组成员一起复制：先拿路由写锁再锁节点行（与其它改路由的写同一锁序），组成员变了推进组版本，照常 `refuseNewDangling`。
- 单个节点的文档生成失败只跳过该节点（`assembleServerConfigs` 返回跳过集合），同批其余服务器与节点照发；不要改回整批失败。被跳过的节点不在下发的文档里，节点端会停掉它的入站直到配置修好。ERROR 只在下发路径记（`ServerConfigs`，供配置纪元与建流对齐）；只读路径（节点列表、`ConfigLaggingCount`）调 `serverConfigsWithSkips` 不记日志：列表把这些节点显示为 failed（「面板生成配置失败：…」），落后计数算上它们。

## 节点补齐 Xboard（迁移 00045）
- 标签 `nodes.tags`（jsonb 字符串数组）：规范化只有 `NormalizeNodeTags`（去首尾空白、非空、无控制字符、≤`NodeTagMaxRunes` 字、不分大小写去重留首次、≤`NodeTagsMax` 个，`service/node_xb_fields.go`）；库里 `nodes_tags_shape` 钉数组、≤10 个、元素都是字符串；后台列表 `?tag=` 按 `n.tags @> jsonb_build_array(?)` 精确筛；标签只在后台用（筛选、批量修改），订阅线路（`repo.DeliveryNodes`、`render.Node`）不取标签，门户与订阅文件都不出（用户 2026-10-14 定）；中转没有自己的标签。
- 每周期流量上限：`traffic_used_bytes` 是实际字节（不乘倍率），在 `IngestTraffic` 同一事务由 `repo.AddNodeUsage` 累加（一条语句，CTE 按 id 升序 `FOR NO KEY UPDATE`，PG18 `RETURNING old/new` 判新超限），新超限推配置与名单纪元。超限只写 `traffic_exceeded_at`，不改 `serving_status`；它已并入 `DeliverableNodeCond` 与 `ServedOnServerCond`，库里 `nodes_traffic_exceeded_shape` 钉住它与「有上限且已用 >= 上限」同真同假：改上限的写（`patchNodeTx`）按锁住的行用 `nodeExceededAt` 重算，不要只改上限不改标记。
- 清零：任务 `node:traffic_reset`（每 5 分钟，`task/task.go`）按站点时区 `trafficPeriodStart`（短月取月末）清到期节点；后台手动重置 `ResetNodeTraffic` 要重认证、幂等、审计 `node.traffic_reset`。原来超限的清零后推两个纪元恢复下发。设或改重置日不清零已用（用户 2026-10-14 定）：`patchNodeTx` 在同一事务里把 `traffic_reset_at` 推到 max(原值, 按新重置日算的本周期起点)（`UpdateNodeSpec` 写 `GREATEST`，只推后不前拨），定时任务只在下一个重置日清零；重置日没变不推；批量修改走同一段；超限标记按锁住的行照常重算。例：9-01 上线、10-14 已用 600 GB 时设上限 1 TB、重置日 20 → 600 GB 照算，10-20 清零（`traffic_reset_day_pg_test.go`，含短月）。
- 时段倍率 `traffic_rate_schedule`：站点时区半开区间 `[start, end)`，不跨零点、不重叠、≤`NodeRateScheduleMax` 段（`ParseRateSchedule`）。扣量按面板收到上报的时刻取 `effectiveRate`（proto 的流量上报没有时间戳，收到时刻即窗口结束时刻），没命中用 `traffic_rate`；规则坏了记 Warn 按基础倍率。节点端断线后补报的流量按面板收到补报（重连后）那一刻的倍率计，是已定口径不是缺陷。站点时区只在本份上报（或本次装载的候选节点）里有节点配了时段时才读（`rateScheduleSet`），没配的不多一次读库。订阅与门户显示的倍率是线路装载时刻的生效倍率。
- 中转节点 `node_relays`（Xboard `parent_id` 的等价物）：独立表，只出现在订阅里，紧跟父节点、继承父节点的协议配置、倍率、ECH、池与资格、分层事实，只换名称/地址/端口，可选 SNI / Host 覆盖（`relayRenderNode`，`service/node_source_relay.go`；父节点的 Config 被缓存共享，只能浅拷贝后改）。中转不占端口、不进配置文档与名单、没有状态机；父节点只能是 nodes 里的节点，天然不能挂中转；每个父节点最多 `relaysPerNodeMax`（20）条。REALITY 父节点的中转 SNI 只能填父节点 `server_names` 之一（否则握手必败，新建与修改回 422 `sni`，`relayCheckSNI`）；父节点地址是 IP、开了 TLS 又没配 server_name 时，列表接口的 `sni_hint` 提示给中转填 SNI（`relaySNIHint`，`service/relays_sni.go`）。改父节点的 server_names 不回头校验已有中转。与「自定义出站链式转发」（`node_outbounds`，节点出口再经别的代理）是两回事：那个改节点的出站，这个改用户的入口。订阅线路的中转与 ECH 都在 `repo.DeliveryNodes` 那一条 SQL 里用标量子查询取，不加往返。
- ECH `node_ech_keys`：密钥由 `protocol.GenerateECHKey` 生成（与 Xboard 同格式），私钥经 Sealer 封存（AAD `node_ech_key:<节点 id>`），审计、日志、错误里不出现私钥。每节点至多一把当前钥；轮换后旧钥再下发 `ECHRetiredKeyGrace`（7 天）供拿着旧订阅的客户端解密；满期只由任务 `node:ech_purge`（每小时）判定，它删行并推配置纪元，读路径（`ECHKeysByNodes`、`GetNodeECH`）不按时间过滤，库里还有的旧钥照常下发与计数（文档最多 `echDocKeysMax` 把），期望文档与节点端手里的才不会错开；停用 `DisableNodeECH` 立即删光。配置文档键 `ech_keys`（当前钥在前、`send_as_retry` 只有当前钥为 true），只在 `protocol.ECHCapable`（按翻译后的内核配置判）时写；订阅只用当前钥。不支持 REALITY 与 QUIC 类协议；开了 ECH 的节点只收 TLS 1.3。
- 批量修改 `BatchEditNodes`（`service/nodes_batch.go`）：白名单字段（池、倍率、标签、服务状态、流量上限与重置日、时段倍率），一个事务，按 id 升序锁全部节点，状态先走 `BatchNodeStatus`，其余逐个走 `patchNodeTx`；任一失败整批回滚，错误带 `node_id`；上限 `NodeBatchEditMax`；接口要重认证与幂等，审计 `node.batch_update`。

## Shadowsocks 2022 服务端身份密钥（共用约定 2026-10-14）
- 每个 2022 节点一把 iPSK，不存库：`HKDF-SHA256(主密钥, info = crypto.InfoSS2022ServerPSK || 0x00 || 方法名 || 0x00 || 节点 id 的 16 字节)`（带方法名：同一节点 128 与 256 两把彼此独立，不是前缀关系），长度按方法（`protocol.SS2022KeyLen`）；派生只有 `service/ss2022.go` 的 `applySS2022ServerPSK`（端口 `PSKDeriver` = `*crypto.Keys`，`UsePSKDeriver` 装配，进 `MissingPorts`）。向量钉在 `service/ss2022_test.go`。
- 配置文档（`buildNodeConfigDoc`）与订阅线路（`deliverableFromRows`）都在 `protocol.KernelConfig` 之后调它：2022 方法写 `password` = base64(iPSK)，其它 shadowsocks 删掉 `password`；没装配派生器时文档只跳过该节点、线路装载回错误。中转经 `relayRenderNode` 浅拷贝父节点 Config，自然用父节点那把。
- 换主密钥会让全部 2022 节点的 iPSK 一起变（节点与订阅同时变，客户端重新拉订阅），是接受的代价；iPSK 只取决于节点 id，线路缓存键不用改。复制节点得新 id、新密钥。

## 同机端口（先到先得）
- L4 唯一口径：`protocol.ListenL4` = 写入 `nodes.listen_l4` = pdnd `kernel/port_claims.go`（`protocol/port_l4.go`、`protocol/port_l4_test.go`）。
- L4 三种取值 `tcp` / `udp` / `tcp+udp`；shadowsocks 无条件 `tcp+udp`（同端口 TCP、UDP 都听，用户 2026-10-14 定）。冲突口径 `protocol.L4Conflicts`：相等，或一方是 `tcp+udp`；`tcp` 与 `udp` 同端口可共存。门禁按 `protocol.ConflictingL4s` 查占用者（`repo.NodePortHolder`），提示标签 `443/TCP+UDP`。
- 库里兜底是两条部分唯一索引 `nodes_listen_tcp_claim_key`（`listen_l4 IN ('tcp','tcp+udp')`）与 `nodes_listen_udp_claim_key`（`IN ('udp','tcp+udp')`），都译成 `repo.ErrListenClaimTaken`（00038）。改 L4 规则时两条索引、`repo.inventoryErrs` 映射、pdnd 用例表一起改。
- 建、改、克隆、迁服务器在服务器行锁内 `claimNodePort`；冲突 409。锁序：改节点为节点行 → 服务器行（`service/nodes.go` `PatchNode`）。

## 服务状态与运行态
- 只有 `serving_status`（`draft` / `active` / `disabled` / `retired`），边表在 `nodeServingTransitions`（`service/node_status.go`）；无旧版 17 态生命周期触发器。
- 服务器心跳在 `servers.last_heartbeat_at`；后台失联 `NodeStaleAfter`（90s，`service/node_types.go`）。`RecordHeartbeat` 每拍写服务器心跳，节点 `runtime_status` 只在变化时写（`service/session_store.go`）。
- 配置回执 `RecordConfigAck`：`switched`/`health_passed` 写 `applied_config_*` 并清 `apply_failed_*`；`failed` 只写失败台账，已应用版本不动。

## 流量上报
- 入口 `IngestTraffic`（`service/traffic_ingest.go`）：`report_id` 必填，`UNIQUE(server_id, report_id)` 去重；同 id 不同摘要不入账并 `ErrTrafficReportConflict`（仍 ack，防死循环）。扣量在同一事务调 `subscription.ChargeTraffic`（先配额、再流量包、按日用量；切日与 `dayOf`+`siteLocation` 同口径）。
- 只扣当前名单成员（`RosterMembership`）；名单外与不合规条目留档、计 `Invalid`/`Unlisted`，不拒整份。遇 `40P01`/`40001` 整笔重试最多 `trafficRetryAttempts`（3）次。

## 设备窗口与 strict 名单
- 改设备策略（`SetDevicePolicy`）只在 settings 报告值真的变了时 `MarkUsersChanged`；在线设备总览 `OnlineDevices` 只统计 active 订阅。
- 在线统计窗口只读 `settings` 的 `DevicePolicy` / `ValidDeviceWindow`（`service/devices.go`）；strict 超限集合只由任务 `RecheckStrictDevices` 写 Redis `node:devices_strict_set`，`poolRosters` 只读，不在会话里按在线表现算。

## 并发与单进程假设
- `Sessions` 的纪元轮次、`MinRoundGap`（缺省 1s）合并推送；发送队列满则 `ErrSessionOverflow` 断流重连。心跳合写、PG LISTEN 纪元、多实例分流尚未做：会话状态在进程内，按单 `serve` 实例设计。
- 全量名单按 `UsersChunkSize`（500 人，一块编码后不到 32 KB）分块，发送循环逐块现转现发（`grpc/control_convert.go` 的 `usersFullChunks`），不要改回先转整份再发：gRPC 发送缓冲 32 KB 以上直接占 1 MB 一档，合批对齐后几百条流同时发全量，块大或整份先转就是几百 MB 的瞬时内存（10-14 闸门量到，守卫 `TestUsersFullChunkFitsSendBuffer`）。增量上限是另一个常量 `UsersDeltaMax`（5000），增量不分块。
- 建流对齐合批（`service/session_align.go`）：同一时段到达的建流共用一次 `ServerConfigs` + `Rosters`，批串行（同时只有一批读库）、一批一对序号（先配置后名单）；批的 ctx 用 `context.WithoutCancel` 派生，单条流断开不连累同批。`AlignTimeout`（缺省 10s）既是等待者的总时限（到期 `ErrAlignBusy`）也是一批读库的时限；没有并发数参数，不要再加逐流的对齐读。

## 保留期
- 任务 `retention:node`（`task/task.go`）按 `service/retention.go` 的 `RetentionTables` 逐表分批清：流量上报、指标、在线设备 31 天，小时汇总 400 天，安装令牌 30 天。
- 安装令牌（`bootstrap_tokens`）只删没用过、且过期或作废满 30 天的（`repo/bootstrap_retention.go`，也排除被身份引用的）；用过的跟身份走，永远不删（`server_identities.enroll_token_id` 以 RESTRICT 引用）。
- 库里 `bootstrap_tokens_guard`（迁移 00037）对 DELETE 只放行同一类行，30 天写死在函数体里：改保留期要同时改 `BootstrapTokenRetentionDays` 并另写迁移改函数。
- 截止时刻在库里按 `now()` 算（`make_interval`），不用进程时钟，免得面板时钟快于库时被 guard 拒掉整批。
- 删掉之后再拿这枚令牌来登记，拒绝原因从 `token_expired` 变成 `token_unknown`（行已不在）；面板与节点端都不该依赖这两个原因的区别。

## 发布检查的提示
- `CheckPublishable`（`service/plan_pools.go`）三种拒绝分开写：没绑池 `poolNoteUnbound`、绑的池都停用 `poolNoteAllDisabled`、池里没有可服务节点；后台据此排查（用户 2026-10-14 定）。门户侧不暴露原因。池状态只管能不能再绑，不影响已有绑定的下发。
