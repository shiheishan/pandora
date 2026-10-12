// Package repo 是 commission 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// Repo 是 commission 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

var (
	// ErrDuplicate 表示唯一约束冲突。
	ErrDuplicate = errors.New("commission: 唯一约束冲突")
	// ErrForeignKey 表示外键约束冲突。
	ErrForeignKey = errors.New("commission: 外键约束冲突")
	// ErrCheck 表示 CHECK 约束不满足。
	ErrCheck = errors.New("commission: CHECK 约束不满足")
)

// mapErr 把 GORM 翻译后的约束错误转成模块哨兵，并包住原错误。
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

// checkMoney 在写入前校验金额最多 2 位小数。
func checkMoney(vals ...decimal.Decimal) error {
	for _, v := range vals {
		if err := money.Check(v); err != nil {
			return err
		}
	}
	return nil
}

// LockUser 在当前事务里对该用户加咨询锁，串行化提现与转余额。
func (r *Repo) LockUser(ctx context.Context, userID uuid.UUID) error {
	key := "commission:" + userID.String()
	err := r.d.Conn(ctx).Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
		key,
	).Error
	return mapErr(err)
}
