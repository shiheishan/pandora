package repo

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// 约束名、SQLSTATE 到哨兵的翻译在 internal/pkg/db 测；这里钉住本模块的表：
// coupon_plans 的套餐外键单独报 ErrPlanGone，其余外键报 ErrForeignKey，唯一冲突报 ErrDuplicate。
func TestConstraintErrsTable(t *testing.T) {
	if got := constraintErrs.ByName[fkCouponPlansPlan]; got != ErrPlanGone {
		t.Fatalf("coupon_plans 套餐外键: got %v", got)
	}
	if got := constraintErrs.ByCode[db.CodeForeignKey]; got != ErrForeignKey {
		t.Fatalf("其余外键: got %v", got)
	}
	if got := constraintErrs.ByCode[db.CodeUnique]; got != ErrDuplicate {
		t.Fatalf("唯一冲突: got %v", got)
	}

	// 拿不到约束名、只有 GORM 哨兵时按码报
	got := mapErr(gorm.ErrForeignKeyViolated)
	if !errors.Is(got, ErrForeignKey) || errors.Is(got, ErrPlanGone) {
		t.Fatalf("仅 GORM FK: got %v", got)
	}

	if mapErr(nil) != nil {
		t.Fatal("nil 应返回 nil")
	}
	other := errors.New("其它错误")
	if got := mapErr(other); got != other {
		t.Fatalf("其它错误应原样返回: %v", got)
	}
}
