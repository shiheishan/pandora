// Package model 是 appearance 模块的 GORM 结构体，只有字段与标签。
package model

import (
	"time"

	"github.com/google/uuid"
)

// Theme 是 site_themes 的一行；Tokens、Branding 是 JSON 对象原文。
type Theme struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code      string    `gorm:"column:code"`
	Name      string    `gorm:"column:name"`
	IsBuiltin bool      `gorm:"column:is_builtin"`
	IsActive  bool      `gorm:"column:is_active"`
	Tokens    string    `gorm:"column:tokens;type:jsonb"`
	Branding  string    `gorm:"column:branding;type:jsonb"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Theme) TableName() string { return "site_themes" }

// Slot 是 site_slots 的一行；Content 是净化后的 HTML。
type Slot struct {
	Key       string     `gorm:"column:slot_key;primaryKey"`
	Content   string     `gorm:"column:content"`
	Enabled   bool       `gorm:"column:enabled"`
	UpdatedBy *uuid.UUID `gorm:"column:updated_by;type:uuid"`
	UpdatedAt time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Slot) TableName() string { return "site_slots" }
