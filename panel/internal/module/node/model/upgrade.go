package model

import (
	"time"

	"github.com/google/uuid"
)

// ServerUpgrade 是 server_upgrades 表的一行（迁移 00060_node_upgrades.sql）。
type ServerUpgrade struct {
	ID            uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	ServerID      uuid.UUID  `gorm:"column:server_id"`
	TargetVersion string     `gorm:"column:target_version"`
	TargetSerial  int64      `gorm:"column:target_serial"`
	TargetSHA256  string     `gorm:"column:target_sha256"`
	FromVersion   *string    `gorm:"column:from_version"`
	FromSHA256    *string    `gorm:"column:from_sha256"`
	Status        string     `gorm:"column:status"`
	ReasonCode    *string    `gorm:"column:reason_code"`
	Detail        *string    `gorm:"column:detail"`
	CreatedBy     *uuid.UUID `gorm:"column:created_by"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	SentAt        *time.Time `gorm:"column:sent_at"`
	StagedAt      *time.Time `gorm:"column:staged_at"`
	FinishedAt    *time.Time `gorm:"column:finished_at"`
	DeadlineAt    time.Time  `gorm:"column:deadline_at"`
}

// TableName 固定表名。
func (ServerUpgrade) TableName() string { return "server_upgrades" }
