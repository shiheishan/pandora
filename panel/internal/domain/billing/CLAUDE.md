# panel/internal/domain/billing/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

订单、支付与复式账本
  - 钱的不变量（金额恒等式、预留图、借贷配平、订单/幂等对称绑定、各 kind 的履约证据）落在迁移的约束与触发器里，这里编排事务与锁序：订单 →（续费 / 变更单才有的）订阅 → 支付意图 → 预留图子资源 → 按 UUID 排序的账本科目
  - payment_intents 的 BEFORE UPDATE 守卫 app.guard_payment_intent 只放行可变列白名单 status、provider_ref、action_payload、failure_code、failure_message、expires_at、updated_at、query_attempts、next_query_at（status 只许按状态机前进、provider_ref 只写一次），其余列改了即 check_violation；给支付意图加要 UPDATE 的列，须在同一迁移里 CREATE OR REPLACE 该函数把列补进白名单（现行定义在 00097）
  - 订单 kind 决定建单与履约路径：new 开订阅、renewal 延周期、upgrade 原地换套餐（D-E-2）、addon 发流量包余额（D-E-1）、topup 入余额。

成员清单
service.go: Service 骨架：NewService、SetUsersChangedNotifier 注入，履约后经 onUsersChanged 在提交后发租户级节点通知，零元单（赠送、全额抵扣、零元续费与变更）由 notifyIfFulfilled 补发；包内共用小工具
checkout.go: 新购下单 CreateOrder（目录校验、库存与限购预留、余额冻结、券核销、幂等绑定与预制响应）与零元单当场捕获履约 captureZeroPayOrder
settlement.go: 支付回调结算主链，整条在一个文件：HandlePaymentWebhook → settlePaymentTx → postOrderPaid → fulfillOrder，回调按 kind 分派履约
  - 续费 / 变更单锁住订阅后按建单同一口径复核订阅状态，不收（支付窗口里被改成终态）就把钱隔离进挂账、订单与订阅不动
  - settlePaymentTx 在调用方事务里执行，回调 / 标记已支付各开一个事务调它，人工单线下已收款在建单事务里调它
provision.go: 开订阅 provisionSubscription 与建配额 initQuotaBalances 的唯一实现，下单履约与礼品卡套餐兑换（grantPlanDirect）共用
order_holds.go: 各建单路径共用的预留父节点与余额冻结（insertHeldReservation / prepareBalanceHold / postBalanceHold）
reservations.go: 结算与释放共用的预留图加锁校验；orderTotal 是金额恒等式 total = max(小计 − 折扣 − 折算, 0) + 税（00071）
release.go: 取消 / 过期释放，held 预留图整体转 released 并退回余额冻结
  - 订单上有收款就拒绝释放，订阅不收而进挂账的收款不算（否则冻结的余额永远退不回来）
  - 续费走专用分支，其余 kind 共用预留图锁
  - 冲突原因用 releaseConflict 带中文文案（errors.Is 仍认作冲突哨兵，过期任务照旧空转），releaseHTTPError 是后台与门户取消共用的错误翻译
release_locks.go: 释放的各加锁步骤：订单（过期扫描 SKIP LOCKED）、活跃支付意图、已结算收款证据（有即拒绝）、预留图（续费单专用分支再锁券与余额冻结）
reservation_expiry.go: 到期预留的批量释放扫描
renewal.go: 续费 CreateRenewal 与周期滚动 RollQuotaPeriods
  - subscriptionAcceptsPaidChange 是可续费 / 可变更订阅状态的唯一口径（建单与结算复核共用，与 00095 守卫一致）
  - 旧周期已走完（状态仍 active 也算）时新周期从付款时刻起算
  - 与变更套餐共用零元单捕获 captureZeroPaySubscriptionOrder 和结算锁 lockOrderSubscriptionForSettlement
plan_change.go: 变更套餐（D-E-2，kind=upgrade）的试算、下单与原地履约：换套餐不换凭据、配额按新套餐重置（新套餐没有的指标变不限量，配额行不删因人工调整只许追加）、降级差额冲回收入退进余额（plan_change_refund 分录）
  - 同一订阅同时只许一张在途续费或变更单
plan_change_quote.go: 变更套餐的剩余价值折算，只算不写：本周期付费合计 × min(时间比, 流量比) 向下取整，付费天数先用、赠送天数最后用
traffic_pack.go: 流量包（D-E-1）目录、下单（kind=addon）、履约成挂用户的 traffic_pack_grants 余额与余额查询
traffic_reset.go: 流量重置日志与后台手动重置；只清套餐已用量，不碰流量包
topup.go: 自助充值单（kind=topup，结算即履约）与管理员调账
payments.go: 发起支付取收银台、渠道回调翻译成平台事件，确认到账后交回 HandlePaymentWebhook；NewPaymentService 注入进程共用的结算 Service（补记履约才接得上节点通知）
payment_methods.go: 门户可用支付方式读模型 PaymentMethods：启用且接受新支付的渠道按 config.methods（缺省 default_method，都没有时渠道本身一行、method 为空串）展开；中文名与空币种补 [] 在处理器
payment_query.go: 向渠道主动查单 QueryOrderPayment（PAY-009，后台与门户共用）：
  - 按订单发起过支付的渠道逐个查，查到已付以 PaymentRef + ":RECONCILED" 为事件号交回 HandlePaymentWebhook——去重靠结算主链按渠道流水号认出已记过的那笔，回调先到后到都只入账一次
  - 取消 / 过期单查到的钱照回调进挂账，金额币种不符整笔 409
  - 渠道停用 / 不支持 / 查询失败都翻成中文错误、订单不动
  - 从未发起支付回 409
