package service

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestAdminSubsPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	now := time.Now().UTC().Truncate(time.Microsecond)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})

	userA := newUser(t, e, ctx)
	liveSub := provision(t, e, ctx, userA, planID, versionID, "生效")
	oldSub := provision(t, e, ctx, userA, planID, versionID, "过期")
	expireSetPeriod(t, e, ctx, oldSub.ID, StatusExpired, now.Add(-48*time.Hour))

	userB := newUser(t, e, ctx)
	expiredOnly := provision(t, e, ctx, userB, planID, versionID, "")
	expireSetPeriod(t, e, ctx, expiredOnly.ID, StatusExpired, now.Add(-72*time.Hour))

	userC := newUser(t, e, ctx)

	consumed := int64(42 * gib)
	setConsumed(t, e, ctx, liveSub.ID, MetricTraffic, PeriodCycle, consumed)

	fake := &fakeOnline{counts: map[uuid.UUID]int{liveSub.ID: 2}}
	e.svc.UseOnlineDeviceCounter(fake)

	wantAmount := monthly().Amount
	m, err := e.svc.AdminSubs(ctx, []uuid.UUID{userA, userB, userC})
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Fatalf("结果用户数: %d", len(m))
	}

	// A：两份订阅，Current 为生效那份
	ua := m[userA]
	if ua.Subscriptions == nil {
		t.Fatal("A Subscriptions 为 nil")
	}
	if len(ua.Subscriptions) != 2 {
		t.Fatalf("A 订阅数: %d", len(ua.Subscriptions))
	}
	if ua.Current == nil {
		t.Fatal("A Current 应为生效订阅")
	}
	if ua.Current.ID != liveSub.ID {
		t.Fatalf("A Current id: %s want %s", ua.Current.ID, liveSub.ID)
	}
	if !ua.Current.Live {
		t.Fatal("A Current 应 Live")
	}
	if ua.Current.OnlineDevices != 2 {
		t.Fatalf("A Current 在线数: %d", ua.Current.OnlineDevices)
	}
	if !ua.Current.Amount.Equal(wantAmount) {
		t.Fatalf("A Current Amount: %s want %s", ua.Current.Amount, wantAmount)
	}
	// 套餐只有一条 cycle 流量配额：第一条 traffic.bytes 就是本期那一行
	var foundConsumed bool
	for _, q := range ua.Current.Quotas {
		if q.Metric == MetricTraffic {
			if q.Consumed != consumed {
				t.Fatalf("A 流量 Consumed: %d want %d", q.Consumed, consumed)
			}
			foundConsumed = true
			break
		}
	}
	if !foundConsumed {
		t.Fatal("A Current 缺少流量配额")
	}

	// B：一份过期
	ub := m[userB]
	if ub.Subscriptions == nil || len(ub.Subscriptions) != 1 {
		t.Fatalf("B 订阅: %+v", ub.Subscriptions)
	}
	if ub.Current == nil {
		t.Fatal("B 应有 Current")
	}
	if ub.Current.ID != expiredOnly.ID {
		t.Fatalf("B Current id: %s", ub.Current.ID)
	}
	if ub.Current.Live {
		t.Fatal("B Current 不应 Live")
	}
	if ub.Current.Status != StatusExpired {
		t.Fatalf("B Status: %s", ub.Current.Status)
	}
	if !ub.Current.Amount.Equal(decimal.RequireFromString("10.00")) {
		t.Fatalf("B Amount: %s", ub.Current.Amount)
	}

	// C：无订阅
	uc := m[userC]
	if uc.Subscriptions == nil {
		t.Fatal("C Subscriptions 为 nil")
	}
	if len(uc.Subscriptions) != 0 {
		t.Fatalf("C 订阅数: %d", len(uc.Subscriptions))
	}
	if uc.Current != nil {
		t.Fatalf("C Current: %+v", uc.Current)
	}

	if fake.calls != 1 {
		t.Fatalf("OnlineDeviceCounts 调用次数: %d", fake.calls)
	}

	// 计数口报错：整页不失败，在线数为 0
	e2 := newEnv(t)
	ctx2 := e2.txCtx()
	u := newUser(t, e2, ctx2)
	p, v := newPlan(t, e2, ctx2, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	provision(t, e2, ctx2, u, p, v, "")
	e2.svc.UseOnlineDeviceCounter(&fakeOnline{err: errors.New("boom")})
	m2, err := e2.svc.AdminSubs(ctx2, []uuid.UUID{u})
	if err != nil {
		t.Fatal(err)
	}
	if len(m2[u].Subscriptions) != 1 || m2[u].Subscriptions[0].OnlineDevices != 0 {
		t.Fatalf("报错降级: %+v", m2[u])
	}
}
