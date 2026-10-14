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

// InboxItem 对应 notification_inbox 的一行（站内信，写入时已渲染）。
type InboxItem struct {
	ID           uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	UserID       uuid.UUID  `gorm:"column:user_id;type:uuid"`
	TemplateCode string     `gorm:"column:template_code"`
	Subject      string     `gorm:"column:subject"`
	Body         string     `gorm:"column:body"`
	ReadAt       *time.Time `gorm:"column:read_at"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (InboxItem) TableName() string { return "notification_inbox" }

// Event 对应 notification_events 的一行（通知去重键）。
type Event struct {
	DedupeKey    string    `gorm:"column:dedupe_key;primaryKey"`
	UserID       uuid.UUID `gorm:"column:user_id;type:uuid"`
	TemplateCode string    `gorm:"column:template_code"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Event) TableName() string { return "notification_events" }

// Preference 对应 notification_preferences 的一行（用户改过的偏好项）。
type Preference struct {
	UserID    uuid.UUID `gorm:"column:user_id;type:uuid;primaryKey"`
	Category  string    `gorm:"column:category;primaryKey"`
	Channel   string    `gorm:"column:channel;primaryKey"`
	Enabled   bool      `gorm:"column:enabled"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Preference) TableName() string { return "notification_preferences" }

// 公告状态。
const (
	AnnouncementDraft     = "draft"
	AnnouncementScheduled = "scheduled"
	AnnouncementPublished = "published"
	AnnouncementWithdrawn = "withdrawn"
)

// Announcement 对应 announcements 的标量列。两个 uuid[] 定向列不在这里：repo 用 SQL 读写
// （写入传 '{…}' 文本再 CAST 成 uuid[]，读出用 array_to_string 再解析），见 repo/announcements.go。
type Announcement struct {
	ID          uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Title       string     `gorm:"column:title"`
	Body        string     `gorm:"column:body"`
	Severity    string     `gorm:"column:severity"`
	Pinned      bool       `gorm:"column:pinned"`
	Status      string     `gorm:"column:status"`
	Version     int        `gorm:"column:version"`
	PublishAt   *time.Time `gorm:"column:publish_at"`
	ExpiresAt   *time.Time `gorm:"column:expires_at"`
	PublishedAt *time.Time `gorm:"column:published_at"`
	WithdrawnAt *time.Time `gorm:"column:withdrawn_at"`
	WithdrawnBy *uuid.UUID `gorm:"column:withdrawn_by;type:uuid"`
	CreatedBy   *uuid.UUID `gorm:"column:created_by;type:uuid"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
}

// TableName 固定表名。
func (Announcement) TableName() string { return "announcements" }

// TelegramBinding 对应 telegram_bindings 的一行。
type TelegramBinding struct {
	UserID   uuid.UUID `gorm:"column:user_id;type:uuid;primaryKey"`
	ChatID   int64     `gorm:"column:chat_id"`
	Username string    `gorm:"column:username"`
	BoundAt  time.Time `gorm:"column:bound_at;autoCreateTime"`
}

// TableName 固定表名。
func (TelegramBinding) TableName() string { return "telegram_bindings" }

// TelegramBindCode 对应 telegram_bind_codes 的一行；只存绑定码的 HMAC。
type TelegramBindCode struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	UserID    uuid.UUID  `gorm:"column:user_id;type:uuid"`
	CodeHash  []byte     `gorm:"column:code_hash"`
	ExpiresAt time.Time  `gorm:"column:expires_at"`
	UsedAt    *time.Time `gorm:"column:used_at"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (TelegramBindCode) TableName() string { return "telegram_bind_codes" }
