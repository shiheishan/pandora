// Package model 是 commission 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00014_commission.sql、00016_commission_review.sql 与 00041_commission_levels.sql。
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
	Level            int             `gorm:"column:level"`         // 第几级邀请人 1–3
	SharePercent     int             `gorm:"column:share_percent"` // 这一级分到佣金总额的百分比 1–100
	CommissionAmount decimal.Decimal `gorm:"column:commission_amount"`
	Status           string          `gorm:"column:status"`
	FrozenUntil      time.Time       `gorm:"column:frozen_until"`
	ReviewRequired   bool            `gorm:"column:review_required"`
	ReviewReason     *string         `gorm:"column:review_reason"`
	AccrualTxnID     uuid.UUID       `gorm:"column:accrual_txn_id;type:uuid"`
	SettleTxnID      *uuid.UUID      `gorm:"column:settle_txn_id;type:uuid"`
	SettledAt        *time.Time      `gorm:"column:settled_at"`
	AutoTransferDue  bool            `gorm:"column:auto_transfer_due"` // 提现关闭时解冻、等自动转入余额
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
	ID              uuid.UUID        `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	UserID          uuid.UUID        `gorm:"column:user_id;type:uuid"`
	Currency        string           `gorm:"column:currency"`
	Amount          decimal.Decimal  `gorm:"column:amount"`
	PayoutSealed    []byte           `gorm:"column:payout_sealed"`
	Method          string           `gorm:"column:method"` // 提现方式，见 settings 的提现方式目录；other 是迁移前的自由填写
	Status          string           `gorm:"column:status"`
	RejectReason    *string          `gorm:"column:reject_reason"`
	PayoutReference *string          `gorm:"column:payout_reference"`
	PaidUSDTAmount  *decimal.Decimal `gorm:"column:paid_usdt_amount"` // USDT 实付数量，仅记录
	PayoutTxnID     *uuid.UUID       `gorm:"column:payout_txn_id;type:uuid"`
	ReviewedBy      *uuid.UUID       `gorm:"column:reviewed_by;type:uuid"`
	ReviewedAt      *time.Time       `gorm:"column:reviewed_at"`
	PaidBy          *uuid.UUID       `gorm:"column:paid_by;type:uuid"`
	RequestedAt     time.Time        `gorm:"column:requested_at;autoCreateTime"`
	CompletedAt     *time.Time       `gorm:"column:completed_at"`
	UpdatedAt       time.Time        `gorm:"column:updated_at;autoUpdateTime"`
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
	EntryID     *uuid.UUID      `gorm:"column:entry_id;type:uuid"` // 自动转余额对应的佣金条目；手动转为 nil
	CreatedAt   time.Time       `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Transfer) TableName() string { return "commission_transfers" }

// Override 是 commission_user_overrides 表的一行：按用户单独设的佣金比例与计佣范围（nil 跟全局走）。
type Override struct {
	UserID      uuid.UUID  `gorm:"column:user_id;type:uuid;primaryKey"`
	RatePercent *int       `gorm:"column:rate_percent"`
	Scope       *string    `gorm:"column:scope"`
	UpdatedBy   *uuid.UUID `gorm:"column:updated_by;type:uuid"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Override) TableName() string { return "commission_user_overrides" }
