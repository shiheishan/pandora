---
paths:
  - "panel/frontend/src/admin/screens/billing/**"
---

# 后台 · 订单与收款

- 保留规则 6：页签「待处理到账」（代码里仍叫 late / arrears，后端叫挂账 suspense）——订单取消后才到账、续费或变更时订阅已结束、超额扣款，是平台欠用户的钱；「转入余额」是贷记。不要改回「欠费 / 催缴」的语义。界面文案一律写「待处理到账」，不写「挂账」。
  - 默认只看待处理（`lateFilterOf` / `lateFilterQuery`：地址不带 `s` 即待处理，「全部」写 `s=all`）；右上「待处理合计」只用接口的 `pending_amount`（只有人民币，一个数）。设计稿的「N 笔 · 最久 N 天」没有接口，不做。
- 支付渠道是装盒的表格（样稿 admin-v4）：「新建渠道」经 `PageActions` 进页头；顶部一行统计由前端从列表算（`providerStats`：在线渠道、收新单中、已停用或暂停、未配置凭据，不含 offline）；列为渠道（名称、编码、类型 `providerKind`、状态标签 `providerTags`〔收新单中 / 已停止新单 / 已完全停用、未配置凭据、未配置 Webhook 密钥〕、说明 `providerNote`〔最低付款、手续费、最近回调〕、支付方式、回调地址）/ 今日成交（接口的 `today`，一个金额，没有笔数）/ 成功率 / 操作。开关「收单」只动 `accepting_new`（关 = 结账页不再显示、进行中的回调照常）；行内「停用」是完全停用，才动 `enabled`，都要过确认框（关开关的确认框叫「暂停收新单」，「停用」一词只留给完全停用；开、关的正文写明结账页最多 5 分钟内跟上，与「?」帮助同口径）。系统内置的 `offline` 渠道只读，不给开关、不能编辑（`model.ts` 的 `isOffline` / `toggleBody`）。可编辑的渠道点整行打开编辑。「回调地址」卡设计稿没画，但它是唯一配置回调前缀的地方，保留在表格下方。
- 新建 / 编辑渠道（`ProviderDialog.tsx`）对易支付、BTCPay Server、CoinPayments 三类在线渠道（`isEditableProvider`、`PROVIDER_ADAPTERS`）：新建时先选类型（`switchAdapter` 换类型保留编码、最低额与手续费），只有易支付有下单 / 查询路径与支付方式；凭据位的标签随类型（`credentialLabels`：商户号与密钥 / Store ID 与 API Key / Client ID 与 Client Secret），BTCPay 另有只写不读的 Webhook 密钥（列表只有 `has_webhook_secret`），CoinPayments 的地址可留空（后端用官方地址）。类型相关的说明在 `ProviderFields.tsx`。code 与类型建后不可改（PUT 请求体不带这两个键）；「最低付款额（元）」写进渠道 config 的 `min_amount`（分，范围 0.01–1000 元，默认 1.00，`parseMinAmount`），低于它的在线支付不发起，线下收款与赠送不受限，卡片备注里也写出来；留空 = 请求体不带 `min_amount`（后端：编辑保留原值、新建按 ¥1.00），前端从不发 0，说明文字随新建 / 编辑写「留空按默认」/「留空表示不改」（`minAmountHint`）；商户号与密钥只写不读，列表只有 `has_credentials`，编辑留空 = 不改；新建的渠道是「已启用、暂停收新单」，在卡片上打开收单才进结账页。后台改动在用户端结账页最多 5 分钟后生效（各网关的渠道缓存），写在「?」帮助与编辑弹窗里。
- 用户手续费（00042）：每个在线渠道「固定（元）+ 比例（%）」，请求体 `fee_fixed` 是分、`fee_percent` 是字符串（`parseFeeFixed` / `parseFeePercent`），两项都 0 = 不收；卡片备注写「向用户加收 x% + ¥y」（`feeLabel`）。订单详情的支付记录在金额后写「含手续费 ¥x」（`surcharge_amount`）。
- 回调地址：渠道卡显示该渠道的 `notify_url`；「回调地址」卡（`CallbackBaseCard.tsx`，`GET/PUT v1/payment-settings`，PUT 要近期重认证、不带幂等键）读写前缀，留空用站点地址。
- 订单详情右栏的「取消原因」：系统写的代码经 `cancelReasonLabel` 翻成中文（`account_deleted` =「账号已注销」，注销账号时一并取消的待付单；`user_cancelled` =「用户自己取消」；超时不写取消原因）；管理员取消时填的原因原样显示。后端加了新的系统原因代码，这里同步加。
- 订单列表与详情（样稿 admin-v4）：列表「人工开单」经 kit `PageActions` 进外框页头，表格装盒（`Table boxed`），表尾左「共 N 条 · 点一行查看详情」、右 `<Pager inline>`；手机（≤639）只留订单号（下面带「用户 · 内容」）与金额（下面带状态标签）两列，其余列 `hideOnMobile`（样稿 .h-m / .m-only）。待处理到账手机收起账龄与状态，支付渠道收起今日成交与成功率（挪到渠道格里一行），收入调整收起生效日（挪到登记人后面）。详情是整页（`OrderPage.tsx`，kit `DetailPage`；地址仍是 `/billing/orders/<id>`，外框 `DETAIL_ROUTES` 登记了 `billing:orders`；返回「订单」带回列表原来的查询串 `s`/`q`/`o`/`user_id`）：页头等宽订单号 + 状态标签 + 副行 `orderMeta`（来源 · 多久前），右侧「取消订单」（`OrderActions.tsx` 的 `CancelDialog`，原因必填，标签「原因（用户能看到）」：原样显示在用户订单详情第一行，用户 10-21 定不拆字段）与「标记已支付」（`MarkPaidDialogs`：先弹填写框填凭证号与收款说明，「下一步」校验后再弹确认框，确认才提交；确认框取消回到填写框、输入保留）。主栏：金额块（`orderAmountFacts`：内容、金额、优惠、余额抵扣〔恒显示〕、应付、实收）、订单项（多于 1 项时）、支付记录（`RowList`）、「向渠道查单」Callout；右栏「详情」（`orderSideFacts`：用户链接、渠道、来源、下单时间、支付截止〔`deadlineLeft`「还剩 N 分钟」〕、开单原因、支付 / 取消 / 过期时间、取消原因）。订单列表、详情、动作三个文件用 `Orders.module.css`，不 import `Billing.module.css`。
- 渠道列写到具体支付方式：后端列表行与详情给 `method`（成功那笔收款的方式，没有为 null），支付尝试也给 `method`；`channelLabel` / `payChannel` 写成「渠道名 · 方式中文名」（`methodLabel`：alipay 支付宝、wxpay 微信支付、offline 线下收款，不认识的原样显示；线下与没有方式时只写渠道名）。
- 订单列表页头「人工开单」前有次要按钮「购买限制」，开 `PurchaseLimitsDialog.tsx`（读写 `v1/settings/purchase`，两个键整份提交，0 = 不限、0–50；前端校验 `model.ts` 的 `purchaseLimitErrors` 与后端同文案；查询键挂在 `['admin','billing']` 前缀下）。弹窗里写明只管用户端自助下单，人工开单与礼品卡兑换不受限。
- 人工开单不提供「从余额扣除」（D-C-3 已决），引导写在「?」帮助里（`billing:orders`）。结算方式是单选组（原生 radio，每项带说明），不是下拉框。
- 人工开单的落点由管理员选（购买模型统一，2026-10-07）：选好用户、套餐、价格后调 `POST v1/orders/manual/preview`（只读，不要重新认证、不带幂等键），服务端按 `purchase.Options` 给出选项与默认值，页面（`PlacementPicker.tsx`、`placement.ts`）只负责把每项的结果写成人话，不重算规则、不算钱。
  - 选项：续一期（订阅同款，生效中的按到期从早到晚，再接过期 30 天内的）、叠加（`stack`，一次性套餐同款，默认就是它：总流量加上、天数顺延）、恢复并改成 P、新开一份、换掉某份改成 P。每项写明到期日变化、剩余价值去向与链接是否变化。服务端给 `unavailable_reason` 的项灰着不能选，写出原因；唯一选项不可用时按钮写「这单开不了」。
  - 有入口上下文（从用户详情某份订阅点「给这份开单」，地址带 `&sub=<订阅 id>`）时预选入口那份；没有默认值时一个都不选，提交按钮灰着写「先选落点」；只有一个选项时不让选，直接写出结果。换掉生效中的订阅永远不是默认（服务端保证，页面不另行兜底）。
  - 提交带 `target: {kind, subscription_id?}`；换了用户、套餐或价格，点过的落点作废；409 或带 `fields.target` 的 422（落点已失效或没选）后重新取一遍选项。
  - preview 的 422 落到表单项上（`previewFailure`）：`fields.price_id` 落到「套餐与周期」，按钮写「先换一个价格」；`fields.entry_subscription_id` 时去掉入口重取、不预选，落点区写明原因。其余失败整块提示并给重试。
  - 待支付单低于站点最低付款额时服务端回 422 带 `fields.settlement`（billing.errManualBelowMinimum），随通用的 `useFailure` 落到「结算方式」上，并重取一次 preview。提交前的拦截全用 preview 给的数：顶层 `min_payment`（与报价、建单同一个 minPayment），每个落点的 `due`（按待用户支付开时的应付，换套餐已先抵）与 `below_minimum`（与建单同一个判定）；`manualBelowMinimum` 只把它写成「这单应付 ¥x，低于…¥y」，给「改用赠送」「改用线下已收款」两个按钮，提交按钮灰着写「低于最低付款额」。不再读渠道列表自己算
  - 响应多 `proration_credit` / `balance_refund` 时即落成了换套餐，`manualCreatedToast` 按落点（`kind`）报续费、叠加、换套餐或新开，换套餐时带退回余额的金额。
  - 删掉了「自动续同款、自动换别的套餐」的说明：后端不再替管理员挑落点。
