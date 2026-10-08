---
paths:
  - "panel/frontend/src/portal/screens/**"
---

# 门户页面通用约定

- 页面只改自己目录，不动 `portal/screens/index.ts` 的 `SCREENS` 登记表；页头标题与副标题由 Shell 按 `portal/pages.ts` 画，页面只管内容区。
- 页面之间的约定地址（改任何一个都要同步两端；购买模型 10-07 起）：
  - 我的套餐：`#/subs`（每份一张卡）、`#/subs/<一份>/detail`（节点与每天用量）、`#/subs/<一份>/traffic`（加流量）、`#/subs/<一份>/change`（换个套餐）、`#/subs/<一份>/rotated?old=<旧尾号>`（换新链接之后）、`#/subs/new`（再买一份）
  - 选购：`#/plans`；`?tab=packs` 流量包标签、`?mode=new` 另买一份、`?sub=<一份>` 流量包加到哪一份
  - 确认：`#/checkout?renew=<一份>[&from=plans]`、`?change=<一份>&plan=<套餐>`、`?change-plan=<套餐>[&sub=<一份>]`（先选换掉哪一份，不预选）、`?new=<套餐>`、`?pack=<流量包>&sub=<一份>`（没带 sub 且多份时在确认页选，选过带 `pick=1`）；都可带 `&price=` 预选买多久。老地址 `?plan=` 照新规则解析（没有套餐新买、有同款续费、否则选换掉哪一份），过期提示里的 `?renew=` 兼容
  - 付款：`#/checkout/pay/<订单>?m=<provider:method>&<完成页上下文>`；完成：`#/checkout/done/<订单>?k=renew|change|new|pack&sub=&was=&old=&refund=&gb=&waived=&revive=`（收银台回跳也回这里，上下文全在地址里）
  - 钱包：`#/wallet`；兑换卡 `#/wallet/redeem`
  - `#/orders/<订单 id>`：订单页展开该行；`?paid=1` 是订单页与钱包支付弹窗的收银台回跳（`common/PayFlow.tsx` 的 `payReturnUrl`），弹支付确认
  - `#/messages?tab=announcements`：消息页公告标签
  - `#/tickets/new?order=<订单 id>`：新建工单预填关联订单；`#/tickets/<id>`、`#/help/<slug>`
- 页头：标题默认取 `pages.ts`；子页经 `portal/head.tsx` 的 `usePageHead(标题, 返回落点)` 覆盖标题并给「‹ 返回」（应用内有上一页就退回，直接打开的地址退到落点）
- 叫法（原型第 7 节）：订阅叫「套餐 / 一份」，订阅地址叫「链接」，客户端叫「App」，导入叫「添加」；渲染文字里「订阅、导入、抵扣、折算、剩余价值、降级、客户端、落点」0 次。守卫：`tests/mock-portal-wording.test.ts` 扫门户全部字符串与 JSX 文字（注释不算）
- 页面数据只经 `common/` 与外框 `portal/queries.ts` 的查询取，同一接口同一查询键；外框 schema 不够用时在 `queries.ts` 里写全，外框经 `select` 取自己的部分（订阅列表、佣金概况即如此），不另起查询键。
- `common/` 只放多个门户页面共用的东西；只有一个页面用的放回该页面目录，后台也要用的提升到 `core/` 或 `ui/`，不让后台引用 `portal/screens/common`。
- schema 按 Go 实际编码写严：无 omitempty 的字段必填（指针 / 可空列 nullable），只有 omitempty 的字段可选；页面对缺席做降级，而不是放宽 schema。实时失效只靠查询的 `meta.topics`。
- 各页面的 schema 被 `panel/frontend/tests/mock-portal*.test.ts` 与 `tests/smoke/` 引用核对，改 schema 要一起跑、一起改。
- 日期显示统一按按日用量接口回的切日时区（`common/subscriptions.ts` 的 `timeZone` → `common/traffic.ts` 的 `formatDate`），与用量柱同一口径；拿不到时才退回浏览器本地时区。
- 幂等键从 `common/intent.ts` 取（转出 `core/intent`）：一次用户意图一把、按请求指纹复用，成功或 4xx 业务拒绝后丢弃，断网与 5xx 保留；没挂幂等中间件的接口不带键。
- 门户只展示 CNY 价格与能收 CNY 的支付方式（余额与 epay 只有 CNY）；USDT 一律不做。
