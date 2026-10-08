---
paths:
  - "panel/frontend/src/portal/screens/checkout/**"
  - "panel/frontend/src/portal/screens/orders/**"
  - "panel/frontend/src/portal/screens/wallet/**"
  - "panel/frontend/src/portal/screens/common/**"
---

# 门户 · 下单、支付与订单

- 确认页的意图由地址决定（`checkout/model.ts` 的 `parseTarget`，地址见 screens-portal.md）；入口页只拼地址不判模式。
- 金额全取报价接口（`common/quote.ts`）：余额开关默认打开，只在 `with_balance` / `without_balance` 两组数之间切换；Forced 锁成打开；Kept 时紧挨金额写「最低要付 ¥1.00，所以这次余额只用 ¥x」；SmallDue 只有换套餐的零头免掉（`waived` > 0，写「差价不到支付最低额，这次免了」）；新买、续费、流量包的应付低于最低额且余额不够时不免，按钮置灰「先充值或用余额付」并给钱包入口（A 路收窄，标记字段名未定，现按 small_due 而 waived=0 认）。文字在 `checkout/copy.ts`
- 建单带 `as_of` 与 `expect {total, balance_applied, payable}`，`use_balance` 传这一档用掉的余额；409 `quote_changed` 重新报价并标「已更新」，用户再点时请求体变了、按指纹换新幂等键；409 `order_pending` 给去那张单（`fields.order_id`）的入口。两个码已登记进 core 的 `SERVER_ERROR_CODES`（`purchaseRefusal` 按码认）
- 付款是一页（`#/checkout/pay/<订单>`，`common/PayFlow.tsx` 的 `PayPanel`，订单页与钱包的弹窗也用它）：手机主按钮「打开支付宝付款」、二维码收进「用另一台手机扫码」；电脑直接给收银台地址的二维码（放不进 40 版时退回「打开付款页」）；3 秒查一次订单，付完自动去完成页，没更新再点「我已付款」（`POST v1/orders/{id}/query`）。
- 完成页（`#/checkout/done/<订单>`，`common/Result.tsx`）分「发生了什么 / 你要做的 / 没变的」；新买一份把新链接与「添加到 App」放最上面。订单详情没有订阅 id，新那份按套餐名与「有效期至」认（`findNewSub`）
- 下单幂等键按请求体指纹复用：双击、失败重试、关掉支付弹窗后再点都回放同一张订单，改了任何参数才换新键。
- 刚下的待支付单记住 30 分钟（确认页是跨页面的 `checkout/model.ts` 的 `placedOrders`，指纹去掉 as_of / expect；充值是 `usePlacedOrder`）：同样的意图再点时先 `recallPayable` 问一次还能不能付，能付就重开它的付款、不下第二张（否则余额冻结两次）；已取消、超时或付掉就 `forget()` 按新请求下单，付款收到 409 也 `forget`。
- 去收银台只做顶层 GET 导航：POST 跳转要自动提交表单，会被入口页 CSP 的 `form-action 'self'` 拦下。支付成功后失效 `['portal']` 前缀下全部查询。
- 「我已支付，刷新状态」（`POST v1/orders/{id}/query`）只对发起过支付（行上 `has_payment_intent`）的单显示，不带幂等键；结果归成已到账 / 渠道尚未确认 / 查询失败三种（订单已关闭才到账的钱也算已到账、引导提工单）。
- `processing` 的订单是支付已发起、等回调：不能取消也不能再付；待支付卡片取 draft / pending_payment / processing。
- 订单页展开由地址驱动（`#/orders/<id>`，点行切换用 replace），不在已加载列表里时单独成卡；`?paid=1` 弹支付确认（3 秒轮询、最多 2 分钟），关掉后抹掉 paid。已结束订单按设计是「显示更早」每次多取 6 条，不用分页器。
- 充值 `POST v1/me/topups` 响应是 200 不是 201；礼品卡兑换（`#/wallet/redeem`，幂等 `gift_card_redeem`）的选项与默认值来自 preview 的 `placement`，没有默认值时按钮置灰「先选一种用法」，兑换带 `choice`；成功后失效整个 `['portal']` 前缀。完成页按选定的用法自己写，不显示服务端 summary（话术里有「订阅」等词）。
- 钱包「余额明细」里挂账类流水按保留规则 6 称「挂账转入」。
