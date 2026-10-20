package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// 巡检「佣金挂在没付的订单上」的 commission 一半：窗口里新产生的条目给出「条目 → 订单」，窗口外的不给。
func TestEntriesSincePG(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrder(t, e, ctx, buyer, "50.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatalf("计提: %v", err)
	}
	var entryID uuid.UUID
	if err := e.row(ctx, `SELECT id FROM commission_entries WHERE order_id = ?`, o.OrderID).Scan(&entryID); err != nil {
		t.Fatal(err)
	}

	refs, err := e.svc.EntriesSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range refs {
		if r.ID == entryID {
			found = true
			if r.OrderID != o.OrderID {
				t.Fatalf("条目 %s 的订单应是 %s，得到 %s", entryID, o.OrderID, r.OrderID)
			}
		}
	}
	if !found {
		t.Fatalf("窗口里的条目应给出 %v", refs)
	}

	refs, err = e.svc.EntriesSince(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refs {
		if r.ID == entryID {
			t.Fatal("窗口外的条目不该给出")
		}
	}
}
