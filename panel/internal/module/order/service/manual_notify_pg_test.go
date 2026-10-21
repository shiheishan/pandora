package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// spyPendingNotifier 记录 ManualPendingCreated 调用，供人工待付单通知测试用。
type spyPendingNotifier struct {
	calls []PendingNotice
	err   error
}

func (s *spyPendingNotifier) ManualPendingCreated(_ context.Context, n PendingNotice) error {
	s.calls = append(s.calls, n)
	return s.err
}

func TestManualPendingNotifiesUser(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month")
	spy := &spyPendingNotifier{}
	e.svc.UsePendingNotifier(spy)
	before := time.Now().UTC()
	c, err := e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: user, PlanID: planID, PriceID: priceID, Reason: manualReason, Settlement: SettlementPending,
	})
	if err != nil {
		t.Fatalf("待付开单: %v", err)
	}
	if c.Status != StatusPending {
		t.Fatalf("状态 %s", c.Status)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("通知 %d 次", len(spy.calls))
	}
	n := spy.calls[0]
	if n.OrderNo != c.OrderNo || n.OrderID != c.OrderID || n.UserID != user || n.PlanID != planID {
		t.Fatalf("通知 %+v", n)
	}
	mustEq(t, "Amount", n.Amount, c.Payable)
	var expiresAt time.Time
	if err := e.row(ctx, `SELECT expires_at FROM orders WHERE id = ?`, c.OrderID).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	// PG timestamptz 只到微秒（四舍五入），Linux 上 Go 的时刻带纳秒：差不到 1 微秒算同一时刻
	if d := n.ExpiresAt.Sub(expiresAt); d >= time.Microsecond || d <= -time.Microsecond {
		t.Fatalf("ExpiresAt 通知 %s 库 %s", n.ExpiresAt, expiresAt)
	}
	if !expiresAt.After(before) {
		t.Fatalf("付款期限 %s 应晚于开单前", expiresAt)
	}
}

func TestManualGrantAndOfflineDoNotNotify(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month")
	spy := &spyPendingNotifier{}
	e.svc.UsePendingNotifier(spy)
	if _, err := e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: user, PlanID: planID, PriceID: priceID, Reason: "故障补偿，赠送一个月",
	}); err != nil {
		t.Fatalf("赠送: %v", err)
	}
	if _, err := e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: user, PlanID: planID, PriceID: priceID,
		Reason: "  " + manualReason + "  ", Settlement: SettlementOffline, Reference: " BANK-NOTIFY-1 ",
	}); err != nil {
		t.Fatalf("线下: %v", err)
	}
	if len(spy.calls) != 0 {
		t.Fatalf("赠送与线下不该通知，有 %d 次", len(spy.calls))
	}
}

func TestManualPendingNotifierError(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month")
	boom := errors.New("boom")
	spy := &spyPendingNotifier{err: boom}
	e.svc.UsePendingNotifier(spy)
	_, err := e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: user, PlanID: planID, PriceID: priceID, Reason: manualReason, Settlement: SettlementPending,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("通知失败应回滚: %v", err)
	}
}
