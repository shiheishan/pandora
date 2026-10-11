---
paths:
  - "panel/internal/domain/billing/**"
---

# 计费：账本、锁序与结算主链

## 锁序（死锁防线）
- 结算锁序：订单行 →（只有 renewal / upgrade 才有的）订阅行 → 支付意图 `ORDER BY id FOR UPDATE` → 预留图（父节点 → 子资源形状 → 套餐 / 计数器 → 券与核销 → 余额冻结）→ 账本科目按 UUID 排序逐行锁。释放（取消 / 过期）第一把锁也是订单。新增动钱路径照这个顺序走。守卫 `checkout_atomic_contract_test.go:TestSettlementReservationAndLockOrderSourceContract`、`release_contract_test.go:TestReleaseTransactionSourceContract`
- 续费与变更在建单侧也是先锁订阅、再锁套餐 / 价格 / 券、最后锁账本科目（`quotePlanChange`、`lockOrderSubscriptionForSettlement`），与结算同序
- 一个事务要动多个账本科目时，用 `prepareAndLockLedgerAccounts` 一次收齐（按业务键插入缺行，再按 UUID 排序加锁），然后再记账。不要逐个 `EnsureAccount`：它的 ON CONFLICT DO UPDATE 取锁顺序不定，会和结算交叉死锁。零元单要记的收入科目也并进同一个锁集合（`prepareBalanceHold`）

## 约束与迁移联动
- `payment_intents` 的 BEFORE UPDATE 守卫 `app.guard_payment_intent` 只放行白名单列：status、provider_ref、action_payload、failure_code、failure_message、expires_at、updated_at、query_attempts、next_query_at；status 只能按状态机前进，provider_ref 只能写一次。给支付意图加要 UPDATE 的列，必须在同一迁移里 CREATE OR REPLACE 该函数补白名单（Down 恢复上一版原文），并 `GRANT UPDATE (列) TO aegis_app`。现行定义在 00097
- 支付渠道只经 `provider_admin.go` 的 `CreateProvider` / `UpdateProvider` 写（后台与 aegis-payctl 共用）：先落行、再按 id 用 `payment_provider:<id>` 作 AAD 加密（与 `loadProvider` 同一个）；保存前用 `Factory.Build` 试构造；adapter 与 code 建后不可改，offline 只读；审计只记 `credentials_changed`。`CreatePaymentIntent` 先用 `resolvePaymentMethod` 校验方式在渠道 methods 内，复用在途意图要同渠道且 `action_payload.method` 相同。守卫 `TestPaymentProviderAdminPG18`
- 金额恒等式 total = max(小计 − 折扣 − 剩余价值折算, 0) + 税，Go 侧只有 `orderTotal` 一处，和 00071 的 `orders_total_identity` 一致；折算额只有 upgrade 单可以非零
- 科目类型 `AccountType` 与迁移里的 CHECK 一一对应，借贷配平最终由延迟约束触发器强制

## 订单 kind
- 一共五种：new 开订阅、renewal 延周期、upgrade 原地换套餐（升级和降级都是 upgrade，方向只在响应里体现）、addon 发流量包余额、topup 入余额
- 加 kind 要同时改 `lockOrderReservationGraph` 的 kind switch、`settlePaymentTx` 的履约 switch（每个 kind 都必须推进到 fulfilled），以及迁移里的履约证据约束。守卫 `TestEveryPaidOrderKindReachesFulfilled`、`TestReservationCaptureSourceContract`

