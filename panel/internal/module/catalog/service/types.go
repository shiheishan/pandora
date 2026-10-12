package service

import (
	"encoding/json"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 套餐状态：draft → active（发布）→ archived；draft 也可直接归档。归档不可恢复。
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

// 流量包状态：active ↔ archived（下架后可以重新上架）。
const (
	PackActive   = "active"
	PackArchived = "archived"
)

// 套餐可见性。invite_only 可以保存，但邀请购买没有实现，不许发布。
const (
	VisibilityPublic        = "public"
	VisibilityAuthenticated = "authenticated"
	VisibilityGroup         = "group"
	VisibilityInviteOnly    = "invite_only"
	VisibilityHidden        = "hidden"
)

// 配额周期。
const (
	PeriodTotal = "total"
	PeriodCycle = "cycle"
	PeriodDay   = "day"
	PeriodMonth = "month"
)

// 流量重置策略。
const (
	ResetNever        = "never"
	ResetNaturalMonth = "natural_month"
	ResetBillingCycle = "billing_cycle"
	ResetFixedDay     = "fixed_day"
)

// 常用指标与换算。
const (
	MetricTraffic = "traffic.bytes"
	MetricDevices = "devices.active"
	// BytesPerGB 是向导里 1 GB 的字节数（1<<30，与前端同口径）。
	BytesPerGB int64 = 1 << 30
	// OveragePolicySuspend 是唯一的超额策略：流量用完且无流量包余额后停止服务。库里不存，读出一律是它。
	OveragePolicySuspend = "suspend"
)

// Plan 是套餐本身（不含版本与价格）。
type Plan struct {
	ID               uuid.UUID
	Code             string
	Name             string
	Description      *string
	Status           string
	CurrentVersionID *uuid.UUID
	Visibility       string
	// VisibleGroupIDs 只在 visibility = group 时非空，按 id 升序；永不为 nil。
	VisibleGroupIDs      []uuid.UUID
	VisibleFrom          *time.Time
	VisibleUntil         *time.Time
	AllowNewPurchase     bool
	AllowRenewal         bool
	AllowUpgrade         bool
	PurchaseLimitPerUser *int
	// StockTotal 为 nil 表示不限量。
	StockTotal    *int
	StockReserved int
	StockSold     int
	// Highlights 是卖点，按给定顺序；永不为 nil。
	Highlights  []string
	Recommended bool
	SortOrder   int
	RowVersion  int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
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

// Version 是套餐版本与它的权益、配额。
type Version struct {
	ID                   uuid.UUID
	PlanID               uuid.UUID
	Version              int
	Status               string
	FrozenAt             *time.Time
	QuotaResetStrategy   string
	QuotaResetDay        *int
	GracePeriodHours     int
	GraceKeepsService    bool
	RenewalExtendsPeriod bool
	RenewalResetsQuota   bool
	RenewalKeepsAddons   bool
	MaxDevices           *int
	MaxConcurrent        *int
	DeviceReleaseHours   int
	ThrottleKbps         *int
	Notes                *string
	RowVersion           int64
	CreatedBy            *uuid.UUID
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

// Price 是一档价格。UnitAmount 单位元、2 位小数。
type Price struct {
	ID              uuid.UUID
	PlanID          uuid.UUID
	Currency        money.Currency
	UnitAmount      decimal.Decimal
	BillingInterval string
	IntervalCount   int
	TrialDays       int
	Status          string
	UserGroupID     *uuid.UUID
	ValidFrom       *time.Time
	ValidUntil      *time.Time
	RowVersion      int64
	CreatedAt       time.Time
}

// PlanDetail 是后台套餐详情：版本按 version 降序，价格按 created_at 降序（含已归档）。
type PlanDetail struct {
	Plan
	Versions []Version
	Prices   []Price
}

// PlanSummary 是后台套餐列表的一行：当前版本的摘要与全部价格（按状态、金额升序）。
type PlanSummary struct {
	Plan
	DraftVersionID *uuid.UUID
	// Version、MaxDevices、TrafficLimit 取当前版本；还没发布过时都为 nil。
	Version      *int
	MaxDevices   *int
	TrafficLimit *int64
	Prices       []Price
}

// TrafficPack 是一个流量包。UnitAmount 单位元、2 位小数；UpdatedAt 是乐观锁。
type TrafficPack struct {
	ID           uuid.UUID
	Name         string
	TrafficBytes int64
	Currency     money.Currency
	UnitAmount   decimal.Decimal
	Recommended  bool
	Status       string
	SortOrder    int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PlanInput 是新建或整体改写套餐资料的输入（改写时客户端回填全部字段）。
type PlanInput struct {
	Code             string
	Name             string
	Description      *string
	Visibility       string
	VisibleGroupIDs  []uuid.UUID
	VisibleFrom      *time.Time
	VisibleUntil     *time.Time
	AllowNewPurchase bool
	AllowRenewal     bool
	AllowUpgrade     bool
	// PurchaseLimitPerUser 为 nil 表示不限购。
	PurchaseLimitPerUser *int
	// StockTotal 为 nil 表示不限量。
	StockTotal  *int
	SortOrder   int
	Highlights  []string
	Recommended bool
}

// VersionInput 是草稿版本语义的全量覆盖。
type VersionInput struct {
	QuotaResetStrategy   string
	QuotaResetDay        *int
	GracePeriodHours     int
	GraceKeepsService    bool
	RenewalExtendsPeriod bool
	RenewalResetsQuota   bool
	RenewalKeepsAddons   bool
	MaxDevices           *int
	MaxConcurrent        *int
	DeviceReleaseHours   int
	// OveragePolicy 只收空串或 suspend。
	OveragePolicy string
	ThrottleKbps  *int
	Notes         *string
	Entitlements  []Entitlement
	Quotas        []Quota
}

// PriceInput 是新建一档价格的输入。
type PriceInput struct {
	Currency        string
	UnitAmount      decimal.Decimal
	BillingInterval string
	IntervalCount   int
	TrialDays       int
	UserGroupID     *uuid.UUID
	ValidFrom       *time.Time
	ValidUntil      *time.Time
}

// PackInput 是新建或改写流量包的输入。
type PackInput struct {
	Name         string
	TrafficBytes int64
	Currency     string
	UnitAmount   decimal.Decimal
	Recommended  bool
	SortOrder    int
}

// WizardPrice 是向导里的一档公开价（不绑用户组、不限时间窗）。
type WizardPrice struct {
	BillingInterval string
	IntervalCount   int
	UnitAmount      decimal.Decimal
	Currency        string
	TrialDays       int
}

// WizardCreate 是「一次建成」向导的输入。Plan 里三个 Allow* 已由调用方把缺省落成 true。
// 线路（节点池）绑定在 P5 与 node 模块一起做，这里不收。
type WizardCreate struct {
	Plan PlanInput
	// TrafficGB 为 nil 或 0 表示不限流量。
	TrafficGB *int64
	// MaxDevices 为 nil 或 0 表示不限设备数。
	MaxDevices *int
	// ThrottleKbps 为 nil 表示不限速。
	ThrottleKbps       *int
	QuotaResetStrategy string
	QuotaResetDay      *int
	Prices             []WizardPrice
	Publish            bool
}

// WizardCreated 是向导建成的结果。
type WizardCreated struct {
	Plan      PlanDetail
	VersionID uuid.UUID
	PriceIDs  []uuid.UUID
	Published bool
}

// OptionalInt 区分「字段缺省」（Set 为假，不动）与「显式 null」（Set 为真、Value 为 nil，改成不限）。
type OptionalInt struct {
	Set   bool
	Value *int
}

// WizardUpdate 是「一次改完」向导的输入。指针字段为 nil 表示这次不动。
type WizardUpdate struct {
	ExpectedRowVersion   int64
	Code                 string
	Name                 string
	Description          *string
	Visibility           string
	VisibleGroupIDs      []uuid.UUID
	SortOrder            int
	AllowNewPurchase     *bool
	AllowRenewal         *bool
	AllowUpgrade         *bool
	PurchaseLimitPerUser *int
	StockTotal           *int
	Highlights           *[]string
	Recommended          *bool
	// TrafficGB 为 nil 表示不动，0 表示改成不限。
	TrafficGB    *int64
	MaxDevices   OptionalInt
	ThrottleKbps OptionalInt
	// Prices 为 nil 或空表示不动；否则只同步清单里出现的币种的公开价。
	Prices *[]WizardPrice
}

// WizardUpdated 是向导改完的结果；Changed 逐条说明改了什么、什么时候生效（界面直接显示）。
type WizardUpdated struct {
	Plan    PlanDetail
	Changed []string
}

// Use 是下单快照的用途，决定按哪套规则判断能不能买。
type Use string

// 下单快照的用途。
const (
	// UseNew 是新购：套餐在售、对这个用户可见、在上架窗口内、接受新购。
	UseNew Use = "new"
	// UseRenewal 是续费：套餐允许续费；价格的任何问题都报 ErrRenewPriceGone。
	UseRenewal Use = "renewal"
	// UseChange 是换套餐的目标：套餐在售、可见、允许变更。
	UseChange Use = "change"
)

// OfferRequest 是取下单快照的条件。
type OfferRequest struct {
	Use Use
	// UserID 是买的人；为 nil 只用于匿名浏览目录。
	UserID *uuid.UUID
	// Manual 为真是后台人工开单：不看可见性、上架窗口与用户组。
	Manual bool
	// Lock 为真时把套餐行与价格行 FOR SHARE 锁到事务结束（建单时用，与后台改目录互斥）。
	Lock bool
	// Now 为零值时取当前时间。
	Now time.Time
}

// Offer 是按价格取的下单快照：价格、所属套餐、套餐的当前发布版本（含权益与配额）。
type Offer struct {
	Plan    Plan
	Version Version
	Price   Price
}

// OfferPlan 是一个可买的套餐：当前发布版本与这次能用的价格（按金额、周期升序）。
type OfferPlan struct {
	Plan    Plan
	Version Version
	Prices  []Price
}
