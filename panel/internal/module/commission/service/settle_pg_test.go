package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 待结科目余额不够（账本被手工动过）是重试也不会好的失败：当场转人工复核，下一轮不再扫到它。
func TestSettleUnderfundedQuarantines(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	// 一笔不对应任何条目的解冻分录把待结科目搬空（模拟账本与条目对不上）
	if _, err := e.ledger.Post(ctx, ledgersvc.Posting{
		Kind:       ledgersvc.KindCommissionSettled,
		SourceType: SourceCommissionEntry, SourceID: uuid.New(), Memo: "测试：搬空待结科目",
		Legs: []ledgersvc.Leg{
			{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionPending, referrer), Direction: ledgersvc.Debit, Amount: dec2("10.00")},
			{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionAvailable, referrer), Direction: ledgersvc.Credit, Amount: dec2("10.00")},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o.OrderID); err != nil {
		t.Fatal(err)
	}

	st, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if err != nil {
		t.Fatalf("余额不足应隔离而不是报错: %v", err)
	}
	if st.Quarantined < 1 {
		t.Fatalf("统计 %+v", st)
	}
	var review bool
	var reason *string
	var status string
	if err := e.row(ctx, `SELECT review_required, review_reason, status FROM commission_entries WHERE order_id = ?`, o.OrderID).
		Scan(&review, &reason, &status); err != nil {
		t.Fatal(err)
	}
	if !review || reason == nil || !strings.Contains(*reason, "余额不足") || status != EntryPending {
		t.Fatalf("应转人工复核: review=%v reason=%v status=%s", review, reason, status)
	}
	// 下一轮扫不到它
	var due int
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ? AND status = 'pending' AND NOT review_required`, o.OrderID).Scan(&due); err != nil || due != 0 {
		t.Fatalf("隔离后仍可被扫到 %d err=%v", due, err)
	}
	if _, err := e.svc.SettleDue(ctx, SettleBatchDefault); err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_settled' AND source_type = ? AND source_id IN (SELECT id FROM commission_entries WHERE order_id = ?)`,
		SourceCommissionEntry, o.OrderID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("被隔离的条目记了 %d 笔解冻 err=%v", n, err)
	}
}

type constraintLedger struct{ Ledger }

func (c constraintLedger) Post(ctx context.Context, p ledgersvc.Posting) (uuid.UUID, error) {
	return uuid.Nil, fmt.Errorf("%w: %w: 测试", ledgersvc.ErrBalanceConstraint, ledgersvc.ErrInsufficientBalance)
}

// 库约束兜底（ErrBalanceConstraint）时语句已失败、事务已作废：不能在同一事务里隔离条目，整笔回滚给下一轮重试。
func TestSettleBalanceConstraintNoQuarantine(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o.OrderID); err != nil {
		t.Fatal(err)
	}

	e.svc.ledger = constraintLedger{Ledger: e.svc.ledger}

	st, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if !errors.Is(err, ledgersvc.ErrBalanceConstraint) {
		t.Fatalf("应返回 ErrBalanceConstraint: %v", err)
	}
	if st.Failed < 1 {
		t.Fatalf("st.Failed 应 >= 1: %+v", st)
	}
	if st.Quarantined != 0 {
		t.Fatalf("st.Quarantined 应为 0: %+v", st)
	}

	var review bool
	var status string
	if err := e.row(ctx, `SELECT review_required, status FROM commission_entries WHERE order_id = ?`, o.OrderID).
		Scan(&review, &status); err != nil {
		t.Fatal(err)
	}
	if review {
		t.Fatalf("条目不应转人工复核 review_required=%v", review)
	}
	if status != EntryPending {
		t.Fatalf("条目状态应仍为 pending: %s", status)
	}
}

// 多个 SettleDue 真并发（各自提交）：每条到期佣金恰好解冻一次，可提现科目等于佣金合计。
func TestConcurrentSettleDue(t *testing.T) {
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
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")

	referrer := newUser(t, e, ctx)
	const orders = 6
	var orderIDs []uuid.UUID
	want := decimal.Zero
	for i := 0; i < orders; i++ {
		b := newUser(t, e, ctx)
		refer(e, b, referrer)
		o := newPaidOrder(t, e, ctx, b, "100.00")
		if err := e.svc.OnOrderPaid(ctx, o); err != nil {
			t.Fatal(err)
		}
		orderIDs = append(orderIDs, o.OrderID)
		want = want.Add(dec2("10.00"))
	}
	// 冻结 0 天：frozen_until 就是付款时间，已到期

	const workers = 4
	var wg sync.WaitGroup
	errsCh := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := e.svc.SettleDue(commitCtx(), SettleBatchDefault); err != nil {
				errsCh <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errsCh)
	for err := range errsCh {
		t.Fatalf("并发解冻: %v", err)
	}

	for _, id := range orderIDs {
		var status string
		var settled int
		if err := e.row(ctx, `
SELECT c.status, (SELECT count(*) FROM ledger_transactions t
                   WHERE t.kind = 'commission_settled' AND t.source_type = ? AND t.source_id = c.id)
  FROM commission_entries c WHERE c.order_id = ?`, SourceCommissionEntry, id).Scan(&status, &settled); err != nil {
			t.Fatal(err)
		}
		if status != EntryAvailable || settled != 1 {
			t.Fatalf("订单 %s 的条目 status=%s 解冻 %d 笔", id, status, settled)
		}
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(want) {
		t.Fatalf("可提现佣金科目 %s，应为 %s", bal, want)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !bal.IsZero() {
		t.Fatalf("待结佣金科目 %s", bal)
	}
}
