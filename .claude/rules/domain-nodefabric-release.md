---
paths:
  - "panel/internal/domain/nodefabric/**"
---

# 节点编排：发布锁、两套状态与生效路由

## node-config-release 发布锁
- 改动节点配置来源（旧版分层配置、三层路由、路由组成员与组序）或生命周期的写，事务里第一把锁是租户级 advisory 锁 `lockLegacyConfigRelease`（键 `"node-config-release/"+tenantID`），之后才锁池 / 组 / 节点行；受影响节点行按 UUID 排序锁（`lockEffectiveReleaseNodes`）
- 现有持锁方：旧版发布、两阶段接入与旧版 Bootstrap、后台建节点与复制、批量改服务状态、旧状态接口的退役与销毁、一步退役、一步上线、删池、全部路由与路由组写。新增同类写照做。单节点 `PatchAdminNode`（含换池、改协议）与 `MoveAdminNode` 目前只锁节点行，不持发布锁
- `PublishConfig` 的完整顺序：发布锁 → 目标池 / 节点行 FOR SHARE（不能用 FOR KEY SHARE）→ 受影响节点行 → 全租户版本分配 → 取代旧层 → 写新层。守卫 `config_legacy_risk_contract_test.go:TestLegacyConfigPublishRiskReductionContract`
- `node_status_legacy.go` 与 `node_pools_admin.go` 里把锁的 SQL 和键字面量直接写在原处，契约测试按 `"node-config-release/"+tenantID` 字面量检查（`TestLegacyTerminalNodeStatusRevokesDeliveryAndIdentity`、`api/admin` 的 `TestPoolDeletionLocksParentBeforeDependencyCounts`），改成调用 helper 前先改测试

## 有效发布物与 generation
- 同一个 `config_source_generation` 只能物化出逐字节相同的发布物；`FetchEffectiveConfig` 发现同代不同字节会 fail closed。所以任何改变节点生效配置输入的写，都要在同一事务、持节点行锁时把 `config_source_generation` 加一（参照 `bumpRoutingNodesTx`、`PatchAdminNode` 的 `configSourceTouched`）。已退役 / 已销毁的节点不推，也不通知
- 守卫 `node_admin_test.go:TestPoolMoveBumpsEffectiveReleaseGeneration`（换池必须推 generation）
- `PatchAdminNode` 只在协议内容真变了（`protocolChanged`：类型、地址、端口、内核、schema 版本、`sameProtocolJSON` 比的协议配置，任一不同）或换池时推 generation；请求里原样带回协议字段只校验、不推（推一次节点就重建入站、断开全部连接）。守卫 `TestPatchBumpsGenerationOnlyWhenProtocolChanges`

## 同机端口门禁（w4deliver，用户 2026-10-07 定：先到先得）
- 键是 (服务器, 端口, L4)。L4 只有一个口径：Go `ListenL4`（port_l4.go）= 迁移 00122 的生成列 `nodes.listen_l4` = pdnd `kernel/port_claims.go` 的 `inboundPortKey`。改规则三处一起改；守卫 `port_l4_test.go:TestListenL4MatchesPdndPortClaims`（读 pdnd 的用例表）、PG18 `runNodeConfigPG18PortClaimBatch`（生成列对 Go）
- 建、改（端口或 L4 变了）、复制、迁移都在服务器行锁内调 `checkNodePortClaim`：先保留端口（`PortPolicy`，经 platform/config 注入，未注入用 `DefaultPortPolicy`，两处缺省值有测试对照），再查同服务器未退役节点，冲突回 409 写明占用者名字。锁序：建节点只锁服务器；改、复制、迁移是节点行 → 服务器行（三者一致）
- 唯一部分索引 `nodes_listen_claim_unique` 兜底，撞上经 `nodeUniqueViolation` 译成端口冲突（其余唯一冲突仍是重名）。迁移时存量有冲突就只建普通索引 `nodes_listen_claim_lookup` 并 WARNING 列清单，后台列表的 `port_conflict_node` 提示管理员处理；清完后手工建唯一索引（语句在 00122 注释里）

## 节点真实运行状态
- pdnd 降级时签名心跳带 `X-Node-Runtime-Reason`，兼容 `/status` 带 `X-Node-Runtime-Status` 与 `X-Node-Runtime-Reason`（都不进正文，正文按 DisallowUnknownFields 解码）。经 `NormalizeRuntimeState` 存进 `nodes.runtime_status / runtime_reason`，`runtime_state_at` 只在变化时前进；健康分 running 90、degraded 40（两条通道同一个 `HealthScore`）
- 这三列在 `zz_notify_nodes_update` 的列清单里（只在变化时通知）；给 nodes 加列仍要同步改这张清单（守卫 `api/node` 的 notify trigger PG18 子测试）
- 「节点入站没按期望在服务」只有 `RuntimeFailingSQL` 一处：端口被占、没起来，或期望生效版本 ≠ 已应用且最近回执是失败。订阅降级与后台列表的 `delivery_degraded` 共用它

