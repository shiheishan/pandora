// Package repo 是 giftcard 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// Repo 是 giftcard 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

var (
	// ErrDuplicate 表示唯一约束冲突。
	ErrDuplicate = errors.New("giftcard: 唯一约束冲突")
	// ErrForeignKey 表示外键约束冲突。
	ErrForeignKey = errors.New("giftcard: 外键约束冲突")
	// ErrCheck 表示 CHECK 约束不满足。
	ErrCheck = errors.New("giftcard: CHECK 约束不满足")
	// ErrAppendOnly 表示只追加表或禁删表的触发器拒绝（restrict_violation 23001）。
	ErrAppendOnly = errors.New("giftcard: 只追加或禁删的表被改写")
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

// checkMoney 在写入前校验非 nil 的金额最多 2 位小数。
func checkMoney(vals ...*decimal.Decimal) error {
	for _, p := range vals {
		if p == nil {
			continue
		}
		if err := money.Check(*p); err != nil {
			return err
		}
	}
	return nil
}

// now 返回写库用的当前时刻（UTC、截到微秒）。
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// forUpdateSuffix 为需要行锁的 SELECT 追加子句。
func forUpdateSuffix(forUpdate bool) string {
	if forUpdate {
		return " FOR UPDATE"
	}
	return ""
}
