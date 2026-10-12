package service

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestReserveRollsBackWithCaller(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := newUser(t, e, ctx)
	code := randCode("RBK")
	createCoupon(t, e, ctx, admin, code, fixedSpec("1.00"))
	u := newUser(t, e, ctx)
	o := order()

	sentinel := errors.New("调用方回滚")
	err := e.txm.Do(ctx, func(ctx context.Context) error {
		if _, err := e.svc.Reserve(ctx, code, target(u, "10.00"), o); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("期望 sentinel，得 %v", err)
	}

	var reserved, redeemed int
	if err := e.row(ctx,
		`SELECT reserved_count, redeemed_count FROM coupons WHERE code = ?`, code,
	).Scan(&reserved, &redeemed); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 || redeemed != 0 {
		t.Fatalf("券计数 reserved=%d redeemed=%d，期望 0、0", reserved, redeemed)
	}

	var n int64
	if err := e.row(ctx,
		`SELECT count(*) FROM coupon_redemptions WHERE order_id = ?`, o.ID,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("占用行数 %d，期望 0", n)
	}

	if _, err := e.svc.Reserve(ctx, code, target(u, "10.00"), order()); err != nil {
		t.Fatalf("回滚后应能再占用: %v", err)
	}
}

func TestEvaluateMatchesReserve(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)

	maxDisc := decimal.RequireFromString("5.00")
	pctSpec := percentSpec(1234)
	pctSpec.MaxDiscount = &maxDisc
	pctCode := randCode("EVL")
	createCoupon(t, e, ctx, admin, pctCode, pctSpec)

	fixCode := randCode("EVF")
	createCoupon(t, e, ctx, admin, fixCode, fixedSpec("3.30"))

	cases := []struct {
		code string
	}{
		{code: pctCode},
		{code: fixCode},
	}
	subtotals := []string{"33.33", "99.99", "1.00"}

	for _, c := range cases {
		for _, sub := range subtotals {
			u := newUser(t, e, ctx)
			tg := target(u, sub)

			a1, err := e.svc.Evaluate(ctx, c.code, tg)
			if err != nil {
				t.Fatalf("Evaluate %s @ %s: %v", c.code, sub, err)
			}
			if a1 == nil {
				t.Fatalf("Evaluate %s @ %s: nil", c.code, sub)
			}

			oref := order()
			a2, err := e.svc.Reserve(ctx, c.code, tg, oref)
			if err != nil {
				t.Fatalf("Reserve %s @ %s: %v", c.code, sub, err)
			}
			if a2 == nil {
				t.Fatalf("Reserve %s @ %s: nil", c.code, sub)
			}

			if !a1.Discount.Equal(a2.Discount) {
				t.Fatalf("折扣不一致 evaluate=%s reserve=%s", a1.Discount, a2.Discount)
			}
			if a1.Code != a2.Code {
				t.Fatalf("码不一致 %q vs %q", a1.Code, a2.Code)
			}
			if a1.CouponID != a2.CouponID {
				t.Fatalf("券 ID 不一致 %s vs %s", a1.CouponID, a2.CouponID)
			}

			var discText string
			if err := e.row(ctx,
				`SELECT discount_amount::text FROM coupon_redemptions WHERE order_id = ?`, oref.ID,
			).Scan(&discText); err != nil {
				t.Fatal(err)
			}
			stored := decimal.RequireFromString(discText)
			if !stored.Equal(a2.Discount) {
				t.Fatalf("库内折扣 %s 与 Reserve %s 不一致", stored, a2.Discount)
			}
		}
	}
}

func TestPausedHeldStillFinishes(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	code := randCode("PAU")

	id, _, err := e.svc.CreateCoupon(actx, code, fixedSpec("1.00"))
	if err != nil {
		t.Fatal(err)
	}

	u1 := newUser(t, e, ctx)
	u2 := newUser(t, e, ctx)
	u3 := newUser(t, e, ctx)
	o1 := order()
	o2 := order()

	if _, err := e.svc.Reserve(ctx, code, target(u1, "10.00"), o1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Reserve(ctx, code, target(u2, "10.00"), o2); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.SetStatus(actx, id, StatusPaused); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.Reserve(ctx, code, target(u3, "10.00"), order()); !errors.Is(err, ErrCouponNotFound) {
		t.Fatalf("暂停后新占用: %v", err)
	}

	if err := e.svc.Capture(ctx, o1.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Release(ctx, o2.ID, "超时"); err != nil {
		t.Fatal(err)
	}

	var reserved, redeemed int
	if err := e.row(ctx,
		`SELECT reserved_count, redeemed_count FROM coupons WHERE id = ?`, id,
	).Scan(&reserved, &redeemed); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 || redeemed != 1 {
		t.Fatalf("计数 reserved=%d redeemed=%d，期望 0、1", reserved, redeemed)
	}

	var st1, st2 string
	if err := e.row(ctx, `SELECT status FROM coupon_redemptions WHERE order_id = ?`, o1.ID).Scan(&st1); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT status FROM coupon_redemptions WHERE order_id = ?`, o2.ID).Scan(&st2); err != nil {
		t.Fatal(err)
	}
	if st1 != "captured" || st2 != "released" {
		t.Fatalf("状态 o1=%s o2=%s，期望 captured、released", st1, st2)
	}
}
