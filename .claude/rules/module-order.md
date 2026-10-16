---
paths:
  - "panel/internal/module/order/**"
  - "panel/internal/module/ledger/**"
  - "panel/internal/module/commission/**"
  - "panel/internal/module/promo/**"
---

# 订单、账本、佣金与优惠券模块

- 锁序（防死锁 40P01）：
  - 结算主链：订单行（`OrderByID` 锁行）→ 订阅（`subs.Lock`，续费/换套餐/流量包/流量重置）→ 预留确认（`captureReservation` 锁套餐库存/限购/券）→ 记账（`postSettlement`，经 `lockAccounts` 稳定顺序锁科目）→ 订单状态（`markPaid`）→ `PaidHook` → 履约（`fulfil`）。
  - 建单主链：新购锁套餐行（`catalog.Offer`）；续费/流量包/流量重置锁订阅；换套餐建单先锁订阅（`subs.Lock`）；再调 `promo.Reserve` / `ledger.HoldForOrder`，最后插订单。取消/超时释放先锁订单行。门户各建单事务与后台开单（`requireUser`）的第一步是 `rejectDeleted`（identity `IsDeleted`，给用户行加共享锁），已注销 409「账号已注销」。后台调账 `AdjustBalance` 对已注销账号只拒加钱，扣减照常（仍要原因、重认证、审计），用于线下退款后冲平留在账号上的余额（注销前的余额、注销时释放的冻结、注销后转入的迟到付款）。
  - 注销账号：identity 在注销事务里锁用户行后先调 commission 的 `LockForDeletion`（拿 `LockUser` 咨询锁），再调 `CancelUserPending`（`service/cancel.go`），按 id 升序锁该用户全部待付单逐张 `releaseOrder`（原因 `ReasonAccountDeleted`，作废在途支付意图），之后到的回调按 `CaseReleasedOrder` 挂账；停完订阅后调 commission 的 `VoidForDeletedUser`（见下面「注销作废」）。咨询锁要在释放冻结之前拿：释放会锁他的余额科目，转余额是拿着咨询锁与可提现佣金再等余额，反过来就互等。
  - 发起支付（`Pay`）：事务里锁订单行、核对可付，调 `payments.BeginIntent` 占住意图后提交；`payments.CompleteIntent` 在事务外调（渠道可能联网，不占库连接和订单锁）。`/orders/:id/pay` 按账号每分钟 10 次（`PayPerMinute`，键 `order_pay`）。
  - 余额全额支付走 `CaptureFromBalance`：用户余额、冻结、收入三科目按 ID 升序一次锁完，冻结直出 captured，不要分步锁。
  - 科目怎么加锁：`ledger` 的 `lockAccounts` 先按 `(Type, UserID, Ref)` 排好去重（`sortedRefs`，只决定建科目 `EnsureAccounts` 的顺序），真正加行锁的是 `repo.LockAccounts` 一条 `ORDER BY id FOR UPDATE`，即按科目 id 升序逐行拿；一笔分录的科目在一条语句里锁完，但语句中途可能拿着 id 小的科目去等 id 大的。
  - 通则（防 40P01）：一个事务里每多记一笔分录，就多一批「拿着前一批科目再去等新科目」的锁。凡是要在同一事务里先锁某人的佣金科目（待结、可提现）、再锁他的余额科目的，一律拆成两个事务：前一个事务提交后，后一个先拿 `LockUser`，再用一笔分录同时锁可提现与余额（手动转余额、自动转余额都这样）；不在解冻、计提这类拿着佣金科目的事务里顺手碰余额。以后加类似路径照这条查：不拿着用户的佣金科目去等余额，也不拿着余额去等平台收入以外的科目。
    四方环（转余额拿着可提现等余额 → 余额付款拿着余额等平台收入 → 别人的订单结算拿着平台收入等待结 → 解冻拿着待结等可提现）要求科目 id 满足「待结 < 可提现 < 余额 < 平台收入」，不可达：`ledger_accounts.id` 是 uuidv7、按创建先后递增，而某人的待结科目只在他第一次拿到计提时建，那时平台收入早已建好（或在同一条 `EnsureAccounts` 里按类型排序先插），所以平台收入的 id 总小于待结。自动转余额的加锁范围与主线早有的手动转余额同形（只多一行条目锁），不是这次引入的；不要为它改结算。
  - 佣金各路径的锁序：计提 `OnOrderPaid`（在订单结算事务里，已持平台收入）→「首单」时 `LockReferee` → 各级受益人的待结佣金；解冻 `settleOne`：条目行（SKIP LOCKED）→ 推荐人的待结、可提现佣金（一笔分录）；自动转余额 `autoTransferOne`：`LockUser` → 条目行（SKIP LOCKED）→ 可提现佣金与余额（一笔分录）；手动转余额、提现申请：`LockUser` 打头；打款 `MarkWithdrawalPaid`：提现行 → 可提现佣金与打款渠道（一笔分录）；注销作废 `VoidForDeletedUser`（`service/deleted.go`）：`LockUser`（注销开头已由 `LockForDeletion` 拿过，重入）→ 在途提现行（按 id 升序）→ 冻结中条目行（一条语句按 id 升序）→ 逐笔分录锁 待结或可提现佣金 + 平台收入 main；拿科目前不持有佣金科目，与以上各路径都不互等。
