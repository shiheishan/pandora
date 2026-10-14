// Package repo 是 promo 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"

	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// Repo 是 promo 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// ErrDuplicate 表示唯一约束冲突。
var ErrDuplicate = errors.New("promo: 唯一约束冲突")

// ErrForeignKey 表示外键约束冲突。
var ErrForeignKey = errors.New("promo: 外键约束冲突")

// ErrPlanGone 表示限定的套餐在写入 coupon_plans 时已不存在（coupon_plans_plan_id_fkey）。
var ErrPlanGone = errors.New("promo: 限定的套餐不存在（coupon_plans 外键）")

const fkCouponPlansPlan = "coupon_plans_plan_id_fkey"

// constraintErrs 把约束违例翻译成本模块哨兵：coupon_plans 的套餐外键单独报 ErrPlanGone，其余外键报 ErrForeignKey。
var constraintErrs = db.ConstraintErrors{
	ByCode: map[string]error{db.CodeUnique: ErrDuplicate, db.CodeForeignKey: ErrForeignKey},
	ByName: map[string]error{fkCouponPlansPlan: ErrPlanGone},
}

// mapErr 把约束违例翻译成本模块哨兵，并包住原错误。
func mapErr(err error) error { return constraintErrs.Map(err) }

// checkMoney 在写入前校验金额最多 2 位小数。
func checkMoney(vals ...decimal.Decimal) error {
	for _, v := range vals {
		if err := money.Check(v); err != nil {
			return err
		}
	}
	return nil
}
