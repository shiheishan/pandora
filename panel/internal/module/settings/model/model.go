// Package model 是 settings 模块的 GORM 结构体：settings 与 feature_switches 两张表。
package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Setting 是 settings 表的一行：一个键一个 JSON 值。
type Setting struct {
	ID        uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Key       string          `gorm:"column:key"`
	Value     json.RawMessage `gorm:"column:value;type:jsonb"`
	UpdatedBy *uuid.UUID      `gorm:"column:updated_by;type:uuid"`
	CreatedAt time.Time       `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Setting) TableName() string { return "settings" }

// FeatureSwitch 是 feature_switches 表的一行；Enabled 为 true 表示功能可用。
type FeatureSwitch struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code      string     `gorm:"column:code"`
	Enabled   bool       `gorm:"column:enabled"`
	Reason    *string    `gorm:"column:reason"`
	ChangedBy *uuid.UUID `gorm:"column:changed_by;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (FeatureSwitch) TableName() string { return "feature_switches" }