## 两套状态
- 生命周期 `status` 由迁移 00005 的 `node_transitions` 触发器强制，只能沿表里的边一步一步走（每一步都要过触发器）；进入 active 只能从 canary 来。一步上线与一步退役的路径表（`activatePaths`、`retirePaths`）只能用表里的边（`node_activate_test.go:TestActivatePathsFollowNodeTransitions` 只守住上线那张）
- 服务状态 `serving_status` 没有触发器，只在 Go 里强制：改它一律先过 `ValidServingTransition`（`servingTransitions` 表）。旧状态接口用 `ProjectNodeLifecycle` 把生命周期投影成服务状态，一步上线也用同一份映射
- 节点「已退出服务」的统一判定：生命周期是 retired / destroyed，或服务状态是 retired。服务状态置 retired 时同事务清掉 `desired_config_version`；一步退役（`RetireNode`）与旧状态接口的终态会同事务吊销有效身份，批量改服务状态遇到有效身份、控制面或在途任务则回 409

## 生效路由
- 合并口径只有 routing_merge.go 一处：层序为 节点私有 → 所在各路由组（按 `sort_order, id`）→ 全局。规则按层顺序拼接；出站按层逆序铺开、同 tag 就地覆盖，保留原位置。UniProxy 下发、长连接推送、有效发布物、后台生效预览都必须经 `loadNodeRoutingLayersTx` + `MergeRouting` / `mergeRoutingLayers`，不要另写合并
- 规则里对内置出站的引用，保存和下发都经 `canonicalRouteTag` 规范成小写 direct / block（pdnd 只认小写）；自定义出站 tag 按原样精确比较，和 pdnd 查表一致。出站重名、占用内置名则不分大小写拒绝
- 引用可见性：节点规则能指向自己、所在组、全局的出站；组规则只能指向内置、本组、全局的出站。写路径用 `refuseNewDanglingTx` 在写前写后各取一次全租户悬空引用，只拒绝本次新造成的，存量不连坐
- 成员关系是节点的属性：组侧改成员要推进出组节点的行版本，节点侧改所属组要推进出组的组行版本，两侧拿旧版本写都回 409

## 其它
- 后台换池只走 `PatchAdminNode` 的 `pool_id`（用户 2026-10-07 定：允许换池；原冻结接口 `assignNodePool` / `CheckNodePoolAssignment` 已删）：先拿配置发布锁、再锁节点行、UPDATE 后在锁内 `syncLegacyDesiredConfigVersion`，并推进 `config_source_generation`（守卫 `TestPatchAdminNodePoolMoveMaterializesUnderReleaseLock`、`TestPoolMoveBumpsEffectiveReleaseGeneration`）。不要再加只查不写的换池入口
- 两阶段接入在提交时比对 `SetReleaseBinding` 注入的发布绑定（产物摘要与版本）；没注入的 Service 一律拒绝，不要为测试方便放行

## 生效配置的热路径与下发纪元
- 拉生效配置先无锁复用当前版本；节点带上已应用版本且仍是当前版时回 204。只有需要物化新版本时才锁节点行，回写当前版本只在真有变化时写（`IS DISTINCT FROM`），无锁路径不写回旧版本
- aegis-node 按（租户，池）缓存用户名单、缓存签名身份，靠序列 `node_delivery_epoch`（00101）判断是否过期：凡影响下发结果的写一提交就让它前进（订阅、套餐版本、池授权、池用户组、用户换组与账号状态（00141）、系统设置、节点身份与状态；配额与流量包只在「用尽 / 未用尽」翻转时）。**新增会影响节点下发结果的表或列，要给 00101 那组延迟约束触发器加同类触发**，否则节点最多旧一个缓存周期（名单 5 秒、身份 30 秒），事件流也收不到
- 节点名单只收 active 账号的订阅（`Service.nodeUsers` JOIN users，用户 2026-10-07 定「封禁即断，解封恢复」）；订阅拉取对非 active 账号回伪装 404（`subscription.checkOwnerStatus`），凭据不吊销，恢复后原链接可用。改状态的入口（后台、风控、以后的注销）不用各自通知节点，纪元触发器统一兜
- 下发变化的推送（w8node，`nodestream_epoch.go`）：aegis-node 每个有连接的租户一个信号循环，每 500ms 读一次纪元（`CurrentDeliveryEpoch`），前进了等 200ms 让提交落定再排一轮租户级推送；名单里最早的订阅到期时刻（`nodeUserSet.nextExpiry`）到了也排一轮。两轮之间至少隔 1 秒（`runPushRounds`），批量变化合并成每秒至多一轮。守卫 `TestWatchNodeChangesFollowsDeliveryEpoch`（三个间隔之和不超过 2 秒）、`TestPushRoundsThrottleAndCoalesce`，PG18 `nodeDeliveryStatusScenario`
- 用户集缓存条目按 `nextExpiry` 硬过期（`ttlEntry.hard`），不走「先回旧值」的宽限：到期没有写、不推进纪元，宽限会让刚到期的人多留 10 秒（守卫 `TestNodeUserSetHardExpiresAtNextSubscriptionExpiry`）