- 结算与挂账边界：
  - 结算由 order 实现 `paymentsvc.Settler`（`SettlePayment`）：金额币种不符回普通错误整笔回滚；订单不可付时（已取消/超时回 `CaseReleasedOrder`，已付回 `CaseExcessCapture`，订阅已停用、换套餐周期已变、或流量重置单的订阅已不在生效中、套餐版本与订单行不同、没有限额流量配额、或作依据的流量配额行（`resetBasis`）这一期已结束还没滚（`periodEnded`）或 `period_start` 晚于建单时刻（`resetPayable`，见下面「流量重置单」）回 `CaseIneligibleSubscription`）返回 `paymentsvc.NotPayable` 转入挂账。
  - 充值单结算记 `KindBalanceTopup`；其他订单走 `PostOrderPaid` 扣减余额冻结。
  - 用户手续费（surcharge，00042）：payment 交来的 `SettleInput.Amount` 已扣掉 surcharge、等于订单应付，订单核对与佣金基数都不含它；`SettleInput.Surcharge` 由 `postSettlement` 原样传给账本（`OrderPaid.Surcharge`、充值同理），在同一笔交易里单独贷记 `platform_revenue / payment_surcharge`（`ledgersvc.RefSurcharge`），不新增交易类型。订单总额与应付不变。用例在 `settle_surcharge_pg_test.go`（充值单余额只加应付、新购单佣金钩子拿到的金额不含手续费、只付应付被拒）。
- 订单模型与金额约束（钱 R1–R6）：
  - 种类六种（`KindNew`、`KindRenewal`、`KindUpgrade`、`KindAddon`、`KindTopup`、`KindTrafficReset`）；状态五种（`StatusPending`、`StatusPaid`、`StatusFulfilled`、`StatusCancelled`、`StatusExpired`）。`KindUpgrade` 覆盖升级与降级。
  - 库里恒等式 `total = greatest(subtotal − discount − proration_credit, 0) − waived`；`OrderTotal` 只算前半段，`waived` 由调用方另减。`proration_credit` 仅 upgrade 单非零。
  - 会员折扣（xb-order #33，00046）：`discount_amount` = 券 + 会员折扣（人工赠送是全额），恒等式不变；`member_discount_amount` 记其中会员的部分（库约束 ≤ discount_amount、人工单必须为 0）。
    口径照 Xboard：券与会员折扣都按小计算、相加，合计不超过小计（会员部分被压，`promosvc.MemberDiscount`：小计 × 百分比 / 100 向零截断到分，只截一次）。
    只在门户的新购、续费、换套餐、流量包上打；充值、后台人工单、流量重置单不打（重置单永远按原价，见下面「流量重置单」）。`place` 里先 Evaluate 券、再读 `promo.MemberPercent` 算会员折扣；Reserve 后只比对券的部分。
    报价 `Quote()` 一次只读一次会员率，每条报价经 `withMember` 按「券 + 会员」重算 `Total` 与 `Refund`（报价行的 `discount` 只含券，另出 `member_discount`、`member_percent`）；`action=reset` 不调 `withMember`。
    换套餐退回余额 `planChangeRefund(credit, subtotal, discount_amount)` 已含会员折扣，建单、报价、履约同一口径。
  - 券限定计费周期（xb-order #34）：`draft.CouponInterval` 传给 promo 的 `Target.Interval`：新购、续费、换套餐给价格档的 `billing_interval`，流量包为 nil（限定了周期的券对它不适用）；流量重置不用券。
  - 换套餐剩余价值：`proration.go` 中 `剩余价值 = 本周期付费合计 × min(时间比, 流量比)`，向零截断到分，比值交叉相乘，付费天数先用、赠天最后用。
  - 余额抵扣 `PlanBalance`；换套餐零头免除 `WaiveSmallDue`（仅 upgrade 单，上限 99 分，单独记 `waived_amount`，不并入券折扣）。
