package service

import (
	"testing"

	"github.com/google/uuid"
)

// LivePlanIDs 只回生效中订阅的套餐，去重；已到期的、别人的不算。
func TestLivePlanIDsPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	other := newUser(t, e, ctx)
	planA, verA := newPlan(t, e, ctx)
	planB, verB := newPlan(t, e, ctx)
	planC, verC := newPlan(t, e, ctx)

	provision(t, e, ctx, user, planA, verA, "a1")
	provision(t, e, ctx, user, planA, verA, "a2")
	expired := provision(t, e, ctx, user, planB, verB, "b")
	e.exec(t, ctx, `UPDATE subscriptions SET status = 'expired' WHERE id = ?`, expired.ID)
	provision(t, e, ctx, other, planC, verC, "c")

	got, err := e.svc.LivePlanIDs(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != planA {
		t.Fatalf("LivePlanIDs = %v，want [%s]", got, planA)
	}
	none, err := e.svc.LivePlanIDs(ctx, uuid.New())
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("没有订阅的用户 = %v / %v，want 空切片", none, err)
	}
}
