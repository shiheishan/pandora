// Package model 是 report 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00032_report.sql。
package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Adjustment 是 revenue_report_adjustments 的一行（只追加）：营收报表的人工调整或它的冲正。
type Adjustment struct {
	ID     uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Amount decimal.Decimal `gorm:"column:amount;type:numeric(20,2)"`
	Reason string          `gorm:"column:reason"`
	// EffectiveOn 是计入报表的日子（站点时区的自然日），YYYY-MM-DD
	EffectiveOn string `gorm:"column:effective_on;type:date"`
	// ReversalOf 非空表示这一行是冲正，指向被冲的原行
	ReversalOf *uuid.UUID `gorm:"column:reversal_of;type:uuid"`
	CreatedBy  uuid.UUID  `gorm:"column:created_by;type:uuid"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Adjustment) TableName() string { return "revenue_report_adjustments" }

// ActivityDaily 是 activity_daily 的一行：站点时区 tz 下某一天的行为汇总（派生读数）。
type ActivityDaily struct {
	TZ          string    `gorm:"column:tz;primaryKey"`
	Day         string    `gorm:"column:day;type:date;primaryKey"` // YYYY-MM-DD
	Registered  int64     `gorm:"column:registered"`
	Logins      int64     `gorm:"column:logins"`
	Orders      int64     `gorm:"column:orders"`
	UniqueIPs   int64     `gorm:"column:unique_ips"`
	ActiveUsers int64     `gorm:"column:active_users"`
	ComputedAt  time.Time `gorm:"column:computed_at"`
}

// TableName 固定表名。
func (ActivityDaily) TableName() string { return "activity_daily" }
