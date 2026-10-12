package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestReserveConcurrentCapacity(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := newUser(t, e, ctx)

	max := 5
	spec := fixedSpec("1.00")
	spec.MaxRedemptions = &max
	code := randCode("CAP")
	createCoupon(t, e, ctx, admin, code, spec)

	users := make([]uuid.UUID, 20)
	for i := range users {
		users[i] = newUser(t, e, ctx)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var okN, usedUp int

	for i := range users {
		wg.Add(1)
		go func(u uuid.UUID) {
			defer wg.Done()
			<-start
			_, err := e.svc.Reserve(ctx, code, target(u, "10.00"), order())
			mu.Lock()
			if err == nil {
				okN++
			} else if errors.Is(err, ErrCouponUsedUp) {
				usedUp++
			} else {
				t.Errorf("Reserve: %v", err)
			}
			mu.Unlock()
		}(users[i])
	}
	close(start)
	wg.Wait()

	if okN != 5 {
		t.Fatalf("成功次数 %d，期望 5", okN)
	}
	if usedUp != 15 {
		t.Fatalf("ErrCouponUsedUp 次数 %d，期望 15", usedUp)
	}

	var reserved, redeemed int
	if err := e.row(ctx,
		`SELECT reserved_count, redeemed_count FROM coupons WHERE code = ?`, code,
	).Scan(&reserved, &redeemed); err != nil {
		t.Fatal(err)
	}
	if reserved != 5 || redeemed != 0 {
		t.Fatalf("计数 reserved=%d redeemed=%d，期望 5、0", reserved, redeemed)
	}

	var held int64
	if err := e.row(ctx,
		`SELECT count(*) FROM coupon_redemptions r JOIN coupons c ON c.id = r.coupon_id WHERE c.code = ? AND r.status = 'held'`,
		code,
	).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 5 {
		t.Fatalf("held 行数 %d，期望 5", held)
	}
}

func TestReservePerUserConcurrent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := newUser(t, e, ctx)

	code := randCode("PER")
	createCoupon(t, e, ctx, admin, code, fixedSpec("1.00"))
	u := newUser(t, e, ctx)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var okN, limitN int

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.svc.Reserve(ctx, code, target(u, "10.00"), order())
			mu.Lock()
			if err == nil {
				okN++
			} else if errors.Is(err, ErrCouponUserLimit) {
				limitN++
			} else {
				t.Errorf("Reserve: %v", err)
			}
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if okN != 1 {
		t.Fatalf("成功次数 %d，期望 1", okN)
	}
	if limitN != 9 {
		t.Fatalf("ErrCouponUserLimit 次数 %d，期望 9", limitN)
	}
}

func TestReleaseThenReuse(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)

	max := 1
	spec := fixedSpec("1.00")
	spec.MaxRedemptions = &max
	code := randCode("REL")
	createCoupon(t, e, ctx, admin, code, spec)

	ua := newUser(t, e, ctx)
	ub := newUser(t, e, ctx)
	o1 := order()
	o2 := order()

	if _, err := e.svc.Reserve(ctx, code, target(ua, "10.00"), o1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Reserve(ctx, code, target(ub, "10.00"), o2); !errors.Is(err, ErrCouponUsedUp) {
		t.Fatalf("B 占用: %v", err)
	}
	if err := e.svc.Release(ctx, o1.ID, "超时"); err != nil {
		t.Fatal(err)
	}

	var reserved int
	if err := e.row(ctx, `SELECT reserved_count FROM coupons WHERE code = ?`, code).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("释放后 reserved=%d", reserved)
	}

	if _, err := e.svc.Reserve(ctx, code, target(ub, "10.00"), o2); err != nil {
		t.Fatalf("B 再占用: %v", err)
	}
	if err := e.svc.Capture(ctx, o2.ID); err != nil {
		t.Fatal(err)
	}

	var redeemed int
	if err := e.row(ctx, `SELECT reserved_count, redeemed_count FROM coupons WHERE code = ?`, code).
		Scan(&reserved, &redeemed); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 || redeemed != 1 {
		t.Fatalf("Capture 后 reserved=%d redeemed=%d", reserved, redeemed)
	}

	var status string
	var capturedAt *string
	if err := e.row(ctx,
		`SELECT status, captured_at::text FROM coupon_redemptions WHERE order_id = ?`, o2.ID,
	).Scan(&status, &capturedAt); err != nil {
		t.Fatal(err)
	}
	if status != "captured" || capturedAt == nil || *capturedAt == "" {
		t.Fatalf("占用行 status=%s captured_at=%v", status, capturedAt)
	}

	if err := e.svc.Release(ctx, o2.ID, ""); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("已 Capture 再 Release: %v", err)
	}
	if err := e.svc.Capture(ctx, o1.ID); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("已释放订单 Capture: %v", err)
	}
	if err := e.svc.Release(ctx, uuid.New(), ""); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("无占用订单 Release: %v", err)
	}

	var reason *string
	if err := e.row(ctx,
		`SELECT status, release_reason FROM coupon_redemptions WHERE order_id = ?`, o1.ID,
	).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "released" || reason == nil || *reason != "超时" {
		t.Fatalf("o1 行 status=%s reason=%v", status, reason)
	}
}

