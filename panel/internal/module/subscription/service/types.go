package service

import (
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 订阅状态（库里 CHECK 只有这两个；Q1）。
const (
	StatusActive  = "active"
	StatusExpired = "expired"
)

// RenewalWindow 是过期后还能原地续费、延期救回的窗口：过期满 30 天由过期扫描写 renewal_closed_at 关窗。
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
	// EventLinkRotated 是换订阅链接（旧凭据吊销、签新凭据、节点身份一起换）；状态不变
	EventLinkRotated = "link_rotated"
	// EventPeriodEndSet 是后台直接改到期时间；状态不变
	EventPeriodEndSet = "period_end_set"
	// EventQuotaLimitSet 是后台改一行配额的当期上限；状态不变
	EventQuotaLimitSet = "quota_limit_set"
)

// 流量重置原因。
const (
	ResetRenewal    = "renewal"
	ResetCycleRoll  = "cycle_roll"
	ResetManual     = "manual"
	ResetGiftCard   = "gift_card"
	ResetPlanChange = "plan_change"
	// ResetOrder 是用户自己买的流量重置（order 模块结算履约时调 ResetTraffic）
	ResetOrder = "order"
)

// 送流量的来源（GrantBonus）。
const (
	BonusGiftCard = "gift_card"
	BonusAdmin    = "admin"
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

// 审计动作名与资源类型。
const (
	ActionExtended           = "subscription.extended"
	ActionTrafficGranted     = "subscription.traffic_granted"
	ActionManualReset        = "traffic.manual_reset"
	ActionDeviceLimitChanged = "subscription.device_limit_changed"
	ActionLabelChanged       = "subscription.label_changed"
	ActionLinkRotated        = "subscription.link_rotated"
	ActionLinkRotatedByAdmin = "subscription.link_rotated_by_admin"
	ResourceSubscription     = "subscription"
)

// 对外的固定错误。同一个变量只读共享，调用方可以 errors.Is 比较。
var (
	// ErrNotFound 是订阅不存在或不是这个用户的（不暴露存在与否）。
	ErrNotFound = errs.New(errs.NotFound)
	// ErrRenewalWindowClosed 是过期超过 30 天、续费窗口已关的订阅再来续费或换套餐。
	ErrRenewalWindowClosed = errs.Newf(errs.Conflict, "这一份已过期超过 30 天，不能再续费，请重新买一份（会是新链接）")
	// ErrExtendWindowClosed 是续费窗口已关的订阅再来延期。
	ErrExtendWindowClosed = errs.Newf(errs.Conflict, "这一份已过期超过 30 天，不能再延长，只能重新买一份（会是新链接）")
	// ErrNotLive 是只给生效中的订阅做的操作（重置流量）遇到不生效的订阅。
	ErrNotLive = errs.Newf(errs.ValidationFailed, "这一份不在生效中，不能重置流量")
	// ErrNoTrafficQuota 是订阅没有流量配额行。
	ErrNoTrafficQuota = errs.Newf(errs.ValidationFailed, "这一份不限流量，不用重置")
	// ErrNotActive 是送流量的那一份已过期（送的流量只给生效中的订阅）。
	ErrNotActive = errs.Newf(errs.Conflict, "这一份已过期，不能再加流量")
	// ErrUnlimited 是送流量的那一份不限流量（没有限额的流量配额行），无需加。
	ErrUnlimited = errs.Newf(errs.Conflict, "这一份不限流量，无需加流量")
	// ErrBonusPeriodSwitching：本期已结束、还没滚到下一期，这时送的流量会在滚动时被清零。
	ErrBonusPeriodSwitching = errs.Newf(errs.Conflict, "本期流量刚结束，正在换到下一期，请几分钟后再试")
	// ErrRenewOneTime 是一次性套餐来续费：一次性套餐不能续费，只能叠加或另买一份。
	ErrRenewOneTime = errs.Newf(errs.Conflict, "一次性套餐不能续费，可以叠加到这一份或另买一份")
	// ErrStackNotOneTime 是叠加的目标不是一次性套餐，或与要叠加的不是同一个套餐。
	ErrStackNotOneTime = errs.Newf(errs.Conflict, "只有同一个一次性套餐才能叠加")
	// ErrVersionUnavailable 是开通、换套餐给的版本不存在、不属于该套餐或没发布。
	ErrVersionUnavailable = errs.Newf(errs.Conflict, "该套餐尚未发布可用版本")
	// ErrStateChanged 是带旧状态的条件更新影响 0 行（并发改了状态）。
	ErrStateChanged = errs.Newf(errs.Conflict, "这一份的状态刚变了，请刷新后重试")
	// ErrRotateWhileExpired 是门户在订阅不生效时换链接：续费后原链接自动恢复，过期期间换掉会让所有设备上的旧链接在续费后失效。
	ErrRotateWhileExpired = errs.Newf(errs.Conflict, "这一份已过期，续费后原链接会自动恢复；过期期间不能换新链接")
	// ErrPullDenied 是订阅拉取认证不通过（前缀或令牌不对、凭据失效、订阅已停用、主人账号停用）：
	// handler 一律回伪装 404，不区分原因。
	ErrPullDenied = errs.New(errs.NotFound)
	// ErrDeliveryUnavailable 是订阅分发要的依赖（UseDelivery、UseNodeSource）还没装配：回 503，不伪造空结果。
	ErrDeliveryUnavailable = errs.Newf(errs.Unavailable, "链接服务暂时不可用，请稍后再试")
)

// Actor 是事件与流水上的操作者。Kind 为 ActorUser、ActorAdmin 时 ID 必填，其余为 nil。
type Actor struct {
	Kind string
	ID   *uuid.UUID
}

// PriceSnapshot 是订阅上的价格快照：开通、续费、叠加、换套餐时由调用方从订单（或礼品卡）映射过来，
// subscription 不回查价格表。Interval 取 month/year（订阅套餐）或 one_time（一次性套餐），IntervalCount > 0。
type PriceSnapshot struct {
	// PriceID 可空：后台直接开通、注册试用可以没有价格档
	PriceID       *uuid.UUID
	Amount        decimal.Decimal
	Interval      string
	IntervalCount int
	// ValidityDays 只对一次性价有意义：开通 / 叠加时决定期末（nil = 永久，期末落远期哨兵）；
	// 只在开通、叠加时用一次，期末已落在 current_period_end，订阅上不另存
	ValidityDays *int
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

// Quota 是一行配额余额。Limit 为 nil 表示不限；Bonus 是本期送的量；Remaining = Limit + Bonus − Consumed（不限时为 nil）。
type Quota struct {
	Metric      string
	Period      string
	PeriodStart time.Time
	PeriodEnd   *time.Time
	Limit       *int64
	Bonus       int64
	Consumed    int64
	Remaining   *int64
}

// Usage 是一份订阅按套餐版本给的用量（门户「你在用」卡显示）。
type Usage struct {
	// TrafficBytes 是版本里跟着套餐类型走的那条流量定义（订阅套餐每期、一次性套餐每次购买的总量）；nil = 不限或没有流量定义
	TrafficBytes *int64
	// DeviceLimit 是生效的设备上限：订阅上的覆盖优先，否则版本的设备数；nil = 不限（口径同 MySubscription.EffectiveDeviceLimit）
	DeviceLimit *int
	// ThrottleKbps 是版本限速；nil = 不限速
	ThrottleKbps *int
}

// Summary 是一份订阅的摘要：行本身、状态口径、目录展示信息、配额、流量与重置次数（门户与后台共用）。
type Summary struct {
	Subscription
	State State
	// PlanName、PlanKind 取自套餐；PlanVersion、PlanMaxDevices 取自订阅锁定的套餐版本
	PlanName       string
	PlanKind       string
	PlanVersion    int
	PlanMaxDevices *int
	// Permanent 是一次性套餐买的永久（期末是远期哨兵，见 IsPermanent）
	Permanent bool
	// TrafficLimit、TrafficBonus、TrafficUsed 取自流量配额行（订阅套餐的 cycle 行、一次性套餐的 total 行）：
	// 上限（nil = 不限或没有流量配额行）、本期送的量、已用
	TrafficLimit *int64
	TrafficBonus int64
	TrafficUsed  int64
	// ResetsUsed 是本周期（订阅套餐）或有效期内（一次性套餐）已付费重置的次数（traffic_reset_logs reason=order，按订单计）；
	// ResetLimit、ResetPrice 取自套餐（nil = 不限次数 / 不卖重置）
	ResetsUsed int
	ResetLimit *int
	ResetPrice *decimal.Decimal
	// Quotas 按 metric 升序、同一指标内周期起点倒序；永不为 nil
	Quotas []Quota
	// Usage 是这一份锁定版本的用量。
	Usage Usage
	// NextUsage 是下个周期会用的用量。只有订阅套餐、没停用、套餐当前版本非空且不是这份锁定的版本、读得到那个版本、且三项里至少一项与 Usage 不同时才有值，否则 nil。一次性套餐恒为 nil。
	NextUsage *Usage
}

// UserSubscriptions 是一个用户的全部订阅。
type UserSubscriptions struct {
	// Subscriptions 按创建时间倒序；永不为 nil
	Subscriptions []Summary
	// Current 是「当前订阅」：生效中的优先，其次到期最晚，再次最近创建；没有订阅为 nil
	Current *Summary
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

// Renewal 是在原订阅上续一期的输入（续费订单、同套餐的订阅套餐卡）。只许订阅套餐（一次性回 ErrRenewOneTime）。
// 版本：提前续费本期不动，到点由配额滚动切到套餐当前版本；过期恢复当场切到当前版本。
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

// Stacking 是一次性套餐的叠加（叠加单履约、同套餐的一次性套餐卡）：total 流量配额上限加上新版本的流量，
// 期末：新价有天数 → max(原期末, now) + N 天；新价永久或原来已永久 → 永久哨兵。
type Stacking struct {
	SubscriptionID uuid.UUID
	// UserID 是订阅的主人，不符回 ErrNotFound
	UserID uuid.UUID
	// PlanVersionID 是买的时候套餐的当前版本：叠加后这一份切到它，加的流量取它的 total 流量配额
	PlanVersionID uuid.UUID
	Price         PriceSnapshot
	PaidAt        time.Time
	// 来源：OrderID（叠加单）或 GiftCodeID（套餐卡的卡密），恰好一个非 nil
	OrderID    *uuid.UUID
	GiftCodeID *uuid.UUID
	Actor      Actor
	// Source 写进 renewed 事件：order 或 gift_card
	Source string
}

// BonusGrant 是送流量（礼品卡、后台「加流量」、盲盒奖品）：加到这一份流量配额行的 bonus_value。
// 订阅套餐加在本周期的 cycle 行（滚动时清零），一次性套餐加在 total 行（一直有效）。
type BonusGrant struct {
	SubscriptionID uuid.UUID
	// UserID 非 nil 时校验归属（礼品卡）；后台传 nil
	UserID *uuid.UUID
	// Bytes > 0
	Bytes int64
	// Source 是 BonusGiftCard 或 BonusAdmin
	Source string
	// ActorID 后台必填
	ActorID *uuid.UUID
	Note    string
}

// PlanChange 是把原订阅换成另一个套餐的输入（换套餐订单、套餐卡遇到不同套餐）。可以跨类型（订阅 ↔ 一次性）。
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

// TrafficReset 是把一份订阅本期的流量已用量清零（后台手动、礼品卡、用户购买）。只给生效中的订阅做。
type TrafficReset struct {
	SubscriptionID uuid.UUID
	// UserID 非 nil 时校验订阅属于他（礼品卡、用户购买）；后台传 nil
	UserID *uuid.UUID
	// Reason 是 ResetManual（ActorID 必填）、ResetGiftCard 或 ResetOrder（UserID 必填，Note 写订单号）
	Reason  string
	ActorID *uuid.UUID
	Note    string
	// At 是判断订阅是否生效用的时刻；零值用 subscription 自己的时钟。订单结算履约时传结算的 now，与订单模块的复核用同一个时刻（不然卡在到期边界时两边结论可能不同，整笔结算报错回滚）
	At time.Time
}

// RenewalPrice 是门户续费界面要的当前价格档。
type RenewalPrice struct {
	ID              uuid.UUID
	UnitAmount      decimal.Decimal
	BillingInterval string
	IntervalCount   int
	// Available：价格仍在售、属于本套餐
	Available bool
}

// MySubscription 是门户「我的订阅」里的一份。
type MySubscription struct {
	Summary
	// EffectiveDeviceLimit = 订阅覆盖 → 套餐版本上限；nil 表示不限（覆盖为 0 时是 0）
	EffectiveDeviceLimit *int
	// OnlineDevices 是设备识别窗口内跨节点去重的在线设备数（node 经 OnlineDeviceCounter 提供，读不到按 0）
	OnlineDevices int
	// NextResetAt 是流量配额行的周期末
	NextResetAt *time.Time
	// Renewable = 没停用、订阅套餐、套餐没归档且允许续费；Stackable = 没停用、一次性套餐、套餐在售且接受新购；Changeable = 没停用
	Renewable    bool
	Stackable    bool
	Changeable   bool
	RenewalPrice *RenewalPrice
	// RenewUntil 是按 RenewalPrice 续一期会到哪天；不能续时为 nil
	RenewUntil *time.Time
	// ClientName 是 App 里的配置名「站点名 · 备注名（或套餐名）」
	ClientName string
	// IsTrial 是注册试用发的那一份（subscription_trials 里有它）；续过费 id 不变，仍算。
	IsTrial bool
}

// MySubscriptions 是门户「我的套餐」。
type MySubscriptions struct {
	Subscriptions []MySubscription
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

// AdminTrafficGrantResult 是后台加流量的结果。
type AdminTrafficGrantResult struct {
	SubscriptionID uuid.UUID
	UserID         uuid.UUID
	UserEmail      string
	GrantedBytes   int64
	// BonusBytes 是加完之后这一份本期送的量合计（bonus_value）
	BonusBytes int64
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

// PullRequest 是一次订阅拉取（GET /{prefix}/{token}）的输入。来源 IP 与 UA 由 service 从 ctx 的 reqmeta 取。
type PullRequest struct {
	Prefix string
	// Token 是路径里的原样令牌，可能带客户端加的扩展名（.yaml、.json 等），由 service 剥掉
	Token string
	// Target 是显式指定的格式（查询参数 target / flag / type 里第一个非空的）；空串按 UA 判断
	Target string
	// Types 是查询参数 types 原文（按协议过滤，如 vless|hysteria2）；空不过滤
	Types string
	// Filter 是查询参数 filter 原文（按节点名关键字过滤，| 或逗号分隔）；空不过滤
	Filter string
	// Host 是请求的 Host 头（可能带端口）；经后台登记过的订阅域名到达的成功拉取给该域名加分
	Host string
}

// Userinfo 是 Subscription-Userinfo 头的四个数：字节与 Unix 秒。Expire 为 0 时不写 expire。
type Userinfo struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   int64
}

// PullResult 是一次成功的拉取（含过期订阅的提示配置），handler 原样写响应头与正文。
type PullResult struct {
	Body        []byte
	ContentType string
	Userinfo    Userinfo
	// UpdateIntervalHours 是 Profile-Update-Interval：正常 12，过期 1（续费后客户端很快拉回真实线路）
	UpdateIntervalHours int
	// ContentDisposition 带客户端里的配置名（与门户 client_name 同一个 render.ProfileName）
	ContentDisposition string
	// Expired 为真是过期订阅的提示配置；WebPageURL 是门户续费页（Profile-Web-Page-Url），只在过期时非空
	Expired    bool
	WebPageURL string
}

// Link 是门户「订阅链接」里的一条：一份订阅当前的有效凭据。
type Link struct {
	SubscriptionID uuid.UUID
	URL            string
	FetchCount     int64
	LastFetchedAt  *time.Time
	// DistinctSources24h 是近 24 小时成功拉取的不同来源数；明显偏高多半是链接被分享出去了
	DistinctSources24h int
	// Expired：订阅不在生效中、链接暂停（只读展示，续费后原链接自动恢复）
	Expired bool
}

// NodePreview 是门户看到的一条线路：只有名称、协议与倍率，地址、端口与协议配置只经订阅链接下发。
type NodePreview struct {
	Name        string
	Protocol    string
	TrafficRate float64
}

// MaxUsageDays 是按日用量一次最多回看的天数（约一个季度）。
const MaxUsageDays = 93

// UsageDay 是一天的计费字节数；Date 形如 2026-10-12（站点时区的自然日）。
type UsageDay struct {
	Date  string
	Bytes int64
}

// DailyUsage 是一份订阅在一个窗口内的按日用量：Days 覆盖窗口内每一天（没流量的补 0），按日期升序，最后一天是今天。
type DailyUsage struct {
	// Timezone 是切日用的时区（站点时区）
	Timezone      string
	PeriodStart   time.Time
	PeriodEnd     *time.Time
	Days          []UsageDay
	TodayBytes    int64
	AvgDailyBytes int64
}

// AdminRotateResult 是后台替用户换链接的结果；刻意没有令牌与链接（后台看不到用户的订阅地址）。
type AdminRotateResult struct {
	SubscriptionID uuid.UUID
	UserEmail      string
}
