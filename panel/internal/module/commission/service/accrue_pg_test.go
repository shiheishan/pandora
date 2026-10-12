package service

import (
	"context"
	"errors"
	"testing"
	"time"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestAccrueOnOrderPaid(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrder(t, e, ctx, buyer, "123.45")

	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatalf("计提: %v", err)
	}
	var (
		gotReferrer, gotReferee uuid.UUID
		base, amount            decimal.Decimal
		rateBP                  int64
		status, orderNo         string
		frozen                  time.Time
	)
	if err := e.row(ctx, `SELECT referrer_user_id, referee_user_id, base_amount, rate_bp, commission_amount, status, order_no, frozen_until
		FROM commission_entries WHERE order_id = ?`, o.OrderID).
		Scan(&gotReferrer, &gotReferee, &base, &rateBP, &amount, &status, &orderNo, &frozen); err != nil {
		t.Fatalf("读条目: %v", err)
	}
	if gotReferrer != referrer || gotReferee != buyer || !base.Equal(dec2("123.45")) || rateBP != 1000 ||
		!amount.Equal(dec2("12.34")) || status != EntryPending || orderNo != o.OrderNo {
		t.Fatalf("条目不对: %s %s base=%s bp=%d amount=%s status=%s no=%s", gotReferrer, gotReferee, base, rateBP, amount, status, orderNo)
	}
	if want := o.PaidAt.Add(72 * time.Hour); frozen.Sub(want).Abs() > time.Second {
		t.Fatalf("冻结到 %s，应为付款时间 + 3 天 %s", frozen, want)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !bal.Equal(dec2("12.34")) {
		t.Fatalf("待结佣金科目 %s", bal)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_accrued' AND source_id = ?`, o.OrderID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("计提交易 %d 笔 err=%v", n, err)
	}
}

func TestAccrueTwiceOnlyOnce(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	for i := 0; i < 2; i++ {
		if err := e.svc.OnOrderPaid(ctx, o); err != nil {
			t.Fatalf("第 %d 次计提: %v", i+1, err)
		}
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, o.OrderID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("条目 %d 行 err=%v", n, err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !bal.Equal(dec2("10.00")) {
		t.Fatalf("待结佣金科目 %s，应只记一次", bal)
	}
}

func TestAccrueSkips(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	referrer := newUser(t, e, ctx)

	count := func(orderID uuid.UUID) int {
		t.Helper()
		var n int
		if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, orderID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 没有推荐人
	lone := newUser(t, e, ctx)
	o := newPaidOrder(t, e, ctx, lone, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil || count(o.OrderID) != 0 {
		t.Fatalf("没有推荐人: err=%v n=%d", err, count(o.OrderID))
	}

	// 充值单
	buyer := newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o = newPaidOrder(t, e, ctx, buyer, "100.00")
	o.Kind = ordersvc.KindTopup
	if err := e.svc.OnOrderPaid(ctx, o); err != nil || count(o.OrderID) != 0 {
		t.Fatalf("充值单: err=%v n=%d", err, count(o.OrderID))
	}

	// 佣金截断为 0
	setConfig(t, e, ctx, admin, 1, 3, "100.00", "every_order")
	o = newPaidOrder(t, e, ctx, buyer, "0.05")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil || count(o.OrderID) != 0 {
		t.Fatalf("截断为 0: err=%v n=%d", err, count(o.OrderID))
	}

	// 比例 0
	setConfig(t, e, ctx, admin, 0, 3, "100.00", "every_order")
	o = newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil || count(o.OrderID) != 0 {
		t.Fatalf("比例 0: err=%v n=%d", err, count(o.OrderID))
	}
}

func TestAccrueFirstOrderScope(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "first_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	for _, total := range []string{"100.00", "200.00"} {
		if err := e.svc.OnOrderPaid(ctx, newPaidOrder(t, e, ctx, buyer, total)); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE referee_user_id = ?`, buyer).Scan(&n); err != nil || n != 1 {
		t.Fatalf("首单口径应只有 1 条，得到 %d err=%v", n, err)
	}
}

func TestAccrueSameSourceNeedsReview(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	e.svc.UseSourceMatcher(&fakeSources{same: map[[2]uuid.UUID]bool{{referrer, buyer}: true}})
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o.OrderID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SettleDue(ctx, SettleBatchDefault); err != nil {
		t.Fatal(err)
	}
	var review bool
	var reason *string
	var status string
	if err := e.row(ctx, `SELECT review_required, review_reason, status FROM commission_entries WHERE order_id = ?`, o.OrderID).
		Scan(&review, &reason, &status); err != nil {
		t.Fatal(err)
	}
	if !review || reason == nil || *reason == "" || status != EntryPending {
		t.Fatalf("同源应待复核且不解冻: review=%v reason=%v status=%s", review, reason, status)
	}
}

func TestSettleDue(t *testing.T) {
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
	// 另一笔冻结 3 天的不该被解冻
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	buyer2 := newUser(t, e, ctx)
	refer(e, buyer2, referrer)
	o2 := newPaidOrder(t, e, ctx, buyer2, "50.00")
	if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.SettleDue(ctx, SettleBatchDefault); err != nil {
		t.Fatalf("解冻: %v", err)
	}
	var status string
	var settleTxn *uuid.UUID
	if err := e.row(ctx, `SELECT status, settle_txn_id FROM commission_entries WHERE order_id = ?`, o.OrderID).Scan(&status, &settleTxn); err != nil {
		t.Fatal(err)
	}
	if status != EntryAvailable || settleTxn == nil {
		t.Fatalf("应已解冻: status=%s txn=%v", status, settleTxn)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !bal.Equal(dec2("5.00")) {
		t.Fatalf("待结佣金科目 %s，应只剩冻结中的 5.00", bal)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(dec2("10.00")) {
		t.Fatalf("可提现佣金科目 %s", bal)
	}
	if err := e.row(ctx, `SELECT status FROM commission_entries WHERE order_id = ?`, o2.OrderID).Scan(&status); err != nil || status != EntryPending {
		t.Fatalf("冻结中的条目 status=%s err=%v", status, err)
	}

	// 再跑一轮不重复记账
	if _, err := e.svc.SettleDue(ctx, SettleBatchDefault); err != nil {
		t.Fatal(err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(dec2("10.00")) {
		t.Fatalf("第二轮后可提现佣金科目 %s", bal)
	}
}

// 同源检查失败或没装配时失败即关：照样计提（不挡结算），但转人工复核；查询放在保存点里，事务照常可用。
func TestAccrueSourceCheckFailClosed(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	referrer := newUser(t, e, ctx)

	reasonOf := func(orderID uuid.UUID) string {
		t.Helper()
		var review bool
		var reason *string
		if err := e.row(ctx, `SELECT review_required, review_reason FROM commission_entries WHERE order_id = ?`, orderID).
			Scan(&review, &reason); err != nil {
			t.Fatal(err)
		}
		if !review || reason == nil {
			t.Fatalf("应转人工复核: review=%v reason=%v", review, reason)
		}
		return *reason
	}

	e.svc.UseSourceMatcher(&fakeSources{err: errors.New("查询超时")})
	b1 := newUser(t, e, ctx)
	refer(e, b1, referrer)
	o1 := newPaidOrder(t, e, ctx, b1, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o1); err != nil {
		t.Fatalf("同源检查失败不该挡结算: %v", err)
	}
	if r := reasonOf(o1.OrderID); r != reviewSourceCheckFailed {
		t.Fatalf("原因 %q", r)
	}

	e.svc.UseSourceMatcher(nil)
	b2 := newUser(t, e, ctx)
	refer(e, b2, referrer)
	o2 := newPaidOrder(t, e, ctx, b2, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
		t.Fatal(err)
	}
	if r := reasonOf(o2.OrderID); r != reviewSourceCheckMissing {
		t.Fatalf("原因 %q", r)
	}
	// 两条都不自动解冻
	if _, err := e.svc.SettleDue(ctx, SettleBatchDefault); err != nil {
		t.Fatal(err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.IsZero() {
		t.Fatalf("待复核的佣金被解冻了: %s", bal)
	}
}

// divZeroSources 在同源查询里真执行一条会失败的 SQL：事务被作废，只有保存点能把它救回来。
type divZeroSources struct {
	exec func(ctx context.Context, q string, args ...any) error
}

func (d divZeroSources) SharesSource(ctx context.Context, _, _ uuid.UUID) (bool, error) {
	return false, d.exec(ctx, `SELECT 1/0`)
}

// 同源查询里的 SQL 失败后，保存点把事务救回：计提分录与条目照常写入并转复核，事务里后续语句照常执行。
func TestAccrueSourceSQLFailureRescuedBySavepoint(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	e.svc.UseSourceMatcher(divZeroSources{exec: e.exec})

	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatalf("同源查询 SQL 失败不该挡结算: %v", err)
	}
	var review bool
	var reason *string
	if err := e.row(ctx, `SELECT review_required, review_reason FROM commission_entries WHERE order_id = ?`, o.OrderID).
		Scan(&review, &reason); err != nil {
		t.Fatalf("事务应已被保存点救回: %v", err)
	}
	if !review || reason == nil || *reason != reviewSourceCheckFailed {
		t.Fatalf("应转复核: review=%v reason=%v", review, reason)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_accrued' AND source_id = ?`, o.OrderID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("计提分录 %d 笔 err=%v", n, err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !bal.Equal(dec2("10.00")) {
		t.Fatalf("待结佣金科目 %s", bal)
	}
	// 同一事务里接着计提另一单（查询照旧失败、照旧被救回），写库照常
	b2 := newUser(t, e, ctx)
	refer(e, b2, referrer)
	o2 := newPaidOrder(t, e, ctx, b2, "50.00")
	if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
		t.Fatalf("第二单: %v", err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !bal.Equal(dec2("15.00")) {
		t.Fatalf("两单后待结佣金科目 %s", bal)
	}
}
