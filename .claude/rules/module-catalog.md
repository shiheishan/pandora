---
paths:
  - "panel/internal/module/catalog/**"
---

# 套餐与目录模块

- 两类套餐（`plans.kind`，建好后不可改，价格经复合外键 `(plan_id, plan_kind)` 钉住）：
  - 订阅套餐 `subscription`：只有 (month,1) (month,3) (month,6) (year,1) 四档价；版本的流量配额 period = `cycle`（每周期一份、新周期清零）。
  - 一次性套餐 `one_time`：只有一个在售价 (one_time,1) + `validity_days`（空 = 永久）；流量配额 period = `total`；`allow_renewal` 恒 false（库里 CHECK）。
  - 配额 period 由 `trafficQuotas(kind, bytes)` 按类型落，不让人选；不限流量就没有配额行。
- 后台写接口按块拆（基本信息、销售、流量重置、价格、线路、用量、发布、归档），每块都以**套餐的 row_version** 做乐观锁：改价、改线路、改用量这些不落在 plans 行上的改动也经 `bumpPlanRow` 推进它。所有写用例第一步 `lockEditablePlan`（FOR UPDATE、未归档、row_version 对上）。
- 新建（`CreatePlan`）一个事务建成：套餐 → 草稿版本 1（用量）→ 线路 → 价格 → 可选发布（`publishTx`）。code 由名称自动生成（`code.go`，与设计稿 autoCode 同一套词表与规则，重名加 `-2`、`-3`），之后不改。
- 用量（流量、设备、限速）改动写进草稿版本（没有就新建，权益从当前版本复制），发布后成为当前版本：新购用新版本；订阅老用户下个周期切换（subscription 的周期滚动做）；一次性老用户不受影响。发布要求至少一档在售价，并经 node 的 `PublishGate.CheckPublishable(planID)` 确认线路里有可服务节点。首个版本发布即上架（draft → active）。
- 价格只追加：改价 = 归档旧价、建新价（`UpdatePrices`，先归档再插，唯一索引保证同档只有一个在售）；在售套餐至少保留一档。价格行不可删（守卫 `catalog_guard_price`）。
- 流量重置挂在套餐上：`plans.reset_price`（空 = 不卖，> 0）与 `reset_limit`（空 = 不限，1–1000；订阅按每周期、一次性按有效期内，计数在 subscription）。订单行快照了金额，所以直接可改、不随版本冻结。
- 线路按套餐绑定（`plan_node_pools(plan_id, pool_id)`），**改动立即生效**：绑池前由 node 的 `PoolGuard.LockAssignablePools` 在同一事务内按 id 升序锁池（存在且未停用），改完调 `PoolGuard.MarkUsersChanged` 由 node 在提交后推进名单纪元。节点下发与名单经 `PlanPools(planIDs)` 取线路（订阅按版本找到套餐）。`PoolUsage` 按套餐统计：`Plans` / `PlanNames` 只算未归档套餐（挡删池、后台节点池列表「被 N 个套餐使用」都用它），`Bindings` 含全部绑定（带 `Archived` 与套餐全部版本 id，供 node 判断有没有人在用）。归档不解绑（老用户到期前照常用节点），但归档套餐的绑定不占池：删池时 node 在同一事务里调 `ReleaseArchivedPoolBindings(poolID)` 把它们删掉（用户 2026-10-16 定）。改绑的写接口只有 catalog 的 `PUT /plans/:id/pools`；node 只注册绑池选择器 `GET /plans/:id/pools`。
- 下单快照（`Offer` / `PlanOffers` / `ListOffers`）：
  - `UseNew`：在售、可见、接受新购；`UseStack`：同上且必须是一次性套餐；`UseChange`：在售、可见、允许变更（可跨类型）；`UseRenewal`：只许订阅套餐（一次性回 `ErrChangeNotAllowed`），归档或关了续费回 `ErrRenewalNotAllowed`。后台开单（Manual）跳过可见性。
  - 没有上架时间窗、价格时间窗、用户组专属价、库存、限购、排序、卖点；目录顺序是推荐在前、再按 created_at。
- 已发布版本及其权益、配额冻结不可变（触发器兜底，撞上转 `ErrSnapshotFrozen`）。
- 改线路（`SetPlanPools`）：只对新加的池检查存在、未停用，原来就绑着、后来停用的池照样保留；已发布过的套餐（`current_version_id` 非空）改完在同一事务里过 `PublishGate.CheckPublishable`，不能一步改成没有可服务节点（审查 1016-A1）。