- 换套餐与购买落点：
  - 同一订阅同时只许一张未完结的续费或变更单（库约束 `orders_one_open_subscription_change` 兜底，建单先拦截回 409 `ErrOpenChange`）。
  - 流量重置单（xb-order #32，`service/reset_order.go`）：只在生效中、有限额流量配额、套餐配了这一份价格快照币种的重置价（catalog `ResetPrice`）的订阅上卖；报价 `action=reset` 不卖时回空数组。
    跨周期判断的依据行 `resetBasis`：限额流量配额里周期最长的一行（没有周期末最长，其余按 `period_end − period_start`），卖（`loadResetOffer`）与收（`resetPayable`）共用；同时有日限额与周期限额时日行滚动不影响（10-14 增量审查定）。履约照旧清全部流量配额行。
    配额滚动任务约 10 分钟一次，新一期起点写边界值而不是滚动时刻，所以设边界为 T：依据行这一期已结束还没滚（`periodEnded`：`period_end <= now`）时不卖，报价与建单都回 409 `ErrResetPeriodSwitching`（报价不回空数组，门户重置入口照原话提示稍后再来）；付款落在这段间隙的进挂账；建单在 T 前、付款在滚动后的，由依据行 `period_start` 晚于建单时刻挡住进挂账。
    订单行是第三种形状：套餐与订阅当时的版本，没有价格档与周期，名字「流量重置 · <套餐名>」。同一订阅同时只许一张未完结的重置单（`orders_one_open_traffic_reset`；建单与报价先查回 `order_pending`，撞索引回 `errOpenReset`）。
    永远按原价收（用户 10-14 定）：不打会员折扣、不收券。`place` 对重置单带券码直接回 rejection `promosvc.ErrCouponNotForReset`（不调 Evaluate），报价带券码落 `coupon_error`、`discount` 为 0；库约束 `orders_traffic_reset_no_discount`（重置单 `discount_amount = 0` 且 `coupon_id` 为空）兜底。可用余额：余额全额抵扣时应付为 0，照样走 `settleZeroPay` 当场履约（这条路不是只为折扣存在，保留）。
    履约调 `subs.ResetTraffic`（原因 `subsvc.ResetOrder`，备注订单号，`At` 传结算时刻，与 `resetPayable` 判生效用同一个 now；写重置日志、通知节点名单）。佣金照现有口径（非充值、总额 > 0 的收款单计佣），基数即原价。
    插件事件与流量包同口径：发 `order.created`、应付为 0（余额全额）的单发 `order.paid`，不发 `subscription.provisioned`。付款后给管理员群组发收款提醒、不给用户发支付成功通知（见 module-notify）。
  - 落点由人选（`planPlacements` / `candidatesFrom` 最多 50 份，生效中优先、新的优先），换掉生效中的永不自动默认。
  - 人工开单（`CreateManualOrder`）：原因必填 5–500 字，审计 `order.manual_created`；结算方式收 `grant`（赠送履约）、`pending`、`offline`（同一事务调 `RecordOffline`），不接受 `balance`。
