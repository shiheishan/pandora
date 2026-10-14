// Package model 是 subscription 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00011_subscription.sql 与 00017_subscription_delivery.sql。
//
// jsonb 列（事件 payload）用 string 承载 JSON 文本，由 service 编解码。
// 库里生成或带默认值、写入时不该由 Go 给出的列（proxy_uuid、node_uid、remaining）标 `<-:false`。
package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Subscription 是 subscriptions 表的一行。
type Subscription struct {
	ID                    uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	UserID                uuid.UUID       `gorm:"column:user_id;type:uuid"`
	PlanID                uuid.UUID       `gorm:"column:plan_id;type:uuid"`
	PlanVersionID         uuid.UUID       `gorm:"column:plan_version_id;type:uuid"`
	PriceID               *uuid.UUID      `gorm:"column:price_id;type:uuid"`
	Status                string          `gorm:"column:status"`
	CurrentPeriodStart    time.Time       `gorm:"column:current_period_start"`
	CurrentPeriodEnd      time.Time       `gorm:"column:current_period_end"`
	RenewalClosedAt       *time.Time      `gorm:"column:renewal_closed_at"`
	SnapshotCurrency      string          `gorm:"column:snapshot_currency"`
	SnapshotAmount        decimal.Decimal `gorm:"column:snapshot_amount;type:numeric(20,2)"`
	SnapshotInterval      string          `gorm:"column:snapshot_interval"`
	SnapshotIntervalCount int             `gorm:"column:snapshot_interval_count"`
	DeviceLimit           *int            `gorm:"column:device_limit"`
	Label                 *string         `gorm:"column:label"`
	ProxyUUID             uuid.UUID       `gorm:"column:proxy_uuid;type:uuid;<-:false"`
	NodeUID               int64           `gorm:"column:node_uid;<-:false"`
	SourceOrderID         *uuid.UUID      `gorm:"column:source_order_id;type:uuid"`
	CreatedAt             time.Time       `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt             time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Subscription) TableName() string { return "subscriptions" }

// Event 是 subscription_events 表的一行（只追加）。
type Event struct {
	ID             uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	SubscriptionID uuid.UUID  `gorm:"column:subscription_id;type:uuid"`
	EventType      string     `gorm:"column:event_type"`
	FromStatus     *string    `gorm:"column:from_status"`
	ToStatus       string     `gorm:"column:to_status"`
	ActorKind      string     `gorm:"column:actor_kind"`
	ActorID        *uuid.UUID `gorm:"column:actor_id;type:uuid"`
	OrderID        *uuid.UUID `gorm:"column:order_id;type:uuid"`
	Payload        string     `gorm:"column:payload;type:jsonb"`
	OccurredAt     time.Time  `gorm:"column:occurred_at;autoCreateTime"`
}

// TableName 固定表名。
func (Event) TableName() string { return "subscription_events" }

// Credential 是 subscription_credentials 表的一行。
type Credential struct {
	ID             uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	SubscriptionID uuid.UUID  `gorm:"column:subscription_id;type:uuid"`
	UserID         uuid.UUID  `gorm:"column:user_id;type:uuid"`
	TokenHash      []byte     `gorm:"column:token_hash;type:bytea"`
	TokenPrefix    string     `gorm:"column:token_prefix"`
	TokenEncrypted []byte     `gorm:"column:token_encrypted;type:bytea"`
	Status         string     `gorm:"column:status"`
	RevokedAt      *time.Time `gorm:"column:revoked_at"`
	RevokedReason  *string    `gorm:"column:revoked_reason"`
	GraceUntil     *time.Time `gorm:"column:grace_until"`
	FetchCount     int64      `gorm:"column:fetch_count"`
	LastFetchedAt  *time.Time `gorm:"column:last_fetched_at"`
	RotatedCount   int        `gorm:"column:rotated_count"`
	RotatedAt      *time.Time `gorm:"column:rotated_at"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Credential) TableName() string { return "subscription_credentials" }

