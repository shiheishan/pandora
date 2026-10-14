// Package model 是 plugin 模块的 GORM 结构体，只有字段与标签。
package model

import (
	"time"

	"github.com/google/uuid"
)

// 投递状态
const (
	StatusQueued = "queued"
	StatusSent   = "sent"
	StatusFailed = "failed"
)

// Hook 是 plugin_hooks 的一行；Events 是 JSON 字符串数组原文，SecretEnc 是签名密钥的封存密文。
type Hook struct {
	ID          uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code        string     `gorm:"column:code"`
	Name        string     `gorm:"column:name"`
	Description string     `gorm:"column:description"`
	Enabled     bool       `gorm:"column:enabled"`
	Events      string     `gorm:"column:events;type:jsonb"`
	EndpointURL string     `gorm:"column:endpoint_url"`
	SecretEnc   []byte     `gorm:"column:secret_enc"`
	TimeoutMS   int        `gorm:"column:timeout_ms"`
	MaxAttempts int        `gorm:"column:max_attempts"`
	CreatedBy   *uuid.UUID `gorm:"column:created_by;type:uuid"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Hook) TableName() string { return "plugin_hooks" }

// Delivery 是 plugin_hook_deliveries 的一行；Payload 是 JSON 对象原文。
type Delivery struct {
	ID             uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	HookID         uuid.UUID  `gorm:"column:hook_id;type:uuid"`
	Event          string     `gorm:"column:event"`
	DedupeKey      string     `gorm:"column:dedupe_key"`
	Payload        string     `gorm:"column:payload;type:jsonb"`
	Status         string     `gorm:"column:status"`
	Attempts       int        `gorm:"column:attempts"`
	MaxAttempts    int        `gorm:"column:max_attempts"`
	NextRetryAt    *time.Time `gorm:"column:next_retry_at"`
	ResponseCode   *int       `gorm:"column:response_code"`
	ErrorMessage   string     `gorm:"column:error_message"`
	LastDurationMS *int       `gorm:"column:last_duration_ms"`
	SentAt         *time.Time `gorm:"column:sent_at"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Delivery) TableName() string { return "plugin_hook_deliveries" }
