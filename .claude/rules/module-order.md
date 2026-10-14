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
  - 多级分销（后台开关 `commission.distribution_enabled`，缺省关）：先按直接邀请人的比例（含单独设置）算这一单的佣金总额，再按 l1/l2/l3 份额逐级截断到分（`LevelAmount`，库约束 `commission_entries_formula` 同式）；关着时只有第一级、份额 100。上溯 `beneficiaries` 防环、最多 3 级，份额 0 的级跳过但继续往上。每级一条条目（`UNIQUE (order_id, level)`），各自冻结、同源复核（受益人对购买人）、作废。
  - 单独佣金设置 `commission_user_overrides`（属 commission）：`termsFor` 只按直接邀请人取比例与范围，NULL 跟全局，比例 0 = 不拿佣金；只影响以后的订单。写操作审计 `commission.override_set` / `commission.override_removed`。
  - 提现开关 `commission.withdraw_enabled`（缺省开）与方式白名单 `commission.withdraw_methods`（缺省 alipay，目录见 `settingssvc.WithdrawMethodCatalog`）。关着时 `RequestWithdrawal` 回 `ErrWithdrawDisabled`；`settleOne` 解冻站点币种条目后同一事务里 `autoTransferEntry` 转余额（锁序 条目行 → `LockUser` → 科目，`commission_transfers.entry_id` 唯一兜底，审计 `commission.auto_transferred_to_balance`），非站点币种留在可提现。
  - 收款账号按方式校验（`ValidatePayoutAccount`：TRC20 base58check、ERC20/BEP20 EIP-55），仍用 Sealer 密封；USDT 打款的流水号是交易哈希（`ValidatePaidUSDT`），实付 USDT 数量只记录，面板不发起任何链上转账。旧申请的方式是 `other`，只显示、不可选。