func TestReserveRules(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)

	// 百分比折扣
	codePct := randCode("PCT")
	createCoupon(t, e, ctx, admin, codePct, percentSpec(3333))
	u := newUser(t, e, ctx)
	applied, err := e.svc.Evaluate(ctx, codePct, target(u, "9.99"))
	if err != nil {
		t.Fatal(err)
	}
	wantDisc := decimal.RequireFromString("3.32")
	if !applied.Discount.Equal(wantDisc) {
		t.Fatalf("折扣 %s，期望 3.32", applied.Discount)
	}
	ordPct := order()
	if _, err := e.svc.Reserve(ctx, codePct, target(u, "9.99"), ordPct); err != nil {
		t.Fatal(err)
	}
	var discDB string
	if err := e.row(ctx,
		`SELECT discount_amount::text FROM coupon_redemptions WHERE order_id = ?`, ordPct.ID,
	).Scan(&discDB); err != nil {
		t.Fatal(err)
	}
	if discDB != "3.32" {
		t.Fatalf("占用行 discount=%s", discDB)
	}

	// 定额封顶到小计
	codeFix := randCode("FIX")
	createCoupon(t, e, ctx, admin, codeFix, fixedSpec("20.00"))
	u2 := newUser(t, e, ctx)
	applied, err = e.svc.Evaluate(ctx, codeFix, target(u2, "15.00"))
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Discount.Equal(decimal.RequireFromString("15.00")) {
		t.Fatalf("定额折扣 %s", applied.Discount)
	}

	// 限定套餐
	P := newPlan(t, e, ctx)
	codePlan := randCode("PLN")
	ps := fixedSpec("1.00")
	ps.PlanIDs = []uuid.UUID{P}
	createCoupon(t, e, ctx, admin, codePlan, ps)
	u3 := newUser(t, e, ctx)
	if _, err := e.svc.Evaluate(ctx, codePlan, target(u3, "10.00")); !errors.Is(err, ErrCouponNotForPlan) {
		t.Fatalf("无 PlanID: %v", err)
	}
	tr := target(u3, "10.00")
	tr.PlanID = &P
	if _, err := e.svc.Evaluate(ctx, codePlan, tr); err != nil {
		t.Fatalf("有 PlanID: %v", err)
	}

	// 限定用户组
	G := newGroup(t, e, ctx)
	codeGrp := randCode("GRP")
	gs := fixedSpec("1.00")
	gs.UserGroupID = &G
	createCoupon(t, e, ctx, admin, codeGrp, gs)
	u4 := newUser(t, e, ctx)
	if _, err := e.svc.Evaluate(ctx, codeGrp, target(u4, "10.00")); !errors.Is(err, ErrCouponNotForUser) {
		t.Fatalf("不在组: %v", err)
	}
	e.users.groups[u4] = G
	if _, err := e.svc.Evaluate(ctx, codeGrp, target(u4, "10.00")); err != nil {
		t.Fatalf("在组: %v", err)
	}

	// 码大小写
	spring := "SPRING" + randHex(8)
	createCoupon(t, e, ctx, admin, spring, fixedSpec("1.00"))
	u5 := newUser(t, e, ctx)
	if _, err := e.svc.Evaluate(ctx, strings.ToLower(spring), target(u5, "10.00")); err != nil {
		t.Fatalf("小写码: %v", err)
	}

	// 停用
	codePause := randCode("PAU")
	idPause, _, err := e.svc.CreateCoupon(adminCtx(ctx, admin), codePause, fixedSpec("1.00"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetStatus(adminCtx(ctx, admin), idPause, StatusPaused); err != nil {
		t.Fatal(err)
	}
	u6 := newUser(t, e, ctx)
	if _, err := e.svc.Evaluate(ctx, codePause, target(u6, "10.00")); !errors.Is(err, ErrCouponNotFound) {
		t.Fatalf("paused Evaluate: %v", err)
	}

	// 空码
	u7 := newUser(t, e, ctx)
	if got, err := e.svc.Evaluate(ctx, "  ", target(u7, "10.00")); err != nil || got != nil {
		t.Fatalf("空码 Evaluate: got=%v err=%v", got, err)
	}
	if got, err := e.svc.Reserve(ctx, "  ", target(u7, "10.00"), order()); err != nil || got != nil {
		t.Fatalf("空码 Reserve: got=%v err=%v", got, err)
	}

	// 释放后同一用户可再占用
	codeReuse := randCode("RUS")
	createCoupon(t, e, ctx, admin, codeReuse, fixedSpec("1.00"))
	u8 := newUser(t, e, ctx)
	ordA := order()
	if _, err := e.svc.Reserve(ctx, codeReuse, target(u8, "10.00"), ordA); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Release(ctx, ordA.ID, ""); err != nil {
		t.Fatal(err)
	}
	ordB := order()
	if _, err := e.svc.Reserve(ctx, codeReuse, target(u8, "10.00"), ordB); err != nil {
		t.Fatalf("释放后再占用: %v", err)
	}

	// 同一订单两张券：冲突不让事务出错
	codeA := randCode("ORDA")
	codeB := randCode("ORDB")
	createCoupon(t, e, ctx, admin, codeA, fixedSpec("1.00"))
	createCoupon(t, e, ctx, admin, codeB, fixedSpec("1.00"))
	u9 := newUser(t, e, ctx)
	ordDup := order()
	if _, err := e.svc.Reserve(ctx, codeA, target(u9, "10.00"), ordDup); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Reserve(ctx, codeB, target(u9, "10.00"), ordDup); !errors.Is(err, ErrOrderHasCoupon) {
		t.Fatalf("第二张券: %v", err)
	}
	// 事务仍可用，第二张券的计数没动、订单上只有一条占用
	var reservedB int
	if err := e.row(ctx, `SELECT reserved_count FROM coupons WHERE code = ?`, codeB).Scan(&reservedB); err != nil {
		t.Fatalf("冲突后事务不可用: %v", err)
	}
	if reservedB != 0 {
		t.Fatalf("第二张券 reserved_count=%d，期望 0", reservedB)
	}
	var cnt int64
	if err := e.row(ctx, `SELECT count(*) FROM coupon_redemptions WHERE order_id = ?`, ordDup.ID).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("订单占用行 %d，期望 1", cnt)
	}
}

