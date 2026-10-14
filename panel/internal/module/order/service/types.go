package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 订单种类（库里 CHECK 同一集合）。KindUpgrade 是换套餐（升级与降级都用它，方向由金额决定）。
const (
	KindNew     = "new"
	KindRenewal = "renewal"
	KindAddon   = "addon"
	KindTopup   = "topup"
	KindUpgrade = "upgrade"
)

// 订单状态：库里只写这五种。StatusPaid 只在结算事务内短暂出现（随后同一事务转 fulfilled）。
const (
	StatusPending   = "pending_payment"
	StatusPaid      = "paid"
	StatusFulfilled = "fulfilled"
	StatusCancelled = "cancelled"
	StatusExpired   = "expired"
)

// FilterStatuses 是列表筛选接受的状态（前端 ORDER_STATUSES 全集）：库里的 5 态，外加等产品确认的
// partially_refunded、refunded（库里不会有，筛它们得到空列表）；不在这里的回 400。
var FilterStatuses = []string{
	StatusPending, StatusPaid, StatusFulfilled, StatusCancelled, StatusExpired,
	"partially_refunded", "refunded",
}

// 预留状态。
const (
	ReservationHeld     = "held"
	ReservationCaptured = "captured"
	ReservationReleased = "released"
)

// 预留流水的事件种类。释放只有超时（expire）与取消（cancel）两条路。
const (
	EventReserve = "reserve"
	EventCapture = "capture"
	EventExpire  = "expire"
	EventCancel  = "cancel"
)

// 预留流水上的操作者种类。
const (
	ActorUser    = "user"
	ActorAdmin   = "admin"
	ActorSystem  = "system"
	ActorPayment = "payment"
)

// 取消、超时的原因：都写进预留的 release_reason；只有取消写订单的 cancel_reason（超时不写）。
const (
	ReasonUserCancelled = "user_cancelled"
	ReasonExpired       = "expired"
)

// 审计动作名（沿用旧代码）与资源类型。
const (
	ActionOrderCreated      = "order.created"
	ActionRenewalCreated    = "subscription.renewal_created"
	ActionTopupCreated      = "balance.topup_created"
	ActionOrderCancelled    = "order.cancelled"
	ActionOrderExpired      = "order.expired"
	ActionOrderMarkedPaid   = "order.marked_paid"
	ActionPaymentSucceeded  = "payment.succeeded"
	ActionPlanChangeCreated = "subscription.plan_change_created"
	ActionManualCreated     = "order.manual_created"
	ResourceOrder           = "order"
)

// 时间与金额口径（与旧代码一致）。
const (
	// PaymentWindow 是建单到付款期限的时长。
	PaymentWindow = 30 * time.Minute
	// QuoteAsOfWindow 是确认下单时带回的报价时刻最多早于现在多久。
	QuoteAsOfWindow = 10 * time.Minute
	// MaxSmallDueCents 是换套餐零头一次最多免掉的钱（分）；只有门户换套餐能免。
	MaxSmallDueCents = 99
	// TopupMinCents、TopupMaxCents 是单笔充值的上下限（分）。
	TopupMinCents = 100
	TopupMaxCents = 5_000_000
	// ExpireBatchDefault、ExpireBatchMax 是超时释放一轮处理的订单数。
	ExpireBatchDefault = 50
	ExpireBatchMax     = 500
	// maxQuoteRows 是换套餐报价最多展开的订阅数与目标套餐数（旧代码同值）。
	maxQuoteRows = 20
	// referenceMaxRunes 是线下凭证号的最大字数。
	referenceMaxRunes = 128
)

