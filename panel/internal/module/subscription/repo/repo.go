// Package repo 是 subscription 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 subscription 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// Lock 是读一行时要加的行锁。
type Lock int

// 行锁强度。
const (
	NoLock Lock = iota
	ForKeyShare
	// ForNoKeyUpdate 是改订阅行前加的锁：与 UPDATE（不改主键）默认的强度相同，
	// 不挡别的事务往子表插行时外键检查要的 FOR KEY SHARE，免得与节点扣量等「先锁配额、再插子表行」的路径成环。
	ForNoKeyUpdate
)

// suffix 返回追加在 SELECT 末尾的锁子句。
func (l Lock) suffix() string {
	switch l {
	case ForKeyShare:
		return " FOR KEY SHARE"
	case ForNoKeyUpdate:
		return " FOR NO KEY UPDATE"
	}
	return ""
}

var (
	// ErrDuplicate 表示唯一约束冲突。
	ErrDuplicate = errors.New("subscription: 唯一约束冲突")
	// ErrForeignKey 表示外键约束冲突（含订阅与用户不匹配的复合外键）。
	ErrForeignKey = errors.New("subscription: 外键约束冲突")
	// ErrCheck 表示 CHECK 约束不满足。
	ErrCheck = errors.New("subscription: CHECK 约束不满足")
	// ErrAppendOnly 表示只追加表或禁删表的触发器拒绝（restrict_violation 23001）。
	ErrAppendOnly = errors.New("subscription: 只追加或禁删的表被改写")
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

// isDuplicate 判断是不是唯一约束冲突（只按类别判断，不看约束名；与 mapErr 同一套口径）。
func isDuplicate(err error) bool { return errors.Is(constraintErrs.Map(err), ErrDuplicate) }