- 账本与佣金：
  - 账本科目按 id 升序加锁（见上面「科目怎么加锁」）。`Post` 拒收专用 kind（`KindBalanceHold`, `KindBalanceRelease`, `KindOrderPaid`, `KindBalanceAdjusted` 须调专用方法）。
  - 可用佣金唯一口径：`available = Available - InFlight - Paid - TransferTotal - ForfeitTotal`（`ForfeitTotal` 是注销时作废的可提现佣金，`commission_forfeitures`）。提现申请 `RequestWithdrawal` 与转余额都先 `LockUser`。提现申请与审批只改状态，打款 `MarkWithdrawalPaid` 才记账。
  - 多级分销（后台开关 `commission.distribution_enabled`，缺省关）：先按直接邀请人的比例（含单独设置）算这一单的佣金总额，再按 l1/l2/l3 份额逐级截断到分（`LevelAmount`，库约束 `commission_entries_formula` 同式）；关着时只有第一级、份额 100。上溯 `beneficiaries` 防环、最多 3 级，份额 0 的级、不拿佣金的人（`notEarning`：账号已注销，或被单独设置排除即单独比例 0）都跳过但继续往上，跳过的人仍占他那一级。已注销读 identity 的无锁读 `AccountDeleted`（`Referrals` 端口），不用加共享锁的 `IsDeleted`：计提在结算事务里，那时已持有库存、券与平台收入的行锁，再等邀请人的用户行会与注销（锁用户行后释放他的待付单、碰同一批库存与券）互等；竞态只会让注销提交那一刻的一单仍记给他。读设置时三级合计超过 100 按内部错误（`decodeCommissionDistribution`）。每级一条条目（`UNIQUE (order_id, level)`），各自冻结、同源复核（受益人对购买人）、作废。
  - 单独佣金设置 `commission_user_overrides`（属 commission）：NULL 跟全局；比例 0 = 这个人不拿佣金，不论他在第几级（用户 10-14 定，`excludedByOverride`）。整单佣金总额按直接邀请人的单独设置算（`orderTerms` → `termsFor`）；直接邀请人不拿佣金（已注销或被排除）时他的单独设置整条不作数、按全局算，再跳过他只给上级分。只影响以后的订单。后台按邮箱找人走 identity 的精确查找 `FindByEmail`（`Referrals` 端口），不用包含匹配。写操作审计 `commission.override_set` / `commission.override_removed`。
  - 提现开关 `commission.withdraw_enabled`（缺省开）与方式白名单 `commission.withdraw_methods`（缺省 alipay，目录见 `settingssvc.WithdrawMethodCatalog`）。关着时（用户 10-14 定，10-16 只剩人民币后即全关）：
    - `RequestWithdrawal` 直接回 `ErrWithdrawDisabled`；白名单照旧管着，开关两种状态都至少要留一种方式（settings 校验回 422 `withdraw_methods`）。读设置时三级合计只在开着多级时查。汇总带 `withdraw_enabled`，门户据此决定显不显示提现。
    - `settleOne` 解冻时只挂 `commission_entries.auto_transfer_due`（不拿用户锁、不碰余额）；`SettleDue` 解冻完再由 `autoTransferDue` 逐条在单独的事务里转（`autoTransferOne`，锁序见上），转失败标记留着、下一轮重试；提现已重新打开时只清标记不转（用户自己提现或转余额）。转的金额取这条佣金与当前可用佣金里小的那个，可用为 0（用户自己转走了）只清标记；`commission_transfers.entry_id` 唯一兜底，审计 `commission.auto_transferred_to_balance`。
    - 关闭前已经解冻的佣金不挂标记、不自动转，由用户自己点「全部转入余额」（门户提示写明）。
  - 注销作废（用户 10-14 定：账号注销时名下未打款的佣金作废、申请中的提现驳回；有已批准未打款的提现时不许注销），identity 的 `DeleteUser` 经 `CommissionVoider` 端口在同一事务里调 `VoidForDeletedUser`（app 的 `commissionVoidAdapter` 适配）：
    - 有已批准待打款（`approved`）的提现就整笔拒绝注销（`ErrApprovedWithdrawalPending`，用户 10-14 定：管理员可能已线下打款）；申请中（`requested`）的驳回，`reject_reason = account_deleted`，复核人记执行注销的管理员；提现只改状态不记账，驳回后钱回到可提现。
    - 他作为受益人的 `pending` 条目（待复核与否）逐条作废：`commission_voided` 冲回计提（借 待结 / 贷 平台收入 main，来源是条目、按来源唯一），`status = voided`、`void_reason = account_deleted`，复核人记执行注销的管理员。
    - 清掉他条目上的 `auto_transfer_due`。
    - 按唯一口径算剩下的可用佣金（已含驳回退回的），为正就记 `commission_voided`（借 可提现 / 贷 平台收入 main，来源 `commission_forfeiture`），写 `commission_forfeitures`（只追加，每人最多一行，`commission_forfeitures_user_key`）。可提现是池、不按条目扣，说不清哪条被提走了多少，所以这部分不改条目状态（已解冻的条目仍是 `available`），口径减去作废合计。
    - 已知窗口（10-14 审查，概率很低，暂不处理）：冻结期为 0 天时，解冻任务若恰在注销提交前几毫秒把并发计提的新条目解冻，这笔会进已注销账号的可提现。之后出钱入口都 409、钱出不去，而 `UNIQUE (user_id)` 挡住补作废；真遇到要人工处理，改法是放宽这条唯一约束、允许补一行。
    - 已打款的提现、已转余额的钱不动；做完后他的待结、可提现佣金科目都为 0；再调一次全 0。科目余额不够冲回时回 409 让管理员先核对账本，不跳过。
    - 之后不再出钱：转余额、提现申请、自动转余额（`autoTransferOne`）、后台批准提现、标记已打款、通过待复核条目都回 409 `ErrAccountDeleted`「账号已注销」（无锁的 `AccountDeleted`，各自已锁住用户咨询锁、提现行或条目行再读，注销的作废一定排在它们提交之后）；后台驳回提现、作废条目照常。
    - 注销提交那一刻才由并发结算计提、没赶上作废的条目：`settleOne` 解冻时读到受益人已注销，不解冻、转人工复核（原因「受益人账号已注销，请作废这条佣金」），由后台作废（作废要记复核人，解冻任务里没有）。
  - 收款账号按方式校验（`ValidatePayoutAccount`：TRC20 base58check、ERC20/BEP20 EIP-55），仍用 Sealer 密封；USDT 打款的流水号是交易哈希（`ValidatePaidUSDT`），实付 USDT 数量只记录，面板不发起任何链上转账。旧申请的方式是 `other`，只显示、不可选。
