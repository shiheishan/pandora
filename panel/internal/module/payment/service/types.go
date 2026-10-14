// Package service 是 payment 模块的业务层：支付渠道、支付意图、回调入账、查单、迟到支付挂账。
// 别的模块只调本包；对外写方法都能在调用方事务里执行（ctx 里有事务就加入）。
package service

import (
	"context"
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ---------------------------------------------------------------------------
// 结算端口：由 order 实现、在构造时经 UseSettler 注册
// ---------------------------------------------------------------------------

// Settler 由 order 实现：在回调入账的同一事务里结算订单。
//
// 调用约定：
//   - 无论线上还是线下收款，Settler 都必须核对 Amount、Currency 等于订单应付；不一致返回普通错误（整笔入账回滚，渠道会重推）。
//   - 订单此刻不收这笔钱（已取消、超时、已付、订阅不再收）时返回 NotPayable(kind, orderStatus)，payment 转挂账。
//   - payment 调用前开了保存点：Settler 返回任何错误，payment 都先回滚到保存点，Settler 这次的写入（哪怕写了一半、
//     哪怕有语句已失败）一律撤销。返回 NotPayable 时 payment 接着在同一事务里记挂账；其他错误原样返回，整笔回滚。
//   - 锁序：Settler 先锁订单行；payment 在调它之前不锁意图（插收款时外键检查只对意图加 KEY SHARE）。
type Settler interface {
	SettlePayment(ctx context.Context, in SettleInput) error
}

// SettleInput 是一笔已确认、已落库的收款。有支付意图时 Amount 与 Currency 已核对等于意图快照，
// 但 Settler 仍要核对等于订单应付（钱的最后一道兜底；线下收款没有意图，只有这一道）。
// Fee 与 ProviderCode 给 ledger.PostOrderPaid 用（渠道实收 = Amount，手续费 = Fee，渠道科目按 ProviderCode）。
type SettleInput struct {
	OrderID      uuid.UUID
	PaymentID    uuid.UUID
	Amount       decimal.Decimal
	Fee          decimal.Decimal
	Currency     money.Currency
	ProviderCode string
	Method       string
}

// errOrderNotPayable 是 NotPayableError 的哨兵；不导出，免得 Settler 返回不带挂账类型的裸哨兵（那会被当成普通错误回滚）。
var errOrderNotPayable = errors.New("payment: 订单不可付")

// CaseKind 是挂账类型。
type CaseKind string

const (
	CaseReleasedOrder          CaseKind = "released_order"          // 订单已取消或超时
	CaseExcessCapture          CaseKind = "excess_capture"          // 订单已经付过
	CaseIneligibleSubscription CaseKind = "ineligible_subscription" // 订阅已不收这笔续费或变更
)

// Valid 报告挂账类型是否在库里登记的 3 种之一。
func (k CaseKind) Valid() bool {
	switch k {
	case CaseReleasedOrder, CaseExcessCapture, CaseIneligibleSubscription:
		return true
	default:
		return false
	}
}

// NotPayableError 是 Settler 拒收一笔收款的原因；用 NotPayable 构造，用 IsNotPayable 或 errors.As 判断。
type NotPayableError struct {
	Kind CaseKind
	// OrderStatus 是订单此刻的状态，记进挂账案；必须是前端 ORDER_STATUSES 里的值（后台挂账列表按它校验），由 order 保证。
	OrderStatus string
}

func (e *NotPayableError) Error() string {
	return "payment: 订单不可付（" + string(e.Kind) + "，订单状态 " + e.OrderStatus + "）"
}

// Is 让 errors.Is(err, errOrderNotPayable) 成立（包内用）。
func (e *NotPayableError) Is(target error) bool { return target == errOrderNotPayable }

// NotPayable 构造 Settler 的拒收错误。
func NotPayable(kind CaseKind, orderStatus string) error {
	return &NotPayableError{Kind: kind, OrderStatus: orderStatus}
}

// IsNotPayable 报告 err 是不是 NotPayable 构造的拒收错误。
func IsNotPayable(err error) bool {
	var npe *NotPayableError
	return errors.As(err, &npe)
}

// ---------------------------------------------------------------------------
// 状态
// ---------------------------------------------------------------------------

// IntentStatus 是支付意图的状态。
type IntentStatus string

const (
	IntentRequiresAction IntentStatus = "requires_action"
	IntentSucceeded      IntentStatus = "succeeded"
	IntentCancelled      IntentStatus = "cancelled"
	IntentExpired        IntentStatus = "expired"
)

// LateStatus 是挂账案的状态。
type LateStatus string

const (
	LateSuspense LateStatus = "suspense"
	LateApplied  LateStatus = "applied"
)

// ChannelStatus 是主动查单时渠道侧的支付状态。
type ChannelStatus string

const (
	ChannelPaid     ChannelStatus = "paid"
	ChannelUnpaid   ChannelStatus = "unpaid"
	ChannelNotFound ChannelStatus = "not_found"
)

// 适配器名。
const (
	AdapterEpay    = "epay"
	AdapterOffline = "offline"
)

// OfflineProviderCode 是迁移里种下的线下收款渠道。
const OfflineProviderCode = "offline"

// OfflineMethod 是线下收款记在 payments.method 上的方式。
const OfflineMethod = "offline"

// EpayMethods 是易支付可用的方式（用户 2026-10-07 定：只出支付宝和微信）。
var EpayMethods = []string{"alipay", "wxpay"}

// ---------------------------------------------------------------------------
// 渠道
// ---------------------------------------------------------------------------

// ProviderView 是后台渠道卡的一行。商户号与密钥只写不读，这里只有 HasCredentials。
type ProviderView struct {
	ID               uuid.UUID
	Code             string
	Adapter          string
	DisplayName      string
	Enabled          bool
	AcceptingNew     bool
	HasCredentials   bool
	BaseURL          string
	Currencies       []string
	SubmitPath       string
	APIPath          string
	Methods          []string
	DefaultMethod    string
	AllowPrivateHost bool
	MinAmount        decimal.Decimal
	// Today 是站点时区今天成功入账的金额，按币种分开。
	Today map[money.Currency]decimal.Decimal
	// SuccessRate24h 是近 24 小时进入终态的意图里成功的比例；没有样本为 nil。
	SuccessRate24h *float64
	// LastCallbackAt 是最近一条渠道事件的接收时间。
	LastCallbackAt *time.Time
}

// ProviderSettings 是建与改共用的可写字段。
type ProviderSettings struct {
	DisplayName      string
	BaseURL          string
	SubmitPath       string
	APIPath          string
	Methods          []string
	DefaultMethod    string
	AllowPrivateHost bool
	// MinAmount 是最低付款额（元，0.01–1000.00）；零值表示新建用默认 1.00、编辑保留原值。
	MinAmount decimal.Decimal
	// MerchantID 与 Key 只写不读：新建必填，编辑留空表示不改。
	MerchantID string
	Key        string
}

// CreateProviderInput 是新建渠道的入参。新建的渠道「已启用、暂停收新单」。
type CreateProviderInput struct {
	Code    string
	Adapter string
	ProviderSettings
}

// ProviderWriteResult 是建、改渠道的结果。
type ProviderWriteResult struct {
	ID                 uuid.UUID
	Code               string
	CredentialsChanged bool
}

// PaymentMethod 是门户可选的一种支付方式；Method 为空串表示交给渠道决定。
type PaymentMethod struct {
	Provider    string
	DisplayName string
	Method      string
	Currencies  []string
}

// ---------------------------------------------------------------------------
// 支付意图与入账
// ---------------------------------------------------------------------------

// CreateIntentInput 是 order 发起支付的入参。order 已锁住订单行、核过订单可付；payment 不反查订单。
type CreateIntentInput struct {
	OrderID      uuid.UUID
	OrderNo      string
	UserID       uuid.UUID
	Amount       decimal.Decimal // 应付金额（外部支付部分），> 0
	Currency     money.Currency
	ProviderCode string
	Method       string // 渠道内方式；空串取渠道默认
	Title        string // 收银台商品名
	ReturnURL    string // 付完回跳地址；不是本站地址时改用缺省
	ClientIP     string
	ExpiresAt    *time.Time // 订单付款期限；意图有效期取它与 30 分钟里较早的
}

// IntentResult 是收银台跳转信息。
type IntentResult struct {
	IntentID    uuid.UUID
	OutTradeNo  string
	HTTPMethod  string
	RedirectURL string
	FormFields  map[string]string
	Amount      decimal.Decimal
	Currency    money.Currency
	ExpiresAt   *time.Time
	// Reused 为 true 表示复用了同渠道、同方式的在途意图。
	Reused bool
}

// RecordResult 是一笔收款（回调、查单补记、线下收款）的入账结果。
// Processed 与 AlreadyHandled 互斥：前者这次推进了业务（结清订单或进了挂账），后者这笔钱此前已入账。
type RecordResult struct {
	Processed       bool
	AlreadyHandled  bool
	SignatureFailed bool
	PaymentID       uuid.UUID // AlreadyHandled 时是此前那一笔
	// QuarantineKind 非空表示没有结清订单，而是进了挂账。
	QuarantineKind CaseKind
	// LedgerTxnID 只在进挂账时有值（late_payment_suspense 交易）；结清订单的账由 order 记。
	LedgerTxnID uuid.UUID
}

// OfflineInput 是一笔线下收款（后台「标记已支付」、人工单「线下已收款」）。
type OfflineInput struct {
	OrderID   uuid.UUID
	OrderNo   string
	UserID    uuid.UUID
	Amount    decimal.Decimal
	Currency  money.Currency
	Reference string // 线下凭证号：同一张凭证不能入账两次
	Reason    string
}

// QueryResult 是一次主动查单的结果；订单状态由 order 自己补。
type QueryResult struct {
	OrderID         uuid.UUID
	OrderNo         string
	ProviderCode    string
	ChannelStatus   ChannelStatus
	Reconciled      bool // 这次查单补记了一笔此前没记上的钱
	AlreadyRecorded bool // 渠道说的那笔钱此前已入账
	QuarantineKind  CaseKind
}

// PatrolStats 是一轮巡检的计数。
type PatrolStats struct {
	Queried    int
	Reconciled int
	Failed     int
}

// ---------------------------------------------------------------------------
// 挂账
// ---------------------------------------------------------------------------

// LateCase 是后台挂账列表的一行。
type LateCase struct {
	ID               uuid.UUID
	Kind             CaseKind
	Status           LateStatus
	Amount           decimal.Decimal
	Currency         money.Currency
	OrderID          uuid.UUID
	OrderNo          string
	OrderStatus      string
	UserID           uuid.UUID
	UserEmail        string
	ReceivedAt       time.Time
	ResolvedAt       *time.Time
	ResolutionReason *string
}

// LateList 是挂账列表与待处理合计（按币种分开）。
type LateList struct {
	Cases   []LateCase
	Total   int64
	Pending map[money.Currency]decimal.Decimal
}

// ---------------------------------------------------------------------------
// 给 order 拼订单详情与列表
// ---------------------------------------------------------------------------

// IntentView 是订单的一次支付尝试。
type IntentView struct {
	ID           uuid.UUID
	ProviderCode string
	ProviderName string
	OutTradeNo   string
	ProviderRef  *string
	Method       string
	Currency     money.Currency
	Amount       decimal.Decimal
	Status       IntentStatus
	ExpiresAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PaymentView 是订单的一笔收款。
type PaymentView struct {
	ID                uuid.UUID
	IntentID          *uuid.UUID
	ProviderCode      string
	ProviderName      string
	ProviderPaymentID string
	Currency          money.Currency
	Amount            decimal.Decimal
	Fee               decimal.Decimal
	Status            string
	Method            *string
	PaidAt            time.Time
}

// OrderHistory 是一张订单的支付尝试与收款（都按时间倒序）。
type OrderHistory struct {
	Intents  []IntentView
	Payments []PaymentView
}

// OrderPaymentSummary 是订单列表上的支付摘要。
type OrderPaymentSummary struct {
	HasIntent bool
	// 渠道：有收款取最近一笔收款的渠道，否则取最近一次支付尝试的渠道；都没有为 nil。
	ProviderCode *string
	ProviderName *string
}

// ---------------------------------------------------------------------------
// 错误
// ---------------------------------------------------------------------------

var (
	// ErrProviderNotFound 是渠道代码不存在（或不是能在线支付的渠道）。
	ErrProviderNotFound = errs.Newf(errs.NotFound, "未知的支付渠道")
	// ErrProviderDisabled 是渠道被管理员停用。
	ErrProviderDisabled = errs.Newf(errs.Unavailable, "该支付渠道已停用，请更换其他支付方式")
	// ErrProviderPaused 是渠道暂停收新单。
	ErrProviderPaused = errs.Newf(errs.Unavailable, "该支付渠道暂停收单，请稍后再试或更换渠道")
	// ErrBelowMinimum 是应付低于所选渠道的最低额。
	ErrBelowMinimum = errs.Newf(errs.Conflict, "支付金额低于该付款方式的最低额")
	// ErrAmountMismatch 是渠道报来的金额或币种与支付意图快照不一致，整笔拒收。
	ErrAmountMismatch = errs.Newf(errs.Conflict, "支付币种或金额与订单不一致")
	// ErrNeverPaid 是订单从没发起过支付，无从查单。
	ErrNeverPaid = errs.Newf(errs.Conflict, "该订单从未发起过支付，无法向渠道查单")
	// ErrCaseResolved 是挂账已经处理过。
	ErrCaseResolved = errs.Newf(errs.Conflict, "这笔挂账已经处理过了")
	// ErrPaymentReused 是渠道流水号（或线下凭证号）已用于别的订单。
	ErrPaymentReused = errs.Newf(errs.Conflict, "凭证号已用于其他订单")
)
