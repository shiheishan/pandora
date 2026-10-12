// Package model 是 commission 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00014_commission.sql 与 00016_commission_review.sql。
package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Entry 是 commission_entries 表的一行。
type Entry struct {
	ID               uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	ReferrerUserID   uuid.UUID       `gorm:"column:referrer_user_id;type:uuid"`
	RefereeUserID    uuid.UUID       `gorm:"column:referee_user_id;type:uuid"`
	OrderID          uuid.UUID       `gorm:"column:order_id;type:uuid"`
	OrderNo          string          `gorm:"column:order_no"`
	PaymentID        *uuid.UUID      `gorm:"column:payment_id;type:uuid"`
	Currency         string          `gorm:"column:currency"`
	BaseAmount       decimal.Decimal `gorm:"column:base_amount"`
	RateBP           int64           `gorm:"column:rate_bp"`
	CommissionAmount decimal.Decimal `gorm:"column:commission_amount"`
	Status           string          `gorm:"column:status"`
	FrozenUntil      time.Time       `gorm:"column:frozen_until"`
	ReviewRequired   bool            `gorm:"column:review_required"`
	ReviewReason     *string         `gorm:"column:review_reason"`
	AccrualTxnID     uuid.UUID       `gorm:"column:accrual_txn_id;type:uuid"`
	SettleTxnID      *uuid.UUID      `gorm:"column:settle_txn_id;type:uuid"`
	SettledAt        *time.Time      `gorm:"column:settled_at"`
	ReviewedBy       *uuid.UUID      `gorm:"column:reviewed_by;type:uuid"`
	ReviewedAt       *time.Time      `gorm:"column:reviewed_at"`
	VoidReason       *string         `gorm:"column:void_reason"`
	VoidTxnID        *uuid.UUID      `gorm:"column:void_txn_id;type:uuid"`
	CreatedAt        time.Time       `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt        time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Entry) TableName() string { return "commission_entries" }

// Withdrawal 是 withdrawals 表的一行。PayoutSealed 是封存后的收款信息。
type Withdrawal struct {
	ID              uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	UserID          uuid.UUID       `gorm:"column:user_id;type:uuid"`
	Currency        string          `gorm:"column:currency"`
	Amount          decimal.Decimal `gorm:"column:amount"`
	PayoutSealed    []byte          `gorm:"column:payout_sealed"`
	Status          string          `gorm:"column:status"`
	RejectReason    *string         `gorm:"column:reject_reason"`
	PayoutReference *string         `gorm:"column:payout_reference"`
	PayoutTxnID     *uuid.UUID      `gorm:"column:payout_txn_id;type:uuid"`
	ReviewedBy      *uuid.UUID      `gorm:"column:reviewed_by;type:uuid"`
	ReviewedAt      *time.Time      `gorm:"column:reviewed_at"`
	PaidBy          *uuid.UUID      `gorm:"column:paid_by;type:uuid"`
	RequestedAt     time.Time       `gorm:"column:requested_at;autoCreateTime"`
	CompletedAt     *time.Time      `gorm:"column:completed_at"`
	UpdatedAt       time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Withdrawal) TableName() string { return "withdrawals" }

// Transfer 是 commission_transfers 表的一行（只追加）。
type Transfer struct {
	ID          uuid.UUID       `gorm:"column:id;type:uuid;primaryKey"`
	UserID      uuid.UUID       `gorm:"column:user_id;type:uuid"`
	Currency    string          `gorm:"column:currency"`
	Amount      decimal.Decimal `gorm:"column:amount"`
	LedgerTxnID uuid.UUID       `gorm:"column:ledger_txn_id;type:uuid"`
	CreatedAt   time.Time       `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Transfer) TableName() string { return "commission_transfers" }
