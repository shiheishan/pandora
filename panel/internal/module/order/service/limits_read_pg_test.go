package service

import (
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 到了份数上限时落点新开项带 DisabledReason，放宽上限后清空。
func TestLandingsSubscriptionCap(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	fl := &fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}}
	e.svc.UsePurchaseLimits(fl)

	planA, verA, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, _ := e.mkPlan(t, ctx, 2000, "month")
	e.provision(t, ctx, user, planA, verA, priceA, 1000)

	got, err := e.svc.Landings(uctx, planB)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Options) == 0 || got.Options[0].Mode != LandingNew {
		t.Fatalf("第一项应为新开，得到 %+v", got.Options)
	}
	if !strings.Contains(got.Options[0].DisabledReason, "每人最多 1 份套餐") {
		t.Fatalf("新开 DisabledReason: %q", got.Options[0].DisabledReason)
	}
	for i := 1; i < len(got.Options); i++ {
		if got.Options[i].DisabledReason != "" {
			t.Fatalf("第 %d 项 DisabledReason 应为空，得到 %q", i, got.Options[i].DisabledReason)
		}
	}

	fl.v.MaxSubscriptions = 2
	got, err = e.svc.Landings(uctx, planB)
	if err != nil {
		t.Fatal(err)
	}
	if got.Options[0].DisabledReason != "" {
		t.Fatalf("上限放宽后新开 DisabledReason 应为空，得到 %q", got.Options[0].DisabledReason)
	}
}

// 未装配购买限制或上限为 0 时，落点新开不因份数禁用。
func TestLandingsSubscriptionCapOffByDefault(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)

	planA, verA, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, verB, priceB := e.mkPlan(t, ctx, 2000, "month")
	planC, _, _ := e.mkPlan(t, ctx, 3000, "month")
	e.provision(t, ctx, user, planA, verA, priceA, 1000)
	e.provision(t, ctx, user, planB, verB, priceB, 2000)

	got, err := e.svc.Landings(uctx, planC)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Options) == 0 || got.Options[0].Mode != LandingNew {
		t.Fatalf("第一项应为新开，得到 %+v", got.Options)
	}
	if got.Options[0].DisabledReason != "" {
		t.Fatalf("未装配 limits 时新开 DisabledReason 应为空，得到 %q", got.Options[0].DisabledReason)
	}

	e.svc.UsePurchaseLimits(&fakeLimits{})
	got, err = e.svc.Landings(uctx, planC)
	if err != nil {
		t.Fatal(err)
	}
	if got.Options[0].DisabledReason != "" {
		t.Fatalf("上限为 0 时新开 DisabledReason 应为空，得到 %q", got.Options[0].DisabledReason)
	}
}

// 新购报价在份数上限时回 subscription_limit_reached，续费报价不受影响。
func TestQuoteNewSubscriptionCap(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

	planA, verA, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, _ := e.mkPlan(t, ctx, 2000, "month")
	sub := e.provision(t, ctx, user, planA, verA, priceA, 1000)

	_, err := e.svc.Quote(uctx, QuoteInput{Action: QuoteNew, PlanID: &planB})
	if codeOf(err) != errs.SubscriptionLimitReached {
		t.Fatalf("新购报价应 subscription_limit_reached，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "每人最多 1 份套餐") {
		t.Fatalf("文案: %v", err)
	}

	// 上限放宽到 2、手里 1 份：新购报价照常回价格行
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 2}})
	fresh, err := e.svc.Quote(uctx, QuoteInput{Action: QuoteNew, PlanID: &planB})
	if err != nil {
		t.Fatalf("没到上限的新购报价: %v", err)
	}
	if len(fresh.Quotes) == 0 {
		t.Fatal("没到上限的新购报价应至少一条")
	}

	res, err := e.svc.Quote(uctx, QuoteInput{Action: QuoteRenew, SubscriptionID: &sub.ID})
	if err != nil {
		t.Fatalf("续费报价: %v", err)
	}
	if len(res.Quotes) == 0 {
		t.Fatal("续费报价应至少一条")
	}
}

// 过付款期限的待付新购单仍占份数名额，新购报价文案含待付提示。
func TestQuoteNewSubscriptionCapCountsPendingNew(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

	planA, _, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, _ := e.mkPlan(t, ctx, 2000, "month")

	c, err := e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: planA, PriceID: priceA})
	if err != nil {
		t.Fatalf("新购套餐 A: %v", err)
	}
	e.mustExec(t, ctx, "UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = ?", c.OrderID)

	_, err = e.svc.Quote(uctx, QuoteInput{Action: QuoteNew, PlanID: &planB})
	if codeOf(err) != errs.SubscriptionLimitReached {
		t.Fatalf("待付新购占名额，新购报价应 subscription_limit_reached，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "（含 1 张没付款的新买订单）") {
		t.Fatalf("文案应含待付新购提示: %v", err)
	}
	if !strings.Contains(err.Error(), "或先付掉、取消没付款的订单") {
		t.Fatalf("文案应指向付掉或取消待付单: %v", err)
	}
}
