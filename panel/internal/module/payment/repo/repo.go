// Package repo 是 payment 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

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

// mapErr 把 GORM 翻译后的约束错误转成模块哨兵。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return fmt.Errorf("%w: %v", ErrDuplicate, err)
	}
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		return fmt.Errorf("%w: %v", ErrForeignKey, err)
	}
	if errors.Is(err, gorm.ErrCheckConstraintViolated) {
		return fmt.Errorf("%w: %v", ErrCheck, err)
	}
	return err
}
