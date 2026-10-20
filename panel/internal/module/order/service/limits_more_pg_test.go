// 购买限制的补充用例。
package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 并发新购：MaxSubscriptions=1，两个 goroutine 同时建单，恰有一单成功、一单 409。
func TestPurchaseLimitSubscriptionsConcurrent(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

	planA, _, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, priceB := e.mkPlan(t, ctx, 2000, "month")

	inputs := []NewOrderInput{
		{PlanID: planA, PriceID: priceA},
		{PlanID: planB, PriceID: priceB},
	}

	results := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = e.svc.CreateNewOrder(uctx, inputs[i])
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
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE user_id = ? AND kind = 'new' AND status = 'pending_payment'`, user); n != 1 {
		t.Fatalf("待付新购单 %d 张，期望 1", n)
	}
}

// 订阅数上限对全额余额（0 元应付）新购同样生效。
func TestPurchaseLimitSubscriptionsAppliesToZeroPay(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

	planA, verA, priceA := e.mkPlan(t, ctx, 1000, "month")
	planB, _, priceB := e.mkPlan(t, ctx, 2000, "month")
	e.provision(t, ctx, user, planA, verA, priceA, 1000)

	e.giveBalance(t, ctx, user, 5000)

	_, err := e.svc.CreateNewOrder(uctx, NewOrderInput{
		PlanID:   planB,
		PriceID:  priceB,
		Checkout: Checkout{UseBalance: dec("50")},
	})
	if codeOf(err) != errs.Conflict {
		t.Fatalf("全额余额新购第二份订阅应 409，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "每人最多 1 份订阅") {
		t.Fatalf("文案应含每人最多 1 份订阅，得到: %v", err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM subscriptions WHERE user_id = ?`, user); n != 1 {
		t.Fatalf("订阅数 %d，期望仍为 1", n)
	}
}

// 待付单上限跳过全额余额支付（应付为 0）。
func TestPurchaseLimitUnpaidSkipsFullBalance(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxUnpaidOrders: 1}})

	// 先门户充值一笔（待付，占满上限）
	if _, err := e.svc.CreateTopup(uctx, TopupInput{Amount: dec("10.00")}); err != nil {
		t.Fatalf("第一笔充值: %v", err)
	}

	planA, _, priceA := e.mkPlan(t, ctx, 1500, "month")
	e.giveBalance(t, ctx, user, 3000)

	// 全额余额新购套餐成功（Status == StatusFulfilled）
	c, err := e.svc.CreateNewOrder(uctx, NewOrderInput{
		PlanID:   planA,
		PriceID:  priceA,
		Checkout: Checkout{UseBalance: dec("15.00")},
	})
	if err != nil {
		t.Fatalf("全额余额新购应成功: %v", err)
	}
	if c.Status != StatusFulfilled {
		t.Fatalf("订单状态 %s，期望 fulfilled", c.Status)
	}

	// 再门户充值一笔仍回 errs.Conflict
	_, err = e.svc.CreateTopup(uctx, TopupInput{Amount: dec("20.00")})
	if codeOf(err) != errs.Conflict {
		t.Fatalf("第三笔充值应 409，得到 %v", err)
	}
}

// 订阅数上限只限制新购，不限制换套餐、叠加单、流量重置单。
func TestPurchaseLimitSubscriptionsIgnoresOtherKinds(t *testing.T) {
	// 换套餐单 CreatePlanChangeOrder
	t.Run("change", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		uctx := asUser(ctx, user)
		e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

		planA, verA, priceA := e.mkPlan(t, ctx, 1000, "month")
		planB, _, priceB := e.mkPlan(t, ctx, 2000, "month")
		sub := e.provision(t, ctx, user, planA, verA, priceA, 1000)

		// 先断言新购会被拒（上限已满）
		_, err := e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: planB, PriceID: priceB})
		if codeOf(err) != errs.Conflict {
			t.Fatalf("上限已满新购应 409，得到 %v", err)
		}

		// 换套餐单成功
		_, err = e.svc.CreatePlanChangeOrder(uctx, PlanChangeOrderInput{
			SubscriptionID: sub.ID,
			PlanID:         planB,
			PriceID:        priceB,
		})
		if err != nil {
			t.Fatalf("换套餐单应成功: %v", err)
		}
	})

	// 叠加单 CreateStackOrder
	t.Run("stack", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		uctx := asUser(ctx, user)
		e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

		planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))
		otherPlan, _, otherPrice := e.mkPlan(t, ctx, 2000, "month")

		sub := provisionOneTime(t, ctx, e, user, planID, versionID, priceID, 1000, intp(30))

		// 先断言新购会被拒
		_, err := e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: otherPlan, PriceID: otherPrice})
		if codeOf(err) != errs.Conflict {
			t.Fatalf("上限已满新购应 409，得到 %v", err)
		}

		// 叠加单成功
		_, err = e.svc.CreateStackOrder(uctx, StackOrderInput{SubscriptionID: sub.ID})
		if err != nil {
			t.Fatalf("叠加单应成功: %v", err)
		}
	})

	// 流量重置单 CreateResetOrder
	t.Run("reset", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		uctx := asUser(ctx, user)
		e.svc.UsePurchaseLimits(&fakeLimits{v: settingssvc.PurchaseLimits{MaxSubscriptions: 1}})

		planA, verA, priceA := e.mkPlan(t, ctx, 1000, "month")
		otherPlan, _, otherPrice := e.mkPlan(t, ctx, 2000, "month")

		e.setResetPrice(t, ctx, planA, 500) // 套餐配重置价

		sub := e.provision(t, ctx, user, planA, verA, priceA, 1000)

		// 先断言新购会被拒
		_, err := e.svc.CreateNewOrder(uctx, NewOrderInput{PlanID: otherPlan, PriceID: otherPrice})
		if codeOf(err) != errs.Conflict {
			t.Fatalf("上限已满新购应 409，得到 %v", err)
		}

		// 流量重置单成功
		_, err = e.svc.CreateResetOrder(uctx, ResetOrderInput{SubscriptionID: sub.ID})
		if err != nil {
			t.Fatalf("流量重置单应成功: %v", err)
		}
	})
}
