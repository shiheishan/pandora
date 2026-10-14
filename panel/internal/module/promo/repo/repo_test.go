package repo

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestMapErrForeignKeyByConstraint(t *testing.T) {
	planFK := &pgconn.PgError{Code: "23503", ConstraintName: fkCouponPlansPlan}
	userGrpFK := &pgconn.PgError{Code: "23503", ConstraintName: "coupons_user_group_id_fkey"}

	got := mapErr(planFK)
	if !errors.Is(got, ErrPlanGone) {
		t.Fatalf("原始 PgError plan FK: got %v", got)
	}
	if errors.Is(got, ErrForeignKey) {
		t.Fatal("plan FK 不应判为 ErrForeignKey")
	}

	wrappedPlan := fmt.Errorf("%w: %w", gorm.ErrForeignKeyViolated, planFK)
	got = mapErr(wrappedPlan)
	if !errors.Is(got, ErrPlanGone) {
		t.Fatalf("GORM 包装的 plan FK: got %v", got)
	}
	if errors.Is(got, ErrForeignKey) {
		t.Fatal("GORM 包装的 plan FK 不应判为 ErrForeignKey")
	}

	got = mapErr(userGrpFK)
	if !errors.Is(got, ErrForeignKey) {
		t.Fatalf("user_group FK: got %v", got)
	}
	if errors.Is(got, ErrPlanGone) {
		t.Fatal("user_group FK 不应判为 ErrPlanGone")
	}

	got = mapErr(gorm.ErrForeignKeyViolated)
	if !errors.Is(got, ErrForeignKey) {
		t.Fatalf("仅 GORM FK: got %v", got)
	}

	got = mapErr(&pgconn.PgError{Code: "23505"})
	if !errors.Is(got, ErrDuplicate) {
		t.Fatalf("23505: got %v", got)
	}

	if mapErr(nil) != nil {
		t.Fatal("nil 应返回 nil")
	}

	other := errors.New("其它错误")
	if mapErr(other) != other {
		t.Fatalf("其它错误应原样返回: %v", mapErr(other))
	}
}
