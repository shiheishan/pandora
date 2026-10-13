package service

import (
	"testing"
	"time"
)

func TestReportOrderCountsPG(t *testing.T) {
	e, ctx := newEnv(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	since := now.Add(-1 * time.Hour)
	staleBefore := now.Add(-30 * time.Minute)

	// 先在本事务里把库里已有的过期待付单释放掉，后面的 ExpireDue 只会动本用例的单，增量才对得上
	if _, err := e.svc.ExpireDue(ctx, 0); err != nil {
		t.Fatal(err)
	}
	c0, err := e.svc.ReportOrderCounts(ctx, since, staleBefore)
	if err != nil {
		t.Fatal(err)
	}

	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month", nil, nil)

	stalePending := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	e.mustExec(t, ctx, `UPDATE orders SET created_at = ? WHERE id = ?`, now.Add(-2*time.Hour), stalePending.OrderID)

	e.buyNew(t, ctx, user, planID, priceID, Checkout{})

	e.giveBalance(t, ctx, user, 1000)
	fulfilled := e.buyNew(t, ctx, user, planID, priceID, Checkout{UseBalance: dec("10")})
	if fulfilled.Status != StatusFulfilled {
		t.Fatalf("履约单状态 %s", fulfilled.Status)
	}

	expiring := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	e.mustExec(t, ctx, `UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = ?`, expiring.OrderID)
	e.mustExec(t, ctx, `UPDATE order_reservations SET expires_at = now() - interval '1 minute' WHERE order_id = ?`, expiring.OrderID)
	stats, err := e.svc.ExpireDue(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Expired < 1 {
		t.Fatalf("超时释放统计 %+v", stats)
	}
	if st := e.order(t, ctx, expiring.OrderID).Status; st != StatusExpired {
		t.Fatalf("过期单状态 %s", st)
	}

	c1, err := e.svc.ReportOrderCounts(ctx, since, staleBefore)
	if err != nil {
		t.Fatal(err)
	}

	if c1.PaidSince-c0.PaidSince != 1 {
		t.Fatalf("PaidSince 增量 %d，期望 1", c1.PaidSince-c0.PaidSince)
	}
	if c1.Pending-c0.Pending != 2 {
		t.Fatalf("Pending 增量 %d，期望 2", c1.Pending-c0.Pending)
	}
	if c1.ClosedSince-c0.ClosedSince != 1 {
		t.Fatalf("ClosedSince 增量 %d，期望 1", c1.ClosedSince-c0.ClosedSince)
	}
	if c1.PendingStale-c0.PendingStale != 1 {
		t.Fatalf("PendingStale 增量 %d，期望 1", c1.PendingStale-c0.PendingStale)
	}
}
