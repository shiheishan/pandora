package service

import (
	"context"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestConcurrentDebitsNoOverdraft(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e, ctx := newCommitEnv(t)
	admin := e.mkUser(t, ctx)
	user := e.mkUser(t, ctx)
	e.fund(t, ctx, user, "10.00")

	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	var okCount, conflictCount int
	var other []error

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.AdjustBalance(asAdmin(ctx, admin), Adjustment{
				UserID: user,
				Amount: dec("-1.00"),
				Reason: "并发扣减测试",
			})
			mu.Lock()
			if err == nil {
				okCount++
			} else if codeOf(err) == errs.Conflict {
				conflictCount++
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
	if okCount != 10 || conflictCount != 10 {
		t.Fatalf("成功应为 10、Conflict 应为 10，得 ok=%d conflict=%d", okCount, conflictCount)
	}
	if !e.balanceOf(t, ctx, user).Equal(dec("0.00")) {
		t.Fatalf("最终余额应为 0，得 %s", e.balanceOf(t, ctx, user))
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
	e.fund(t, ctx, a, "100.00")
	e.fund(t, ctx, b, "100.00")

	const n = 40
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errList []error
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
				Kind: KindCommissionToBalance,
				Memo: "并发顺序测试",
				Legs: legs,
			})
			if err != nil {
				mu.Lock()
				errList = append(errList, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	for _, err := range errList {
		t.Errorf("Post 失败: %v", err)
	}
	if !e.balanceOf(t, ctx, a).Equal(dec("100.00")) || !e.balanceOf(t, ctx, b).Equal(dec("100.00")) {
		t.Fatalf("A/B 余额应仍为 100，得 A=%s B=%s", e.balanceOf(t, ctx, a), e.balanceOf(t, ctx, b))
	}
	e.assertNoDrift(t, ctx)
}

func TestConcurrentEnsureSameAccount(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)

	const n = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errList []error

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Post(context.Background(), Posting{
				Kind: KindBalanceTopup,
				Legs: []Leg{
					{Account: PlatformAccount(ChannelCash, "test"), Direction: Debit, Amount: dec("1.00")},
					{Account: UserAccount(UserBalance, user), Direction: Credit, Amount: dec("1.00")},
				},
			})
			if err != nil {
				mu.Lock()
				errList = append(errList, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for _, err := range errList {
		t.Errorf("fund 失败: %v", err)
	}
	if !e.balanceOf(t, ctx, user).Equal(dec("10.00")) {
		t.Fatalf("余额应为 10，得 %s", e.balanceOf(t, ctx, user))
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
