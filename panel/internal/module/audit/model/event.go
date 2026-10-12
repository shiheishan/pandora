// Package model 是 audit 模块的 GORM 结构体。
package model

import (
	"time"

	"github.com/google/uuid"
)

// Event 对应 audit_events 的一行；id 与 occurred_at 由库生成。
type Event struct {
	ID            uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	OccurredAt    time.Time  `gorm:"column:occurred_at;default:now()"`
	ActorKind     string     `gorm:"column:actor_kind"`
	ActorID       *uuid.UUID `gorm:"column:actor_id;type:uuid"`
	ActorAudience *string    `gorm:"column:actor_audience"`
	AuthContext   *string    `gorm:"column:auth_context"`
	Action        string     `gorm:"column:action"`
	ResourceType  *string    `gorm:"column:resource_type"`
	ResourceID    *uuid.UUID `gorm:"column:resource_id;type:uuid"`
	Outcome       string     `gorm:"column:outcome"`
	Data          string     `gorm:"column:data;type:jsonb"`
	RequestID     *string    `gorm:"column:request_id"`
	IPHash        []byte     `gorm:"column:ip_hash"`
	IPEnc         []byte     `gorm:"column:ip_enc"`
	UserAgent     *string    `gorm:"column:user_agent"`
}

// TableName 固定表名。
func (Event) TableName() string { return "audit_events" }