// 对外的固定错误（文案沿用旧代码）。同一个变量只读共享，调用方可以 errors.Is 比较。
var (
	ErrNotFound           = errs.New(errs.NotFound)
	ErrSamePlanUseRenewal = errs.Newf(errs.Conflict, "你已有这个套餐的订阅，请在原订阅上续费，订阅链接不变")
	ErrPurchaseLimit      = errs.Newf(errs.Conflict, "已达到该套餐的限购次数")
	ErrOpenChange         = errs.Newf(errs.Conflict, "这条订阅还有未完成的续费或变更套餐订单，请先支付或取消")
	ErrQuoteChanged       = errs.Newf(errs.QuoteChanged, "金额刚变了，请再确认一次")
	ErrBelowMinimum       = errs.Newf(errs.ValidationFailed, "应付金额低于支付渠道的最低付款额，请先充值或使用余额支付")
	ErrAlreadyPaid        = errs.Newf(errs.Conflict, "订单已支付，不能取消")
	ErrStateChanged       = errs.Newf(errs.Conflict, "订单状态已变化，请刷新后再操作")
	ErrNotPayable         = errs.Newf(errs.Conflict, "该订单当前状态不可支付")
	ErrNoExternalPayment  = errs.Newf(errs.Conflict, "该订单无需外部支付")
	ErrPaymentLapsed      = errs.Newf(errs.OrderLapsed, "这张订单已超过付款期限，请取消后重新下单")
	ErrMarkPaidZero       = errs.Newf(errs.Conflict, "这张订单不需要支付")
	ErrUnknownStatus      = errs.Newf(errs.BadRequest, "不支持的订单状态")
	ErrPlanChangeSamePlan = errs.Newf(errs.Conflict, "与当前套餐相同，请使用续费")
	ErrPlanChangeCurrency = errs.Newf(errs.Conflict, "变更套餐不能更换币种")
	// errProrationMixedCurrency：不同币种的钱加不到一起，也就没有「剩余价值」可言
	errProrationMixedCurrency = errs.Newf(errs.Conflict, "这条订阅本周期的付费订单币种不一致，无法折算")
	// errManualBelowMinimum 是后台待支付单应付低于最低额：用户付不了。带 fields.settlement，后台据它落到「结算方式」上
	errManualBelowMinimum = &errs.Error{Code: errs.ValidationFailed, Message: manualBelowMinimumMessage,
		Fields: map[string]string{"settlement": manualBelowMinimumMessage}}
	errOfflineNothingDue       = errs.Newf(errs.Conflict, "这张订单不需要支付，请改用赠送")
	errOfflineChangeNothingDue = errs.Newf(errs.Conflict, "原套餐的剩余价值已经抵完新价，这张单不需要支付，请改用赠送")
)

// manualBelowMinimumMessage 是后台待支付单低于最低额时落在「结算方式」上的话。
const manualBelowMinimumMessage = "应付金额低于支付渠道的最低付款额，用户无法在线支付，请改用赠送或线下已收款"

// Actor 是一次状态变化的操作者（写预留流水）。Kind 为 ActorUser、ActorAdmin 时 ID 必填。
type Actor struct {
	Kind string
	ID   *uuid.UUID
}

// Expect 是确认下单时前端带回的报价：AsOf 必须在 [now-10min, now] 内，三项金额任一与服务端重算不等回 409 quote_changed。
// 为 nil 时照常执行（余额同样经 PlanBalance 规范化）。
type Expect struct {
	AsOf           time.Time
	Total          decimal.Decimal
	BalanceApplied decimal.Decimal
	Payable        decimal.Decimal
}

// Checkout 是门户三种建单入口共有的部分。
type Checkout struct {
	// UseBalance 是想用的余额（开关打开时前端传可用余额，关掉传 0）
	UseBalance decimal.Decimal
	CouponCode string
	Expect     *Expect
}

// Created 是建单的结果（门户 201 响应的内容）。Status 为 pending_payment，或零元单、线下已收款当场履约后的 fulfilled。
// Total 是免掉零头之后的订单总额（与库里 total_amount 一致）。Change 只在换套餐单上非 nil。
type Created struct {
	OrderID        uuid.UUID
	OrderNo        string
	Currency       money.Currency
	Total          decimal.Decimal
	Discount       decimal.Decimal
	BalanceApplied decimal.Decimal
	Payable        decimal.Decimal
	// Waived 是免掉的零头（只有门户换套餐会有）；对外的 discount_amount 照旧含它
	Waived decimal.Decimal
	Status string
	Change *ChangeAmounts
}