## 结算主链只有一条
- 渠道回调、主动查单补记、后台 mark-paid、人工单「线下已收款」都走 `HandlePaymentWebhook` / `settlePaymentTx`（在调用方事务里执行），不要另写记账路径。线下收款由 `offlinePaymentInput` 合成，凭证号即 provider_payment_id
- 去重靠 (provider_id, provider_payment_id) 认出已经记过的那笔，不靠事件号：主动查单的事件号是 `PaymentRef + ":RECONCILED"`，与回调的事件号不同
- `subscriptionAcceptsPaidChange(status, renewalClosed)`（active / trialing / grace / past_due，外加原地续费窗口没关的 expired）是可续费、可变更的唯一判定，建单时用，结算锁住订阅后复核也用，与 00124 重建的挂账守卫同一集合（`TestSubscriptionPaidChangeStatusesMatchLatePaymentGuard`）。窗口只看 `subscriptions.renewal_closed_at` 这一列，不各处按 now() 现算复核不通过时，钱经 `quarantineUnexpectedPayment` 进挂账，订单与订阅都不动；不要让履约去撞订阅状态机，否则整笔回滚，连收款证据都留不下
- 挂账 case_kind 只有三种：released_order / excess_capture / ineligible_subscription。释放时检查收款证据要排除 ineligible_subscription 那笔，否则余额冻结永远退不回来（`lockAndRejectSettledPaymentEvidence`）
- 开订阅、建配额只有 `provisionSubscription` / `initQuotaBalances` 一份实现，订单履约与礼品卡套餐兑换（`grantPlanDirect`）共用；礼品卡的余额、流量、延期发放也在本包（giftgrant.go），giftcard 只经 `Granter` 接口调用
- 履约改变了可服务用户时，在提交后经 `notifyUsersChanged` 通知节点；「建单即履约」的零元单路径要调 `notifyIfFulfilled`。`PaymentService` 必须用装配时注入的同一个 `Service`（`NewPaymentService` 的 settle 参数），临时 `NewService` 没挂通知

## 产品口径（与设计稿不同处）
- 同一订阅同时只许一张未完结的续费或变更单（00071 唯一索引兜底，`ensureNoOpenSubscriptionOrder` 先回可读的 409），否则冻结在订单里的剩余价值会失效
- 变更套餐时配额行不能删（人工调整只许追加），新套餐没有的指标把上限置空，等于不限量；降级差额（含券折扣）退进余额
- 剩余价值 = 本周期付费合计 × min(时间比, 流量比)，向下取整，付费天数先用、赠送天数最后用；全程整数运算（`prorationCredit`）
- 流量重置只清套餐已用量，不碰流量包余额（traffic_pack_grants）；后台手动重置按订阅行（`ManualResetInput.SubscriptionID`，`POST v1/subscriptions/{id}/traffic-reset`）
- 人工单结算方式只收 grant / pending / offline，balance（从余额扣）未定，不接受

## 到期与续费（2026-10-07 用户定，w5expiry）
- 过期扫描 `ScanExpiredSubscriptions`（expire.go，aegis-admin 一分钟一轮）：到点改 status=expired、写 expired 事件与 `subscription.expired` 钩子，只改状态、不动凭据；过期满 30 天写 `renewal_closed_at` 并吊销凭据（旧链接作废），之后只能新购换链接。状态机 expired → active 由 00124 放开，「已取消」不放开
- 续费履约只有 `renewSubscriptionTx` 一份（renewal_fulfill.go，订单与套餐卡共用）：基准是付款时刻（订单 paid_at）。付款时没到期 = 提前续费：接在原到期日之后，本期配额一行都不动，到点由 `rollCycleQuotaSQL` 把 cycle 行滚进新周期（剩不足两个周期就并到订阅周期末）；订单在到期前创建、回调晚到的也按提前续费。付款时已到期 = 过期恢复：从付款时刻起算，total / cycle / day / month 全部对齐清零（`restartQuotaPeriodsTx`），事件 from_status 记 expired
- 加时长口径（礼品卡加时长与后台加时长同一个 `extendSubscriptionTx`）：生效中没到期只给时间；救回（已过期 30 天内、走过到期日、试用中）状态回 active，cycle 流量按「延长天数 ÷ 套餐周期天数」折算加到本周期上限（`rescueQuotaTx`，limit_value 只属于本周期，下次过期恢复或滚动写回），已用量沿用；走过到期日的 day / month 从今天重新对齐。只有付费续费给满额。永不过期的订阅不能延长
- 同套餐只续不新开（same_plan.go）：门户新购带 `RejectSamePlan` 遇到可原地续费的同套餐订阅回 409；人工开单改开续费单（`CreateRenewal` 的 Manual* 字段，幂等域仍是 order_create，00125 放开这一组合，审计在建单事务里）；套餐卡改走 `grantPlanRenewal`
- 变更套餐的折算：重开周期的事件还包括 `extended` 且 payload.restart；提前续费待开始的周期按 cycle 行长度把额度算进流量比例（`pendingCycleAllowance`）
- `MarkOrderPaid` 的审计与结算在同一事务

