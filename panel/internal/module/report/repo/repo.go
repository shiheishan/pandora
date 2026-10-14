// Package repo 是 report 模块唯一使用 GORM 的数据访问层：营收报表调整、行为趋势按天汇总、数据库探测。
package repo

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

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

// mapErr 把 GORM 翻译后的约束错误与触发器的 23001 转成模块哨兵，并包住原错误。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return fmt.Errorf("%w: %v", ErrDuplicate, err)
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		return fmt.Errorf("%w: %v", ErrForeignKey, err)
	case errors.Is(err, gorm.ErrCheckConstraintViolated):
		return fmt.Errorf("%w: %v", ErrCheck, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23001":
			return fmt.Errorf("%w: %v", ErrAppendOnly, err)
		case "23505":
			return fmt.Errorf("%w: %v", ErrDuplicate, err)
		case "23503":
			return fmt.Errorf("%w: %v", ErrForeignKey, err)
		case "23514":
			return fmt.Errorf("%w: %v", ErrCheck, err)
		}
	}
	return err
}
