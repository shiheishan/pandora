package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// refFor 在巡检给出的收款里找某订单那一条。
func refFor(refs []integrity.OrderRef, orderID uuid.UUID) (integrity.OrderRef, bool) {
	for _, r := range refs {
		if r.OrderID == orderID {
			return r, true
		}
	}
	return integrity.OrderRef{}, false
}

// 巡检「收款了订单却没付」的 payment 一半：窗口里正常入账的收款给出来，进了挂账的、窗口外的不给。
func TestSettledPaymentsSincePG(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-integrity-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)

	pay := func(orderID uuid.UUID, orderNo, suffix string) {
		t.Helper()
		intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")
		in := fakeInbound("evt-integrity-"+suffix, "payment.succeeded", "pay-integrity-"+suffix, intent.OutTradeNo, 990)
		ack, err := e.svc.HandleWebhook(ctx, code, in)
		if err != nil || ack.HTTPStatus != http.StatusOK {
			t.Fatalf("回调 %s: err=%v ack=%s", suffix, err, string(ack.Body))
		}
	}

	okOrder, okNo := e.mkOrder(t, ctx, user)
	pay(okOrder, okNo, "ok-"+uuid.NewString()[:8])

	lateOrder, lateNo := e.mkOrder(t, ctx, user)
	e.settler.mu.Lock()
	e.settler.notPayable[lateOrder] = NotPayable(CaseArchivedPlan, archivedOrderStatus)
	e.settler.mu.Unlock()
	pay(lateOrder, lateNo, "late-"+uuid.NewString()[:8])

	refs, err := e.svc.SettledPaymentsSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := refFor(refs, okOrder)
	if !ok {
		t.Fatalf("正常入账的收款应给出 %v", refs)
	}
	var paymentID uuid.UUID
	if err := e.row(ctx, `SELECT id FROM payments WHERE order_id = ?`, okOrder).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if r.ID != paymentID {
		t.Fatalf("ID 应是收款 id %s，得到 %s", paymentID, r.ID)
	}
	if _, ok := refFor(refs, lateOrder); ok {
		t.Fatal("进了挂账的收款不该给出")
	}

	refs, err = e.svc.SettledPaymentsSince(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := refFor(refs, okOrder); ok {
		t.Fatal("窗口外的收款不该给出")
	}
}
