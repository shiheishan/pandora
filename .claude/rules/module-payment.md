---
paths:
  - "panel/internal/module/payment/**"
---

# 支付与渠道模块

- 易支付（EPay）约束：
  - 易支付方式只收支付宝与微信支付（`EpayMethods = alipay, wxpay`），后台配置与下单严格过滤。
  - 协议规范：金额为元单位十进制字符串（`money.FormatProvider`）；MD5 签名参数名 ASCII 升序拼接，末尾直接拼商户密钥（不做 URL 编码）；异步通知为 GET 请求，处理成功必须回纯文本 `success`；无渠道事件 ID，以 `trade_no:TRADE_STATUS` 合成幂等键。
- 商户单号（`outTradeNo`）：
  - 订单首次支付使用订单号；后续重新发起支付使用 `订单号-序号`（`CountIntents` + 1，上限 64 字符）。
  - 回调与查单优先按 `out_trade_no` 查找对应的在途支付意图（`IntentRequiresAction`），意图记录对应渠道与方式快照。
- 回调与查单入账主链：
  - 回调 `HandleWebhook` 与查单 `Query` 均统一收拢至 `recordPayment` / `recordPaymentTx`。
  - 防重与幂等：按 `(ProviderID, PaymentRef)` 加咨询锁（`LockPaymentKey`），已入账记录比对金额与币种，一致标记 `AlreadyHandled`，不一致回 409。
  - 结算保存点：调用 `Settler.SettlePayment` 前开启保存点 `payment_settle`。结算报错回滚至保存点；若返回 `NotPayableError` 则转入挂账（`CaseReleasedOrder`, `CaseExcessCapture`, `CaseIneligibleSubscription`），并在同一事务落 `late_payment_cases` 与 `late_payment_suspense` 账目。
  - 订单结清后自动作废该订单名下其他在途意图（`CloseOrderIntents`）。
- 安全与查单约定：
  - 渠道凭据经 AAD 加密封存（`sealer.Seal` / `Open`），数据库不存明文密钥。
  - 查单 HTTP 客户端在拨号层执行 `checkDialAddr`，按解析后的真实 IP 拦截内网、环回与元数据地址（防 SSRF 与 DNS 重绑定）；生产环境严禁开启 `AllowPrivateHost`。
  - 主动查单（`Query`）禁止在外部事务内或持有订单锁时调用，避免与回调入账加锁顺序交叉死锁。后台查单必须落审计记录。