- 优惠券与会员折扣（promo）：
  - 券限定计费周期 `coupon_intervals`（00046，取值 catalog 的 billing_interval 枚举，空 = 不限）：只在建券时随券插入（单张与批量的数据修改 CTE 同一条语句）；`apply` 在 `checkPlan` 之后 `checkInterval`，`Target.Interval` 为 nil（流量包、流量重置）或不在里面回 `ErrCouponNotForPeriod`（rejection，报价里落到 `coupon_error`）。
  - 流量重置单不收券（用户 10-14 定）：rejection `ErrCouponNotForReset`「流量重置不能使用优惠码」由 order 对重置单直接回，promo 的 `apply` 不认订单种类。
  - 券试算 `Preview` 经 `CatalogReader.PriceSubtotal` 一次拿币种、金额与计费周期（一个价格档只查一次 catalog）。
  - 会员折扣 `member_discounts`（00046，减免百分比 1–100，与 Xboard `v2_user.discount` 同语义，一人一行，没有行 = 没有）：后台 `GET/PUT/DELETE /v1/member-discounts/:user_id`（PUT、DELETE 近期重认证；审计 `member_discount.set` / `member_discount.cleared`，资源 user；写前在同一事务里 `LockMemberDiscountByUser` 锁住旧行读 before）；order 经 `MemberPercent` 读（不加锁：报价与建单各读一次，变了由 expect 比对拦下）。金额函数 `MemberDiscount`（`service/member_calc.go`）。
