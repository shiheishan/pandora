package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	orderrepo "github.com/aegispanel/aegis/internal/module/order/repo"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 5 个用户并发新购（券 + 部分余额）：全部成功；
// 再对这 5 单并发地取消 2 单、回调结算 3 单：券计数、冻结与账务全部守恒。
func TestConcurrentPurchaseThenCancelOrSettle(t *testing.T) {
	e, ctx := newCommitEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month")
	cc := couponCode()
	couponID := e.mkCoupon(t, ctx, cc, 500)

	const buyers = 5
	users := make([]uuid.UUID, buyers)
	for i := range users {
		users[i] = e.mkUser(t, ctx)
		e.giveBalance(t, ctx, users[i], 500)
	}

	type result struct {
		user    uuid.UUID
		created Created
		err     error
	}
	results := make([]result, buyers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range users {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			c, err := e.svc.CreateNewOrder(asUser(ctx, users[i]), NewOrderInput{
				PlanID: planID, PriceID: priceID,
				Checkout: Checkout{UseBalance: dec("5"), CouponCode: cc},
			})
			results[i] = result{user: users[i], created: c, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	var won []result
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("并发新购失败: %v", r.err)
		}
		won = append(won, r)
	}
	if len(won) != buyers {
		t.Fatalf("成功 %d 单，期望 %d", len(won), buyers)
	}
	for _, r := range won {
		mustEq(t, "应付", r.created.Payable, dec("20"))
	}

	// 先逐单发起支付（拿到收银台单号），再并发：前 2 单取消、后 3 单回调
	outTradeNos := make([]string, len(won))
	for i, r := range won {
		intent, err := e.svc.Pay(asUser(ctx, r.user), PayInput{OrderID: r.created.OrderID, Provider: code})
		if err != nil {
			t.Fatalf("发起支付: %v", err)
		}
		outTradeNos[i] = intent.OutTradeNo
	}
	errsOut := make([]error, len(won))
	start2 := make(chan struct{})
	for i, r := range won {
		wg.Add(1)
		go func(i int, r result) {
			defer wg.Done()
			<-start2
			if i < 2 {
				_, errsOut[i] = e.svc.Cancel(asUser(ctx, r.user), CancelInput{OrderID: r.created.OrderID})
				return
			}
			ref := fmt.Sprintf("pay-c-%d-%s", i, randHex(6))
			ack, err := e.payments.HandleWebhook(ctx, code, epayInbound(ref, outTradeNos[i], 2000))
			if err == nil && !ackOK(ack) {
				err = fmt.Errorf("回执: %s", ack.Body)
			}
			errsOut[i] = err
		}(i, r)
	}
	close(start2)
	wg.Wait()
	for i, err := range errsOut {
		if err != nil {
			t.Fatalf("第 %d 单并发收尾失败: %v", i, err)
		}
	}

	for i, r := range won {
		want := StatusFulfilled
		holdWant := "captured"
		if i < 2 {
			want = StatusCancelled
			holdWant = "released"
		}
		if st := e.order(t, ctx, r.created.OrderID).Status; st != want {
			t.Fatalf("第 %d 单 %s，期望 %s", i, st, want)
		}
		if st := e.strCol(t, ctx, `SELECT status FROM balance_holds WHERE order_id = ?`, r.created.OrderID); st != holdWant {
			t.Fatalf("第 %d 单冻结 %s，期望 %s", i, st, holdWant)
		}
	}
	var cReserved, cRedeemed int
	if err := e.row(ctx, `SELECT reserved_count, redeemed_count FROM coupons WHERE id = ?`, couponID).Scan(&cReserved, &cRedeemed); err != nil {
		t.Fatal(err)
	}
	if cReserved != 0 || cRedeemed != 3 {
		t.Fatalf("券 reserved=%d redeemed=%d，期望 0 与 3", cReserved, cRedeemed)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM balance_holds WHERE status = 'held'`); n != 0 {
		t.Fatalf("还有 %d 笔冻结没收尾", n)
	}
	e.assertNoDrift(t, ctx)
}

// 同一订单并发「取消」与「回调」各 10 次（同一笔渠道收款重放）：要么 cancelled 且这笔钱进一个 released_order 挂账，
// 要么 fulfilled 且取消全部回 ErrAlreadyPaid；收款只有一行，资源守恒。
func TestConcurrentCancelVersusWebhook(t *testing.T) {
	e, ctx := newCommitEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month")
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	in := epayInbound("pay-race-"+randHex(6), intent.OutTradeNo, 1000)

	const n = 10
	cancelErrs := make([]error, n)
	hookErrs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			<-start
			_, cancelErrs[i] = e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: c.OrderID})
		}(i)
		go func(i int) {
			defer wg.Done()
			<-start
			ack, err := e.payments.HandleWebhook(ctx, code, in)
			if err == nil && !ackOK(ack) {
				err = fmt.Errorf("回执: %s", ack.Body)
			}
			hookErrs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range hookErrs {
		if err != nil {
			t.Fatalf("第 %d 次回调: %v", i, err)
		}
	}
	if k := e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, c.OrderID); k != 1 {
		t.Fatalf("收款 %d 行，期望 1", k)
	}
	o := e.order(t, ctx, c.OrderID)
	cases := e.count(t, ctx, `SELECT count(*) FROM late_payment_cases WHERE order_id = ?`, c.OrderID)
	switch o.Status {
	case StatusCancelled:
		for i, err := range cancelErrs {
			if err != nil {
				t.Fatalf("订单已取消时第 %d 次取消不该失败: %v", i, err)
			}
		}
		kind, _, _ := e.lateCase(t, ctx, c.OrderID)
		if cases != 1 || kind != string(paymentsvc.CaseReleasedOrder) {
			t.Fatalf("挂账 %d 条 kind=%q，期望 1 条 released_order", cases, kind)
		}
	case StatusFulfilled:
		for i, err := range cancelErrs {
			if err != nil && !errors.Is(err, ErrAlreadyPaid) {
				t.Fatalf("第 %d 次取消意外错误: %v", i, err)
			}
		}
		if cases != 0 {
			t.Fatalf("履约时不该有挂账，得到 %d", cases)
		}
	default:
		t.Fatalf("订单停在 %s", o.Status)
	}
	e.assertNoDrift(t, ctx)
}

// 同一用户并发给同一订阅建两张续费单：恰好一张成功，另一张被拦（order_pending 或唯一索引兜底的 ErrOpenChange）。
func TestConcurrentRenewalsOneWins(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 1000, "month")
	sub := e.provision(t, ctx, user, planID, versionID, priceID, 1000)

	results := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = e.svc.CreateRenewalOrder(asUser(ctx, user), RenewalOrderInput{SubscriptionID: sub.ID})
		}(i)
	}
	close(start)
	wg.Wait()

	ok, open := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrOpenChange) || codeOf(err) == errs.Conflict || codeOf(err) == errs.OrderPending:
			open++
		default:
			t.Fatalf("意外错误: %v", err)
		}
	}
	if ok != 1 || open != 1 {
		t.Fatalf("成功 %d、冲突 %d，期望各 1", ok, open)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE subscription_id = ? AND kind = 'renewal' AND status = 'pending_payment'`, sub.ID); n != 1 {
		t.Fatalf("待付续费单 %d 张", n)
	}
}

// 锁序回归（审查 B1）：用户 A 用余额全额零元新购（券 C），同时用户 B 同套餐、同券的订单回调到账，并发多轮。
// 结算若先锁平台收入科目再锁券，就会与零元建单（先券、后平台收入）成环（40P01）。断言每轮两边都成功、券计数守恒。
func TestConcurrentZeroPayVersusWebhookNoDeadlock(t *testing.T) {
	e, ctx := newCommitEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month")
	cc := couponCode()
	couponID := e.mkCoupon(t, ctx, cc, 500)

	const rounds = 8
	for round := 0; round < rounds; round++ {
		// B：待付单（券 C），先发起支付拿收银台单号
		userB := e.mkUser(t, ctx)
		b := e.buyNew(t, ctx, userB, planID, priceID, Checkout{CouponCode: cc})
		intent, err := e.svc.Pay(asUser(ctx, userB), PayInput{OrderID: b.OrderID, Provider: code})
		if err != nil {
			t.Fatalf("第 %d 轮 B 发起支付: %v", round, err)
		}
		cents, err := money.ToCents(intent.Amount)
		if err != nil {
			t.Fatal(err)
		}
		// A：余额够全额付
		userA := e.mkUser(t, ctx)
		e.giveBalance(t, ctx, userA, 2000)

		var wg sync.WaitGroup
		var errA, errB error
		var createdA Created
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			createdA, errA = e.svc.CreateNewOrder(asUser(ctx, userA), NewOrderInput{
				PlanID: planID, PriceID: priceID,
				Checkout: Checkout{UseBalance: dec("20"), CouponCode: cc},
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			ref := fmt.Sprintf("pay-dl-%d-%s", round, randHex(6))
			ack, err := e.payments.HandleWebhook(ctx, code, epayInbound(ref, intent.OutTradeNo, cents))
			if err == nil && !ackOK(ack) {
				err = fmt.Errorf("回执: %s", ack.Body)
			}
			errB = err
		}()
		close(start)
		wg.Wait()
		if errA != nil {
			t.Fatalf("第 %d 轮 A 零元新购失败（可能是死锁）: %v", round, errA)
		}
		if errB != nil {
			t.Fatalf("第 %d 轮 B 回调失败（可能是死锁）: %v", round, errB)
		}
		if createdA.Status != StatusFulfilled {
			t.Fatalf("第 %d 轮 A 状态 %s", round, createdA.Status)
		}
		if st := e.order(t, ctx, b.OrderID).Status; st != StatusFulfilled {
			t.Fatalf("第 %d 轮 B 状态 %s", round, st)
		}
	}

	var cReserved, cRedeemed int
	if err := e.row(ctx, `SELECT reserved_count, redeemed_count FROM coupons WHERE id = ?`, couponID).Scan(&cReserved, &cRedeemed); err != nil {
		t.Fatal(err)
	}
	if cReserved != 0 || cRedeemed != 2*rounds {
		t.Fatalf("券 reserved=%d redeemed=%d，期望 0 与 %d", cReserved, cRedeemed, 2*rounds)
	}
	e.assertNoDrift(t, ctx)
}

// 超时释放不等锁：到期的单正被别的事务锁着时，这一轮跳过（计入 Skipped），订单不动；锁放开后下一轮照常释放。
func TestExpireDueSkipsLockedOrder(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month")
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	e.mustExec(t, ctx, `UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = ?`, c.OrderID)
	e.mustExec(t, ctx, `UPDATE order_reservations SET expires_at = now() - interval '1 minute' WHERE order_id = ?`, c.OrderID)

	var stats ExpireStats
	err := e.txm.Do(ctx, func(tctx context.Context) error {
		// 模拟回调或取消正持有订单锁
		if _, _, err := e.svc.repo.OrderByID(tctx, c.OrderID, orderrepo.ForNoKeyUpdate); err != nil {
			return err
		}
		var err error
		stats, err = e.svc.ExpireDue(ctx, 10) // 用不带事务的 ctx：自己开事务，与上面的锁并发
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Expired != 0 || stats.Failed != 0 {
		t.Fatalf("订单被锁时不该释放或失败，统计 %+v", stats)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusPending {
		t.Fatalf("订单 %s，期望仍待付", st)
	}

	stats, err = e.svc.ExpireDue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Expired != 1 {
		t.Fatalf("锁放开后应释放 1 单，统计 %+v", stats)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusExpired {
		t.Fatalf("订单 %s，期望 expired", st)
	}
}
