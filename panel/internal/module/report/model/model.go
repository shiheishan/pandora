// Package model 是 report 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00032_report.sql 与 00059_report_integrity.sql。
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

// IntegrityCheck 是 integrity_checks 的一行：一项数据巡检的最新结果（迁移 00059_report_integrity.sql）。
type IntegrityCheck struct {
	Code  string `gorm:"column:code;primaryKey"`
	Count int64  `gorm:"column:count"`
	// Sample 是样例 id 的 JSON 数组文本（最多 5 个，新的在前；没有违例时为 []）
	Sample    string    `gorm:"column:sample;type:jsonb"`
	CheckedAt time.Time `gorm:"column:checked_at"`
	// FailingSince 是这一段违例从哪次巡检开始；Count 为 0 时为空
	FailingSince *time.Time `gorm:"column:failing_since"`
	// AlertedCount 是这一段里上次告警时的违例数；Count 为 0 时为 0
	AlertedCount int64 `gorm:"column:alerted_count"`
}

// TableName 固定表名。
func (IntegrityCheck) TableName() string { return "integrity_checks" }
