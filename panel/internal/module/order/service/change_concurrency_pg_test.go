package service

import (
	"errors"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// race 同时跑 fns（一起起跑），返回各自的错误。
func race(fns ...func() error) []error {
	out := make([]error, len(fns))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, fn := range fns {
		wg.Add(1)
		go func(i int, fn func() error) {
			defer wg.Done()
			<-start
			out[i] = fn()
		}(i, fn)
	}
	close(start)
	wg.Wait()
	return out
}

// oneWins 断言恰好一个成功、其余都是「已有未完结的续费或变更单」（订阅行锁排队后看到前一张，或唯一索引兜底的 409）。
func oneWins(t *testing.T, results []error) {
	t.Helper()
	ok, open := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrOpenChange) || codeOf(err) == errs.Conflict:
			open++
		default:
			t.Fatalf("意外错误: %v", err)
		}
	}
	if ok != 1 || open != len(results)-1 {
		t.Fatalf("成功 %d、冲突 %d，期望 1 与 %d", ok, open, len(results)-1)
	}
}

// 同一订阅并发下两张换套餐单：恰好一张成功；并发下换套餐单与续费单：也恰好一张成功。
func TestConcurrentPlanChanges(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	planB, _, priceB := e.mkPlan(t, ctx, 10000, "month", nil, nil)

	sub := e.activeSub(t, ctx, user, 3000, 50)
	change := func() error {
		_, err := e.svc.CreatePlanChangeOrder(uctx, PlanChangeOrderInput{SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB})
		return err
	}
	oneWins(t, race(change, change))
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE subscription_id = ? AND status = 'pending_payment'`, sub.ID); n != 1 {
		t.Fatalf("待付单 %d 张", n)
	}

	sub2 := e.activeSub(t, ctx, user, 3000, 50)
	change2 := func() error {
		_, err := e.svc.CreatePlanChangeOrder(uctx, PlanChangeOrderInput{SubscriptionID: sub2.ID, PlanID: planB, PriceID: priceB})
		return err
	}
	renew2 := func() error {
		_, err := e.svc.CreateRenewalOrder(uctx, RenewalOrderInput{SubscriptionID: sub2.ID})
		return err
	}
	oneWins(t, race(change2, renew2))
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE subscription_id = ? AND status = 'pending_payment'`, sub2.ID); n != 1 {
		t.Fatalf("待付单 %d 张", n)
	}
	e.assertNoDrift(t, ctx)
}
