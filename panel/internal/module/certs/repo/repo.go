// Package repo 是 certs 模块唯一使用 GORM 的数据访问层（迁移 00025_certs.sql 的六张表）。
package repo

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 certs 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// ErrDuplicate 表示唯一约束冲突（凭据、证书重名）。
var ErrDuplicate = errors.New("certs: 唯一约束冲突")

// ErrForeignKey 表示外键约束冲突。
var ErrForeignKey = errors.New("certs: 外键约束冲突")

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

// DBNow 是数据库时钟。
func (r *Repo) DBNow(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := r.d.Conn(ctx).Raw(`SELECT now()`).Row().Scan(&now)
	return now, err
}
