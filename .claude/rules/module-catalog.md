---
paths:
  - "panel/internal/module/catalog/**"
---

# 套餐与目录模块

- 套餐向导与事务编排：
  - 「一次建成」向导（`CreatePlanComplete`）与更新在同一个事务内完成：创建套餐 → 创建版本 → 更新版本权益与配额（`updateVersionTx`）→ 绑定节点池（`bindWizardPoolsTx`）→ 创建价格档 → 可选直接发布（`publishVersionTx`）。任一步失败整笔回滚，不留半成品。
- 节点池绑定约束（`plan_node_pools`）：
  - 节点池归属 node 模块，绑池操作必须由 `PoolGuard.LockAssignablePools` 在同一事务内按 ID 升序加 `FOR SHARE` 锁，确保节点池存在且未停用。
  - 只有草稿版本允许变更绑池（`SetVersionPools` → repo `ReplaceVersionPools`）；已发布版本绑池受库级冻结触发器保护，禁止修改（撞触发器转为 `ErrSnapshotFrozen`）。
- 卖点与参数规则：
  - 卖点校验统一收拢在 `normalizeHighlights`：去首尾空白、最多 5 条、每条 1–40 字、不空不重，禁止 NUL 字符。返回切片永不为 nil。
  - 套餐 Code 格式要求 `^[a-z0-9][a-z0-9_-]{1,63}$`。
  - 流量包（`TrafficPack`）流量上限为 1 PiB（`maxPackBytes` = 1<<50），乐观锁通过触发器维护的 `updated_at` / `row_version` 控制。
- 版本语义与价格冻结：
  - 套餐已发布版本（`published`）及其关联权益、配额、绑池与价格行（`prices`）均冻结不可变，保障订单与订阅的快照一致性。
  - 价格行不可删（守卫 `catalog_guard_price`），唯一例外是删用户组的级联：00050 起 `prices.user_group_id` 是 ON DELETE CASCADE，守卫只放行「所属组已不存在」的 DELETE；被订单项、订阅、礼品卡引用的价格由它们的 RESTRICT 外键挡住，删组整笔回滚。catalog 的 Go 代码不删价格。
  - 价格没有试用天数，版本没有宽限期（只存不用，00049 删列；订阅只有 active / expired）。注册试用是 identity → subscription 的 `TrialGranter`，与价格无关。
- 流量重置价格（xb-order #32，`plan_reset_prices`，00046）：
  - 每个套餐每个币种至多一档，`unit_amount > 0`；没有行 = 门户不卖流量重置。订单行快照了金额，所以价格直接可改、不随版本冻结。
  - 后台 `GET/PUT /v1/plans/:id/reset-prices`（PUT 近期重认证 + 幂等 `catalog_plan_reset_prices`，整组替换，审计 `plan.reset_prices_set` 记前后）；order 经 `ResetPrice(planID, cur)` 按订阅价格快照的币种取一档。