// ChangeAmounts 是换套餐单另给的两项：原订阅的剩余价值，与履约时退进余额的差额（降级才有）。
type ChangeAmounts struct {
	ProrationCredit decimal.Decimal
	BalanceRefund   decimal.Decimal
}

// CreatedReply 由 handler 提供：把建单结果做成幂等可重放的响应。service 在建单事务里调用它，
// ctx 里有幂等认领时随即 CompleteBound（资源 order），保证「订单落库」与「幂等完成」同一事务提交。
type CreatedReply func(Created) (idemsvc.Response, error)

// NewOrderInput 是门户新购（POST /v1/orders）。
type NewOrderInput struct {
	PlanID  uuid.UUID
	PriceID uuid.UUID
	// NewCopy 为真是「另买一份」：已有可续的同款也照样新开
	NewCopy bool
	// Label 是新那一份的备注名（规范化见 subscription.NormalizeLabel）
	Label string
	Checkout
	Reply CreatedReply
}

// RenewalOrderInput 是门户续费（POST /v1/me/subscriptions/{id}/renew）。
type RenewalOrderInput struct {
	SubscriptionID uuid.UUID
	// PriceID 为 nil 时续订阅当前的价格档
	PriceID *uuid.UUID
	Checkout
	Reply CreatedReply
}

// PackOrderInput 是门户流量包下单（POST /v1/me/traffic-pack-orders）。
type PackOrderInput struct {
	PackID         uuid.UUID
	SubscriptionID uuid.UUID
	Checkout
	Reply CreatedReply
}

// TopupInput 是门户充值（POST /v1/me/topups）。Currency 为空时取 CNY（旧代码口径）。
type TopupInput struct {
	Amount   decimal.Decimal
	Currency money.Currency
	Reply    TopupReply
}

// TopupCreated 是充值建单的结果。
type TopupCreated struct {
	OrderID  uuid.UUID
	OrderNo  string
	Amount   decimal.Decimal
	Currency money.Currency
}

// TopupReply 同 CreatedReply，用于充值。
type TopupReply func(TopupCreated) (idemsvc.Response, error)

// BalancePlan 是一单用多少余额、还要在线付多少（旧 purchase.Balance 的 decimal 版）。
type BalancePlan struct {
	// Applied 用掉的余额
	Applied decimal.Decimal
	// Payable 还需在线支付
	Payable decimal.Decimal
	// Kept 为了凑够支付最低额而少用、留在余额里的钱
	Kept decimal.Decimal
	// Forced 应付本身低于最低额且余额够付：只能全用余额付
	Forced bool
	// BelowMinimum 应付低于最低额、用尽余额仍付不了（旧 Short）；只有换套餐零头能免
	BelowMinimum bool
	// SmallDue 剩下的零头已免掉（只有换套餐）
	SmallDue bool
	// Waived 免掉的零头
	Waived decimal.Decimal
}

// QuoteInput 是门户统一报价（POST /v1/me/checkout/quote）。Action 取 new、renew、pack；change 第二期才支持。
type QuoteInput struct {
	Action         string
	SubscriptionID *uuid.UUID
	PlanID         *uuid.UUID
	PackID         *uuid.UUID
	CouponCode     string
	NewCopy        bool
}

// 报价动作。
const (
	QuoteNew    = "new"
	QuoteRenew  = "renew"
	QuotePack   = "pack"
	QuoteChange = "change"
)

// QuoteResult 是报价响应：Balance 是报价时的可用余额，MinPayment 是能在线付的最低额。
type QuoteResult struct {
	AsOf       time.Time
	Currency   money.Currency
	Balance    decimal.Decimal
	MinPayment decimal.Decimal
	Quotes     []Quote
}

