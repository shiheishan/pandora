// Package repo 是 identity 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 identity 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// ErrDuplicate 表示唯一约束冲突。
var ErrDuplicate = errors.New("identity: 唯一约束冲突")

// ErrForeignKey 表示外键约束冲突。
var ErrForeignKey = errors.New("identity: 外键约束冲突")

// constraintErrs 把约束违例翻译成本模块哨兵（统一做法见 internal/pkg/db/constraint.go）。
// 别的模块指向 users、user_groups 的外键多是 ON DELETE RESTRICT，删除被引用的行回 23001（restrict_violation），这里同样当外键冲突。
var constraintErrs = db.ConstraintErrors{ByCode: map[string]error{
	db.CodeUnique:     ErrDuplicate,
	db.CodeForeignKey: ErrForeignKey,
	db.CodeRestrict:   ErrForeignKey,
}}

// mapErr 把约束违例翻译成本模块哨兵，并包住原错误。
func mapErr(err error) error { return constraintErrs.Map(err) }
