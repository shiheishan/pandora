// Package model 是 catalog 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00008_catalog.sql。
//
// jsonb 列一律用 string 承载 JSON 文本（卖点数组、权益值），由 service 编解码。
package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Plan 是 plans 表的一行。
type Plan struct {
	ID                   uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code                 string     `gorm:"column:code"`
	Name                 string     `gorm:"column:name"`
	Description          *string    `gorm:"column:description"`
	Status               string     `gorm:"column:status"`
	CurrentVersionID     *uuid.UUID `gorm:"column:current_version_id;type:uuid"`
	Visibility           string     `gorm:"column:visibility"`
	VisibleFrom          *time.Time `gorm:"column:visible_from"`
	VisibleUntil         *time.Time `gorm:"column:visible_until"`
	AllowNewPurchase     bool       `gorm:"column:allow_new_purchase"`
	AllowRenewal         bool       `gorm:"column:allow_renewal"`
	AllowUpgrade         bool       `gorm:"column:allow_upgrade"`
	PurchaseLimitPerUser *int       `gorm:"column:purchase_limit_per_user"`
	StockTotal           *int       `gorm:"column:stock_total"`
	StockReserved        int        `gorm:"column:stock_reserved"`
	StockSold            int        `gorm:"column:stock_sold"`
	Highlights           string     `gorm:"column:highlights;type:jsonb"`
	Recommended          bool       `gorm:"column:recommended"`
	SortOrder            int        `gorm:"column:sort_order"`
	RowVersion           int64      `gorm:"column:row_version;default:1"`
	CreatedAt            time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt            time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Plan) TableName() string { return "plans" }

// PlanVisibleGroup 是 plan_visible_groups 表的一行。
type PlanVisibleGroup struct {
	PlanID      uuid.UUID `gorm:"column:plan_id;type:uuid;primaryKey"`
	UserGroupID uuid.UUID `gorm:"column:user_group_id;type:uuid;primaryKey"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (PlanVisibleGroup) TableName() string { return "plan_visible_groups" }

// PlanVersion 是 plan_versions 表的一行。
type PlanVersion struct {
	ID                   uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	PlanID               uuid.UUID  `gorm:"column:plan_id;type:uuid"`
	Version              int        `gorm:"column:version"`
	Status               string     `gorm:"column:status"`
	FrozenAt             *time.Time `gorm:"column:frozen_at"`
	QuotaResetStrategy   string     `gorm:"column:quota_reset_strategy"`
	QuotaResetDay        *int       `gorm:"column:quota_reset_day"`
	GracePeriodHours     int        `gorm:"column:grace_period_hours"`
	GraceKeepsService    bool       `gorm:"column:grace_keeps_service"`
	RenewalExtendsPeriod bool       `gorm:"column:renewal_extends_period"`
	RenewalResetsQuota   bool       `gorm:"column:renewal_resets_quota"`
	RenewalKeepsAddons   bool       `gorm:"column:renewal_keeps_addons"`
	MaxDevices           *int       `gorm:"column:max_devices"`
	MaxConcurrent        *int       `gorm:"column:max_concurrent"`
	DeviceReleaseHours   int        `gorm:"column:device_release_hours"`
	ThrottleKbps         *int       `gorm:"column:throttle_kbps"`
	Notes                *string    `gorm:"column:notes"`
	RowVersion           int64      `gorm:"column:row_version;default:1"`
	CreatedBy            *uuid.UUID `gorm:"column:created_by;type:uuid"`
	CreatedAt            time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (PlanVersion) TableName() string { return "plan_versions" }

// Entitlement 是 entitlements 表的一行；Value 是 JSON 文本。
type Entitlement struct {
	ID            uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	PlanVersionID uuid.UUID `gorm:"column:plan_version_id;type:uuid"`
	Code          string    `gorm:"column:code"`
	Value         string    `gorm:"column:value;type:jsonb"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Entitlement) TableName() string { return "entitlements" }

// QuotaDefinition 是 quota_definitions 表的一行；LimitValue 为空表示不限。
type QuotaDefinition struct {
	ID            uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	PlanVersionID uuid.UUID `gorm:"column:plan_version_id;type:uuid"`
	Metric        string    `gorm:"column:metric"`
	LimitValue    *int64    `gorm:"column:limit_value"`
	Unit          string    `gorm:"column:unit"`
	Period        string    `gorm:"column:period"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (QuotaDefinition) TableName() string { return "quota_definitions" }

// Price 是 prices 表的一行；UnitAmount 单位元、2 位小数。
type Price struct {
	ID              uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	PlanID          uuid.UUID       `gorm:"column:plan_id;type:uuid"`
	Currency        string          `gorm:"column:currency"`
	UnitAmount      decimal.Decimal `gorm:"column:unit_amount;type:numeric(20,2)"`
	BillingInterval string          `gorm:"column:billing_interval"`
	IntervalCount   int             `gorm:"column:interval_count"`
	TrialDays       int             `gorm:"column:trial_days"`
	Status          string          `gorm:"column:status"`
	UserGroupID     *uuid.UUID      `gorm:"column:user_group_id;type:uuid"`
	ValidFrom       *time.Time      `gorm:"column:valid_from"`
	ValidUntil      *time.Time      `gorm:"column:valid_until"`
	RowVersion      int64           `gorm:"column:row_version;default:1"`
	CreatedAt       time.Time       `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Price) TableName() string { return "prices" }

// TrafficPack 是 traffic_packs 表的一行；UnitAmount 单位元、2 位小数。
type TrafficPack struct {
	ID           uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Name         string          `gorm:"column:name"`
	TrafficBytes int64           `gorm:"column:traffic_bytes"`
	Currency     string          `gorm:"column:currency"`
	UnitAmount   decimal.Decimal `gorm:"column:unit_amount;type:numeric(20,2)"`
	Recommended  bool            `gorm:"column:recommended"`
	Status       string          `gorm:"column:status"`
	SortOrder    int             `gorm:"column:sort_order"`
	CreatedAt    time.Time       `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (TrafficPack) TableName() string { return "traffic_packs" }

// PlanResetPrice 是 plan_reset_prices 表的一行（00043）：套餐按币种的流量重置价格；UnitAmount 单位元、2 位小数。
type PlanResetPrice struct {
	PlanID     uuid.UUID       `gorm:"column:plan_id;type:uuid;primaryKey"`
	Currency   string          `gorm:"column:currency;primaryKey"`
	UnitAmount decimal.Decimal `gorm:"column:unit_amount;type:numeric(20,2)"`
	CreatedAt  time.Time       `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (PlanResetPrice) TableName() string { return "plan_reset_prices" }
