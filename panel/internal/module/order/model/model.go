// Package model 是 order 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00013_order.sql。
// jsonb 列（订单行的权益、配额快照）用 string 承载 JSON 数组文本，由 service 编解码。
package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Order 是 orders 表的一行。
type Order struct {
	ID             uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	OrderNo        string          `gorm:"column:order_no"`
	UserID         uuid.UUID       `gorm:"column:user_id;type:uuid"`
	Kind           string          `gorm:"column:kind"`
	Status         string          `gorm:"column:status"`
	Currency       string          `gorm:"column:currency"`
	SubtotalAmount decimal.Decimal `gorm:"column:subtotal_amount"`
	DiscountAmount decimal.Decimal `gorm:"column:discount_amount"`
	// MemberDiscountAmount 是 DiscountAmount 里属于会员折扣的部分（00046；券的部分 = DiscountAmount − 它）
	MemberDiscountAmount  decimal.Decimal `gorm:"column:member_discount_amount"`
	ProrationCreditAmount decimal.Decimal `gorm:"column:proration_credit_amount"`
	WaivedAmount          decimal.Decimal `gorm:"column:waived_amount"`
	TotalAmount           decimal.Decimal `gorm:"column:total_amount"`
	BalanceApplied        decimal.Decimal `gorm:"column:balance_applied"`
	PayableAmount         decimal.Decimal `gorm:"column:payable_amount"`
	PaidAmount            decimal.Decimal `gorm:"column:paid_amount"`
	CouponID              *uuid.UUID      `gorm:"column:coupon_id;type:uuid"`
	CreatedBy             *uuid.UUID      `gorm:"column:created_by;type:uuid"`
	ManualReason          *string         `gorm:"column:manual_reason"`
	SubscriptionID        *uuid.UUID      `gorm:"column:subscription_id;type:uuid"`
	SubscriptionLabel     *string         `gorm:"column:subscription_label"`
	StateVersion          int64           `gorm:"column:state_version;default:1"`
	ExpiresAt             time.Time       `gorm:"column:expires_at"`
	PaidAt                *time.Time      `gorm:"column:paid_at"`
	FulfilledAt           *time.Time      `gorm:"column:fulfilled_at"`
	CancelledAt           *time.Time      `gorm:"column:cancelled_at"`
	ExpiredAt             *time.Time      `gorm:"column:expired_at"`
	CancelReason          *string         `gorm:"column:cancel_reason"`
	CreatedAt             time.Time       `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt             time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Order) TableName() string { return "orders" }

// Item 是 order_items 表的一行（只插不改）。
type Item struct {
	ID                    uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	OrderID               uuid.UUID       `gorm:"column:order_id;type:uuid"`
	PriceID               *uuid.UUID      `gorm:"column:price_id;type:uuid"`
	PlanID                *uuid.UUID      `gorm:"column:plan_id;type:uuid"`
	PlanVersionID         *uuid.UUID      `gorm:"column:plan_version_id;type:uuid"`
	TrafficPackID         *uuid.UUID      `gorm:"column:traffic_pack_id;type:uuid"`
	SnapshotName          string          `gorm:"column:snapshot_name"`
	SnapshotPlanVersion   *int            `gorm:"column:snapshot_plan_version"`
	SnapshotInterval      *string         `gorm:"column:snapshot_interval"`
	SnapshotIntervalCount *int            `gorm:"column:snapshot_interval_count"`
	SnapshotTrafficBytes  *int64          `gorm:"column:snapshot_traffic_bytes"`
	SnapshotEntitlements  string          `gorm:"column:snapshot_entitlements;type:jsonb"`
	SnapshotQuotas        string          `gorm:"column:snapshot_quotas;type:jsonb"`
	Quantity              int             `gorm:"column:quantity"`
	UnitAmount            decimal.Decimal `gorm:"column:unit_amount"`
	LineAmount            decimal.Decimal `gorm:"column:line_amount"`
	Currency              string          `gorm:"column:currency"`
	CreatedAt             time.Time       `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Item) TableName() string { return "order_items" }

// Reservation 是 order_reservations 表的一行：每单一行。
type Reservation struct {
	ID            uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	OrderID       uuid.UUID  `gorm:"column:order_id;type:uuid"`
	UserID        uuid.UUID  `gorm:"column:user_id;type:uuid"`
	State         string     `gorm:"column:state"`
	ExpiresAt     time.Time  `gorm:"column:expires_at"`
	HeldAt        time.Time  `gorm:"column:held_at"`
	CapturedAt    *time.Time `gorm:"column:captured_at"`
	ReleasedAt    *time.Time `gorm:"column:released_at"`
	ReleaseReason *string    `gorm:"column:release_reason"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Reservation) TableName() string { return "order_reservations" }

// ReservationEvent 是 order_reservation_events 表的一行（只追加）。
type ReservationEvent struct {
	ID            uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	ReservationID uuid.UUID  `gorm:"column:reservation_id;type:uuid"`
	OrderID       uuid.UUID  `gorm:"column:order_id;type:uuid"`
	FromState     *string    `gorm:"column:from_state"`
	ToState       string     `gorm:"column:to_state"`
	EventKind     string     `gorm:"column:event_kind"`
	ActorKind     string     `gorm:"column:actor_kind"`
	ActorID       *uuid.UUID `gorm:"column:actor_id;type:uuid"`
	Reason        *string    `gorm:"column:reason"`
	OccurredAt    time.Time  `gorm:"column:occurred_at;autoCreateTime"`
}

// TableName 固定表名。
func (ReservationEvent) TableName() string { return "order_reservation_events" }

// StockReservation 是 order_stock_reservations 表的一行（只追加）。
type StockReservation struct {
	ID            uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	ReservationID uuid.UUID `gorm:"column:reservation_id;type:uuid"`
	OrderID       uuid.UUID `gorm:"column:order_id;type:uuid"`
	PlanID        uuid.UUID `gorm:"column:plan_id;type:uuid"`
	Quantity      int       `gorm:"column:quantity"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (StockReservation) TableName() string { return "order_stock_reservations" }

// LimitReservation 是 order_purchase_limit_reservations 表的一行（只追加）。
type LimitReservation struct {
	ID            uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	ReservationID uuid.UUID `gorm:"column:reservation_id;type:uuid"`
	OrderID       uuid.UUID `gorm:"column:order_id;type:uuid"`
	PlanID        uuid.UUID `gorm:"column:plan_id;type:uuid"`
	UserID        uuid.UUID `gorm:"column:user_id;type:uuid"`
	Quantity      int       `gorm:"column:quantity"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (LimitReservation) TableName() string { return "order_purchase_limit_reservations" }

// PurchaseCounter 是 plan_purchase_counters 表的一行。
type PurchaseCounter struct {
	PlanID    uuid.UUID `gorm:"column:plan_id;type:uuid;primaryKey"`
	UserID    uuid.UUID `gorm:"column:user_id;type:uuid;primaryKey"`
	Purchased int       `gorm:"column:purchased"`
	Reserved  int       `gorm:"column:reserved"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (PurchaseCounter) TableName() string { return "plan_purchase_counters" }
