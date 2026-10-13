package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type fakeOnline struct {
	counts map[uuid.UUID]int
	err    error
	calls  int
	got    []uuid.UUID
}

func (f *fakeOnline) OnlineDeviceCounts(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	f.calls++
	f.got = append([]uuid.UUID(nil), ids...)
	if f.err != nil {
		return nil, f.err
	}
	return f.counts, nil
}

func TestMySubscriptionsOnlineDevicesPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	a := provision(t, e, ctx, user, planID, versionID, "")
	b := provision(t, e, ctx, user, planID, versionID, "")
	fake := &fakeOnline{counts: map[uuid.UUID]int{a.ID: 3}}
	e.svc.UseOnlineDeviceCounter(fake)
	mine, err := e.svc.MySubscriptions(userCtx(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Subscriptions) != 2 {
		t.Fatalf("订阅数: %d", len(mine.Subscriptions))
	}
	for _, s := range mine.Subscriptions {
		switch s.ID {
		case a.ID:
			if s.OnlineDevices != 3 {
				t.Fatalf("a 在线数: %d", s.OnlineDevices)
			}
		case b.ID:
			if s.OnlineDevices != 0 {
				t.Fatalf("b 在线数: %d", s.OnlineDevices)
			}
		default:
			t.Fatalf("未知订阅 %s", s.ID)
		}
	}
	if fake.calls != 1 {
		t.Fatalf("调用次数: %d", fake.calls)
	}
	if len(fake.got) != 2 {
		t.Fatalf("入参 id 数: %d", len(fake.got))
	}
	seen := map[uuid.UUID]bool{fake.got[0]: true, fake.got[1]: true}
	if !seen[a.ID] || !seen[b.ID] {
		t.Fatalf("入参: %v", fake.got)
	}
}

func TestMySubscriptionsOnlineDevicesErrorPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	provision(t, e, ctx, user, planID, versionID, "")
	provision(t, e, ctx, user, planID, versionID, "")
	e.svc.UseOnlineDeviceCounter(&fakeOnline{err: errors.New("boom")})
	mine, err := e.svc.MySubscriptions(userCtx(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Subscriptions) != 2 {
		t.Fatalf("订阅数: %d", len(mine.Subscriptions))
	}
	for _, s := range mine.Subscriptions {
		if s.OnlineDevices != 0 {
			t.Fatalf("%s 在线数应为 0: %d", s.ID, s.OnlineDevices)
		}
	}
}

func TestMySubscriptionsNoCounterPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	provision(t, e, ctx, user, planID, versionID, "")
	mine, err := e.svc.MySubscriptions(userCtx(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Subscriptions) != 1 || mine.Subscriptions[0].OnlineDevices != 0 {
		t.Fatalf("%+v", mine)
	}
}

func TestSubscriptionsByIDsPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	a := provision(t, e, ctx, user, planID, versionID, "")
	empty, err := e.svc.SubscriptionsByIDs(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("空入参: %+v", empty)
	}
	m, err := e.svc.SubscriptionsByIDs(ctx, []uuid.UUID{a.ID, uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Fatalf("结果数: %d", len(m))
	}
	sub, ok := m[a.ID]
	if !ok {
		t.Fatal("缺少 a")
	}
	if sub.UserID != a.UserID || sub.PlanID != a.PlanID || sub.PlanVersionID != a.PlanVersionID {
		t.Fatalf("字段: %+v vs %+v", sub, a)
	}
}
