# panel/internal/domain/billing/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

订单、支付与复式账本。钱的不变量（金额恒等式、预留图、借贷配平、订单/幂等对称绑定、各 kind 的履约证据）落在迁移的约束与触发器里，这里编排事务与锁序：订单 →（续费 / 变更单才有的）订阅 → 支付意图 → 预留图子资源 → 按 UUID 排序的账本科目。订单 kind 决定建单与履约路径：new 开订阅、renewal 延周期、upgrade 原地换套餐（D-E-2）、addon 发流量包余额（D-E-1）、topup 入余额。

成员清单
checkout.go: 新购下单 CreateOrder 与支付回调 HandlePaymentWebhook 主链路，回调按 kind 分派履约；provisionSubscription / initQuotaBalances 开订阅与建配额；超 800 行的存量大文件
order_holds.go: 各建单路径共用的预留父节点与余额冻结（insertHeldReservation / prepareBalanceHold / postBalanceHold）
reservations.go: 结算与释放共用的预留图加锁校验；orderTotal 是金额恒等式 total = max(小计 − 折扣 − 折算, 0) + 税（00071）
release.go: 取消 / 过期释放，held 预留图整体转 released 并退回余额冻结；续费走专用分支，其余 kind 共用预留图锁
reservation_expiry.go: 到期预留的批量释放扫描
renewal.go: 续费 CreateRenewal 与周期滚动 RollQuotaPeriods；与变更套餐共用零元单捕获 captureZeroPaySubscriptionOrder 和结算锁 lockOrderSubscriptionForSettlement
plan_change.go: 变更套餐（D-E-2，kind=upgrade）的试算、下单与原地履约：换套餐不换凭据、配额按新套餐重置（新套餐没有的指标变不限量，配额行不删因人工调整只许追加）、降级差额冲回收入退进余额（plan_change_refund 分录）；同一订阅同时只许一张在途续费或变更单
plan_change_quote.go: 变更套餐的剩余价值折算，只算不写：本周期付费合计 × min(时间比, 流量比) 向下取整，付费天数先用、赠送天数最后用
traffic_pack.go: 流量包（D-E-1）目录、下单（kind=addon）、履约成挂用户的 traffic_pack_grants 余额与余额查询
traffic_reset.go: 流量重置日志与后台手动重置；只清套餐已用量，不碰流量包
topup.go: 自助充值单（kind=topup，结算即履约）与管理员调账
payments.go: 发起支付取收银台、渠道回调翻译成平台事件，确认到账后交回 HandlePaymentWebhook
unexpected_payment.go: 已释放或已付清订单又来的钱，隔离进挂账（late_payment_suspense）
late_payment.go: 挂账的查看与转入余额，供后台消费
manual_order.go: 管理员人工单与 mark-paid，复用下单与回调主链路
my_orders.go: 门户订单读模型
coupon.go: 优惠券校验 applyCoupon、核销 redeemCoupon 与试算
commission.go: 分销佣金计提、解冻、提现申请与打款记账
commission_available.go: 「可用佣金」唯一口径（D-F-1），提现与转余额共用
commission_transfer.go: 佣金转入余额，与提现同一把科目锁
giftgrant.go: 礼品卡的发放侧（余额、流量包余额、延期、重置、开套餐）
ledger.go: 科目类型与余额方向、EnsureAccount、Post 记账与 Balance
*_test.go: 源码契约测试（锁序、kind 分支完整）、纯函数单元测试与 PG18 集成测试（*_pg18_test.go，由 deploy/run-pg18-gates.sh 的 billing / order_release / traffic_pack / plan_change 等域驱动）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
