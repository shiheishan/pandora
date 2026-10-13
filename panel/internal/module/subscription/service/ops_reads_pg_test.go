package service

import (
	"context"
	"slices"
	"testing"
	"time"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/google/uuid"
)

func TestOpsReadsPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	now := time.Now().UTC().Truncate(time.Microsecond)

	planP1, verP1 := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	planP2, verP2 := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})

	userA := newUser(t, e, ctx)
	userB := newUser(t, e, ctx)
	userC := newUser(t, e, ctx)

	liveA := provision(t, e, ctx, userA, planP1, verP1, "A-生效")
	expireSetPeriod(t, e, ctx, liveA.ID, StatusActive, now.Add(48*time.Hour))

	oldA := provision(t, e, ctx, userA, planP1, verP1, "A-旧")
	expireSetPeriod(t, e, ctx, oldA.ID, StatusExpired, now.Add(-72*time.Hour))

	expB := provision(t, e, ctx, userB, planP2, verP2, "B-过期")
	expireSetPeriod(t, e, ctx, expB.ID, StatusExpired, now.Add(-24*time.Hour))

	// 库里可能有别的用例提交的订阅，只比本用例的三个用户
	scope := []uuid.UUID{userA, userB, userC}
	assertUserFilter(t, e.svc, ctx, scope, identitysvc.SubFilter{State: "active"}, []uuid.UUID{userA}, false)
	assertUserFilter(t, e.svc, ctx, scope, identitysvc.SubFilter{State: "expired"}, []uuid.UUID{userB}, false)

	idsNone, exNone, err := e.svc.UserIDsBySubFilter(ctx, identitysvc.SubFilter{State: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if !exNone {
		t.Fatal("State none 应为 exclude=true")
	}
	if !slices.Contains(idsNone, userA) || !slices.Contains(idsNone, userB) {
		t.Fatalf("none 应含 A、B，得 %v", idsNone)
	}
	if slices.Contains(idsNone, userC) {
		t.Fatal("C 无订阅不应在 none 排除集里")
	}

	idsNoLive, exNoLive, err := e.svc.UserIDsBySubFilter(ctx, identitysvc.SubFilter{HasLive: ptrBool(false)})
	if err != nil {
		t.Fatal(err)
	}
	if !exNoLive {
		t.Fatal("HasLive=false 应为 exclude=true")
	}
	if !slices.Contains(idsNoLive, userA) {
		t.Fatalf("有生效订阅的 A 应在排除集，得 %v", idsNoLive)
	}

	assertUserFilter(t, e.svc, ctx, scope, identitysvc.SubFilter{PlanID: &planP1}, []uuid.UUID{userA}, false)

	idsContradict, exC, err := e.svc.UserIDsBySubFilter(ctx, identitysvc.SubFilter{State: "none", PlanID: &planP1})
	if err != nil {
		t.Fatal(err)
	}
	if exC || len(idsContradict) != 0 {
		t.Fatalf("State none + PlanID 应空且 exclude=false，得 ids=%v ex=%v", idsContradict, exC)
	}

	within := identitysvc.SubFilter{ExpiresWithinDays: 7}
	assertUserFilter(t, e.svc, ctx, scope, within, []uuid.UUID{userA}, false)

	outside := identitysvc.SubFilter{ExpiresWithinDays: 1}
	idsOut, _, err := e.svc.UserIDsBySubFilter(ctx, outside)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(idsOut, userA) {
		t.Fatalf("48h 到期不应落在 1 天窗口，得 %v", idsOut)
	}

	subs, err := e.svc.CurrentSubs(ctx, []uuid.UUID{userA, userB, userC})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := subs[userC]; ok {
		t.Fatal("C 不应在 CurrentSubs 结果里")
	}
	ca, ok := subs[userA]
	if !ok || !ca.Live || ca.LiveCount != 1 {
		t.Fatalf("A: ok=%v sub=%+v", ok, ca)
	}
	if ca.PlanName == "" {
		t.Fatal("A 应有套餐名")
	}
	if !ca.PeriodEnd.Equal(now.Add(48*time.Hour)) && ca.PeriodEnd.Sub(now.Add(48*time.Hour)).Abs() > time.Second {
		t.Fatalf("A 当前订阅到期应对齐生效份，得 %v", ca.PeriodEnd)
	}
	cb, ok := subs[userB]
	if !ok || cb.Live {
		t.Fatalf("B: ok=%v Live=%v", ok, cb.Live)
	}

	fetches, n, err := e.svc.UserFetches(ctx, userC)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(fetches) != 0 {
		t.Fatalf("无拉取记录应为空与 0，得 len=%d n=%d", len(fetches), n)
	}
}

// assertUserFilter 只看 scope 里的用户：结果与 want 在 scope 内一致。
func assertUserFilter(t *testing.T, svc *Service, ctx context.Context, scope []uuid.UUID, f identitysvc.SubFilter, want []uuid.UUID, wantEx bool) {
	t.Helper()
	all, ex, err := svc.UserIDsBySubFilter(ctx, f)
	if err != nil {
		t.Fatalf("UserIDsBySubFilter(%+v): %v", f, err)
	}
	var ids []uuid.UUID
	for _, id := range all {
		if slices.Contains(scope, id) {
			ids = append(ids, id)
		}
	}
	if ex != wantEx {
		t.Fatalf("exclude: want %v got %v (ids=%v)", wantEx, ex, ids)
	}
	if len(ids) != len(want) {
		t.Fatalf("ids: want %v got %v", want, ids)
	}
	for _, id := range want {
		if !slices.Contains(ids, id) {
			t.Fatalf("missing %v in %v", id, ids)
		}
	}
}
