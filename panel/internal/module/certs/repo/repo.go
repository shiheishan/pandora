// Package repo 是 certs 模块唯一使用 GORM 的数据访问层（迁移 00025_certs.sql 的六张表）。
package repo

import (
	"context"
	"errors"
	"time"

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

// constraintErrs 把约束违例翻译成本模块哨兵（统一做法见 internal/pkg/db/constraint.go）。
var constraintErrs = db.ConstraintErrors{ByCode: map[string]error{
	db.CodeUnique:     ErrDuplicate,
	db.CodeForeignKey: ErrForeignKey,
}}

// mapErr 把约束违例翻译成本模块哨兵，并包住原错误。
func mapErr(err error) error { return constraintErrs.Map(err) }

// DBNow 是数据库时钟。
func (r *Repo) DBNow(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := r.d.Conn(ctx).Raw(`SELECT now()`).Row().Scan(&now)
	return now, err
}
