// Package model 是 payment 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00012_payment.sql、00042_payment_fee_crypto.sql。
package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Provider 是 payment_providers 表的一行。
type Provider struct {
	ID                uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code              string          `gorm:"column:code"`
	Adapter           string          `gorm:"column:adapter"`
	DisplayName       string          `gorm:"column:display_name"`
	CredentialsSealed []byte          `gorm:"column:credentials_sealed"`
	BaseURL           string          `gorm:"column:base_url"`
	SubmitPath        string          `gorm:"column:submit_path"`
	APIPath           string          `gorm:"column:api_path"`
	Methods           []string        `gorm:"column:methods;type:jsonb;serializer:json"`
	DefaultMethod     string          `gorm:"column:default_method"`
	AllowPrivateHost  bool            `gorm:"column:allow_private_host"`
	MinAmount         decimal.Decimal `gorm:"column:min_amount;type:numeric(20,2)"`
	FeeFixed          decimal.Decimal `gorm:"column:fee_fixed;type:numeric(20,2)"`
	FeePercent        decimal.Decimal `gorm:"column:fee_percent;type:numeric(5,2)"`
	Enabled           bool            `gorm:"column:enabled"`
	AcceptingNew      bool            `gorm:"column:accepting_new"`
	CreatedAt         time.Time       `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt         time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Provider) TableName() string { return "payment_providers" }

// Intent 是 payment_intents 表的一行。
type Intent struct {
	ID              uuid.UUID         `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	OrderID         uuid.UUID         `gorm:"column:order_id;type:uuid"`
	OrderNo         string            `gorm:"column:order_no"`
	UserID          uuid.UUID         `gorm:"column:user_id;type:uuid"`
	ProviderID      uuid.UUID         `gorm:"column:provider_id;type:uuid"`
	OutTradeNo      string            `gorm:"column:out_trade_no"`
	ProviderRef     *string           `gorm:"column:provider_ref"`
	Method          string            `gorm:"column:method"`
	Amount          decimal.Decimal   `gorm:"column:amount;type:numeric(20,2)"`
	SurchargeAmount decimal.Decimal   `gorm:"column:surcharge_amount;type:numeric(20,2)"`
	Status          string            `gorm:"column:status"`
	HTTPMethod      string            `gorm:"column:http_method"`
	RedirectURL     string            `gorm:"column:redirect_url"`
	FormFields      map[string]string `gorm:"column:form_fields;type:jsonb;serializer:json"`
	ExpiresAt       *time.Time        `gorm:"column:expires_at"`
	QueryAttempts   int               `gorm:"column:query_attempts"`
	NextQueryAt     *time.Time        `gorm:"column:next_query_at"`
	CreatedAt       time.Time         `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time         `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Intent) TableName() string { return "payment_intents" }

// Payment 是 payments 表的一行。
type Payment struct {
	ID                uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	OrderID           uuid.UUID       `gorm:"column:order_id;type:uuid"`
	UserID            uuid.UUID       `gorm:"column:user_id;type:uuid"`
	PaymentIntentID   *uuid.UUID      `gorm:"column:payment_intent_id;type:uuid"`
	ProviderID        uuid.UUID       `gorm:"column:provider_id;type:uuid"`
	ProviderPaymentID string          `gorm:"column:provider_payment_id"`
	Amount            decimal.Decimal `gorm:"column:amount;type:numeric(20,2)"`
	FeeAmount         decimal.Decimal `gorm:"column:fee_amount;type:numeric(20,2)"`
	SurchargeAmount   decimal.Decimal `gorm:"column:surcharge_amount;type:numeric(20,2)"`
	Status            string          `gorm:"column:status"`
	Method            *string         `gorm:"column:method"`
	PaidAt            time.Time       `gorm:"column:paid_at;default:now()"`
	CreatedAt         time.Time       `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Payment) TableName() string { return "payments" }

// Event 是 payment_events 表的一行。
type Event struct {
	ID                uuid.UUID         `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	ProviderID        uuid.UUID         `gorm:"column:provider_id;type:uuid"`
	ProviderEventID   string            `gorm:"column:provider_event_id"`
	EventType         string            `gorm:"column:event_type"`
	ProviderPaymentID *string           `gorm:"column:provider_payment_id"`
	RawPayload        map[string]string `gorm:"column:raw_payload;type:jsonb;serializer:json"`
	SignatureVerified bool              `gorm:"column:signature_verified"`
	ProcessingStatus  string            `gorm:"column:processing_status"`
	ProcessingError   *string           `gorm:"column:processing_error"`
	ProcessedAt       *time.Time        `gorm:"column:processed_at"`
	ReceivedAt        time.Time         `gorm:"column:received_at;default:now()"`
}

// TableName 固定表名。
func (Event) TableName() string { return "payment_events" }

// LateCase 是 late_payment_cases 表的一行。
type LateCase struct {
	ID               uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	OrderID          uuid.UUID       `gorm:"column:order_id;type:uuid"`
	OrderNo          string          `gorm:"column:order_no"`
	OrderStatus      string          `gorm:"column:order_status"`
	UserID           uuid.UUID       `gorm:"column:user_id;type:uuid"`
	PaymentID        uuid.UUID       `gorm:"column:payment_id;type:uuid"`
	PaymentEventID   *uuid.UUID      `gorm:"column:payment_event_id;type:uuid"`
	CaseKind         string          `gorm:"column:case_kind"`
	Amount           decimal.Decimal `gorm:"column:amount;type:numeric(20,2)"`
	Status           string          `gorm:"column:status"`
	SuspenseTxnID    uuid.UUID       `gorm:"column:suspense_txn_id;type:uuid"`
	AppliedTxnID     *uuid.UUID      `gorm:"column:applied_txn_id;type:uuid"`
	ResolutionReason *string         `gorm:"column:resolution_reason"`
	ReceivedAt       time.Time       `gorm:"column:received_at;default:now()"`
	ResolvedAt       *time.Time      `gorm:"column:resolved_at"`
	CreatedAt        time.Time       `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt        time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (LateCase) TableName() string { return "late_payment_cases" }
