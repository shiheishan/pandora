---
paths:
  - "panel/internal/domain/payment/**"
  - "panel/internal/domain/billing/payments.go"
  - "panel/internal/domain/billing/payment_query*.go"
  - "panel/internal/domain/billing/payment_methods.go"
  - "panel/internal/domain/billing/provider_admin.go"
---

# 支付渠道与易支付

- 易支付只出支付宝和微信（用户 2026-10-07 定）：`EpayMethods = alipay, wxpay`。后台只能勾这两种；存量配置里的其它方式（如 qqpay）在 `providerMethods` 与 `PaymentMethods` 的 SQL 里同口径过滤，结账页不出、下单拒绝；epay 渠道过滤后一种方式都不剩时下单回 422，不让渠道默认值兜底
- 对外单号（out_trade_no）：一张订单的第一次支付就是订单号；之后每次再发起（换方式、换渠道、旧意图作废）是「订单号-序号」，序号 = 该订单已有意图数 + 1（`nextOutTradeNo`，调用方已锁订单行）。很多易支付站点拒绝重复单号，而且作废的那笔仍可能被付，两笔必须分得开
  - 对外单号写进意图的 `provider_ref`（渠道建单时没回自己的标识就用它，(tenant, provider, provider_ref) 唯一）与 `action_payload.out_trade_no`
  - 回调先按 `provider_ref` 定位意图所属订单（`orderIDByProviderRef`），找不到（改动之前建的意图）再按订单号
  - 主动查单按订单名下每一笔支付的 out_trade_no 逐个问渠道（老意图没有记的按订单号），查到已付按订单 id 走 `settlePaymentTx`
- 同一订单只入账一次：先到的那笔（不论新旧单号）结清订单，另一笔迟到时订单已付，走现有的意外付款挂账（excess_capture），不另开路径
- 后台查单（`AdminQueryOrderPayment`）的审计：走了补记就在补记的结算事务里写（`settleHook`）；没有业务写入时单独一个事务写，写不进去回内部错误
- 渠道适配层（payment/）不依赖 httpx 和数据库：渠道记录经 `payment.Loader` 注入；停用回 `payment.ErrProviderDisabled`、不支持回 `payment.ErrNotSupported`，由 billing 翻成中文 httpx 错误
