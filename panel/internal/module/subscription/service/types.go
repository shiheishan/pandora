package service

import (
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 订阅状态（库里 CHECK 只有这两个；Q1）。
const (
	StatusActive  = "active"
	StatusExpired = "expired"
)

// RenewalWindow 是过期后还能原地续费、延期救回的窗口：过期满 30 天由过期扫描（P4）写 renewal_closed_at 关窗。
const RenewalWindow = 30 * 24 * time.Hour

// 事件与流水上的操作者种类。
const (
	ActorUser      = "user"
	ActorAdmin     = "admin"
	ActorSystem    = "system"
	ActorPayment   = "payment"
	ActorScheduler = "scheduler"
)

// 订阅事件类型。
const (
	EventActivated     = "activated"
	EventRenewed       = "renewed"
	EventExtended      = "extended"
	EventPlanChanged   = "plan_changed"
	EventExpired       = "expired"
	EventRenewalClosed = "renewal_closed"
)

// 流量重置原因。
const (
	ResetRenewal    = "renewal"
	ResetCycleRoll  = "cycle_roll"
	ResetManual     = "manual"
	ResetGiftCard   = "gift_card"
	ResetPlanChange = "plan_change"
)

// 流量包余额来源。
const (
	PackSourceOrder    = "order"
	PackSourceGiftCard = "gift_card"
	PackSourceAdmin    = "admin"
)

// 延期来源（写进 extended 事件）。
const (
	ExtendSourceGiftCard = "gift_card"
	ExtendSourceAdmin    = "admin"
)

// 配额周期与常用指标（与 catalog 同口径）。
const (
	PeriodTotal   = "total"
	PeriodCycle   = "cycle"
	PeriodDay     = "day"
	PeriodMonth   = "month"
	MetricTraffic = "traffic.bytes"
)

// 审计动作名（沿用旧代码）与资源类型。
const (
	ActionExtended           = "subscription.extended"
	ActionTrafficGranted     = "subscription.traffic_granted"
	ActionManualReset        = "traffic.manual_reset"
	ActionDeviceLimitChanged = "subscription.device_limit_changed"
	ActionLabelChanged       = "subscription.label_changed"
	ActionPackTransferred    = "traffic_pack.transferred"
	ResourceSubscription     = "subscription"
)

// 对外的固定错误。同一个变量只读共享，调用方可以 errors.Is 比较。
var (
	// ErrNotFound 是订阅不存在或不是这个用户的（不暴露存在与否）。
	ErrNotFound = errs.New(errs.NotFound)
	// ErrRenewalWindowClosed 是过期超过 30 天、续费窗口已关的订阅再来续费或换套餐。
	ErrRenewalWindowClosed = errs.Newf(errs.Conflict, "这条订阅已过期超过 30 天，不能再原地续费，请重新购买（会换新的订阅链接）")
	// ErrExtendWindowClosed 是续费窗口已关的订阅再来延期。
	ErrExtendWindowClosed = errs.Newf(errs.Conflict, "这条订阅已过期超过 30 天，不能再延长，只能重新购买（会换新的订阅链接）")
	// ErrPackNeedsLive 是流量包要加到的那一份不在生效中。
	ErrPackNeedsLive = errs.Newf(errs.Conflict, "流量包只能加到在用的套餐上，先续费或买个套餐")
	// ErrPackDuplicate 是同一来源（Source, SourceID）的流量包已经发过。
	ErrPackDuplicate = errs.Newf(errs.Conflict, "这笔流量包已经发放过")
	// ErrTransferTarget 是流量包转移的目标已停用。
	ErrTransferTarget = errs.Newf(errs.Conflict, "只能转到一份还在用（或过期不满 30 天）的套餐上")
	// ErrTransferSource 是流量包转移的来源还在用（生效中或可救回）。
	ErrTransferSource = errs.Newf(errs.Conflict, "这份还在用，流量包不能转走；等它停用后再转")
	// ErrNotLive 是只给生效中的订阅做的操作（重置流量）遇到不生效的订阅。
	ErrNotLive = errs.Newf(errs.ValidationFailed, "这一份不在生效中，不能重置流量")
	// ErrNoTrafficQuota 是订阅没有流量配额行。
	ErrNoTrafficQuota = errs.Newf(errs.ValidationFailed, "这个订阅没有流量配额")
	// ErrVersionUnavailable 是开通、换套餐给的版本不存在、不属于该套餐或没发布。
	ErrVersionUnavailable = errs.Newf(errs.Conflict, "该套餐尚未发布可用版本")
	// ErrStateChanged 是带旧状态的条件更新影响 0 行（并发改了状态）。
	ErrStateChanged = errs.Newf(errs.Conflict, "订阅状态已变化，请刷新后重试")
)

// Actor 是事件与流水上的操作者。Kind 为 ActorUser、ActorAdmin 时 ID 必填，其余为 nil。
type Actor struct {
	Kind string
	ID   *uuid.UUID
}

// PriceSnapshot 是订阅上的价格快照：开通、续费、换套餐时由调用方从订单（或礼品卡）映射过来，
// subscription 不回查价格表。Interval 取 day/week/month/quarter/year/one_time，IntervalCount > 0。
type PriceSnapshot struct {
	// PriceID 可空：礼品卡开通可以没有价格档
	PriceID       *uuid.UUID
	Currency      money.Currency
	Amount        decimal.Decimal
	Interval      string
	IntervalCount int
}

// Subscription 是一份订阅的行本身（不含配额与目录信息）。
type Subscription struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	PlanID          uuid.UUID
	PlanVersionID   uuid.UUID
	Status          string
	PeriodStart     time.Time
	PeriodEnd       time.Time
	RenewalClosedAt *time.Time
	Price           PriceSnapshot
	// DeviceLimit 是管理员覆盖：nil 沿用套餐版本，0 不限
	DeviceLimit   *int
	Label         *string
	ProxyUUID     uuid.UUID
	NodeUID       int64
	SourceOrderID *uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Quota 是一行配额余额。Limit 为 nil 表示不限；Remaining = Limit − Consumed（不限时为 nil）。
type Quota struct {
	Metric      string
	Period      string
	PeriodStart time.Time
	PeriodEnd   *time.Time
	Limit       *int64
	Consumed    int64
	Remaining   *int64
}

// Summary 是一份订阅的摘要：行本身、状态口径、目录展示信息、配额与挂在上面的流量包余量。
type Summary struct {
	Subscription
	State State
	// PlanName、PlanVersion、PlanMaxDevices、QuotaResetStrategy 取自订阅锁定的套餐版本
	PlanName           string
	PlanVersion        int
	PlanMaxDevices     *int
	QuotaResetStrategy string
	// Quotas 按 metric 升序、同一指标内周期起点倒序（与旧接口一致）；永不为 nil
	Quotas []Quota
	// PackRemainingBytes 是挂在这一份上的流量包余量
	PackRemainingBytes int64
}

// UserSubscriptions 是一个用户的全部订阅。
type UserSubscriptions struct {
	// Subscriptions 按创建时间倒序；永不为 nil
	Subscriptions []Summary
	// Current 是「当前订阅」：生效中的优先，其次到期最晚，再次最近创建；没有订阅为 nil
	Current *Summary
	// UnattachedPackBytes 是还没加到任何一份的流量包余量
	UnattachedPackBytes int64
}

// Provision 是开通一份新订阅的输入（订单结算、礼品卡套餐卡、后台直接开通）。
// 权益（配额定义、设备数）随 PlanVersionID 由 subscription 经 catalog.VersionsByIDs 读取：版本发布即冻结，
// 不从调用方另收一份，免得两处口径不一。
type Provision struct {
	UserID        uuid.UUID
	PlanID        uuid.UUID
	PlanVersionID uuid.UUID
	Price         PriceSnapshot
	// SourceOrderID 是新购订单；礼品卡、后台直接开通为 nil。同一单只能开一份（库里唯一）
	SourceOrderID *uuid.UUID
	// Label 是备注名，空串不起名；与本人另一份撞名时自动加「 2」「 3」后缀，试满 20 次仍撞就不起名
	Label string
	// Actor 写进 activated 事件（订单结算 payment，礼品卡 user，后台 admin）
	Actor Actor
}

// Renewal 是在原订阅上续一期的输入（续费订单、同套餐的套餐卡）。版本沿用订阅自己的，不换。
type Renewal struct {
	SubscriptionID uuid.UUID
	// UserID 是订阅的主人，不符回 ErrNotFound
	UserID uuid.UUID
	// Price 是这次续费用的价格档（可以与原来的周期不同）；订阅上的价格快照随之更新
	Price PriceSnapshot
	// PaidAt 是付款时刻；OrderCreatedAt 用来认出「到期前下单、到期后才回调」（礼品卡为 nil）
	PaidAt         time.Time
	OrderCreatedAt *time.Time
	OrderID        *uuid.UUID
	Actor          Actor
	// Source 写进 renewed 事件：order 或 gift_card
	Source string
}

// RenewalResult 是一次续期的结果。
type RenewalResult struct {
	SubscriptionID uuid.UUID
	FromStatus     string
	// Restarted 为真是过期恢复：从付款时刻起算一整期，配额全部清零
	Restarted   bool
	PreviousEnd time.Time
	PeriodEnd   time.Time
}

// Extension 是只往后推到期时间、不开新周期的延期（礼品卡加时长、后台加时长）；已过期 30 天内的会被救回。
type Extension struct {
	SubscriptionID uuid.UUID
	// UserID 非 nil 时校验订阅属于他（礼品卡）；后台加时长传 nil
	UserID *uuid.UUID
	Days   int
	Actor  Actor
	// Source 是 ExtendSourceGiftCard 或 ExtendSourceAdmin
	Source string
	// Reason 可空，后台加时长时是管理员填的原因（写进事件）
	Reason string
}

// ExtensionResult 是一次延期的结果。
type ExtensionResult struct {
	SubscriptionID uuid.UUID
	UserID         uuid.UUID
	FromStatus     string
	// Rescued 为真是救回（已过期或已走过到期日）；Restarted 为真是走过到期日、从现在重开周期
	Rescued     bool
	Restarted   bool
	PreviousEnd time.Time
	PeriodEnd   time.Time
	// Prorated 是救回时按天数折算加进本周期 cycle 配额的额度（指标 → 字节）
	Prorated map[string]int64
}

// PlanChange 是把原订阅换成另一个套餐的输入（换套餐订单、套餐卡遇到不同套餐）。
// 剩余价值、补差价、退余额都由调用方（order / giftcard）算并记账，这里只落订阅与配额。
type PlanChange struct {
	SubscriptionID uuid.UUID
	UserID         uuid.UUID
	PlanID         uuid.UUID
	PlanVersionID  uuid.UUID
	Price          PriceSnapshot
	// 来源：OrderID（换套餐订单）或 GiftCodeID（套餐卡的卡密），恰好一个非 nil
	OrderID    *uuid.UUID
	GiftCodeID *uuid.UUID
	Actor      Actor
}

// PlanChangeResult 是一次换套餐的结果。
type PlanChangeResult struct {
	SubscriptionID    uuid.UUID
	FromPlanID        uuid.UUID
	FromPlanVersionID uuid.UUID
	FromStatus        string
	PreviousEnd       time.Time
	PeriodEnd         time.Time
}

// PackGrant 是发一笔流量包余额的输入。SubscriptionID 为 nil 时留作未分配。
type PackGrant struct {
	UserID         uuid.UUID
	SubscriptionID *uuid.UUID
	// Source 是 PackSourceOrder / PackSourceGiftCard / PackSourceAdmin；(Source, SourceID) 唯一，重复发放回 409
	Source   string
	SourceID uuid.UUID
	Bytes    int64
}

// Pack 是一笔流量包余额。
type Pack struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	SubscriptionID *uuid.UUID
	Source         string
	SourceID       uuid.UUID
	GrantedBytes   int64
	ConsumedBytes  int64
	RemainingBytes int64
	CreatedAt      time.Time
}

