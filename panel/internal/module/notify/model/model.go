// Package model 是 notify 模块的 GORM 结构体。
package model

import (
	"time"

	"github.com/google/uuid"
)

// 投递状态。
const (
	StatusQueued     = "queued"
	StatusSent       = "sent"
	StatusFailed     = "failed"
	StatusSuppressed = "suppressed"
)

// Template 对应 notification_templates 的一行。
type Template struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code      string    `gorm:"column:code"`
	Channel   string    `gorm:"column:channel"`
	Version   int       `gorm:"column:version;default:1"`
	Subject   string    `gorm:"column:subject"`
	Body      string    `gorm:"column:body"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Template) TableName() string { return "notification_templates" }

// Delivery 对应 notification_deliveries 的一行。ID 由 service 生成（封存 payload 的 AAD 要用）。
type Delivery struct {
	ID            uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	UserID        *uuid.UUID `gorm:"column:user_id;type:uuid"`
	TemplateCode  string     `gorm:"column:template_code"`
	Channel       string     `gorm:"column:channel"`
	RecipientHash []byte     `gorm:"column:recipient_hash"`
	PayloadEnc    []byte     `gorm:"column:payload_enc"`
	Status        string     `gorm:"column:status;default:queued"`
	Attempts      int        `gorm:"column:attempts;default:0"`
	MaxAttempts   int        `gorm:"column:max_attempts;default:5"`
	NextRetryAt   time.Time  `gorm:"column:next_retry_at;default:now()"`
	ErrorMessage  *string    `gorm:"column:error_message"`
	SentAt        *time.Time `gorm:"column:sent_at"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Delivery) TableName() string { return "notification_deliveries" }
