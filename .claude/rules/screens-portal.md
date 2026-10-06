---
paths:
  - "panel/frontend/src/portal/screens/**"
---

# 门户页面通用约定

- 页面只改自己目录，不动 `portal/screens/index.ts` 的 `SCREENS` 登记表；页头标题与副标题由 Shell 按 `portal/pages.ts` 画，页面只管内容区。
- 页面之间的约定地址（改任何一个都要同步两端）：
  - `#/subs?sub=<订阅 id>`：我的订阅选中哪条
  - `#/plans?tab=packs`：流量包标签
  - `#/checkout?plan=<套餐>&price=<价格>`（结账页再判新购 / 续费 / 变更）、`?renew=<订阅 id>[&price=]`、`?pack=<流量包>`
  - `#/orders/<订单 id>`：订单页展开该行；`?paid=1` 是收银台回跳（`common/PayFlow.tsx` 的 `return_url`），弹支付确认
  - `#/messages?tab=announcements`：消息页公告标签
  - `#/tickets/new?order=<订单 id>`：新建工单预填关联订单；`#/tickets/<id>`、`#/help/<slug>`
- 页面数据只经 `common/` 与外框 `portal/queries.ts` 的查询取，同一接口同一查询键；外框 schema 不够用时在 `queries.ts` 里写全，外框经 `select` 取自己的部分（订阅列表、佣金概况即如此），不另起查询键。
- `common/` 只放多个门户页面共用的东西；只有一个页面用的放回该页面目录，后台也要用的提升到 `core/` 或 `ui/`，不让后台引用 `portal/screens/common`。
- schema 按 Go 实际编码写严：无 omitempty 的字段必填（指针 / 可空列 nullable），只有 omitempty 的字段可选；页面对缺席做降级，而不是放宽 schema。实时失效只靠查询的 `meta.topics`。
- 各页面的 schema 被 `panel/frontend/tests/mock-portal*.test.ts` 与 `tests/smoke/` 引用核对，改 schema 要一起跑、一起改。
- 日期显示统一按按日用量接口回的切日时区（`common/subscriptions.ts` 的 `timeZone` → `common/traffic.ts` 的 `formatDate`），与用量柱同一口径；拿不到时才退回浏览器本地时区。
- 幂等键从 `common/intent.ts` 取（转出 `core/intent`）：一次用户意图一把、按请求指纹复用，成功或 4xx 业务拒绝后丢弃，断网与 5xx 保留；没挂幂等中间件的接口不带键。
- 门户只展示 CNY 价格与能收 CNY 的支付方式（余额与 epay 只有 CNY）；USDT 一律不做。
