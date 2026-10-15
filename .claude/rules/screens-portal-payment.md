---
paths:
  - "panel/frontend/src/portal/screens/checkout/**"
  - "panel/frontend/src/portal/screens/orders/**"
  - "panel/frontend/src/portal/screens/wallet/**"
  - "panel/frontend/src/portal/screens/common/**"
---

# 门户 · 下单、支付与订单

- 确认页的意图由地址决定（`checkout/model.ts` 的 `parseTarget`，地址见 screens-portal.md）；入口页只拼地址不判模式。
- 金额全取报价接口（`common/quote.ts`）：余额开关默认打开，只在 `with_balance` / `without_balance` 两组数之间切换；Forced 锁成打开；Kept 时紧挨金额写「最低要付 ¥1.00，所以这次余额只用 ¥x」；只有换套餐抵扣后 ≤ ¥0.99 的零头免掉（`small_due` + `waived`，结果页写「零头 ¥0.xx 已免」）；其余用尽余额还差的低于最低额时 `below_minimum` 为真：余额行锁成「已全用上」，按钮置灰「余额不够，先充值再来」，写明还差多少、在线付款最少多少，给钱包入口（后端下单 422）。用过的优惠码记在地址 `?coupon=`，充值回来还在。文字在 `checkout/copy.ts`
- 建单带 `as_of` 与 `expect {total, balance_applied, payable}`，`use_balance` 传这一档用掉的余额；409 `quote_changed` 重新报价并标「已更新」，用户再点时请求体变了、按指纹换新幂等键；409 `order_pending`（`fields.order_id`）给「取消它 / 去付款」，那张已超过付款期限（`fields.lapsed` 为 `"true"`，`common/quote.ts` 的 `refusalOf`）时只给「取消它」；付款页发起支付回 409 `order_lapsed` 时给「取消这张单，重新下单」，取消后回选购页（`isPaymentLapsed`）。这几个码（含下面的 `payment_in_progress`）都登记在 core 的 `SERVER_ERROR_CODES`，一律按码与 fields 认，不匹配文案
- 付款是一页（`#/checkout/pay/<订单>`，`common/PayFlow.tsx` 的 `PayPanel`，订单页与钱包的弹窗也用它）：手机主按钮「打开支付宝付款」、二维码收进「用另一台手机扫码」；电脑直接给收银台地址的二维码，二维码之下是次按钮「在这台电脑上付款」并写一句用途（`payHereNote`：支付宝「没带手机？在这台电脑上用支付宝网页付」）；放不进 40 版时退回「打开付款页」；3 秒查一次订单，付完自动去完成页，没更新再点「我已付款」（`POST v1/orders/{id}/query`）。
- 完成页（`#/checkout/done/<订单>`，`common/Result.tsx`）分「发生了什么 / 你要做的 / 没变的」；新买一份把新链接与「添加到 App」放最上面。落到哪一份按订单详情的 `subscription_id` 认（`result-copy.ts` 的 `doneSub`；履约后才有，新购没履约时不猜，续费等先用地址里的 `sub`）。换套餐的「在 App 里点一次更新」旁边给 `common/UpdateHelp.tsx` 的链接，去帮助中心这台设备那一段
- 下单幂等键按请求体指纹复用：双击、失败重试、关掉支付弹窗后再点都回放同一张订单，改了任何参数才换新键。
- 刚下的待支付单记住 30 分钟（确认页是跨页面的 `checkout/model.ts` 的 `placedOrders`，指纹去掉 as_of / expect；充值是 `usePlacedOrder`）：同样的意图再点时先 `recallPayable` 问一次还能不能付，能付就重开它的付款、不下第二张（否则余额冻结两次）；已取消、超时或付掉就 `forget()` 按新请求下单。
- 发起支付失败怎么办在 `common/payRetry.ts`（`payFailureAction` / `afterOrderCheck`），`PayPanel` 按它做：
  - 409 `payment_in_progress`（同一张单上一次还在去渠道建单）：不 `forget`、不调 `onUnpayable`，显示「正在发起支付，请稍候…」，按 `PAY_IN_PROGRESS_BACKOFF_MS`（2/5/10 秒，合计 17 秒，要盖过后端建单最长 15 秒）自动重发，最多 `PAY_IN_PROGRESS_MAX_RETRIES`（3）次，用完显示错误加「重试」；定时器卸载时清掉。
  - 409 `conflict`（这次发起被新的发起取代，或订单状态变了）：先 `order.refetch()`；仍是 pending_payment 就写「这次付款已被新的发起取代，请重试。」并给「重试」，不是待付才调 `onUnpayable`（`forget`）；刷新失败只显示错误加「重试」。
  - 409 `order_lapsed`：调 `onUnpayable`。
