package service

import (
	"testing"

	"github.com/google/uuid"
)

func TestVersionsInUsePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()

	planA, verActive := newPlan(t, e, ctx)
	planB, verExpired := newPlan(t, e, ctx)
	planC, verPastEnd := newPlan(t, e, ctx)

	// 生效订阅
	u1 := newUser(t, e, ctx)
	_ = provision(t, e, ctx, u1, planA, verActive, "生效")

	// 已过期订阅（status = 'expired'）
	u2 := newUser(t, e, ctx)
	subExpired := provision(t, e, ctx, u2, planB, verExpired, "已过期状态")
	e.exec(t, ctx, `UPDATE subscriptions SET status = 'expired' WHERE id = ?`, subExpired.ID)

	// 到期时间已过但 status 还是 active 的订阅
	u3 := newUser(t, e, ctx)
	subPastEnd := provision(t, e, ctx, u3, planC, verPastEnd, "周期已过")
	e.exec(t, ctx, `UPDATE subscriptions SET current_period_start = now() - interval '2 days',
		current_period_end = now() - interval '1 hour' WHERE id = ?`, subPastEnd.ID)

	inUse, err := e.svc.VersionsInUse(ctx, []uuid.UUID{verActive, verExpired, verPastEnd})
	if err != nil {
		t.Fatal(err)
	}
	if !inUse[verActive] {
		t.Fatalf("verActive %s 应在结果中", verActive)
	}
	if inUse[verExpired] {
		t.Fatalf("verExpired %s 不应在结果中", verExpired)
	}
	if inUse[verPastEnd] {
		t.Fatalf("verPastEnd %s 不应在结果中", verPastEnd)
	}
	if len(inUse) != 1 {
		t.Fatalf("期望结果仅有 1 个版本，实际: %v", inUse)
	}

	// 空输入
	empty, err := e.svc.VersionsInUse(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("空输入应返回空 map，实际: %v", empty)
	}
}
