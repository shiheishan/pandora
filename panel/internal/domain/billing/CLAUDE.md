# panel/internal/domain/billing/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

订单、支付与复式账本。钱的不变量（金额恒等式、预留图、借贷配平、订单/幂等对称绑定、各 kind 的履约证据）落在迁移的约束与触发器里，这里编排事务与锁序：订单 →（续费 / 变更单才有的）订阅 → 支付意图 → 预留图子资源 → 按 UUID 排序的账本科目。订单 kind 决定建单与履约路径：new 开订阅、renewal 延周期、upgrade 原地换套餐（D-E-2）、addon 发流量包余额（D-E-1）、topup 入余额。

成员清单
service.go: Service 骨架（从 checkout.go 拆出）：NewService、SetUsersChangedNotifier 注入，履约后经 onUsersChanged 在提交后发租户级节点通知，零元单（赠送、全额抵扣、零元续费与变更）由 notifyIfFulfilled 补发；包内共用小工具
checkout.go: 新购下单 CreateOrder（目录校验、库存与限购预留、余额冻结、券核销、幂等绑定与预制响应）与零元单当场捕获履约 captureZeroPayOrder
settlement.go: 支付回调结算主链，整条在一个文件：HandlePaymentWebhook → settlePaymentTx → postOrderPaid → fulfillOrder，回调按 kind 分派履约；续费 / 变更单锁住订阅后按建单同一口径复核订阅状态，不收（支付窗口里被改成终态）就把钱隔离进挂账、订单与订阅不动（R117）；settlePaymentTx 在调用方事务里执行，回调 / 标记已支付各开一个事务调它，人工单线下已收款在建单事务里调它
settlement_legacy.go: 只有注释：预留图之前的旧版支付回调实现原样存档（从 checkout.go 挪来），不参与编译
provision.go: 开订阅 provisionSubscription 与建配额 initQuotaBalances 的唯一实现，下单履约与礼品卡套餐兑换（grantPlanDirect）共用
order_holds.go: 各建单路径共用的预留父节点与余额冻结（insertHeldReservation / prepareBalanceHold / postBalanceHold）
reservations.go: 结算与释放共用的预留图加锁校验；orderTotal 是金额恒等式 total = max(小计 − 折扣 − 折算, 0) + 税（00071）
release.go: 取消 / 过期释放，held 预留图整体转 released 并退回余额冻结；订单上有收款就拒绝释放，订阅不收而进挂账的收款不算（否则冻结的余额永远退不回来）；续费走专用分支，其余 kind 共用预留图锁；冲突原因用 releaseConflict 带中文文案（errors.Is 仍认作冲突哨兵，过期任务照旧空转），releaseHTTPError 是后台与门户取消共用的错误翻译（R95）
release_locks.go: 释放的各加锁步骤（从 release.go 拆出）：订单（过期扫描 SKIP LOCKED）、活跃支付意图、已结算收款证据（有即拒绝）、预留图（续费单专用分支再锁券与余额冻结）
reservation_expiry.go: 到期预留的批量释放扫描
renewal.go: 续费 CreateRenewal 与周期滚动 RollQuotaPeriods；subscriptionAcceptsPaidChange 是可续费 / 可变更订阅状态的唯一口径（建单与结算复核共用，与 00095 守卫一致）；旧周期已走完（状态仍 active 也算）时新周期从付款时刻起算；与变更套餐共用零元单捕获 captureZeroPaySubscriptionOrder 和结算锁 lockOrderSubscriptionForSettlement
plan_change.go: 变更套餐（D-E-2，kind=upgrade）的试算、下单与原地履约：换套餐不换凭据、配额按新套餐重置（新套餐没有的指标变不限量，配额行不删因人工调整只许追加）、降级差额冲回收入退进余额（plan_change_refund 分录）；同一订阅同时只许一张在途续费或变更单
plan_change_quote.go: 变更套餐的剩余价值折算，只算不写：本周期付费合计 × min(时间比, 流量比) 向下取整，付费天数先用、赠送天数最后用
traffic_pack.go: 流量包（D-E-1）目录、下单（kind=addon）、履约成挂用户的 traffic_pack_grants 余额与余额查询
traffic_reset.go: 流量重置日志与后台手动重置；只清套餐已用量，不碰流量包
topup.go: 自助充值单（kind=topup，结算即履约）与管理员调账
payments.go: 发起支付取收银台、渠道回调翻译成平台事件，确认到账后交回 HandlePaymentWebhook
unexpected_payment.go: 已释放或已付清订单又来的钱、续费 / 变更单结算时订阅已不收的钱，按 case_kind（released_order / excess_capture / ineligible_subscription）隔离进挂账（late_payment_suspense）
late_payment.go: 挂账的查看与转入余额，供后台消费
manual_order.go: 管理员人工单与 mark-paid，复用下单与回调主链路；mark-paid 的钱进了挂账时入账与审计照写、回 409 说明去向，同一凭证重复标记回 409；人工单结算方式 grant（赠送当场履约，缺省）/ pending（建待支付单交给用户付，仍记开单人）/ offline（带凭证号，建单事务里按 offline 渠道结清，收入与佣金同 mark-paid，offlinePaymentInput 是两条路共用的回调形状），balance 暂不接受（D-C-3）
my_orders.go: 门户订单读模型：myOrderSelectSQL 是列表与详情共用的行形状（首项周期与商品名快照），列表带筛选段计数 counts，详情带优惠码、订阅到期与支付渠道名；ParseOrderStatuses 是门户与后台订单列表共用的状态白名单（逗号多值、精确匹配、未知回 400）
coupon.go: 优惠券校验 applyCoupon、核销 redeemCoupon 与试算（套餐 PreviewForPrice、流量包 PreviewForTrafficPack 共用 previewCoupon 外壳，响应带券面）
commission.go: 分销佣金计提、解冻、提现申请与打款记账；计佣范围 commission.scope（first_order 只给被推荐人第一笔计佣订单返佣，缺省 every_order），ValidCommissionScope 供后台校验；CommissionDefault* 是分销参数缺行时的唯一回退值（= 00028 / 00029 生效的种子：费率 0、冻结 3 天、最低提现 10000），计提与后台分销页共用
commission_available.go: 「可用佣金」唯一口径（D-F-1），提现与转余额共用
commission_transfer.go: 佣金转入余额，与提现同一把科目锁；ListMyCommissionTransfers 列本人转出（门户佣金记录）
giftgrant.go: 礼品卡的发放侧（余额、流量包余额、延期、重置、开套餐）
ledger.go: 科目类型与余额方向、EnsureAccount、Post 记账与 Balance
*_test.go: 源码契约测试（锁序、kind 分支完整；经 platform/sourcetest 按声明名取源码，不按文件名读）、纯函数单元测试与 PG18 集成测试（*_pg18_test.go，由 deploy/run-pg18-gates.sh 的 billing / order_release / traffic_pack / plan_change 等域驱动；commission_ledger_pg18_test.go、commission_scope_pg18_test.go 与 manual_order_pg18_test.go 是挂在 TestOrderReleasePG18 上的子用例；ineligible_settlement_pg18_test.go 的订阅终态结算进挂账与 TestPlanChangePG18 同在 plan_change 域）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
