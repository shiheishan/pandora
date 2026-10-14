---
paths:
  - "panel/internal/module/order/**"
  - "panel/internal/module/ledger/**"
  - "panel/internal/module/commission/**"
  - "panel/internal/module/promo/**"
---

# 订单、账本、佣金与优惠券模块

- 锁序（防死锁 40P01）：
  - 结算主链：订单行（`OrderByID` 锁行）→ 订阅（`subs.Lock`，续费/换套餐/流量包）→ 预留确认（`captureReservation` 锁套餐库存/限购/券）→ 记账（`postSettlement`，经 `lockAccounts` 稳定顺序锁科目）→ 订单状态（`markPaid`）→ `PaidHook` → 履约（`fulfil`）。
  - 建单主链：新购锁套餐行（`catalog.Offer`）；续费/流量包锁订阅；换套餐建单先锁订阅（`subs.Lock`）；再调 `promo.Reserve` / `ledger.HoldForOrder`，最后插订单。取消/超时释放先锁订单行。
  - 余额全额支付走 `CaptureFromBalance`：用户余额、冻结、收入三科目按 ID 升序一次锁完，冻结直出 captured，不要分步锁。
- 结算与挂账边界：
  - 结算由 order 实现 `paymentsvc.Settler`（`SettlePayment`）：金额币种不符回普通错误整笔回滚；订单不可付时（已取消/超时回 `CaseReleasedOrder`，已付回 `CaseExcessCapture`，订阅已停用或换套餐周期已变回 `CaseIneligibleSubscription`）返回 `paymentsvc.NotPayable` 转入挂账。
  - 充值单结算记 `KindBalanceTopup`；其他订单走 `PostOrderPaid` 扣减余额冻结。
  - 用户手续费（surcharge，00042）：payment 交来的 `SettleInput.Amount` 已扣掉 surcharge、等于订单应付，订单核对与佣金基数都不含它；`SettleInput.Surcharge` 由 `postSettlement` 原样传给账本（`OrderPaid.Surcharge`、充值同理），在同一笔交易里单独贷记 `platform_revenue / payment_surcharge`（`ledgersvc.RefSurcharge`），不新增交易类型。订单总额与应付不变。
- 订单模型与金额约束（钱 R1–R6）：
  - 种类五种（`KindNew`、`KindRenewal`、`KindUpgrade`、`KindAddon`、`KindTopup`）；状态五种（`StatusPending`、`StatusPaid`、`StatusFulfilled`、`StatusCancelled`、`StatusExpired`）。`KindUpgrade` 覆盖升级与降级。
  - 库里恒等式 `total = greatest(subtotal − discount − proration_credit, 0) − waived`；`OrderTotal` 只算前半段，`waived` 由调用方另减。`proration_credit` 仅 upgrade 单非零。
  - 换套餐剩余价值：`proration.go` 中 `剩余价值 = 本周期付费合计 × min(时间比, 流量比)`，向零截断到分，比值交叉相乘，付费天数先用、赠天最后用。
  - 余额抵扣 `PlanBalance`；换套餐零头免除 `WaiveSmallDue`（仅 upgrade 单，上限 99 分，单独记 `waived_amount`，不并入券折扣）。
- 换套餐与购买落点：
  - 同一订阅同时只许一张未完结的续费或变更单（库约束 `orders_one_open_subscription_change` 兜底，建单先拦截回 409 `ErrOpenChange`）。
  - 落点由人选（`planPlacements` / `candidatesFrom` 最多 50 份，生效中优先、新的优先），换掉生效中的永不自动默认。
  - 人工开单（`CreateManualOrder`）：原因必填 5–500 字，审计 `order.manual_created`；结算方式收 `grant`（赠送履约）、`pending`、`offline`（同一事务调 `RecordOffline`），不接受 `balance`。
- 账本与佣金：
  - 账本科目按 `(Type, UserID, Ref)` 稳定顺序加锁（`sortedRefs`）。`Post` 拒收专用 kind（`KindBalanceHold`, `KindBalanceRelease`, `KindOrderPaid`, `KindBalanceAdjusted` 须调专用方法）。
  - 可用佣金唯一口径：`available = Available - InFlight - Paid - TransferTotal`。提现申请 `RequestWithdrawal` 与转余额都先 `LockUser`。提现申请与审批只改状态，打款 `MarkWithdrawalPaid` 才记账。
