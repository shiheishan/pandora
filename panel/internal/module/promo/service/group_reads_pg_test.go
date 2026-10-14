package service

import (
	"testing"
)

func TestCouponCountsByGroupPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)

	g1 := newGroup(t, e, ctx)
	g2 := newGroup(t, e, ctx)

	// 给 g1 建两张限定券：一张正常生效，一张停用（paused 也算限定）
	spec1 := fixedSpec("1.00")
	spec1.UserGroupID = &g1
	if _, _, err := e.svc.CreateCoupon(actx, randCode("G1A"), spec1); err != nil {
		t.Fatal(err)
	}

	spec2 := fixedSpec("2.00")
	spec2.UserGroupID = &g1
	c2ID, _, err := e.svc.CreateCoupon(actx, randCode("G1B"), spec2)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetStatus(actx, c2ID, StatusPaused); err != nil {
		t.Fatal(err)
	}

	// g2 不建限定券

	counts, err := e.svc.CouponCountsByGroup(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if n := counts[g1]; n != 2 {
		t.Fatalf("g1 期望限定券数量为 2，实际为 %d", n)
	}

	if _, exists := counts[g2]; exists {
		t.Fatalf("g2 期望不在结果里，实际存在: %d", counts[g2])
	}
}
