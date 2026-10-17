package service

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 套餐类型：建好后不可改（价格经复合外键钉住）。
const (
	// KindSubscription 是订阅套餐：按时间算，月 / 季 / 半年 / 年四档价，每周期一份流量、新周期清零。
	KindSubscription = "subscription"
	// KindOneTime 是一次性套餐：按流量算，一个价格 + 一份总流量，不清零；有效天数可选，空为永久；不能续费。
	KindOneTime = "one_time"
)

// 套餐状态：draft → active（首个版本发布即上架）→ archived；draft 也可直接归档。归档不可恢复。
const (
	PlanDraft    = "draft"
	PlanActive   = "active"
	PlanArchived = "archived"
)

// 版本状态：draft → published（发布即冻结）。
const (
	VersionDraft     = "draft"
	VersionPublished = "published"
)

// 价格状态：active → archived（只追加，改价 = 归档旧的、建新的）。
const (
	PriceActive   = "active"
	PriceArchived = "archived"
)

// 套餐可见性。
const (
	VisibilityPublic        = "public"
	VisibilityAuthenticated = "authenticated"
	VisibilityGroup         = "group"
	VisibilityHidden        = "hidden"
)

// 价格周期。订阅套餐只有 (month,1) (month,3) (month,6) (year,1) 四档；一次性套餐只有 (one_time,1)。
const (
	IntervalMonth   = "month"
	IntervalYear    = "year"
	IntervalOneTime = "one_time"
)

// 配额周期：订阅套餐的流量配额按周期（cycle），一次性套餐是总量（total）。建版本时按套餐类型落，不让人选。
const (
	PeriodTotal = "total"
	PeriodCycle = "cycle"
)

// 流量配额的指标与单位。
const (
	MetricTraffic = "traffic.bytes"
	UnitBytes     = "bytes"
)