// Quote 是一条报价：一个（订阅，套餐或流量包，价格档）组合。
type Quote struct {
	SubscriptionID *uuid.UUID
	PlanID         *uuid.UUID
	PackID         *uuid.UUID
	PriceID        *uuid.UUID
	Currency       money.Currency
	Interval       string
	IntervalCount  int
	Subtotal       decimal.Decimal
	Discount       decimal.Decimal
	// CouponCode 是生效的优惠码；CouponError 是优惠码不能用的中文原因（不让整个报价失败）
	CouponCode  *string
	CouponError *string
	// Credit 是换套餐时原套餐剩余价值（其余动作为 0）；CreditDetail 是「怎么算的」，只有换套餐且原订阅有周期时非 nil
	Credit         decimal.Decimal
	CreditDetail   *CreditDetail
	Total          decimal.Decimal
	Refund         decimal.Decimal
	WithBalance    BalancePlan
	WithoutBalance BalancePlan
	PeriodStart    *time.Time
	PeriodEnd      *time.Time
	PreviousEnd    *time.Time
}

// PayInput 是门户发起支付（POST /v1/orders/{id}/pay）。
type PayInput struct {
	OrderID   uuid.UUID
	Provider  string
	Method    string
	ReturnURL string
}

// CancelInput 是取消订单。门户：Admin=false，取消自己的单，原因固定 user_cancelled；
// 后台：Admin=true，ExpectedStateVersion 必填，Reason 5–500 字。
type CancelInput struct {
	OrderID              uuid.UUID
	Admin                bool
	ExpectedStateVersion *int64
	Reason               string
}

// CancelResult 是取消的结果。AlreadyTerminal 为真表示订单早已是同一终态（重复取消）。
type CancelResult struct {
	Order           OrderRow
	AlreadyTerminal bool
}

// QueryResult 是主动查单的结果（payment.Query 的结果加上查完后的订单状态）。
type QueryResult struct {
	OrderID         uuid.UUID
	OrderNo         string
	ProviderCode    string
	ChannelStatus   string
	Reconciled      bool
	AlreadyRecorded bool
	QuarantineKind  *string
	OrderStatus     string
}

// MarkPaidInput 是后台标记已支付（线下收款）。
type MarkPaidInput struct {
	OrderID   uuid.UUID
	Reason    string
	Reference string
}

// MarkPaidResult 是标记已支付的结果。
type MarkPaidResult struct {
	Processed      bool
	AlreadyHandled bool
	PaymentID      *uuid.UUID
	SubscriptionID *uuid.UUID
	LedgerTxnID    *uuid.UUID
}

// ExpireStats 是一轮超时释放的统计。
type ExpireStats struct {
	Scanned int
	Expired int
	Skipped int
	Failed  int
}

