// Package repo 是 payment 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 payment 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// ErrDuplicate 表示唯一约束冲突。
var ErrDuplicate = errors.New("payment: 唯一约束冲突")

// ErrForeignKey 表示外键约束冲突。
var ErrForeignKey = errors.New("payment: 外键约束冲突")

// ErrCheck 表示检查约束冲突。
var ErrCheck = errors.New("payment: 检查约束冲突")

// constraintErrs 把约束违例翻译成本模块哨兵（统一做法见 internal/pkg/db/constraint.go）。
var constraintErrs = db.ConstraintErrors{ByCode: map[string]error{
	db.CodeUnique:     ErrDuplicate,
	db.CodeForeignKey: ErrForeignKey,
	db.CodeCheck:      ErrCheck,
}}

// mapErr 把约束违例翻译成本模块哨兵，并包住原错误。
func mapErr(err error) error { return constraintErrs.Map(err) }
