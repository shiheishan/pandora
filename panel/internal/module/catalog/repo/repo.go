// Package repo 是 catalog 模块唯一使用 GORM 的数据访问层。金额写入前一律 money.Check。
package repo

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 catalog 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// Lock 是读一行时要加的行锁。
type Lock int

// 行锁强度。
const (
	NoLock Lock = iota
	ForShare
	ForUpdate
)

// suffix 返回追加在 SELECT 末尾的锁子句。
func (l Lock) suffix() string {
	switch l {
	case ForShare:
		return " FOR SHARE"
	case ForUpdate:
		return " FOR UPDATE"
	}
	return ""
}

var (
	// ErrDuplicate 表示唯一约束冲突（套餐代码重复、已有草稿版本、同档在售价重复）。
	ErrDuplicate = errors.New("catalog: 唯一约束冲突")
	// ErrForeignKey 表示外键约束冲突（用户组不存在、引用了别的套餐的版本等）。
	ErrForeignKey = errors.New("catalog: 外键约束冲突")
	// ErrCheck 表示 CHECK 约束不满足。
	ErrCheck = errors.New("catalog: CHECK 约束不满足")
	// ErrFrozen 表示目录冻结触发器拒绝（restrict_violation）：改了已发布的版本、它的子表或价格。
	ErrFrozen = errors.New("catalog: 已发布目录快照不可修改")
)

// mapErr 把 GORM 翻译后的约束错误与冻结触发器的 23001 转成模块哨兵。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return ErrDuplicate
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		return ErrForeignKey
	case errors.Is(err, gorm.ErrCheckConstraintViolated):
		return ErrCheck
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23001":
			return ErrFrozen
		case "23505":
			return ErrDuplicate
		case "23503":
			return ErrForeignKey
		case "23514":
			return ErrCheck
		}
	}
	return err
}
