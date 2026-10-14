---
paths:
  - "panel/internal/module/payment/**"
---

# 支付与渠道模块

- 易支付（EPay）约束：
  - 易支付方式只收支付宝与微信支付（`EpayMethods = alipay, wxpay`），后台配置与下单严格过滤。
  - 协议规范：金额为元单位十进制字符串（`money.FormatProvider`）；MD5 签名参数名 ASCII 升序拼接，末尾直接拼商户密钥（不做 URL 编码）；异步通知为 GET 请求，处理成功必须回纯文本 `success`；无渠道事件 ID，以 `trade_no:TRADE_STATUS` 合成幂等键。
- 加密货币渠道（#57，适配器在 `provider/btcpay`、`provider/coinpayments`，外部 HTTP 一律走 `provider/safehttp`）：
  - BTCPay Server（Greenfield）：凭据 = Store ID（商户号位）+ API Key（密钥位）+ Webhook 密钥，都封存。按站点币种建发票、跳 `checkoutLink`；Webhook 在 BTCPay 商店里配（后台渠道卡显示回调地址），`BTCPay-Sig: sha256=hex(HMAC-SHA256)` 常数时间比对，验签通过后按发票号回查，金额币种状态一律以回查为准。只有 `Settled` 且附加状态不是手动标记（`Marked`）或部分付款（`PaidPartial`，商店开了付款容差时出现）才算成功；`Expired` / `Invalid` 算失败；部分付款不算成功，留证据给后台线下处理。事件 ID = `发票号:状态[:MARKED|:PARTIAL]`。
  - CoinPayments（新版 v2 REST，`https://a-api.coinpayments.net`）：凭据 = Client ID + Client Secret。请求与回调同一种签名：`base64(HMAC-SHA256(secret, BOM + 方法 + 完整 URL + ClientID + 时间戳 + 请求体))`，回调还核对 `X-CoinPayments-Client` 与时间窗（±5 分钟），URL 用建发票时登记的回调地址（`NotifyURLs` 候选）。只有 `completed` 算成功；`cancelled` / `timedOut` / `deleted` 算失败；任一笔付款实收低于应收按待定（部分付款不算成功）。法币币种 ID 从公开接口查，包级缓存 24 小时。
  - 两个适配器都：币种与金额以渠道发票的法币金额与意图核对，不符拒绝；不跟随跳转；Raw 与日志里不出现 API Key、密钥。
- 用户手续费（#15，代码里叫 surcharge，区别于渠道成本 `Fee`）：
  - 每个在线渠道「固定 + 比例」（`fee_fixed` 元、`fee_percent` 百分比），缺省 0；线下渠道库里 CHECK 不许设。公式 `Surcharge(应付) = 应付 × 比例 / 100 + 固定`，向上取整到分（`service/surcharge.go`）。
  - 发起支付时意图金额 = 订单应付 + surcharge，`surcharge_amount` 记在意图和收款上；最低付款额按应付判断。入账时 `SettleInput.Amount = 实收 − surcharge`（等于订单应付，订单那边的核对不变），`SettleInput.Surcharge` 交给账本单独贷记 `platform_revenue / payment_surcharge`，不计入佣金基数、不改订单总额。
  - 迟到进挂账时只有 `实收 − surcharge` 进 `late_payment_suspense` 和挂账案金额（以后转余额只转这部分），surcharge 照记手续费收入。
  - 门户 `GET /v1/payment-methods?amount=<分>` 每种方式带这张单的 `fee` 与 `total`；用余额付不收手续费。
- 回调地址（#43）：`notifyBase` 优先用 settings 的 `payment.notify_base_url`（后台 `GET/PUT /v1/payment-settings`，生产只收 https，`AllowPrivateHosts` 时也收 http），存的值不可用就回退站点地址；回调地址 = 前缀 + `/v1/webhooks/payments/<渠道代码>`。验签候选 `notifyURLCandidates` = [当前前缀拼的, 站点地址拼的]，改前缀前建的发票仍能验过。
- 发起支付两段式（`intent.go`），渠道调用不在数据库事务里：
  - `BeginIntent` 在调用方事务里（order.Pay 持订单行锁）：校验、锁在途意图；同渠道同方式同金额、未过期且已建好收银台的直接复用；另一张**占位意图**（`requires_action` 且 `redirect_url = ''`）建单还不到 `intentCreatingWindow`（30 秒）时回 409「支付正在发起」；其余在途意图作废，插一条新的占位意图。不调渠道。
  - 调用方提交后，`CompleteIntent` 在事务外调渠道 `CreatePayment`（BTCPay、CoinPayments 要联网，最长 10–20 秒），再开短事务按 `WHERE status = requires_action AND redirect_url = ''` 回写收银台信息和 `provider_ref`；0 行 = 占位已被作废（并发再点、订单取消或超时），回 409，渠道那边的发票不用（付了按迟到挂账处理）。渠道失败或返回不合格时作废占位、回 503，再点支付会以 `订单号-序号` 重建。落库用 `context.WithoutCancel`，用户断开也不留半状态。
  - 进程在两段之间退出留下的占位意图，30 秒后再点支付时被作废重建；巡检照常会查它。
  - 产品代码里只有 `CompleteIntent` 调 `CreatePayment`；测试用的一步建单 `CreateIntent` 只在 `helpers_pg_test.go` 里。
- 商户单号（`outTradeNo`）：
  - 订单首次支付使用订单号；后续重新发起支付使用 `订单号-序号`（`CountIntents` + 1，上限 64 字符）。
  - 回调与查单优先按 `out_trade_no` 查找对应的在途支付意图（`IntentRequiresAction`），意图记录对应渠道与方式快照。
- 回调与查单入账主链：
  - 回调 `HandleWebhook` 与查单 `Query` 均统一收拢至 `recordPayment` / `recordPaymentTx`。
  - 发票绑定：意图有 `provider_ref`（BTCPay、CoinPayments 建单时存的发票号）时，这次的渠道单号必须等于它，否则 `ErrProviderRefMismatch` 整笔拒收、记 Error 日志（同店别的发票、孤儿发票、几套系统共用账户撞单号）；易支付不存 provider_ref，不受影响。
  - 防重与幂等：按 `(ProviderID, PaymentRef)` 加咨询锁（`LockPaymentKey`），已入账记录比对金额与币种，一致标记 `AlreadyHandled`，不一致回 409。
  - 结算保存点：调用 `Settler.SettlePayment` 前开启保存点 `payment_settle`。结算报错回滚至保存点；若返回 `NotPayableError` 则转入挂账（`CaseReleasedOrder`, `CaseExcessCapture`, `CaseIneligibleSubscription`），并在同一事务落 `late_payment_cases` 与 `late_payment_suspense` 账目。
  - 订单结清后自动作废该订单名下其他在途意图（`CloseOrderIntents`）。
- 安全与查单约定：
  - 渠道凭据经 AAD 加密封存（`sealer.Seal` / `Open`），数据库不存明文密钥。
  - 外部 HTTP 客户端统一用 `safehttp.Client(allowPrivate)`（包级两个共用客户端，适配器重建时复用连接）：不走环境代理、不跟随跳转、在拨号层执行 `safehttp.CheckDialAddr`，按解析后的真实 IP 拦截内网、环回与元数据地址（防 SSRF 与 DNS 重绑定），响应用 `safehttp.ReadBody` 限 256 KiB；生产环境严禁开启 `AllowPrivateHost`。
  - 主动查单（`Query`）禁止在外部事务内或持有订单锁时调用，避免与回调入账加锁顺序交叉死锁。后台查单必须落审计记录。
