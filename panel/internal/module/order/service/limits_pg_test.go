package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// fakeLimits 是测试用的购买限制设置。
type fakeLimits struct{ v settingssvc.PurchaseLimits }

func (f *fakeLimits) PurchaseLimits(context.Context) (settingssvc.PurchaseLimits, error) {
	return f.v, nil
}

func TestPurchaseLimitUnpaidOrders(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	fl := &fakeLimits{v: settingssvc.PurchaseLimits{MaxUnpaidOrders: 1}}
	e.svc.UsePurchaseLimits(fl)

	if _, err := e.svc.CreateTopup(uctx, TopupInput{Amount: dec("10.00")}); err != nil {
		t.Fatalf("第一笔充值: %v", err)
	}
	_, err := e.svc.CreateTopup(uctx, TopupInput{Amount: dec("20.00")})
	if codeOf(err) != errs.Conflict {
		t.Fatalf("第二笔充值应 409，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "1 笔订单没付款") {
		t.Fatalf("文案应含未付款提示: %v", err)
	}

	fl.v.MaxUnpaidOrders = 0
	if _, err := e.svc.CreateTopup(uctx, TopupInput{Amount: dec("20.00")}); err != nil {
		t.Fatalf("上限为 0 后再充值: %v", err)
	}
}

func TestPurchaseLimitUnpaidIgnoresLapsed(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxUnpaidOrders: 1}})

	c, err := e.svc.CreateTopup(uctx, TopupInput{Amount: dec("5.00")})
	if err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, ctx, `UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = ?`, c.OrderID)

	if _, err := e.svc.CreateTopup(uctx, TopupInput{Amount: dec("6.00")}); err != nil {
		t.Fatalf("过期待付单不计入上限，应再充值成功: %v", err)
	}
}

func TestPurchaseLimitSubscriptions(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

	planA, verA, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, priceB := e.mkPlan(t, ctx, 2000, "month")
	sub := e.provision(t, ctx, user, planA, verA, priceA, 1000)

	_, err := e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: planB, PriceID: priceB, NewCopy: true, Label: "第二份"})
	if codeOf(err) != errs.SubscriptionLimitReached {
		t.Fatalf("新购另一套餐应 409，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "每人最多 1 份套餐") {
		t.Fatalf("文案: %v", err)
	}

	if _, err := e.svc.CreateRenewalOrder(uctx, RenewalOrderInput{SubscriptionID: sub.ID}); err != nil {
		t.Fatalf("续费不受份数上限: %v", err)
	}
}

func TestPurchaseLimitSubscriptionsCountsPendingNew(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

	planA, _, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, priceB := e.mkPlan(t, ctx, 2000, "month")

	if _, err := e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: planA, PriceID: priceA}); err != nil {
		t.Fatalf("新购套餐 A: %v", err)
	}
	_, err := e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: planB, PriceID: priceB})
	if codeOf(err) != errs.SubscriptionLimitReached {
		t.Fatalf("待付新购占名额，新购 B 应 409，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "（含 1 张没付款的新买订单）") {
		t.Fatalf("文案应含待付新购提示: %v", err)
	}
}

func TestPurchaseLimitManualOrderNotLimited(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxUnpaidOrders: 1, MaxSubscriptions: 1}})

	planA, verA, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, priceB := e.mkPlan(t, ctx, 2000, "month")
	e.provision(t, ctx, user, planA, verA, priceA, 1000)
	if _, err := e.svc.CreateTopup(asUser(ctx, user), TopupInput{Amount: dec("1.00")}); err != nil {
		t.Fatal(err)
	}

	c, err := e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: user, PlanID: planB, PriceID: priceB,
		Reason: manualReason, Settlement: SettlementPending,
		Target: &Choice{Kind: PlacementNew},
	})
	if err != nil {
		t.Fatalf("人工开单不受门户购买限制: %v", err)
	}
	if c.Status != StatusPending {
		t.Fatalf("待付人工单状态 %s", c.Status)
	}
}

func TestPurchaseLimitUnpaidConcurrent(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxUnpaidOrders: 1}})

	results := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = e.svc.CreateTopup(uctx, TopupInput{Amount: dec("10.00")})
		}(i)
	}
	close(start)
	wg.Wait()

	ok, conflict := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case codeOf(err) == errs.Conflict:
			conflict++
		default:
			t.Fatalf("意外错误: %v", err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("成功 %d、冲突 %d，期望各 1", ok, conflict)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE user_id = ? AND status = 'pending_payment'`, user); n != 1 {
		t.Fatalf("待付单 %d 张，期望 1", n)
	}
}
