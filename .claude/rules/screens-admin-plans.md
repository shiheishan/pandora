---
paths:
  - "panel/frontend/src/admin/screens/plans/**"
---

# 后台 · 套餐

- 样稿 admin-v4 还原：列表是一张装盒的表（搜索、类型页签 全部 / 订阅 / 一次性、状态下拉靠右；「新建」经 kit `PageActions` 进外框页头；归档行用 `rowClassName` 整行半透明）+ 新建弹窗（先选类型）。详情是整页（kit `DetailPage`，外框 `DETAIL_ROUTES` 已登记 `plans`）：返回「套餐」（带回列表查询串）、标题 + 状态 / 推荐标签 + 副行 `planSubtitle`（类型 · N 个有效订阅，不含代码）、有草稿时页头右侧「发布 vN」；页签 概览 / 价格 / 线路 / 版本（计数写在页签文字里）；右栏「基本资料」（`basicFacts`：名称、代码、说明、类型，「编辑」开 `BasicModal`）。概览顶部是 `overviewNotes` 的 Callout（草稿带「去发布」跳版本页签，没线路 / 没节点带「去线路」），下面用量、流量重置、销售（新购 / 续费〔仅订阅〕/ 从其他套餐变更分行）各一组 KvList，各组「编辑」开对应小弹窗，最后是归档 DangerRow。价格行补「折合每月 ¥x，省 y%」（`perMonthNote`，相对当前在售月付价）。地址：`/plans`、`/plans/<id>`、`/plans/<id>/<prices|pools|versions>`，查询串 `kind`、`status`、`q`。列表一次取全量（不分页），搜索与筛选在前端做，类型页签的计数只按状态筛（与设计稿一致）。
- 两类套餐的口径见 `redesign-2026-10-16.md`「套餐」。一次性套餐没有「价格」页签，价格与有效天数在概览的「价格与有效期」组里改；销售设置里一次性套餐没有「允许续费」，请求体 `allow_renewal` 恒 false。归档后整个详情只读（不给编辑、发布、开关），文案写明老用户不能续费。
- 后端按块写（`PUT v1/plans/:id/{basic,sales,reset,prices,pools,usage}`、`POST .../versions/:vid/publish`、`POST .../archive`），都带套餐的 `expected_row_version`、要近期二次认证与幂等键，成功都回 `{plan: PlanDetail}`：写后先用 `api.ts` 的 `useApplyPlan` 把响应写回详情缓存（下一次写拿到新 row_version），再按前缀失效。详情里的写统一走 `EditModals.tsx` 的 `usePlanWrite`。
- 新建是一步 `POST v1/plans`：订阅只带填了的档（`prices`），一次性只带 `unit_amount` + `validity_days`，另一类型的键不出现；`publish: true` 时发布失败（没有可服务节点、没有在售价）整单不建。422 的 `prices.<i>` 指的是**发出去的第 i 档**，经 `createFieldKey` 映射回表单的 `tier.<周期>`。套餐代码由后端按名称生成，前端 `autoCode` 只做预览，规则与 Go `catalog/service/code.go` 逐条相同，改一边要改另一边（假后端 `dev/mock/admin/plans-store.ts` 也有一份）。
- 订阅改价按档发：停售 = 该档 `unit_amount: null`，添加 = 该档新金额（已有在售价时替换）；在售套餐至少留一档由后端 422 拒绝，前端不另拦。
- 「新建版本」没有单独接口：改用量（`PUT usage`）就是写进草稿（没有就新建），所以「新建版本」打开用量弹窗，已有草稿时禁用。
- 线路开关以详情里的 `pool_ids` 为准算新集合（选择器只列未停用的池，已绑但停用的池不在里面，不能从选择器的 `bound` 反推，否则会被悄悄解绑）；改动立即生效。绑池选择器的数字显示 `deliverable_nodes / active_nodes`；「0 节点」提示（`poolBindingWarning`）只按 `deliverable_nodes` 求和。
- 写失败走 `failure.ts` 的 `useCatalogFailure`：只有 422 的 `fields` 给表单（没有表单可标的写把 fields 的话 Toast 出来）；409 的 `fields` 是乐观锁现值，Toast 信封原文，并重拉数据。不要直接用通用 `useFailure`。
- 文案、换算、校验、请求体都在 `model.ts`（配 `model.test.ts`），组件只摆放。金额只有人民币；界面的 GB 是 GiB（与后端一致），限速按 Mbps 填、发 kbps（×1000）。
- 别的模块在用：`api.ts` 的 `usePlans`（订单页人工开单）、`planOptionsKey`（用户、内容）、`model.ts` 的 `periodLabel(interval: string, count)`（订单页）、`schemas.ts` 的 `PlanRow` / `PriceRow`（订单页）。改这些先看引用方。
