package service

// 冻结、扣款、释放、调账之间的并发：真提交（newCommitEnv），证明不透支、不死锁、状态只走一次。

import (
	"errors"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// errCollector 收集 goroutine 里的错误，结束后在主 goroutine 里报告。
type errCollector struct {
	mu   sync.Mutex
	list []error
}

func (c *errCollector) add(err error) {
	c.mu.Lock()
	c.list = append(c.list, err)
	c.mu.Unlock()
}

func (c *errCollector) report(t *testing.T) {
	t.Helper()
	for _, err := range c.list {
		t.Errorf("意外错误: %v", err)
	}
}

// TestConcurrentCaptureAndSettleNoDeadlock：冻结即扣款（锁用户余额、冻结、收入）与别的订单结算
// （锁渠道、冻结、收入）同时跑，全部成功、没有 40P01。
func TestConcurrentCaptureAndSettleNoDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.CNY
	e.fund(t, ctx, user, cur, "100.00")

	const n = 10
	pending := make([]uuid.UUID, n)
	for i := range pending {
		pending[i] = uuid.New()
		if _, err := e.svc.HoldForOrder(ctx, HoldInput{
			OrderID: pending[i], UserID: user, Currency: cur, Amount: dec("1.00"),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	var bad errCollector
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, _, err := e.svc.CaptureFromBalance(ctx, HoldInput{
				OrderID: uuid.New(), UserID: user, Currency: cur, Amount: dec("1.00"),
			}); err != nil {
				bad.add(err)
			}
		}()
		go func(order uuid.UUID) {
			defer wg.Done()
			if _, err := e.svc.PostOrderPaid(ctx, OrderPaid{
				OrderID: order, UserID: user, Currency: cur,
				Total: dec("3.00"), Paid: dec("2.00"), Fee: dec("0"), Provider: "epay",
			}); err != nil {
				bad.add(err)
			}
		}(pending[i])
	}
	wg.Wait()
	bad.report(t)

	if got := e.balanceOf(t, ctx, user, cur); !got.Equal(dec("80.00")) {
		t.Fatalf("用户余额应为 80，得 %s", got)
	}
	if got := e.accountBalance(t, ctx, UserAccount(UserBalanceHold, user), cur); !got.IsZero() {
		t.Fatalf("冻结科目应为 0，得 %s", got)
	}
	if got := e.accountBalance(t, ctx, PlatformAccount(PlatformRevenue, RefMain), cur); !got.Equal(dec("40.00")) {
		t.Fatalf("收入应为 40，得 %s", got)
	}
	if got := e.accountBalance(t, ctx, PlatformAccount(ChannelCash, "epay"), cur); !got.Equal(dec("20.00")) {
		t.Fatalf("渠道应为 20，得 %s", got)
	}
	e.assertNoDrift(t, ctx)
}

// TestConcurrentReleaseAndSettle：同一张订单的释放与纯余额结算同时到，恰好一边成功，
// 另一边拿到 ErrHoldReleased 或 ErrHoldCaptured。
func TestConcurrentReleaseAndSettle(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.CNY
	e.fund(t, ctx, user, cur, "100.00")

	const n = 10
	orders := make([]uuid.UUID, n)
	for i := range orders {
		orders[i] = uuid.New()
		if _, err := e.svc.HoldForOrder(ctx, HoldInput{
			OrderID: orders[i], UserID: user, Currency: cur, Amount: dec("1.00"),
		}); err != nil {
			t.Fatal(err)
		}
	}

	type outcome struct {
		released   bool
		releaseErr error
		settleErr  error
	}
	results := make([]outcome, n)
	var wg sync.WaitGroup
	for i := range orders {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			results[i].released, results[i].releaseErr = e.svc.ReleaseOrderHold(ctx, orders[i], "并发释放")
		}(i)
		go func(i int) {
			defer wg.Done()
			_, results[i].settleErr = e.svc.PostOrderPaid(ctx, OrderPaid{
				OrderID: orders[i], UserID: user, Currency: cur,
				Total: dec("1.00"), Paid: dec("0"),
			})
		}(i)
	}
	wg.Wait()

	captured := 0
	for i, r := range results {
		switch {
		case r.releaseErr == nil && r.released && errors.Is(r.settleErr, ErrHoldReleased):
		case r.settleErr == nil && errors.Is(r.releaseErr, ErrHoldCaptured):
			captured++
		default:
			t.Errorf("订单 %d 结果不对: released=%v releaseErr=%v settleErr=%v", i, r.released, r.releaseErr, r.settleErr)
		}
	}

	var nReleased, nCaptured int64
	if err := e.row(ctx, `SELECT count(*) FILTER (WHERE status = 'released'), count(*) FILTER (WHERE status = 'captured')
		FROM balance_holds WHERE user_id = ?`, user).Scan(&nReleased, &nCaptured); err != nil {
		t.Fatal(err)
	}
	if nReleased+nCaptured != n || nCaptured != int64(captured) {
		t.Fatalf("冻结状态不对: released=%d captured=%d（goroutine 统计 captured=%d）", nReleased, nCaptured, captured)
	}
	wantBal := dec("100.00").Sub(decimal.NewFromInt(int64(captured)))
	if got := e.balanceOf(t, ctx, user, cur); !got.Equal(wantBal) {
		t.Fatalf("用户余额应为 %s，得 %s", wantBal, got)
	}
	if got := e.accountBalance(t, ctx, PlatformAccount(PlatformRevenue, RefMain), cur); !got.Equal(decimal.NewFromInt(int64(captured))) {
		t.Fatalf("收入应为 %d，得 %s", captured, got)
	}
	if got := e.accountBalance(t, ctx, UserAccount(UserBalanceHold, user), cur); !got.IsZero() {
		t.Fatalf("冻结科目应为 0，得 %s", got)
	}
	e.assertNoDrift(t, ctx)
}