// PackBalance 是用户的流量包余额：全部剩余与逐笔明细（最近的在前，最多 100 笔）。
type PackBalance struct {
	RemainingBytesTotal int64
	Packs               []Pack
}

// Transfer 是把流量包从「未分配」（From 为 nil）或一份已停用的订阅，挂到同一用户一份没停用的订阅上。
type Transfer struct {
	UserID uuid.UUID
	From   *uuid.UUID
	To     uuid.UUID
	Actor  Actor
}

// TrafficReset 是把一份订阅本期的流量已用量清零（后台手动、礼品卡）。只给生效中的订阅做。
type TrafficReset struct {
	SubscriptionID uuid.UUID
	// UserID 非 nil 时校验订阅属于他（礼品卡）；后台传 nil
	UserID *uuid.UUID
	// Reason 是 ResetManual（ActorID 必填）或 ResetGiftCard
	Reason  string
	ActorID *uuid.UUID
	Note    string
}

// RenewalPrice 是门户续费界面要的当前价格档。
type RenewalPrice struct {
	ID              uuid.UUID
	Currency        money.Currency
	UnitAmount      decimal.Decimal
	BillingInterval string
	IntervalCount   int
	// Available：价格仍在售、属于本套餐、组价与用户组相符、在有效期内
	Available bool
}

