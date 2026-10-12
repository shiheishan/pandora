// Package repo 是 order 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// Repo 是 order 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// Lock 是读一行时要加的行锁。
type Lock int

// 行锁强度。
const (
	NoLock Lock = iota
	// ForNoKeyUpdate 与 UPDATE 默认强度相同，不挡子表外键检查的 FOR KEY SHARE。
	ForNoKeyUpdate
	ForUpdate
)

// suffix 返回追加在 SELECT 末尾的锁子句。
func (l Lock) suffix() string {
	switch l {
	case ForNoKeyUpdate:
		return " FOR NO KEY UPDATE"
	case ForUpdate:
		return " FOR UPDATE"
	}
	return ""
}

var (
	// ErrDuplicate 表示唯一约束冲突。
	ErrDuplicate = errors.New("order: 唯一约束冲突")
	// ErrForeignKey 表示外键约束冲突。
	ErrForeignKey = errors.New("order: 外键约束冲突")
	// ErrCheck 表示 CHECK 约束不满足。
	ErrCheck = errors.New("order: CHECK 约束不满足")
	// ErrAppendOnly 表示只追加表或禁删表的触发器拒绝（restrict_violation 23001）。
	ErrAppendOnly = errors.New("order: 只追加或禁删的表被改写")
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

// IsDuplicate 判断错误是否为唯一约束冲突。
func IsDuplicate(err error) bool { return errors.Is(err, ErrDuplicate) }

// now 返回写库用的当前时刻（UTC、截到微秒）。
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

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

// likePattern 把用户输入转成 ILIKE 的包含模式：先转义 \ % _，再两头加 %。
func likePattern(q string) string {
	return "%" + escapeLike(q) + "%"
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
