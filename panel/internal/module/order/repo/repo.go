// Package repo 是 order 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"

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
	// ForNoKeyUpdateSkipLocked 拿不到锁就当这行不存在（超时释放的单笔事务用：订单正被结算或取消锁着时跳过）。
	ForNoKeyUpdateSkipLocked
)

// suffix 返回追加在 SELECT 末尾的锁子句。
func (l Lock) suffix() string {
	switch l {
	case ForNoKeyUpdate:
		return " FOR NO KEY UPDATE"
	case ForUpdate:
		return " FOR UPDATE"
	case ForNoKeyUpdateSkipLocked:
		return " FOR NO KEY UPDATE SKIP LOCKED"
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

// constraintErrs 把约束违例翻译成本模块哨兵（统一做法见 internal/pkg/db/constraint.go）。
var constraintErrs = db.ConstraintErrors{ByCode: map[string]error{
	db.CodeUnique:     ErrDuplicate,
	db.CodeForeignKey: ErrForeignKey,
	db.CodeCheck:      ErrCheck,
	db.CodeRestrict:   ErrAppendOnly,
}}

// mapErr 把约束违例翻译成本模块哨兵，并包住原错误。
func mapErr(err error) error { return constraintErrs.Map(err) }

// IsDuplicate 判断错误是否为唯一约束冲突。
func IsDuplicate(err error) bool { return errors.Is(err, ErrDuplicate) }

// now 返回写库用的当前时刻（UTC、截到微秒）。
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// checkMoney 在写入前校验金额最多 2 位小数。
func checkMoney(vals ...decimal.Decimal) error {
	for _, v := range vals {
		if err := money.Check(v); err != nil {
			return err
		}
	}
	return nil
}
