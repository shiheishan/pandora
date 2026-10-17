---
paths:
  - "panel/frontend/src/portal/screens/**"
---

# 门户页面通用约定

- 页面只改自己目录，不动 `portal/screens/index.ts` 的 `SCREENS` 登记表；内容列、页标题、Logo 由 Shell 按 `portal/pages.ts` 画，页面只管内容区。外观一律按样稿 `dev/prototype/portal-v5.html`，用 `portal/kit` 的套件（`portal-kit.md`）。
- 页面列表（`pages.ts` 的 `PAGES`）：我的套餐 `#/subs`、选购 `#/plans`、确认 `#/checkout`、订单 `#/orders`、钱包 `#/wallet`、邀请返利 `#/referral`、工单支持 `#/tickets`、消息 `#/messages`、帮助中心 `#/help`、账号安全 `#/account`、我的 `#/me`（只在手机有：资料卡、订单 / 消息 / 工单三格、邀请横幅、帮助中心、账号安全、外观、退出登录；桌面落回我的套餐）。「概览」10-16 删除，原来的公告横条与待支付提醒并进我的套餐页。
- 页面之间的约定地址（改任何一个都要同步两端；购买模型 10-07 起）：
  - 我的套餐：`#/subs`（每份一张卡）、`#/subs/<一份>/detail`（节点与每天用量）、`#/subs/<一份>/traffic`（流量不够：重置与换更大套餐）、`#/subs/<一份>/change`（换个套餐）、`#/subs/<一份>/rotated?old=<旧尾号>`（换新链接之后）、`#/subs/new`（再买一份）
  - 选购：`#/plans`；`?kind=one_time` 一次性标签（缺省按时间；目录里只有一类时不显示标签、忽略它）
  - 确认：`#/checkout?renew=<一份>[&from=plans]`、`?change=<一份>&plan=<套餐>`、`?new=<套餐>`、`?stack=<一份>`（一次性同款叠加）、`?reset=<一份>`（流量重置）；`?plan=<套餐>`：没有份时就是新买，有份时先按落点接口选放到哪里（`checkout/Landing.tsx`，不预选，只有一个选项直接跳），`?change-plan=<套餐>` 同 `?plan=`；都可带 `&price=` 预选买多久。流量包地址 `?pack=`、`?tab=packs` 与「另买一份」模式 `?mode=new` 已删（10-16）
  - 付款：`#/checkout/pay/<订单>?m=<provider:method>&<完成页上下文>`；完成：`#/checkout/done/<订单>?k=renew|change|new|stack|reset&sub=&was=&old=&refund=&waived=&revive=&stack=<叠加的字节>`（收银台回跳也回这里，上下文全在地址里）
  - 钱包：`#/wallet`；兑换卡 `#/wallet/redeem`
  - `#/orders/<订单 id>`：订单页展开该行；`?paid=1` 是订单页与钱包支付弹窗的收银台回跳（`common/PayFlow.tsx` 的 `payReturnUrl`），弹支付确认
  - `#/messages?tab=announcements`：消息页公告标签
  - `#/tickets/new?order=<订单 id>`：新建工单预填关联订单；`#/tickets/<id>`、`#/help/<slug>`
- 页头只有标题，没有副标题（10-16 设计稿）：标题默认取 `pages.ts` 的 `PAGES`（页面键 → 标题字符串）；子页经 `portal/head.tsx` 的 `usePageHead(标题, 返回落点)` 覆盖标题并给「‹ 返回」（应用内有上一页就退回，直接打开的地址退到落点）
- 叫法（原型第 7 节）：订阅叫「套餐 / 一份」，订阅地址叫「链接」，客户端叫「App」，导入叫「添加」；渲染文字里「订阅、导入、抵扣、折算、剩余价值、降级、客户端、落点、约付、周期、重置订阅、重置链接」0 次（原型 flow.md 第 7 节；「周期」说成「买多久：1 个月 / 3 个月 / 1 年」，「流量重置卡」是卡名不算）。守卫：`tests/mock-portal-wording.test.ts` 扫门户全部字符串与 JSX 文字（注释不算）。10-16 起界面文字里另有「门户、流量包、自动续费」0 次（同一文件的第二条用例，扫整个 `src/portal/`）。唯一例外是第三方 App 自己的中文按钮名（如 v2rayNG 的「更新订阅」）：只限 `screens/common/app-update.ts` 里 `button:` 的值，每篇第一次出现带禁用词的按钮名时补半句我们的叫法
- 页面数据只经 `common/` 与外框 `portal/queries.ts` 的查询取，同一接口同一查询键；外框 schema 不够用时在 `queries.ts` 里写全，外框经 `select` 取自己的部分（订阅列表、佣金概况即如此），不另起查询键。
- `common/` 只放多个门户页面共用的东西；只有一个页面用的放回该页面目录，后台也要用的提升到 `core/` 或 `ui/`，不让后台引用 `portal/screens/common`。
- schema 按 Go 实际编码写严：无 omitempty 的字段必填（指针 / 可空列 nullable），只有 omitempty 的字段可选；页面对缺席做降级，而不是放宽 schema。实时失效只靠查询的 `meta.topics`。
- 各页面的 schema 被 `panel/frontend/tests/mock-portal*.test.ts` 与 `tests/smoke/` 引用核对，改 schema 要一起跑、一起改。
- 购买路径（我的套餐、选购、确认与付款、完成页、订单、钱包与兑换卡）的页面文字或结构改了，同改 `panel/frontend/tests/browser`（按角色与可见文字选元素、按页面文字断言，见 `frontend-browser-e2e.md`），否则要等 CI 的浏览器步骤红了才发现。
- 日期显示统一按按日用量接口回的切日时区（`common/subscriptions.ts` 的 `timeZone` → `common/traffic.ts` 的 `formatDate`），与用量柱同一口径；拿不到时才退回浏览器本地时区。
- 幂等键从 `common/intent.ts` 取（转出 `core/intent`）：一次用户意图一把、按请求指纹复用，成功或 4xx 业务拒绝后丢弃，断网与 5xx 保留；没挂幂等中间件的接口不带键。
- 整个系统只有人民币（10-16）：金额一律 `formatMoney(minor)` 单参，页面不读任何 `currency` 字段；支付方式：余额、易支付，以及按站点币种建发票的 BTCPay Server、CoinPayments（用户在渠道收银台里选币付款，10-14 用户定要做加密货币支付；面板不直接收 USDT 转账）。
