---
paths:
  - "panel/internal/module/subscription/**"
---

# 订阅模块（状态、交付、拉取、续费）

## 状态口径（全仓库只认这里）
- 库里 `status` 只有 `active` / `expired`（`service/types.go`）；产品上的「能不能续费、换套餐、挂流量包」用 `StateAt` → `StateLive` / `StateRevivable` / `StateEnded`，关窗只看 `renewal_closed_at`（`service/state.go`）
- `State.AcceptsChange()` 是可续费、可变更、可作流量包转入目标的唯一判定（`Renew`、`Extend`、`ChangePlan`、`TransferPacks`、order 侧下单前都应对齐）
- 「当前订阅」：`SummariesByUsers` / `ByUser` 里 `currentIndex`（生效优先 → 到期晚 → 创建晚）（`service/read.go`、`service/state.go`）；后台别另写一套
- `status` 转换表只在 `CanTransition`（`active↔expired`）；关窗只写 `renewal_closed_at`、不经 `transition`（`service/state.go` 注释）

## 谁能连、能拉哪些线（与 node 分工）
- 节点名单：`Service.ServingSubscriptions` + `repo.ServingSubscriptions`（生效且未耗尽流量；账号、套餐绑池、池用户组不在这里判）（`service/serving.go`）
- 订阅里的线路列表：`NodeSource.DeliverableNodes` 由 node 实现（`node/service/node_source.go`），subscription 经 `UseNodeSource` 注入；没装配拉取/预览回 `ErrDeliveryUnavailable`（503）（`service/nodes.go`、`service/pull.go`）
- `DeliverableNodes` 的结果按（套餐版本, 用户组）在 node 进程内缓存 5 秒、被所有命中者共享：拉取与预览只读不改返回的切片和 `Config`（`node/service/delivery_cache.go`）
- 拉取路径先判主人 `identity` 账号 `active`、再调 `DeliverableNodes`，用户组取自同一次 `Owners.Get`（`identitysvc.User.UserGroupID`），不另读；门户预览 `MyNodes` 还要求 `StateLive` + 有效凭据（`service/pull.go`、`service/nodes_preview.go`）
- 池用户组限定：node 侧只有 `poolAdmits`（`node/service/delivery.go`）；默认组（`user_group_id` 为空）进不了限定了组的池
- 没划进池的节点不服务任何人（fail closed）；心跳超时、运行异常不在 SQL 里硬删，线路侧用 `preferFresh` 分层（`node/service/delivery.go`）
- 影响名单的事实变了：写路径里调 `notifyRoster` → 提交后 `RosterNotifier.SubscriptionsChanged`（node 注册）（`service/service.go`、`service/rotate.go`）；换备注名 `SetMyLabel` **不**通知名单

## 订阅拉取（`GET /:prefix/:token`）
- 认证不缓存；前缀进程内 `atomic.Pointer` 缓存，不对就不查库（`service/pull.go`）
- 除「令牌有效、可救回」外一律 `ErrPullDenied` → handler 伪装 404；过期回 200 + `render.RenderExpired`（`service/pull.go`、`handler/subscribe.go`）
- 凭据可用：`active`，或已吊销但 `grace_until` 仍晚于 now；换链接 `rotateLocked` 吊销时 `grace` 传 nil、立即失效（`service/pull.go`、`service/rotate.go`）
- 令牌按 `token_hash` 查库后再 `hmac.Equal`；短于 16 字符不查库（`service/pull.go`）
- 成功/过期提示才占凭据每小时限流；未认证失败按来源 IP HMAC 采样落库（`service/pull.go`、`service/fetchlog.go`）
- `Profile-Update-Interval`：正常 12 小时，过期 1 小时；续费页 `renewalPageURL`（`service/pull.go`）

## 链接、换链、备注
- 门户链接列表：`MyLinks` 列续费窗口没关的订阅（过期可救回仍列，`Expired` 只读）（`service/links.go`）
- 门户换链：仅 `StateLive`；过期回 `ErrRotateWhileExpired`（`service/rotate.go`）
- `rotateLocked`：吊销凭据、签新凭据、`RotateProxyUUID`、事件 `link_rotated`、审计、`notifyRoster`（`service/rotate.go`）
- 门户限频：`handler/delivery.go` 的 `rotateRules`（间隔 10 分钟、每份 24h 5 次、账号 24h 20 次）；后台 `AdminRotate` 不限频、要原因进审计
- 备注名：`NormalizeLabel`（`service/label.go`），配置名 `render.ProfileName`（`render/profile.go`）；路由 `sub_rename` 每分钟 10 次（`handler/handler.go`）

## 到期与续费窗口
- `RenewalWindow` = 30 天（`service/types.go`）；`ExpireDue`：到期改 `expired`（不动凭据）→ 满窗写 `renewal_closed_at` 并吊销凭据（`service/expire.go`）
- 续费 `Renew`：提前续接 `RenewalBase`；过期恢复 `restartQuotas`（`service/renew.go`、`service/period.go`）
- 延期 `Extend`：救回折算 cycle 上限、走过到期日重对齐 day/month（`service/extend.go`）
- 开通 `Provision`：唯一生效订阅时 `attachUnassignedPacks` 挂未分配流量包（`service/provision.go`）

## 流量包
- addon 履约必须带生效订阅：`CheckPackTarget`；订单来源 `GrantTrafficPack` 不强制生效中（晚到回调照发）（`service/packs.go`）
- `TransferPacks`：来源须 `StateEnded`，目标须未停用；不能从仍在用的那份转出（`service/packs.go`）
- `(Source, SourceID)` 防重：`ErrPackDuplicate`

## 配额滚动
- `RollQuotas`：第一遍后 `QuotaRollsRemain`，有则隔 `rollQuotaRetryDelay` 再滚一遍（`service/quota_roll.go`）
- 周期起止只在 `period.go` 算，repo 只筛到期行

## 锁序
- 给 order/giftcard 的写方法在 `s.tx.Do` 内自己 `lockOwned`（FOR NO KEY UPDATE）；调用方先锁订单再调 subscription（`service/service.go` 包注释）