## 购买模型统一（2026-10-07 用户定，w7buya；设计稿 purchase-model-design.md）
- 规则只写一处：纯函数包 `internal/domain/purchase`（只依赖标准库）放落点选项与默认值（`Options`、`Choice.Match` / `Resolve`）、备注名规范化（`NormalizeLabel`）、余额与支付最低额（`ApplyBalance`、`WaiveSmallDue`）。billing、giftcard（经 Granter）与后台开单都调它，门户的金额与默认值由服务端算好下发
- 落点由人选：候选一条 SQL（`placement_candidates.go` 的 `loadCandidatesTx`：只取生效中或过期 30 天内的，生效中优先、新的优先，最多 50 份；状态三档只经 `subscriptionAcceptsPaidChange`），选项与展示数据 `placementsTx`，落地前 `resolvePlacementTx` 重新 Match、`lockPlacementSubscription` 锁住那一份复核。不同款不预选，换掉生效中的那份永不自动默认；不要再写「系统自动挑一条」的代码。`renewableSamePlanSubscription` 只留给门户新购没带 new_copy 时拦截
- 统一报价 `Service.Quote`（checkout_quote.go）与四个建单入口用同一套读取：新购 `checkout_catalog.go`、续费 `renewal_price.go`、换套餐 `plan_change_load.go`（source / targets / snapshot），报价传 lock=false 不加锁。建单带 `Expectation`（as_of 只收 [now-10min, now]）时与重算不符回 409 `quote_changed`；换套餐的时间比例按 as_of 算，流量按当前用量
- 余额一律经 `balancePlan`（checkout_amounts.go）：`ApplyBalance` 先算（低于最低额时余额够就全用 Forced，不够就用尽余额后标 Short，都与开关无关）。只有门户换套餐抵扣后的零头可以免（用户 8.1 第 1 题推荐 A，`WaiveSmallDue`，固定上限 99 分、与最低额无关），并进订单折扣（有券就并进券的折扣，00036 要求券核销折扣等于订单折扣），审计 digest 记 `small_due_waived`；`reservationLockRequest.SmallDueWaived` 只认 upgrade 单。新购、续费、流量包免不了又付不了回 422（报价里 `below_minimum` 提前标出）；后台待支付单不做 Forced、不免，低于最低额 422 让管理员改用赠送或线下收款
- 后台开单 preview（`ManualOrderOptions`）给 `min_payment` 与每个落点的 `due` / `below_minimum`（`ManualPlacement` 内嵌 purchase.Placement）：判定只有 `manualDueBelowMinimum` 一处，建单 `balancePlan` 的 Manual 分支与 preview 共用；待支付单低于最低额的 422 带 `fields.settlement`
- 门户订单详情的 `subscription_id`（`MyOrderDetail`）：履约过（status = fulfilled 或 fulfilled_at 非空）才给，取 `orders.subscription_id` 连到的那一份（新购履约时写入，续费、换套餐、流量包建单时就有），没履约为 null，不另加列
- 支付最低额：渠道 config `min_amount`（分，1–100000，易支付默认 100；编辑时不传保留原值），「能不能在线付」取启用且接单的 CNY 渠道里最小的那个，按租户在进程内缓存一分钟（本进程的渠道写入后 `invalidateMinPayment`；别的进程、adminops 的开关最多晚一分钟生效）；`CreatePaymentIntent` 再按所选渠道兜底 409，并拒绝已过付款期限的单（409 `order_lapsed`，`ErrOrderPaymentExpired`）
- 新购：`NewCopy` 是「另买一份」，`Label` 经 `NormalizeLabel` 存 `orders.subscription_label`，另买同款而已有那份没起名时 422；门户新购（RejectSamePlan）同一套餐同时只能有一张未付款新购单（409 `order_pending`，Fields 带 order_id，已过付款期限的另带 `lapsed: "true"`；已过付款期限、还没被释放任务关掉的那张也算，防止它的晚到回调与同款新单双开；人工单不受限）。履约 `provisionSubscription` 写备注名，撞名加「 2」「 3」后缀（保存点重试，不让结算失败），并在这是唯一一份生效中订阅时把未分配的流量包挂上（转移流水 actor system）
- 流量包挂订阅（00137）：addon 单必须带一份生效中的订阅、余额挂上去；送流量没有在用的那份时未分配；`transferTrafficPacksTx` 只从未分配或彻底停用的那份转到生效中或可救回的那份（例外：升级前的旧包——只有 00138 回填的 migration 流水、没有 user / admin 流水的——可以从生效中的那份挪一次，用户 2026-10-07 定，口径 `legacyMovableSQL` 与 00137 守卫同一条），每笔写 `traffic_pack_transfers`（追加写），提交时约束触发器核对同事务有流水。门户转移、后台加流量（挂这一行）、履约自动挂都走它或 `GrantTrafficPackTx`
- 守卫：PG18 sub_period 域的 `placement`、`purchase quote` 子测试，traffic_pack 域的 `traffic packs belong to a subscription` 子测试

