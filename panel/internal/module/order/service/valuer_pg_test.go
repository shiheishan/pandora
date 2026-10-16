package service

import (
	"testing"
)

// RemainingValue 与换套餐报价同一份折算（付费时长用掉两成 → 剩 80.00）；HasPaidOrder 只认应付大于 0 的已付单（余额全额付不算）。
func TestRemainingValueAndPaidHistory(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 10000, 20)
	e.usePaidTime(t, ctx, sub.ID, 1, 5)

	v, gift, err := e.svc.RemainingValue(ctx, sub.ID, e.svc.clock())
	if err != nil {
		t.Fatal(err)
	}
	mustEq(t, "剩余价值", v, dec("80"))
	if gift != 0 {
		t.Fatalf("赠送天数 %d", gift)
	}

	if paid, err := e.svc.HasPaidOrder(ctx, user); err != nil || paid {
		t.Fatalf("余额全额付不算付过费: %v %v", paid, err)
	}
	planID, _, priceID := e.mkPlan(t, ctx, 500, "month")
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	if paid, err := e.svc.HasPaidOrder(ctx, user); err != nil || paid {
		t.Fatalf("待支付不算付过费: %v %v", paid, err)
	}
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	e.payAndNotify(t, ctx, user, c.OrderID, code, "pay-"+randHex(8))
	if paid, err := e.svc.HasPaidOrder(ctx, user); err != nil || !paid {
		t.Fatalf("渠道付过款应算付过费: %v %v", paid, err)
	}
}
