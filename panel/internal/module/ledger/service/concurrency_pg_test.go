package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

func TestConcurrentDebitsNoOverdraft(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.CNY
	e.fund(t, ctx, user, cur, "10.00")

	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	var okCount, insufCount int
	var other []error

	amt := dec("1.00")
	susp := PlatformAccount(Suspense, RefManualAdjust)
	userBal := UserAccount(UserBalance, user)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Post(context.Background(), Posting{
				Kind:     KindBalanceAdjusted,
				Currency: cur,
				Memo:     "并发扣减测试",
				Legs: []Leg{
					{Account: userBal, Direction: Debit, Amount: amt},
					{Account: susp, Direction: Credit, Amount: amt},
				},
			})
			mu.Lock()
			if err == nil {
				okCount++
			} else if errors.Is(err, ErrInsufficientBalance) {
				insufCount++
			} else {
				other = append(other, err)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	for _, err := range other {
		t.Errorf("意外错误: %v", err)
	}
	if okCount != 10 || insufCount != 10 {
		t.Fatalf("成功应为 10、余额不足应为 10，得 ok=%d insuf=%d", okCount, insufCount)
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("0.00")) {
		t.Fatalf("最终余额应为 0，得 %s", e.balanceOf(t, ctx, user, cur))
	}
	e.assertNoDrift(t, ctx)
}

func TestConcurrentOppositeOrderNoDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e, ctx := newCommitEnv(t)
	a := e.mkUser(t, ctx)
	b := e.mkUser(t, ctx)
	cur := money.CNY
	e.fund(t, ctx, a, cur, "100.00")
	e.fund(t, ctx, b, cur, "100.00")

	const n = 40
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	amt := dec("1.00")

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			var legs []Leg
			if idx%2 == 0 {
				legs = []Leg{
					{Account: UserAccount(UserBalance, a), Direction: Debit, Amount: amt},
					{Account: UserAccount(UserBalance, b), Direction: Credit, Amount: amt},
				}
			} else {
				legs = []Leg{
					{Account: UserAccount(UserBalance, b), Direction: Debit, Amount: amt},
					{Account: UserAccount(UserBalance, a), Direction: Credit, Amount: amt},
				}
			}
			_, err := e.svc.Post(context.Background(), Posting{
				Kind:     KindCommissionToBalance,
				Currency: cur,
				Memo:     "并发顺序测试",
				Legs:     legs,
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		t.Errorf("Post 失败: %v", err)
	}
	if !e.balanceOf(t, ctx, a, cur).Equal(dec("100.00")) || !e.balanceOf(t, ctx, b, cur).Equal(dec("100.00")) {
		t.Fatalf("A/B 余额应仍为 100，得 A=%s B=%s", e.balanceOf(t, ctx, a, cur), e.balanceOf(t, ctx, b, cur))
	}
	e.assertNoDrift(t, ctx)
}

func TestConcurrentEnsureSameAccount(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.CNY

	const n = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Post(context.Background(), Posting{
				Kind:     KindBalanceTopup,
				Currency: cur,
				Legs: []Leg{
					{Account: PlatformAccount(ChannelCash, "test"), Direction: Debit, Amount: dec("1.00")},
					{Account: UserAccount(UserBalance, user), Direction: Credit, Amount: dec("1.00")},
				},
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		t.Errorf("fund 失败: %v", err)
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("10.00")) {
		t.Fatalf("余额应为 10，得 %s", e.balanceOf(t, ctx, user, cur))
	}
	var acctN int64
	if err := e.row(ctx, `SELECT count(*) FROM ledger_accounts WHERE owner_user_id = ?`, user).Scan(&acctN); err != nil {
		t.Fatal(err)
	}
	if acctN != 1 {
		t.Fatalf("user_balance 科目应只有 1 条，得 %d", acctN)
	}
	e.assertNoDrift(t, ctx)
}
