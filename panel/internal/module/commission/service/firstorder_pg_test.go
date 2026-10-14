package service

import (
	"sync"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 「首单」口径下同一被推荐人的 USD 单与 CNY 单并发结算（两单不共用平台收入科目锁）：
// 被推荐人咨询锁让两边串行，只出一条佣金。去掉 LockReferee 这里会出两条。
func TestFirstOrderConcurrentCurrencies(t *testing.T) {
	e, _ := newEnv(t)
	e.cleanCommitted(t)
	ctx := commitCtx()
	admin := newUser(t, e, ctx)
	prev, err := e.settings.Commission(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.settings.SetCommission(adminCtx(commitCtx(), admin), settingssvc.CommissionUpdate{
			RatePercent: &prev.RatePercent, FreezeDays: &prev.FreezeDays, MinWithdraw: &prev.MinWithdraw, Scope: &prev.Scope,
		}); err != nil {
			t.Errorf("还原分销参数: %v", err)
		}
	})
	setConfig(t, e, ctx, admin, 10, 3, "10.00", "first_order")

	for round := 0; round < 5; round++ {
		referrer, referee := newUser(t, e, ctx), newUser(t, e, ctx)
		refer(e, referee, referrer)
		orders := []money.Currency{money.USD, money.CNY}
		var wg sync.WaitGroup
		errsCh := make(chan error, len(orders))
		start := make(chan struct{})
		for _, cur := range orders {
			o := newPaidOrderIn(t, e, ctx, referee, "100.00", cur)
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := e.svc.OnOrderPaid(commitCtx(), o); err != nil {
					errsCh <- err
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errsCh)
		for err := range errsCh {
			t.Fatalf("第 %d 轮计提: %v", round+1, err)
		}
		var n int
		if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE referee_user_id = ?`, referee).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("第 %d 轮：首单口径应只有 1 条佣金，得到 %d", round+1, n)
		}
	}
}
