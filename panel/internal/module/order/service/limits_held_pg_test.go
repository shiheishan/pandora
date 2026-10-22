package service

// 份数上限的计数口径：过期但还能续费的订阅算一份（用户 10-20 定），关了续费窗口的不算；
// 过了付款期限、还没被超时任务扫掉的待付新购单也算（迟到回调仍会结算出一份订阅）。

import (
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestPurchaseLimitSubscriptionsCountsRevivable(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

	planA, verA, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, priceB := e.mkPlan(t, ctx, 2000, "month")
	sub := e.provision(t, ctx, user, planA, verA, priceA, 1000)
	// 3 天前过期、续费窗口没关：还能续费，算一份
	e.mustExec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = now() - interval '33 days',
		current_period_end = now() - interval '3 days' WHERE id = ?`, sub.ID)

	_, err := e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: planB, PriceID: priceB})
	if codeOf(err) != errs.SubscriptionLimitReached {
		t.Fatalf("有一份能续费的过期订阅时新开应 409，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "过期不到 30 天、还能续费的也算") {
		t.Fatalf("文案应说明含能续费的: %v", err)
	}
}

func TestPurchaseLimitSubscriptionsIgnoresClosedWindow(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

	planA, verA, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, priceB := e.mkPlan(t, ctx, 2000, "month")
	sub := e.provision(t, ctx, user, planA, verA, priceA, 1000)
	// 续费窗口已过（过期扫描已关窗）：不再算一份
	e.mustExec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = now() - interval '90 days',
		current_period_end = now() - interval '40 days', renewal_closed_at = now() WHERE id = ?`, sub.ID)

	if _, err := e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: planB, PriceID: priceB}); err != nil {
		t.Fatalf("关了续费窗口的订阅不计入，新开应成功: %v", err)
	}
}

func TestPurchaseLimitSubscriptionsCountsLapsedPendingNew(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

	planA, _, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, priceB := e.mkPlan(t, ctx, 2000, "month")
	c, err := e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: planA, PriceID: priceA})
	if err != nil {
		t.Fatalf("新购套餐 A: %v", err)
	}
	// 过了付款期限、超时任务还没扫：迟到回调仍会结算，份数上限照样算它
	e.mustExec(t, ctx, `UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = ?`, c.OrderID)

	_, err = e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: planB, PriceID: priceB})
	if codeOf(err) != errs.SubscriptionLimitReached {
		t.Fatalf("过期未扫的待付新购单仍占名额，新购 B 应 409，得到 %v", err)
	}
}