## 换套餐的三个入口（2026-10-07 用户定，w6plan）
- 门户改套餐、后台人工开单遇到不同套餐、套餐卡遇到不同套餐，都在原订阅上换套餐、链接不变，共用一份折算（`plan_change_quote.go`）与一份履约（`applyPlanChangeTx`，plan_change_apply.go）。不要另写一套
- 选哪条原订阅：由人选（购买模型统一）——后台开单带 `Target`（`POST v1/orders/manual/preview` 给选项与默认值，多个选项没带回 422），套餐卡带 `choice`；已取消、窗口关闭的不出现在选项里
- 人工开单：`CreateManualOrder` 按 Target 分派 → `CreatePlanChange` 带 Manual* 字段（后台开单跳过可见性与用户组，保留 allow_upgrade、版本已发布、订阅状态）（与人工续费同一套）：幂等声明属于管理员、域是 order_create（00134 放开 upgrade + created_by 这一组合）；赠送 `waiveNewPrice` 把新价全额减免，剩余价值全额退余额（`assertManualGrantRenewal` 认 renewal / upgrade 两种）；线下已收款在建单事务里 `settleManualOfflineTx`；审计 order.manual_created（digest 带 plan_change）与订单同事务
- 套餐卡：`GiftGranter.GrantPlan`（带卡密）→ `grantPlanChange`（plan_change_grant.go），无订单，卡算 0 元，剩余价值全额退余额；退款分录 `source_type='gift_card_code'`、plan_changed 事件 order_id 为空且 payload 带 source=gift_card / gift_card_code_id / refund_currency，00134 的 `app.assert_gift_plan_change` 在提交时核对兑换流水、事件归属与金额。订阅上挂着未完结的续费或变更单时拒绝兑换（409）。giftcard 经接口拿到的是基本类型（订阅、mode、退款与币种），不 import billing
- 守卫：PG18 sub_period 域的 `plan change entries` 子测试

## 佣金
- 可用佣金 = `user_commission_available` 科目余额 − requested / reviewing / approved 状态的在途提现，唯一口径在 commission_available.go。提现申请与转余额都先 `lockUserCommissionAccounts`，再按这个口径校验。守卫 `TestRequestWithdrawalUsesLedgerAvailabilityUnderSharedLock`、`TestCommissionTransferUsesSameLedgerAvailability`
- 提现的申请与审批只改状态，只有打款（`PostWithdrawalPayout`）才记账

## 测试
- `settlement_pg18_test.go` 被 `panel/deploy/test-settlement-runner_static_test.sh` 按文件名 grep marker 字面量：不能改名，marker 也不能挪到别的文件
- order_release、plan_change、payment_query 三个 PG18 域各占一个库，不能并进 billing 域：order_release 开跑先断言 00040 的全局水位还是干净的，而别的域会把它置上（见 run-pg18-gates.sh 的 `DOMAINS` 注释）
- 配额周期滚动 `RollQuotaPeriods` 的第二遍按需（w12period，总协调 2026-10-09 定）：第一遍之后用不加锁的探测 `rollQuotaDueRemain`（与两条滚动语句共用 `rollQuotaDueWhere` / `rollCycleDueWhere`，到期条件只有一份）看还有没有到期的行；没有（没有到期的行，或全部滚完）就不做第二遍，有（被 push 锁着而跳过）就隔 `rollQuotaRetryDelay` 补一遍，不等下一轮——被跳过的用户在重置前一直是用尽状态，晚 10 分钟是用户可见的。PG18 `checkQuotaRollPG18` 第 6 项钉住：仅有的到期行被锁住、间隔内放开 → 第二遍滚掉；两遍都锁着 → 留给下一轮；无到期行 → 不付重试间隔
