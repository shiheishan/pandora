// Package model 是 ledger 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00009_ledger.sql。
package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Account 是 ledger_accounts 表的一行。
type Account struct {
	ID          uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	AccountType string          `gorm:"column:account_type"`
	Currency    string          `gorm:"column:currency"`
	OwnerUserID *uuid.UUID      `gorm:"column:owner_user_id;type:uuid"`
	OwnerRef    string          `gorm:"column:owner_ref"`
	Balance     decimal.Decimal `gorm:"column:balance;type:numeric(20,2)"`
	CreatedAt   time.Time       `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Account) TableName() string { return "ledger_accounts" }

// Transaction 是 ledger_transactions 表的一行。
type Transaction struct {
	ID         uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Kind       string     `gorm:"column:kind"`
	Currency   string     `gorm:"column:currency"`
	SourceType *string    `gorm:"column:source_type"`
	SourceID   *uuid.UUID `gorm:"column:source_id;type:uuid"`
	Memo       string     `gorm:"column:memo"`
	ActorKind  string     `gorm:"column:actor_kind"`
	ActorID    *uuid.UUID `gorm:"column:actor_id;type:uuid"`
	OccurredAt time.Time  `gorm:"column:occurred_at;default:now()"`
}

// TableName 固定表名。
func (Transaction) TableName() string { return "ledger_transactions" }

// Entry 是 ledger_entries 表的一行。
type Entry struct {
	ID            uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	TransactionID uuid.UUID       `gorm:"column:transaction_id;type:uuid"`
	AccountID     uuid.UUID       `gorm:"column:account_id;type:uuid"`
	Currency      string          `gorm:"column:currency"`
	Direction     string          `gorm:"column:direction"`
	Amount        decimal.Decimal `gorm:"column:amount;type:numeric(20,2)"`
	CreatedAt     time.Time       `gorm:"column:created_at;default:now()"`
}

// TableName 固定表名。
func (Entry) TableName() string { return "ledger_entries" }

// Hold 是 balance_holds 表的一行。
type Hold struct {
	ID           uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	OrderID      uuid.UUID       `gorm:"column:order_id;type:uuid"`
	UserID       uuid.UUID       `gorm:"column:user_id;type:uuid"`
	Currency     string          `gorm:"column:currency"`
	Amount       decimal.Decimal `gorm:"column:amount;type:numeric(20,2)"`
	Status       string          `gorm:"column:status"`
	HoldTxnID    uuid.UUID       `gorm:"column:hold_txn_id;type:uuid"`
	CaptureTxnID *uuid.UUID      `gorm:"column:capture_txn_id;type:uuid"`
	ReleaseTxnID *uuid.UUID      `gorm:"column:release_txn_id;type:uuid"`
	HeldAt       time.Time       `gorm:"column:held_at;default:now()"`
	CapturedAt   *time.Time      `gorm:"column:captured_at"`
	ReleasedAt   *time.Time      `gorm:"column:released_at"`
	CreatedAt    time.Time       `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Hold) TableName() string { return "balance_holds" }
