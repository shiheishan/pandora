package service

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPeriodOrderIDsPG：本周期的付费来源订单从最近一次重起算起——
// 开通 → 提前续费 → 过期后续回（重起）→ 提前续费 → 过期后礼品卡救回（重起、无订单）→ 提前续费 →
// 到期前下单、回调晚到（不重起）。
func TestPeriodOrderIDsPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})

	expect := func(subID uuid.UUID, want ...uuid.UUID) {
		t.Helper()
		got, err := e.svc.PeriodOrderIDs(ctx, subID)
		if err != nil {
			t.Fatal(err)
		}
		if want == nil {
			want = []uuid.UUID{}
		}
		if got == nil || !slices.Equal(got, want) {
			t.Fatalf("本周期订单 %v want %v", got, want)
		}
	}
	renew := func(subID uuid.UUID) uuid.UUID {
		t.Helper()
		oid := newOrder(t, e, ctx, user, "renewal", &subID)
		paid := time.Now().UTC().Truncate(time.Microsecond)
		if _, err := e.svc.Renew(ctx, Renewal{
			SubscriptionID: subID, UserID: user, Price: monthly(), PaidAt: paid, OrderCreatedAt: &paid,
			OrderID: &oid, Actor: Actor{Kind: ActorPayment}, Source: "order",
		}); err != nil {
			t.Fatal(err)
		}
		return oid
	}

	o1 := newOrder(t, e, ctx, user, "new", nil)
	sub, err := e.svc.Provision(ctx, Provision{
		UserID: user, PlanID: planID, PlanVersionID: versionID, Price: monthly(),
		SourceOrderID: &o1, Actor: Actor{Kind: ActorPayment},
	})
	if err != nil {
		t.Fatal(err)
	}
	expect(sub.ID, o1)
	o2 := renew(sub.ID)
	expect(sub.ID, o1, o2)

	// 过期后续回：renewed 事件 payload.restart = true，周期重起
	lapse(t, e, ctx, sub.ID, 3)
	o3 := renew(sub.ID)
	expect(sub.ID, o3)
	o4 := renew(sub.ID)
	expect(sub.ID, o3, o4)

	// 过期后礼品卡救回：extended 事件 payload.restart = true，周期重起、没有订单
	lapse(t, e, ctx, sub.ID, 2)
	if _, err := e.svc.Extend(ctx, Extension{
		SubscriptionID: sub.ID, UserID: &user, Days: 7,
		Actor: Actor{Kind: ActorUser, ID: &user}, Source: ExtendSourceGiftCard,
	}); err != nil {
		t.Fatal(err)
	}
	expect(sub.ID)
	o5 := renew(sub.ID)
	expect(sub.ID, o5)

	// 未到期的延期不重起
	if _, err := e.svc.Extend(ctx, Extension{
		SubscriptionID: sub.ID, Days: 1, Actor: Actor{Kind: ActorUser, ID: &user}, Source: ExtendSourceGiftCard,
	}); err != nil {
		t.Fatal(err)
	}
	expect(sub.ID, o5)

	// 到期前下单、回调晚到：过期扫描已把状态改成 expired，renewed 事件的 from_status 是 expired，
	// 但 RenewalBase 接在原到期之后（payload.restart = false），不是重起，之前的订单仍在本周期
	oldEnd := lapse(t, e, ctx, sub.ID, 1)
	o6 := newOrder(t, e, ctx, user, "renewal", &sub.ID)
	created := oldEnd.Add(-time.Hour)
	paid := time.Now().UTC().Truncate(time.Microsecond)
	res, err := e.svc.Renew(ctx, Renewal{
		SubscriptionID: sub.ID, UserID: user, Price: monthly(), PaidAt: paid, OrderCreatedAt: &created,
		OrderID: &o6, Actor: Actor{Kind: ActorPayment}, Source: "order",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Restarted || res.FromStatus != StatusExpired {
		t.Fatalf("回调晚到应接在原到期之后：restarted=%v from=%s", res.Restarted, res.FromStatus)
	}
	expect(sub.ID, o5, o6)

	// 批量版：每份都有一项，没有订单的是空切片
	other := uuid.New()
	m, err := e.svc.PeriodOrderIDsBySubscriptions(ctx, []uuid.UUID{sub.ID, other})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m[sub.ID], []uuid.UUID{o5, o6}) || m[other] == nil || len(m[other]) != 0 {
		t.Fatalf("批量结果 %v", m)
	}

	// 别的订阅、不存在的订阅：空
	expect(uuid.New())
}