// OrderRow 是列表与详情共用的一行订单（金额是元，handler 换成分）。
type OrderRow struct {
	ID                uuid.UUID
	OrderNo           string
	UserID            uuid.UUID
	Kind              string
	Status            string
	Currency          money.Currency
	Subtotal          decimal.Decimal
	Discount          decimal.Decimal
	ProrationCredit   decimal.Decimal
	Waived            decimal.Decimal
	Total             decimal.Decimal
	BalanceApplied    decimal.Decimal
	Payable           decimal.Decimal
	Paid              decimal.Decimal
	CouponID          *uuid.UUID
	CreatedBy         *uuid.UUID
	ManualReason      *string
	SubscriptionID    *uuid.UUID
	SubscriptionLabel *string
	StateVersion      int64
	ExpiresAt         time.Time
	PaidAt            *time.Time
	FulfilledAt       *time.Time
	CancelledAt       *time.Time
	ExpiredAt         *time.Time
	CancelReason      *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ItemRow 是一条订单行快照。Entitlements、Quotas 是快照的 JSON 数组原文。
type ItemRow struct {
	ID            uuid.UUID
	PriceID       *uuid.UUID
	PlanID        *uuid.UUID
	PlanVersionID *uuid.UUID
	TrafficPackID *uuid.UUID
	Name          string
	PlanVersion   *int
	Interval      *string
	IntervalCount *int
	TrafficBytes  *int64
	Entitlements  string
	Quotas        string
	Quantity      int
	UnitAmount    decimal.Decimal
	LineAmount    decimal.Decimal
	Currency      money.Currency
	CreatedAt     time.Time
}

// ListFilter 是订单列表的筛选。门户只用 Statuses、Limit、Offset（用户取自登录态）。
// Q 匹配订单号（包含，不分大小写）或用户邮箱（包含）；From、To 按 created_at 左闭右开。
type ListFilter struct {
	Statuses []string
	UserID   *uuid.UUID
	Q        string
	From     *time.Time
	To       *time.Time
	Limit    int
	Offset   int
}

// MyOrderView 是「我的订单」的一行。ItemName 恒有值（充值单是「余额充值」），Cancellable 只在待支付时为真。
type MyOrderView struct {
	OrderRow
	PlanName         *string
	ItemName         string
	Interval         *string
	IntervalCount    *int
	Cancellable      bool
	HasPaymentIntent bool
}

// MyOrderCounts 是「我的订单」顶部的分组计数（按全部订单，不受状态筛选影响）：
// Open 待支付，Paid 已支付或已履约，Closed 已取消或已过期，Refunded 恒为 0（Q2 不做退款）。
type MyOrderCounts struct {
	Open, Paid, Closed, Refunded int64
}

// MyOrderList 是「我的订单」的一页。
type MyOrderList struct {
	Orders []MyOrderView
	Total  int64
	Counts MyOrderCounts
}

// PaymentLine 是门户订单详情里的一笔收款。
type PaymentLine struct {
	Status       string
	Amount       decimal.Decimal
	Currency     money.Currency
	CreatedAt    time.Time
	Method       *string
	ProviderName string
}

// MyOrderDetail 是门户订单详情。
type MyOrderDetail struct {
	Order                 MyOrderView
	Items                 []ItemRow
	Payments              []PaymentLine
	CouponCode            *string
	SubscriptionPeriodEnd *time.Time
}

// AdminOrderView 是后台订单列表的一行。
type AdminOrderView struct {
	OrderRow
	UserEmail     string
	PlanName      *string
	Interval      *string
	IntervalCount *int
	ItemCount     int
	Manual        bool
	ProviderCode  *string
	ProviderName  *string
}

// AdminOrderList 是后台订单列表的一页。
type AdminOrderList struct {
	Orders []AdminOrderView
	Total  int64
}

// AdminOrderDetail 是后台订单详情。
type AdminOrderDetail struct {
	Order          AdminOrderView
	CreatedByEmail *string
	Items          []ItemRow
}

// PaidOrder 是结算成功时交给 PaidHook 的订单摘要。Total 是订单总额（含余额抵扣部分），佣金基数用它（Q3）。
type PaidOrder struct {
	OrderID   uuid.UUID
	OrderNo   string
	UserID    uuid.UUID
	Kind      string
	Currency  money.Currency
	Total     decimal.Decimal
	Paid      decimal.Decimal
	PaidAt    time.Time
	PaymentID uuid.UUID
}

// PaidHook 是结算成功后的可选回调（commission 在第四波实现）。在结算事务里调用：返回错误整笔结算回滚。
// 调用时机沿用旧代码计佣的口径：只在收款结算（SettlePayment：渠道到账、后台标记已付）把订单转为 paid 时调用，
// 且 Kind 不是 topup、Total > 0。零应付的单（余额全额付、全额折扣）建单时当场履约，旧代码不计佣，这里也不调用。
//
// 锁序约定：调用时本事务已持有订单、目标订阅、套餐 / 券与本单的账务科目（含平台收入科目 platform_revenue）。
// 实现方（commission）要锁的科目（推荐人余额、佣金科目等）必须排在平台收入科目之后锁，
// 不得先锁自己的科目再等平台收入，也不得回头锁套餐、券、订阅，否则会与别的结算成环（40P01）。
type PaidHook interface {
	OnOrderPaid(ctx context.Context, o PaidOrder) error
}