// MySubscription 是门户「我的订阅」里的一份。
type MySubscription struct {
	Summary
	// EffectiveDeviceLimit = 订阅覆盖 → 套餐版本上限；nil 表示不限（覆盖为 0 时是 0，与旧接口同）
	EffectiveDeviceLimit *int
	// OnlineDevices 来自节点在线数据，P5 之前恒为 0
	OnlineDevices int
	// NextResetAt 是流量配额行的周期末
	NextResetAt *time.Time
	// Renewable = 没停用且套餐允许续费；Changeable = 没停用
	Renewable    bool
	Changeable   bool
	RenewalPrice *RenewalPrice
	// RenewUntil 是按 RenewalPrice 续一期会到哪天；不能续时为 nil
	RenewUntil *time.Time
	// ClientName 是 App 里的配置名「站点名 · 备注名（或套餐名）」
	ClientName string
}

// MySubscriptions 是门户「我的套餐」。
type MySubscriptions struct {
	Subscriptions       []MySubscription
	UnattachedPackBytes int64
}

// LabelResult 是改名之后这份订阅的名字：Label 为 nil 表示没起名。
type LabelResult struct {
	Label      *string
	ClientName string
}

// AdminExtendResult 是后台加时长的结果。
type AdminExtendResult struct {
	SubscriptionID uuid.UUID
	UserEmail      string
	Days           int
	PreviousEnd    time.Time
	PeriodEnd      time.Time
}

// AdminTrafficGrantResult 是后台加流量包的结果。
type AdminTrafficGrantResult struct {
	SubscriptionID uuid.UUID
	UserID         uuid.UUID
	UserEmail      string
	GrantID        uuid.UUID
	GrantedBytes   int64
	// RemainingBytesTotal 是发放之后这一份上全部流量包的剩余合计
	RemainingBytesTotal int64
}

// ResetLogFilter 是后台流量重置日志的筛选与分页。
type ResetLogFilter struct {
	UserID *uuid.UUID
	// Reason 为空不筛；否则必须是五种原因之一
	Reason string
	Limit  int
	Offset int
}

// ResetLogView 是后台看到的一条重置日志。
type ResetLogView struct {
	ID             uuid.UUID
	UserEmail      string
	PlanName       string
	Metric         string
	Reason         string
	ConsumedBefore int64
	ActorEmail     string
	Note           string
	CreatedAt      time.Time
}

// ResetStats 是近 30 天的重置统计。
type ResetStats struct {
	Last30Days  int
	ByReason    map[string]int
	FreedBytes  int64
	ManualCount int
}
