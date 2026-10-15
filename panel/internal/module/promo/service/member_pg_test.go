package service

import (
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestMemberDiscountPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	user := newUser(t, e, ctx)

	// 1. 没设置时 MemberPercent 回 0、MemberDiscountOf.Percent 为 nil
	p, err := e.svc.MemberPercent(ctx, user)
	if err != nil {
		t.Fatalf("MemberPercent: %v", err)
	}
	if p != 0 {
		t.Fatalf("未设置时 MemberPercent=%d, 期望 0", p)
	}
	view, err := e.svc.MemberDiscountOf(actx, user)
	if err != nil {
		t.Fatalf("MemberDiscountOf: %v", err)
	}
	if view.Percent != nil {
		t.Fatalf("未设置时 Percent=%v, 期望 nil", view.Percent)
	}
	if view.UserID != user {
		t.Fatalf("UserID=%v, 期望 %v", view.UserID, user)
	}

	// 2. Set 10 后 MemberPercent 回 10、UpdatedBy 是当前管理员；再 Set 25 覆盖成 25
	v1, err := e.svc.SetMemberDiscount(actx, user, 10)
	if err != nil {
		t.Fatalf("SetMemberDiscount(10): %v", err)
	}
	if v1.Percent == nil || *v1.Percent != 10 {
		t.Fatalf("SetMemberDiscount 返回 percent=%v, 期望 10", v1.Percent)
	}
	if v1.UpdatedBy == nil || *v1.UpdatedBy != admin {
		t.Fatalf("UpdatedBy=%v, 期望 admin %v", v1.UpdatedBy, admin)
	}
	p1, err := e.svc.MemberPercent(ctx, user)
	if err != nil {
		t.Fatalf("MemberPercent: %v", err)
	}
	if p1 != 10 {
		t.Fatalf("MemberPercent=%d, 期望 10", p1)
	}

	// 覆盖成 25
	v2, err := e.svc.SetMemberDiscount(actx, user, 25)
	if err != nil {
		t.Fatalf("SetMemberDiscount(25): %v", err)
	}
	if v2.Percent == nil || *v2.Percent != 25 {
		t.Fatalf("SetMemberDiscount 返回 percent=%v, 期望 25", v2.Percent)
	}
	p2, err := e.svc.MemberPercent(ctx, user)
	if err != nil {
		t.Fatalf("MemberPercent: %v", err)
	}
	if p2 != 25 {
		t.Fatalf("MemberPercent=%d, 期望 25", p2)
	}

	// 3. Set 0、101 回 422；不存在的用户回 NotFound
	for _, badPercent := range []int{0, 101} {
		_, err := e.svc.SetMemberDiscount(actx, user, badPercent)
		if errCode(err) != errs.ValidationFailed {
			t.Fatalf("SetMemberDiscount(%d) 期望 422 invalid, 实际 %v", badPercent, err)
		}
	}
	nonExistentUser := uuid.New()
	_, err = e.svc.SetMemberDiscount(actx, nonExistentUser, 10)
	if errCode(err) != errs.NotFound {
		t.Fatalf("不存在的用户 Set 期望 NotFound, 实际 %v", err)
	}
	_, err = e.svc.MemberDiscountOf(actx, nonExistentUser)
	if errCode(err) != errs.NotFound {
		t.Fatalf("不存在的用户 Of 期望 NotFound, 实际 %v", err)
	}

	// 4. Clear 后回 0；再 Clear 一次不报错
	if err := e.svc.ClearMemberDiscount(actx, user); err != nil {
		t.Fatalf("ClearMemberDiscount: %v", err)
	}
	pAfterClear, err := e.svc.MemberPercent(ctx, user)
	if err != nil {
		t.Fatalf("MemberPercent after clear: %v", err)
	}
	if pAfterClear != 0 {
		t.Fatalf("Clear 后 MemberPercent=%d, 期望 0", pAfterClear)
	}
	// 再 Clear 一次（幂等）
	if err := e.svc.ClearMemberDiscount(actx, user); err != nil {
		t.Fatalf("第二次 ClearMemberDiscount: %v", err)
	}

	// 5. 审计断言：Set 与 Clear 的审计记录数
	var setCount int64
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, ActionMemberDiscountSet, user).Scan(&setCount); err != nil {
		t.Fatal(err)
	}
	if setCount != 2 {
		t.Fatalf("ActionMemberDiscountSet 审计条数 %d, 期望 2", setCount)
	}

	var clearCount int64
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, ActionMemberDiscountCleared, user).Scan(&clearCount); err != nil {
		t.Fatal(err)
	}
	if clearCount != 1 {
		t.Fatalf("ActionMemberDiscountCleared 审计条数 %d, 期望 1", clearCount)
	}
}
