// Package repo 是 identity 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"
	"strings"

	"gorm.io/gorm"

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

// mapErr 把 GORM 翻译后的约束错误转成模块哨兵。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrDuplicate
	}
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		return ErrForeignKey
	}
	return err
}

// likePattern 把用户输入转成 ILIKE 的包含模式：先转义 \ % _，再两头加 %。
func likePattern(q string) string {
	return "%" + escapeLike(q) + "%"
}

// escapeLike 为 ILIKE/LIKE 转义 \、%、_。
func escapeLike(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