// Plan 是套餐本身（不含版本与价格）。
type Plan struct {
	ID          uuid.UUID
	Code        string
	Name        string
	Description *string
	// Kind 是 KindSubscription 或 KindOneTime，建好后不可改。
	Kind             string
	Status           string
	CurrentVersionID *uuid.UUID
	Visibility       string
	// VisibleGroupIDs 只在 visibility = group 时非空，按 id 升序；永不为 nil。
	VisibleGroupIDs  []uuid.UUID
	AllowNewPurchase bool
	// AllowRenewal 对一次性套餐恒为 false（库里 CHECK）。
	AllowRenewal bool
	AllowUpgrade bool
	Recommended  bool
	// ResetPrice 是每次流量重置的价格（元）；nil = 不卖流量重置。
	ResetPrice *decimal.Decimal
	// ResetLimit 是重置次数上限：订阅按每周期、一次性按有效期内；nil = 不限次数。
	ResetLimit *int
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Entitlement 是版本上的一项权益；Value 是任意 JSON。
type Entitlement struct {
	Code  string
	Value json.RawMessage
}

// Quota 是版本上的一项配额定义；Limit 为 nil 表示不限。
type Quota struct {
	Metric string
	Limit  *int64
	Unit   string
	Period string
}

// Version 是套餐版本（用量：流量、设备、限速）与它的权益、配额。
type Version struct {
	ID           uuid.UUID
	PlanID       uuid.UUID
	Version      int
	Status       string
	FrozenAt     *time.Time
	MaxDevices   *int
	ThrottleKbps *int
	RowVersion   int64
	CreatedBy    *uuid.UUID
	// CreatedByEmail 只在后台详情里填（建版本的管理员）；账号已删或其它读取为 nil。
	CreatedByEmail *string
	CreatedAt      time.Time
	// Entitlements 按 code 升序，永不为 nil。
	Entitlements []Entitlement
	// Quotas 按 metric、period 升序，永不为 nil。
	Quotas []Quota
}

// TrafficLimit 返回 traffic.bytes 配额的上限（有多条时取第一条）；没有这项或不限时为 nil。
func (v Version) TrafficLimit() *int64 {
	for _, q := range v.Quotas {
		if q.Metric == MetricTraffic {
			return q.Limit
		}
	}
	return nil
}

// Price 是一档价格。
type Price struct {
	ID       uuid.UUID
	PlanID   uuid.UUID
	PlanKind string
	// UnitAmount 单位元、2 位小数。
	UnitAmount      decimal.Decimal
	BillingInterval string
	IntervalCount   int
	// ValidityDays 只有一次性价有；nil = 永久。只影响之后的购买。
	ValidityDays *int
	Status       string
	RowVersion   int64
	CreatedAt    time.Time
}

// PlanUsage 是套餐列表与详情共用的摘要：当前版本的用量（还没发布过时取草稿版本）、版本号、草稿与线路。
type PlanUsage struct {
	// Version 是当前发布版本的版本号；还没发布过为 nil。
	Version *int
	// DraftVersionID、DraftVersion 是未发布的草稿版本（每个套餐至多一个）；没有为 nil。
	DraftVersionID *uuid.UUID
	DraftVersion   *int
	// TrafficBytes、MaxDevices、ThrottleKbps 取当前版本，还没发布过取草稿版本；nil = 不限。
	TrafficBytes *int64
	MaxDevices   *int
	ThrottleKbps *int
	// PoolIDs 是套餐绑定的节点池（按 id 升序），永不为 nil。
	PoolIDs []uuid.UUID
}

// PlanSummary 是后台套餐列表的一行。
type PlanSummary struct {
	Plan
	PlanUsage
	// Prices 是在售价格，按周期从短到长（一次性只有一个），永不为 nil。
	Prices []Price
}

// PlanDetail 是后台套餐详情。
type PlanDetail struct {
	Plan
	PlanUsage
	// Versions 按 version 降序（含 CreatedByEmail），永不为 nil。
	Versions []Version
	// Prices 是全部价格（含已归档），按 created_at 降序，永不为 nil。
	Prices []Price
}

// PlanFilter 是后台套餐列表的筛选；空串表示不限。Q 按名称或代码模糊匹配（不区分大小写）。
type PlanFilter struct {
	Kind   string
	Status string
	Q      string
}

// TierInput 是订阅套餐的一档价：周期必须是四档之一。
type TierInput struct {
	BillingInterval string
	IntervalCount   int
	UnitAmount      decimal.Decimal
}

// TierChange 是改价时的一档：UnitAmount 为 nil 表示不卖这一档（有在售价就归档）。
type TierChange struct {
	BillingInterval string
	IntervalCount   int
	UnitAmount      *decimal.Decimal
}

// OneTimeInput 是一次性套餐的唯一价格与有效天数（nil = 永久）。
type OneTimeInput struct {
	UnitAmount   decimal.Decimal
	ValidityDays *int
}

// ResetInput 是流量重置的价格与次数上限（nil = 不限次数）。
type ResetInput struct {
	Price decimal.Decimal
	Limit *int
}

// UsageInput 是套餐用量：nil 表示不限（流量字节、设备数、限速 kbps）。
type UsageInput struct {
	TrafficBytes *int64
	MaxDevices   *int
	ThrottleKbps *int
}

// SalesInput 是套餐的销售设置。一次性套餐的 AllowRenewal 必须为 false。
type SalesInput struct {
	Visibility       string
	VisibleGroupIDs  []uuid.UUID
	AllowNewPurchase bool
	AllowRenewal     bool
	AllowUpgrade     bool
	Recommended      bool
}

// PlanCreate 是新建套餐（先选类型）的输入：套餐、草稿版本 1、价格、线路、流量重置一个事务建成，Publish 为真时直接发布上架。
// Code 不由人填，按名称自动生成（code.go）。
type PlanCreate struct {
	Kind        string
	Name        string
	Description *string
	// Tiers 只给订阅套餐：至少一档，周期不重复。
	Tiers []TierInput
	// OneTime 只给一次性套餐，必填。
	OneTime *OneTimeInput
	Usage   UsageInput
	// PoolIDs 已去重；空表示不绑（发布时由 PublishGate 拒绝）。
	PoolIDs []uuid.UUID
	// Reset 为 nil 表示不卖流量重置。
	Reset   *ResetInput
	Sales   SalesInput
	Publish bool
}

// BasicUpdate 是改名称与说明（code 不改）。
type BasicUpdate struct {
	ExpectedRowVersion int64
	Name               string
	Description        *string
}

// SalesUpdate 是改销售设置。
type SalesUpdate struct {
	ExpectedRowVersion int64
	Sales              SalesInput
}

// ResetUpdate 是改流量重置；Enabled 为假时 Price、Limit 不看（关掉）。
type ResetUpdate struct {
	ExpectedRowVersion int64
	Enabled            bool
	Price              decimal.Decimal
	Limit              *int
}

// PricesUpdate 是改价：订阅套餐给 Tiers（只动列出的档），一次性套餐给 OneTime。改价 = 归档旧价、建新价。
type PricesUpdate struct {
	ExpectedRowVersion int64
	Tiers              []TierChange
	OneTime            *OneTimeInput
}

// PoolsUpdate 是整体替换套餐的线路（已去重，空即清空），立即生效。
type PoolsUpdate struct {
	ExpectedRowVersion int64
	PoolIDs            []uuid.UUID
}

// UsageUpdate 是改用量：写进草稿版本（没有就从当前版本复制一个），不发布。
type UsageUpdate struct {
	ExpectedRowVersion int64
	Usage              UsageInput
}

// Use 是下单快照的用途，决定按哪套规则判断能不能买。
type Use string

// 下单快照的用途。
const (
	// UseNew 是新购：套餐在售、对这个用户可见、接受新购。
	UseNew Use = "new"
	// UseRenewal 是续费：只许订阅套餐、没归档、允许续费；价格的任何问题都报 ErrRenewPriceGone。
	UseRenewal Use = "renewal"
	// UseChange 是换套餐的目标（可跨类型）：套餐在售、可见、允许变更。
	UseChange Use = "change"
	// UseStack 是一次性套餐再买同一个叠加：套餐在售、可见、接受新购、是一次性套餐。
	UseStack Use = "stack"
)

// OfferRequest 是取下单快照的条件。
type OfferRequest struct {
	Use Use
	// UserID 是买的人；为 nil 只用于匿名浏览目录。
	UserID *uuid.UUID
	// Manual 为真是后台人工开单：不看可见性与用户组。
	Manual bool
	// PlanID 是期望的套餐；非 nil 时 Offer 校验价格属于它（续费、叠加传订阅的套餐，防止拿别的套餐的价格）。
	PlanID *uuid.UUID
	// Lock 为真时套餐行与价格行 FOR SHARE，锁到事务结束（建单时用）：与后台改目录互斥。
	Lock bool
	// Now 是调用方的业务时刻；目录本身不再按时间判断（没有上架与价格时间窗），可不填。
	Now time.Time
}

// Offer 是按价格取的下单快照：价格、所属套餐、套餐的当前发布版本（含权益与配额）。
type Offer struct {
	Plan    Plan
	Version Version
	Price   Price
}

// OfferPlan 是一个可买的套餐：当前发布版本与这次能用的价格（按周期从短到长）。
type OfferPlan struct {
	Plan    Plan
	Version Version
	Prices  []Price
}
