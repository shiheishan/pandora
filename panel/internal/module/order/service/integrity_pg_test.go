package service

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// 巡检的计数是全库的（库里可能有别的包提交的行），用例只比前后差，样例按本例建的 id 找。

func checkDelta(t *testing.T, what string, before, after integrity.Result, want int64, id uuid.UUID) {
	t.Helper()
	if after.Count-before.Count != want {
		t.Fatalf("%s：违例数 %d → %d，期望多 %d", what, before.Count, after.Count, want)
	}
	if want > 0 && !slices.Contains(after.Sample, id.String()) {
		t.Fatalf("%s：样例 %v 里没有 %s", what, after.Sample, id)
	}
	if want == 0 && slices.Contains(after.Sample, id.String()) {
		t.Fatalf("%s：正常订单 %s 不该出现在样例 %v", what, id, after.Sample)
	}
}

func (e *env) mustCheck(t *testing.T, ctx context.Context, f func(context.Context) (integrity.Result, error)) integrity.Result {
	t.Helper()
	r, err := f(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// 停在 paid 的订单算「已付未落地」；正常的待付单不算。
func TestCheckPaidUnfulfilledPG(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month")

	before := e.mustCheck(t, ctx, e.svc.CheckPaidUnfulfilled)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	checkDelta(t, "待付单", before, e.mustCheck(t, ctx, e.svc.CheckPaidUnfulfilled), 0, c.OrderID)

	// 模拟履约那一步被绕过：只改成 paid（满足 orders_paid_shape），不转 fulfilled
	e.mustExec(t, ctx, `UPDATE orders SET status = 'paid', paid_amount = total_amount, paid_at = now() WHERE id = ?`, c.OrderID)
	checkDelta(t, "停在 paid", before, e.mustCheck(t, ctx, e.svc.CheckPaidUnfulfilled), 1, c.OrderID)
}

// 预留与订单状态脱节：held 预留挂在已取消的单上、待付单的预留不是 held，各算一条；正常待付单与正常取消都不算。
func TestCheckReservationsPG(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month")

	before := e.mustCheck(t, ctx, e.svc.CheckReservations)
	cancelled := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	if _, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: cancelled.OrderID}); err != nil {
		t.Fatalf("取消: %v", err)
	}
	pending := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	checkDelta(t, "正常待付与正常取消", before, e.mustCheck(t, ctx, e.svc.CheckReservations), 0, cancelled.OrderID)

	// 订单已取消，预留却回到 held（券与冻结一直占着）
	e.mustExec(t, ctx, `UPDATE order_reservations SET state = 'held', released_at = NULL, release_reason = NULL WHERE order_id = ?`,
		cancelled.OrderID)
	checkDelta(t, "取消单挂着 held 预留", before, e.mustCheck(t, ctx, e.svc.CheckReservations), 1, cancelled.OrderID)

	// 待付单的预留已被收走
	e.mustExec(t, ctx, `UPDATE order_reservations SET state = 'captured', captured_at = now() WHERE order_id = ?`, pending.OrderID)
	after := e.mustCheck(t, ctx, e.svc.CheckReservations)
	if after.Count-before.Count != 2 || !slices.Contains(after.Sample, pending.OrderID.String()) {
		t.Fatalf("待付单预留不是 held：%d → %d，样例 %v", before.Count, after.Count, after.Sample)
	}
}

// UnsettledAmong 只回没收到钱的订单：paid / fulfilled 与不存在的 id 不回。
func TestUnsettledAmongPG(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month")
	paid := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	e.mustExec(t, ctx, `UPDATE orders SET status = 'paid', paid_amount = total_amount, paid_at = now() WHERE id = ?`, paid.OrderID)
	pending := e.buyNew(t, ctx, user, planID, priceID, Checkout{})

	got, err := e.svc.UnsettledAmong(ctx, []uuid.UUID{paid.OrderID, pending.OrderID, uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != pending.OrderID {
		t.Fatalf("应只回待付单 %s，得到 %v", pending.OrderID, got)
	}
	if got, err := e.svc.UnsettledAmong(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("空入参 got=%v err=%v", got, err)
	}
}