// TestConcurrentHoldAndAdjustNoOverdraft：冻结与后台扣减调账抢同一份余额，不透支、不死锁。
func TestConcurrentHoldAndAdjustNoOverdraft(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e, ctx := newCommitEnv(t)
	admin := e.mkUser(t, ctx)
	user := e.mkUser(t, ctx)
	cur := money.CNY
	e.fund(t, ctx, user, cur, "10.00")

	const n = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	var bad errCollector
	holdsOK, adjustsOK := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := e.svc.HoldForOrder(ctx, HoldInput{
				OrderID: uuid.New(), UserID: user, Currency: cur, Amount: dec("1.00"),
			})
			switch {
			case err == nil:
				mu.Lock()
				holdsOK++
				mu.Unlock()
			case !errors.Is(err, ErrInsufficientBalance):
				bad.add(err)
			}
		}()
		go func() {
			defer wg.Done()
			_, err := e.svc.AdjustBalance(asAdmin(ctx, admin), Adjustment{
				UserID: user, Amount: dec("-1.00"), Currency: string(cur), Reason: "并发扣减测试",
			})
			switch {
			case err == nil:
				mu.Lock()
				adjustsOK++
				mu.Unlock()
			case codeOf(err) != errs.Conflict:
				bad.add(err)
			}
		}()
	}
	wg.Wait()
	bad.report(t)

	if holdsOK+adjustsOK != n {
		t.Fatalf("成功总数应为 %d，得 冻结 %d + 调账 %d", n, holdsOK, adjustsOK)
	}
	if got := e.balanceOf(t, ctx, user, cur); !got.IsZero() {
		t.Fatalf("用户余额应为 0，得 %s", got)
	}
	if got := e.accountBalance(t, ctx, UserAccount(UserBalanceHold, user), cur); !got.Equal(decimal.NewFromInt(int64(holdsOK))) {
		t.Fatalf("冻结科目应为 %d，得 %s", holdsOK, got)
	}
	e.assertNoDrift(t, ctx)
}