// QuotaBalance 是 quota_balances 表的一行。LimitValue 为 nil 表示不限；Remaining 是库里的生成列。
type QuotaBalance struct {
	ID             uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	SubscriptionID uuid.UUID  `gorm:"column:subscription_id;type:uuid"`
	Metric         string     `gorm:"column:metric"`
	Period         string     `gorm:"column:period"`
	PeriodStart    time.Time  `gorm:"column:period_start"`
	PeriodEnd      *time.Time `gorm:"column:period_end"`
	LimitValue     *int64     `gorm:"column:limit_value"`
	Consumed       int64      `gorm:"column:consumed"`
	Remaining      *int64     `gorm:"column:remaining;<-:false"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (QuotaBalance) TableName() string { return "quota_balances" }

// PackGrant 是 traffic_pack_grants 表的一行。SubscriptionID 为 nil 表示未分配。
type PackGrant struct {
	ID             uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	UserID         uuid.UUID  `gorm:"column:user_id;type:uuid"`
	SubscriptionID *uuid.UUID `gorm:"column:subscription_id;type:uuid"`
	Source         string     `gorm:"column:source"`
	SourceID       uuid.UUID  `gorm:"column:source_id;type:uuid"`
	GrantedBytes   int64      `gorm:"column:granted_bytes"`
	ConsumedBytes  int64      `gorm:"column:consumed_bytes"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (PackGrant) TableName() string { return "traffic_pack_grants" }

// PackTransfer 是 traffic_pack_transfers 表的一行（只追加）。
type PackTransfer struct {
	ID                 uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	GrantID            uuid.UUID  `gorm:"column:grant_id;type:uuid"`
	UserID             uuid.UUID  `gorm:"column:user_id;type:uuid"`
	FromSubscriptionID *uuid.UUID `gorm:"column:from_subscription_id;type:uuid"`
	ToSubscriptionID   uuid.UUID  `gorm:"column:to_subscription_id;type:uuid"`
	RemainingBytes     int64      `gorm:"column:remaining_bytes"`
	ActorKind          string     `gorm:"column:actor_kind"`
	ActorID            *uuid.UUID `gorm:"column:actor_id;type:uuid"`
	CreatedAt          time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (PackTransfer) TableName() string { return "traffic_pack_transfers" }

// ResetLog 是 traffic_reset_logs 表的一行（只追加）。
type ResetLog struct {
	ID             uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	SubscriptionID uuid.UUID  `gorm:"column:subscription_id;type:uuid"`
	UserID         uuid.UUID  `gorm:"column:user_id;type:uuid"`
	Metric         string     `gorm:"column:metric"`
	Reason         string     `gorm:"column:reason"`
	ConsumedBefore int64      `gorm:"column:consumed_before"`
	ConsumedAfter  int64      `gorm:"column:consumed_after"`
	ActorID        *uuid.UUID `gorm:"column:actor_id;type:uuid"`
	Note           *string    `gorm:"column:note"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (ResetLog) TableName() string { return "traffic_reset_logs" }

// FetchLog 是 subscription_fetch_log 表的一行（只追加）。未认证失败（not_found）的 CredentialID、SubscriptionID 为 nil。
type FetchLog struct {
	ID             uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	CredentialID   *uuid.UUID `gorm:"column:credential_id;type:uuid"`
	SubscriptionID *uuid.UUID `gorm:"column:subscription_id;type:uuid"`
	FetchedAt      time.Time  `gorm:"column:fetched_at;autoCreateTime"`
	IPHash         []byte     `gorm:"column:ip_hash;type:bytea"`
	UAHash         []byte     `gorm:"column:ua_hash;type:bytea"`
	IPEnc          []byte     `gorm:"column:ip_enc;type:bytea"`
	UAEnc          []byte     `gorm:"column:ua_enc;type:bytea"`
	UAFamily       string     `gorm:"column:ua_family"`
	Result         string     `gorm:"column:result"`
	Format         *string    `gorm:"column:format"`
	NodeCount      int        `gorm:"column:node_count"`
	BytesSent      int        `gorm:"column:bytes_sent"`
}

// TableName 固定表名。
func (FetchLog) TableName() string { return "subscription_fetch_log" }

// SubscriptionTemplate 是 subscription_templates 表的一行：一种订阅格式的后台自定义模板（没有行就用内置模板）。
type SubscriptionTemplate struct {
	Format    string    `gorm:"column:format;primaryKey"`
	Content   string    `gorm:"column:content"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TableName 固定表名。
func (SubscriptionTemplate) TableName() string { return "subscription_templates" }