- 枚举只收库里的取值（`schemas.ts`）：订单 5 态（不做退款，没有退款态，支付记录也没有 refunds 与失败原因）、类型 6 种 new / renewal / upgrade / topup / traffic_reset / stack（换套餐不论补差退差都是 upgrade；`traffic_reset` 是用户端买的流量重置；没有流量包 addon）、支付意图 requires_action / succeeded / cancelled / expired、收款只有 succeeded、挂账 suspense / applied；后端不回的键（organization_id、tax_amount、行的 product_id）不写进 schema。
- 取消订单、标记已支付的失败文案（凭证号重复、订阅已结束款项进挂账都回 409）是后端中文原文，经 `useFailure` 原样 Toast，不要按状态码改写。
- 会改用户余额或订阅的写（标记已支付、向渠道查单补记、人工开单、转入余额）成功后，除了 `useInvalidateBilling` 还要调 `users/api.ts` 的 `useInvalidateUsers`。
- 「向渠道查单」只对发起过支付的待支付订单出现（`canQueryChannel`：`provider_code` 非空且非 offline）；查单与取消不挂 reauth，人工开单与标记已支付挂 reauth。
- 用户详情的「为其开单」（`users/tabs.tsx`）跳 `#/billing/orders?new=<用户 id>`，某份订阅的「给这份开单」再带 `&sub=<订阅 id>`；订单页靠 `?new=` 打开人工开单弹窗并预选用户。改这个参数要同步 `users/tabs.tsx`。
- 人工开单（落点、最低付款额、赠送 / 线下已收款）的页面文字或结构改了，同改 `panel/frontend/tests/browser/admin.spec.ts`（按角色与可见文字选元素、按页面文字断言，见 `frontend-browser-e2e.md`），否则要等 CI 的浏览器步骤红了才发现。
- 只有人民币（10-16）：schema、页面、假后端都没有 `currency` / `currencies`，`formatMoney` 只传一个参数；收入调整不按币种筛。
- 订单详情的 `discount_amount` 是全部减免（券与赠送），没有会员折扣。