func TestPreview(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	u := newUser(t, e, ctx)

	P := newPlan(t, e, ctx)
	R := uuid.New()
	K := uuid.New()
	e.cat.prices[R] = fakePrice{plan: P, currency: "CNY", amount: decimal.RequireFromString("30.00")}
	e.cat.packs[K] = fakePrice{currency: "CNY", amount: decimal.RequireFromString("12.00")}

	code := randCode("PRV")
	ps := percentSpec(1000)
	ps.PlanIDs = []uuid.UUID{P}
	createCoupon(t, e, ctx, admin, code, ps)

	pv, err := e.svc.Preview(ctx, u, code, PreviewItem{PlanID: &P, PriceID: &R})
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Subtotal.Equal(decimal.RequireFromString("30.00")) {
		t.Fatalf("Subtotal %s", pv.Subtotal)
	}
	if !pv.Discount.Equal(decimal.RequireFromString("3.00")) {
		t.Fatalf("Discount %s", pv.Discount)
	}
	if !pv.Payable.Equal(decimal.RequireFromString("27.00")) {
		t.Fatalf("Payable %s", pv.Payable)
	}
	if pv.Applied == nil {
		t.Fatal("Applied 应为非 nil")
	}

	if _, err := e.svc.Preview(ctx, u, code, PreviewItem{PackID: &K}); !errors.Is(err, ErrCouponNotForPlan) {
		t.Fatalf("流量包: %v", err)
	}

	pv, err = e.svc.Preview(ctx, u, "", PreviewItem{PlanID: &P, PriceID: &R})
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Discount.IsZero() || !pv.Payable.Equal(decimal.RequireFromString("30.00")) || pv.Applied != nil {
		t.Fatalf("空码 Preview: discount=%s payable=%s applied=%v", pv.Discount, pv.Payable, pv.Applied)
	}

	wrongPlan := uuid.New()
	_, err = e.svc.Preview(ctx, u, code, PreviewItem{PlanID: &wrongPlan, PriceID: &R})
	if errCode(err) != errs.NotFound {
		t.Fatalf("价格不属于套餐: code=%s err=%v", errCode(err), err)
	}
}
