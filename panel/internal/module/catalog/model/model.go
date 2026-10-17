// Package model 是 catalog 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00008_catalog.sql 与 00056_catalog_two_kinds.sql。
//
// jsonb 列一律用 string 承载 JSON 文本（权益值），由 service 编解码。
package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Plan 是 plans 表的一行。
type Plan struct {
	ID               uuid.UUID        `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code             string           `gorm:"column:code"`
	Name             string           `gorm:"column:name"`
	Description      *string          `gorm:"column:description"`
	Kind             string           `gorm:"column:kind"`
	Status           string           `gorm:"column:status"`
	CurrentVersionID *uuid.UUID       `gorm:"column:current_version_id;type:uuid"`
	Visibility       string           `gorm:"column:visibility"`
	AllowNewPurchase bool             `gorm:"column:allow_new_purchase"`
	AllowRenewal     bool             `gorm:"column:allow_renewal"`
	AllowUpgrade     bool             `gorm:"column:allow_upgrade"`
	Recommended      bool             `gorm:"column:recommended"`
	ResetPrice       *decimal.Decimal `gorm:"column:reset_price;type:numeric(20,2)"`
	ResetLimit       *int             `gorm:"column:reset_limit"`
	RowVersion       int64            `gorm:"column:row_version;default:1"`
	CreatedAt        time.Time        `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt        time.Time        `gorm:"column:updated_at;autoUpdateTime"`
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
	ID           uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	PlanID       uuid.UUID  `gorm:"column:plan_id;type:uuid"`
	Version      int        `gorm:"column:version"`
	Status       string     `gorm:"column:status"`
	FrozenAt     *time.Time `gorm:"column:frozen_at"`
	MaxDevices   *int       `gorm:"column:max_devices"`
	ThrottleKbps *int       `gorm:"column:throttle_kbps"`
	RowVersion   int64      `gorm:"column:row_version;default:1"`
	CreatedBy    *uuid.UUID `gorm:"column:created_by;type:uuid"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
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

// Price 是 prices 表的一行；UnitAmount 单位元、2 位小数。PlanKind 随套餐（复合外键），ValidityDays 只有一次性价有。
type Price struct {
	ID              uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	PlanID          uuid.UUID       `gorm:"column:plan_id;type:uuid"`
	PlanKind        string          `gorm:"column:plan_kind"`
	UnitAmount      decimal.Decimal `gorm:"column:unit_amount;type:numeric(20,2)"`
	BillingInterval string          `gorm:"column:billing_interval"`
	IntervalCount   int             `gorm:"column:interval_count"`
	ValidityDays    *int            `gorm:"column:validity_days"`
	Status          string          `gorm:"column:status"`
	RowVersion      int64           `gorm:"column:row_version;default:1"`
	CreatedAt       time.Time       `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Price) TableName() string { return "prices" }

// PlanNodePool 是 plan_node_pools 表的一行（00056 起按套餐绑定）。
type PlanNodePool struct {
	PlanID    uuid.UUID `gorm:"column:plan_id;type:uuid;primaryKey"`
	PoolID    uuid.UUID `gorm:"column:pool_id;type:uuid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (PlanNodePool) TableName() string { return "plan_node_pools" }