- 去收银台只做顶层 GET 导航：POST 跳转要自动提交表单，会被入口页 CSP 的 `form-action 'self'` 拦下。支付成功后失效 `['portal']` 前缀下全部查询。
- 「我已支付，刷新状态」（`POST v1/orders/{id}/query`）只对发起过支付（行上 `has_payment_intent`）的单显示，不带幂等键；结果归成已到账 / 渠道尚未确认 / 查询失败三种（订单已关闭才到账的钱也算已到账、引导提工单）。
- 订单状态只用库里的 5 态（`00013_order.sql`：pending_payment / paid / fulfilled / cancelled / expired），外加等产品确认的退款两态；没有 draft / processing。发起过支付等回调的单仍是 pending_payment（看 `has_payment_intent`），待支付卡片只取 pending_payment。
- 订单页展开由地址驱动（`#/orders/<id>`，点行切换用 replace），不在已加载列表里时单独成卡；`?paid=1` 弹支付确认（3 秒轮询、最多 2 分钟），关掉后抹掉 paid。已结束订单按设计是「显示更早」每次多取 6 条，不用分页器。
- 充值 `POST v1/me/topups` 响应是 200 不是 201；礼品卡兑换（`#/wallet/redeem`，幂等 `gift_card_redeem`）的选项与默认值来自 preview 的 `placement`，没有默认值时按钮置灰「先选一种用法」，兑换带 `choice`；成功后失效整个 `['portal']` 前缀。完成页按选定的用法自己写，不显示服务端 summary（话术里有「订阅」等词）。
- 钱包「余额明细」里挂账类流水按保留规则 6 称「挂账转入」。
- 支付手续费（00042）：渠道可向用户加收「固定 + 比例」手续费，用余额付不收。三处选付款方式（确认页、订单页弹窗 `Choose`、钱包充值）都用 `usePaymentMethods(应付分)` 取带 `fee` / `total` 的方式列表，文字统一走 `common/fee.ts`（`feeLine`、`feeRule`）；付款面板显示发起支付回来的 `amount`（含手续费）并在下面写「含手续费 ¥x，订单 ¥y」（`surchargeNote`）。订单的应付、总额与完成页的「付了多少」不含手续费。
- 确认页 `?reset=<一份>`（`model.ts` 的 `reset` 目标）：报价 `action: 'reset'`，建单 `POST v1/me/subscriptions/{id}/traffic-reset`（幂等 `order_create`，体只有余额 / 优惠码 / as_of / expect），没有「买多久」；完成页 `k=reset`。
- 会员折扣：报价行 `discount` 只含券，`member_discount` / `member_percent` 是会员折扣（`subtotal − discount − member_discount − credit = total`）；确认页在优惠码后写「会员折扣（几折）」（`copy.ts` 的 `memberWord`），换套餐算式同样减掉。订单行的 `discount_amount` 含会员折扣，`member_discount_amount` 必填。流量重置（`target.kind === 'reset'`）永远按原价：确认页不显示优惠码输入，报价与建单都不带券码，会员折扣恒为 0（用户 10-14 定）