payment_query_audit.go: 后台查单入口 AdminQueryOrderPayment：
  - 照常查单后另开事务写一条 order.payment_queried 审计（操作人、订单、渠道、result = reconciled / already_recorded / paid / unpaid / not_found / failed，失败带错误码），订单不存在不记
  - 审计写不进去回 500，不假装成功
  - 门户查单不经过这里
payment_query_patrol.go: 定时巡检 ReconcileDuePayments：
  - 逐个认领到期的在途支付意图（短事务 + FOR UPDATE SKIP LOCKED + 把 next_query_at 推到未来，多实例不会并发查同一单，查单不占行锁），DefaultPaymentQueryPatrol 为 5 分钟首查、指数退避、最多 6 次、订单过期前 90 秒最后一查、一轮 20 单间隔 500ms
  - 渠道不支持查单即把次数拉满不再查（列见迁移 00097）
unexpected_payment.go: 已释放或已付清订单又来的钱、续费 / 变更单结算时订阅已不收的钱，按 case_kind（released_order / excess_capture / ineligible_subscription）隔离进挂账（late_payment_suspense）
late_payment.go: 挂账的查看与转入余额，供后台消费
manual_order.go: 管理员人工单与 mark-paid，复用下单与回调主链路
  - mark-paid 的钱进了挂账时入账与审计照写、回 409 说明去向，同一凭证重复标记回 409
  - 人工单结算方式 grant（赠送当场履约，缺省）/ pending（建待支付单交给用户付，仍记开单人）/ offline（带凭证号，建单事务里按 offline 渠道结清，收入与佣金同 mark-paid，offlinePaymentInput 是两条路共用的回调形状），balance 暂不接受（D-C-3）
portal_catalog.go: 门户套餐目录读模型 PortalCatalog：
  - 可见性（public / 登录后 authenticated / 组内 group）、已发布当前版本、有 CNY / USD 适用价格（含组价）才列，逐套餐带当前版本额度与适用价格
  - 下单路径独立复查，这里不是唯一防线
my_orders.go: 门户订单读模型：myOrderSelectSQL 是列表与详情共用的行形状（首项周期与商品名快照、has_payment_intent 有无任何支付意图），列表带筛选段计数 counts，详情带优惠码、订阅到期与支付渠道名
  - ParseOrderStatuses 是门户与后台订单列表共用的状态白名单（逗号多值、精确匹配、未知回 400）
coupon.go: 优惠券校验 applyCoupon、核销 redeemCoupon 与试算（套餐 PreviewForPrice、流量包 PreviewForTrafficPack 共用 previewCoupon 外壳，响应带券面）
coupon_admin.go: 后台优惠券用例：
  - AdminListCoupons 分页带总数与实际减免合计、AdminCreateCoupon（券码重复回 409）、AdminGenerateCoupons 一个事务内随机出 10 位码（去掉易混字符）、ON CONFLICT DO NOTHING 撞码重试并封顶、只审计规格与数量，AdminSetCouponStatus 只停用不删除，AdminCouponRedemptions 核销明细
  - 规格 AdminCouponSpec 由 handler 规范化后传入
commission.go: 分销佣金计提、解冻、提现申请与打款记账
  - 计佣范围 commission.scope（first_order 只给被推荐人第一笔计佣订单返佣，缺省 every_order），ValidCommissionScope 供后台校验
  - CommissionDefault* 是分销参数缺行时的唯一回退值（= 00028 / 00029 生效的种子：费率 0、冻结 3 天、最低提现 10000），计提与后台分销页共用
commission_admin.go: 后台佣金与提现用例：
  - AdminListWithdrawals（收款信息只回密文，handler 解开）、AdminReviewWithdrawal 只审待处理的申请、AdminMarkWithdrawalPaid 锁行 → PostWithdrawalPayout 记账 → CAS 置 paid 同一事务、AdminCommissionOverview 汇总并按 CommissionDefault* 回退参数、AdminSetCommissionConfig 只 upsert 给出的参数且有改动才审计
commission_available.go: 「可用佣金」唯一口径（D-F-1），提现与转余额共用
commission_transfer.go: 佣金转入余额，与提现同一把科目锁；ListMyCommissionTransfers 列本人转出（门户佣金记录）
giftgrant.go: 礼品卡的发放侧（余额、流量包余额、延期、重置、开套餐）
ledger.go: 科目类型与余额方向、EnsureAccount、Post 记账与 Balance
*_test.go: 源码契约测试（锁序、kind 分支完整；经 platform/sourcetest 按声明名取源码，不按文件名读）、
  - 纯函数单元测试与 PG18 集成测试（*_pg18_test.go，由 deploy/run-pg18-gates.sh 的 billing / order_release / traffic_pack / plan_change 等域驱动
  - commission_ledger_pg18_test.go、commission_scope_pg18_test.go 与 manual_order_pg18_test.go 是挂在 TestOrderReleasePG18 上的子用例
  - ineligible_settlement_pg18_test.go 的订阅终态结算进挂账与 TestPlanChangePG18 同在 plan_change 域
  - payment_query_pg18_test.go 是 payment_query 域（主动查单补记、补记与回调去重、取消单进挂账、金额不符、门户行 has_payment_intent、后台查单审计、并发巡检），渠道替身在 payment_query_stub_test.go
  - 本包 PG18 的共用夹具在 order_release_pg18_fixture_test.go、共用断言在 order_release_pg18_assert_test.go，settlement 的夹具与断言在 settlement_pg18_{fixture,assert}_test.go
  - order_release_pg18_test.go 只有一个 832 行的门禁函数，纯挪动拆不开，行数守卫单独豁免
  - settlement_pg18_test.go 被 deploy/test-settlement-runner_static_test.sh 按文件名 grep，门禁字面量留在它里面）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
