// Package repo 是 report 模块唯一使用 GORM 的数据访问层：营收报表调整、行为趋势按天汇总、数据巡检结果、数据库探测。
package repo

import (
	"errors"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 report 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

var (
	// ErrDuplicate 表示唯一约束冲突。
	ErrDuplicate = errors.New("report: 唯一约束冲突")
	// ErrForeignKey 表示外键约束冲突。
	ErrForeignKey = errors.New("report: 外键约束冲突")
	// ErrCheck 表示 CHECK 约束不满足。
	ErrCheck = errors.New("report: CHECK 约束不满足")
	// ErrAppendOnly 表示只追加或禁删的表被改写（触发器的 restrict_violation 23001）。
	ErrAppendOnly = errors.New("report: 只追加或禁删的表被改写")
)

// constraintErrs 把约束违例翻译成本模块哨兵（统一做法见 internal/pkg/db/constraint.go）。
var constraintErrs = db.ConstraintErrors{ByCode: map[string]error{
	db.CodeUnique:     ErrDuplicate,
	db.CodeForeignKey: ErrForeignKey,
	db.CodeCheck:      ErrCheck,
	db.CodeRestrict:   ErrAppendOnly,
}}

// mapErr 把约束违例翻译成本模块哨兵，并包住原错误。
func mapErr(err error) error { return constraintErrs.Map(err) }
