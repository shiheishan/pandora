---
paths:
  - "panel/frontend/src/portal/screens/checkout/**"
  - "panel/frontend/src/portal/screens/orders/**"
  - "panel/frontend/src/portal/screens/wallet/**"
  - "panel/frontend/src/portal/screens/common/**"
---

# 门户 · 下单、支付与订单

- 结账模式由地址决定：`?pack=` 流量包、`?renew=` 续费、`?plan=` 时没有生效订阅走新购、同套餐走续费、换套餐走变更（`checkout/model.ts` 的 `resolveMode`）；入口页只拼地址不判模式。
- 设计稿把「余额」当四选一的支付方式，后端是抵扣金额：做成「使用余额抵扣」开关，`use_balance = min(余额, 优惠后应付)`，抵扣后应付为 0 时隐藏支付方式、按钮改「确认支付」。
- 变更套餐的折算、优惠与退余额全用服务端试算（`change-plan/preview`），前端不自己算；续费遇改价（`renewal_price.available=false`）只列当前价格并在预览里标「原价格已调整」。
- 下单幂等键按请求体指纹复用：双击、失败重试、关掉支付弹窗后再点都回放同一张订单，改了任何参数才换新键。
- 刚下的待支付单由 `common/intent.ts` 的 `usePlacedOrder` 记住 30 分钟：结账与充值同样的请求再点时先 `recallPayable` 问一次还能不能付，能付就重开它的支付、不下第二张（否则余额抵扣冻结两次）；已取消、超时或付掉就 `forget()` 按新请求下单，支付弹窗收到 409 也 `forget`。
- 去收银台只做顶层 GET 导航：POST 跳转要自动提交表单，会被入口页 CSP 的 `form-action 'self'` 拦下。支付成功后失效 `['portal']` 前缀下全部查询。
- 「我已支付，刷新状态」（`POST v1/orders/{id}/query`）只对发起过支付（行上 `has_payment_intent`）的单显示，不带幂等键；结果归成已到账 / 渠道尚未确认 / 查询失败三种（订单已关闭才到账的钱也算已到账、引导提工单）。
- `processing` 的订单是支付已发起、等回调：不能取消也不能再付；待支付卡片取 draft / pending_payment / processing。
- 订单页展开由地址驱动（`#/orders/<id>`，点行切换用 replace），不在已加载列表里时单独成卡；`?paid=1` 弹支付确认（3 秒轮询、最多 2 分钟），关掉后抹掉 paid。已结束订单按设计是「显示更早」每次多取 6 条，不用分页器。
- 充值 `POST v1/me/topups` 响应是 200 不是 201；礼品卡兑换（幂等 `gift_card_redeem`）后失效整个 `['portal']` 前缀（余额、订阅、流量包都可能变且没有推送）。
- 钱包「余额明细」里挂账类流水按保留规则 6 称「挂账转入」。
