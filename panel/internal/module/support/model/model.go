// Package model 是 support 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00027_support.sql。
package model

import (
	"time"

	"github.com/google/uuid"
)

// Ticket 是 tickets 表的一行。
type Ticket struct {
	ID                  uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	TicketNo            string     `gorm:"column:ticket_no"`
	UserID              uuid.UUID  `gorm:"column:user_id;type:uuid"`
	Subject             string     `gorm:"column:subject"`
	Category            string     `gorm:"column:category"`
	Priority            string     `gorm:"column:priority"`
	Status              string     `gorm:"column:status"`
	AssignedTo          *uuid.UUID `gorm:"column:assigned_to;type:uuid"`
	RelatedOrderID      *uuid.UUID `gorm:"column:related_order_id;type:uuid"`
	RelatedOrderNo      *string    `gorm:"column:related_order_no"`
	SLAFirstResponseDue time.Time  `gorm:"column:sla_first_response_due"`
	SLAResolutionDue    time.Time  `gorm:"column:sla_resolution_due"`
	FirstRespondedAt    *time.Time `gorm:"column:first_responded_at"`
	EscalatedAt         *time.Time `gorm:"column:escalated_at"`
	ResolvedAt          *time.Time `gorm:"column:resolved_at"`
	ClosedAt            *time.Time `gorm:"column:closed_at"`
	ClosedReason        *string    `gorm:"column:closed_reason"`
	ClosedNote          *string    `gorm:"column:closed_note"`
	CreatedAt           time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt           time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Ticket) TableName() string { return "tickets" }

// Message 是 ticket_messages 表的一行（只追加）。
type Message struct {
	ID           uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	TicketID     uuid.UUID  `gorm:"column:ticket_id;type:uuid"`
	AuthorID     *uuid.UUID `gorm:"column:author_id;type:uuid"`
	AuthorKind   string     `gorm:"column:author_kind"`
	Body         string     `gorm:"column:body"`
	InternalNote bool       `gorm:"column:internal_note"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Message) TableName() string { return "ticket_messages" }

// Macro 是 ticket_macros 表的一行：一条快捷回复。
type Macro struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Title     string     `gorm:"column:title"`
	Body      string     `gorm:"column:body"`
	SortOrder int        `gorm:"column:sort_order"`
	CreatedBy *uuid.UUID `gorm:"column:created_by;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Macro) TableName() string { return "ticket_macros" }
