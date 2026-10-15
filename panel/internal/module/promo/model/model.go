// Package model 是 promo 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00010_promo.sql 与 00043_xb_order.sql。
package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 占用状态：repo 写库时用；service 用 service/types.go 里同值的常量。
const (
	RedemptionHeld     = "held"
	RedemptionCaptured = "captured"
	RedemptionReleased = "released"
)

// Coupon 是 coupons 表的一行。
type Coupon struct {
	ID                    uuid.UUID        `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code                  string           `gorm:"column:code"`
	Name                  string           `gorm:"column:name"`
	AmountOff             *decimal.Decimal `gorm:"column:amount_off"`
	PercentOffBP          *int64           `gorm:"column:percent_off_bp"`
	Currency              string           `gorm:"column:currency"`
	MaxDiscount           *decimal.Decimal `gorm:"column:max_discount"`
	MinOrderAmount        decimal.Decimal  `gorm:"column:min_order_amount"`
	MaxRedemptions        *int             `gorm:"column:max_redemptions"`
	MaxRedemptionsPerUser int              `gorm:"column:max_redemptions_per_user"`
	RedeemedCount         int              `gorm:"column:redeemed_count"`
	ReservedCount         int              `gorm:"column:reserved_count"`
	UserGroupID           *uuid.UUID       `gorm:"column:user_group_id;type:uuid"`
	ValidFrom             *time.Time       `gorm:"column:valid_from"`
	ValidUntil            *time.Time       `gorm:"column:valid_until"`
	Status                string           `gorm:"column:status"`
	CreatedBy             *uuid.UUID       `gorm:"column:created_by;type:uuid"`
	CreatedAt             time.Time        `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt             time.Time        `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Coupon) TableName() string { return "coupons" }

// CouponPlan 是 coupon_plans 表的一行：券限定的一个套餐。
type CouponPlan struct {
	CouponID uuid.UUID `gorm:"column:coupon_id;type:uuid;primaryKey"`
	PlanID   uuid.UUID `gorm:"column:plan_id;type:uuid;primaryKey"`
}

// TableName 固定表名。
func (CouponPlan) TableName() string { return "coupon_plans" }

// CouponInterval 是 coupon_intervals 表的一行（00043）：券限定的一个计费周期（价格档的 billing_interval）。
type CouponInterval struct {
	CouponID        uuid.UUID `gorm:"column:coupon_id;type:uuid;primaryKey"`
	BillingInterval string    `gorm:"column:billing_interval;primaryKey"`
}

// TableName 固定表名。
func (CouponInterval) TableName() string { return "coupon_intervals" }

// MemberDiscount 是 member_discounts 表的一行（00043）：单个用户的会员折扣，Percent 是减免百分比 1–100。
type MemberDiscount struct {
	UserID    uuid.UUID  `gorm:"column:user_id;type:uuid;primaryKey"`
	Percent   int        `gorm:"column:percent"`
	UpdatedBy *uuid.UUID `gorm:"column:updated_by;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (MemberDiscount) TableName() string { return "member_discounts" }

// Redemption 是 coupon_redemptions 表的一行。
type Redemption struct {
	ID             uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	CouponID       uuid.UUID       `gorm:"column:coupon_id;type:uuid"`
	UserID         uuid.UUID       `gorm:"column:user_id;type:uuid"`
	OrderID        uuid.UUID       `gorm:"column:order_id;type:uuid"`
	OrderNo        string          `gorm:"column:order_no"`
	DiscountAmount decimal.Decimal `gorm:"column:discount_amount"`
	Currency       string          `gorm:"column:currency"`
	Status         string          `gorm:"column:status"`
	ReleaseReason  *string         `gorm:"column:release_reason"`
	CapturedAt     *time.Time      `gorm:"column:captured_at"`
	ReleasedAt     *time.Time      `gorm:"column:released_at"`
	CreatedAt      time.Time       `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Redemption) TableName() string { return "coupon_redemptions" }
