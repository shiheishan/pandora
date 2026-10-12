package service

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

func TestHoldAndRelease(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	order := uuid.New()
	e.fund(t, ctx, user, cur, "10.00")

	hold, err := e.svc.HoldForOrder(ctx, HoldInput{
		OrderID: order, UserID: user, Currency: cur, Amount: dec("4.00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if hold.Status != HoldHeld || !hold.Amount.Equal(dec("4.00")) {
		t.Fatalf("冻结状态/金额不对: %+v", hold)
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("6.00")) {
		t.Fatalf("可用余额应为 6")
	}
	if !e.accountBalance(t, ctx, UserAccount(UserBalanceHold, user), cur).Equal(dec("4.00")) {
		t.Fatalf("hold 科目应为 4")
	}

	ok, err := e.svc.ReleaseOrderHold(ctx, order, "取消")
	if err != nil || !ok {
		t.Fatalf("首次释放应 true: ok=%v err=%v", ok, err)
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("10.00")) {
		t.Fatalf("释放后可用应为 10")
	}
	if !e.accountBalance(t, ctx, UserAccount(UserBalanceHold, user), cur).Equal(dec("0")) {
		t.Fatalf("hold 科目应为 0")
	}
	var st string
	if err := e.row(ctx, `SELECT status FROM balance_holds WHERE order_id = ?`, order).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != HoldReleased {
		t.Fatalf("hold 行应为 released，得 %s", st)
	}

	ok2, err := e.svc.ReleaseOrderHold(ctx, order, "")
	if err != nil || ok2 {
		t.Fatalf("再次释放应 false 无错: ok=%v err=%v", ok2, err)
	}
	ok3, err := e.svc.ReleaseOrderHold(ctx, uuid.New(), "")
	if err != nil || ok3 {
		t.Fatalf("不存在订单应 false: ok=%v err=%v", ok3, err)
	}
	e.assertNoDrift(t, ctx)
}

func TestHoldInsufficient(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	e.fund(t, ctx, user, cur, "3.00")
	_, err := e.svc.HoldForOrder(ctx, HoldInput{
		OrderID: uuid.New(), UserID: user, Currency: cur, Amount: dec("4.00"),
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("期望余额不足，得 %v", err)
	}
}

func TestHoldTwice(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	order := uuid.New()
	e.fund(t, ctx, user, cur, "10.00")
	if _, err := e.svc.HoldForOrder(ctx, HoldInput{
		OrderID: order, UserID: user, Currency: cur, Amount: dec("1.00"),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.HoldForOrder(ctx, HoldInput{
		OrderID: order, UserID: user, Currency: cur, Amount: dec("1.00"),
	})
	if !errors.Is(err, ErrHoldExists) {
		t.Fatalf("第二次冻结应 ErrHoldExists，得 %v", err)
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("9.00")) {
		t.Fatalf("ErrHoldExists 后同一 ctx 可用余额应为 9，得 %s", e.balanceOf(t, ctx, user, cur))
	}
}

func TestPostOrderPaidMixed(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	order := uuid.New()
	e.fund(t, ctx, user, cur, "10.00")
	if _, err := e.svc.HoldForOrder(ctx, HoldInput{
		OrderID: order, UserID: user, Currency: cur, Amount: dec("3.00"),
	}); err != nil {
		t.Fatal(err)
	}

	txnID, err := e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: order, UserID: user, Currency: cur,
		Total: dec("10.00"), Paid: dec("7.00"), Fee: dec("0.20"), Provider: "epay",
	})
	if err != nil || txnID == uuid.Nil {
		t.Fatalf("结算应成功: id=%v err=%v", txnID, err)
	}
	if !e.accountBalance(t, ctx, UserAccount(UserBalanceHold, user), cur).Equal(dec("0")) {
		t.Fatalf("hold 应为 0")
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("7.00")) {
		t.Fatalf("用户余额应为 7")
	}
	if !e.accountBalance(t, ctx, PlatformAccount(PlatformRevenue, RefMain), cur).Equal(dec("10.00")) {
		t.Fatalf("revenue 应为 10")
	}
	if !e.accountBalance(t, ctx, PlatformAccount(ChannelCash, "epay"), cur).Equal(dec("6.80")) {
		t.Fatalf("channel 应为 6.80")
	}
	if !e.accountBalance(t, ctx, PlatformAccount(PlatformFeeExpense, "epay"), cur).Equal(dec("0.20")) {
		t.Fatalf("fee 应为 0.20")
	}
	var st string
	var capID uuid.UUID
	if err := e.row(ctx, `SELECT status, capture_txn_id FROM balance_holds WHERE order_id = ?`, order).Scan(&st, &capID); err != nil {
		t.Fatal(err)
	}
	if st != HoldCaptured || capID != txnID {
		t.Fatalf("冻结应 captured 且 capture_txn_id 匹配: %s %v", st, capID)
	}
	_, err = e.svc.ReleaseOrderHold(ctx, order, "")
	if !errors.Is(err, ErrHoldCaptured) {
		t.Fatalf("已扣款后释放应 ErrHoldCaptured，得 %v", err)
	}
	e.assertNoDrift(t, ctx)
}

func TestPostOrderPaidChannelOnly(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	order := uuid.New()
	if _, err := e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: order, UserID: user, Currency: cur,
		Total: dec("5.00"), Paid: dec("5.00"), Fee: dec("0"), Provider: "epay",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: order, UserID: user, Currency: cur,
		Total: dec("5.00"), Paid: dec("5.00"), Fee: dec("0"), Provider: "epay",
	})
	if !errors.Is(err, ErrDuplicatePosting) {
		t.Fatalf("第二笔应 ErrDuplicatePosting，得 %v", err)
	}
	var paidN int64
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions WHERE kind='order_paid' AND source_id = ?`, order).Scan(&paidN); err != nil {
		t.Fatal(err)
	}
	if paidN != 1 {
		t.Fatalf("重复后 order_paid 应仍为 1 笔，得 %d", paidN)
	}
}

func TestPostOrderPaidAfterRelease(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	order := uuid.New()
	e.fund(t, ctx, user, cur, "10.00")
	if _, err := e.svc.HoldForOrder(ctx, HoldInput{
		OrderID: order, UserID: user, Currency: cur, Amount: dec("3.00"),
	}); err != nil {
		t.Fatal(err)
	}
	ok, err := e.svc.ReleaseOrderHold(ctx, order, "释放后支付")
	if err != nil || !ok {
		t.Fatalf("释放应成功: ok=%v err=%v", ok, err)
	}
	_, err = e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: order, UserID: user, Currency: cur,
		Total: dec("10.00"), Paid: dec("7.00"), Provider: "epay",
	})
	if !errors.Is(err, ErrHoldReleased) {
		t.Fatalf("冻结已释放后结算应 ErrHoldReleased，得 %v", err)
	}
	if codeOf(err) != errs.Conflict {
		t.Fatalf("ErrHoldReleased 应为 Conflict，得 %s", codeOf(err))
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("10.00")) {
		t.Fatalf("余额应仍为 10，得 %s", e.balanceOf(t, ctx, user, cur))
	}
	e.assertNoDrift(t, ctx)
}

func TestCaptureFromBalance(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	order := uuid.New()
	e.fund(t, ctx, user, cur, "10.00")

	h, txnID, err := e.svc.CaptureFromBalance(ctx, HoldInput{
		OrderID: order, UserID: user, Currency: cur, Amount: dec("4.00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != HoldCaptured {
		t.Fatalf("状态应为 captured，得 %s", h.Status)
	}
	if h.CaptureTxnID == nil || *h.CaptureTxnID != txnID || txnID == uuid.Nil {
		t.Fatalf("CaptureTxnID 应等于返回的 txnID: cap=%v txn=%v", h.CaptureTxnID, txnID)
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("6.00")) {
		t.Fatalf("用户余额应为 6")
	}
	if !e.accountBalance(t, ctx, UserAccount(UserBalanceHold, user), cur).Equal(dec("0")) {
		t.Fatalf("hold 科目应为 0")
	}
	if !e.accountBalance(t, ctx, PlatformAccount(PlatformRevenue, RefMain), cur).Equal(dec("4.00")) {
		t.Fatalf("revenue 应为 4")
	}
	var st string
	var capID string
	if err := e.row(ctx, `SELECT status, capture_txn_id::text FROM balance_holds WHERE order_id = ?`, order).Scan(&st, &capID); err != nil {
		t.Fatal(err)
	}
	if st != HoldCaptured || capID != txnID.String() {
		t.Fatalf("hold 行应为 captured 且 capture_txn_id 匹配: %s %s", st, capID)
	}
	_, err = e.svc.ReleaseOrderHold(ctx, order, "")
	if !errors.Is(err, ErrHoldCaptured) {
		t.Fatalf("已扣款后释放应 ErrHoldCaptured，得 %v", err)
	}
	_, _, err = e.svc.CaptureFromBalance(ctx, HoldInput{
		OrderID: order, UserID: user, Currency: cur, Amount: dec("4.00"),
	})
	if !errors.Is(err, ErrHoldExists) {
		t.Fatalf("同单再 Capture 应 ErrHoldExists，得 %v", err)
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("6.00")) {
		t.Fatalf("ErrHoldExists 后余额应仍为 6，得 %s", e.balanceOf(t, ctx, user, cur))
	}
	e.assertNoDrift(t, ctx)
}

func TestCaptureFromBalanceInsufficient(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	e.fund(t, ctx, user, cur, "3.00")
	_, _, err := e.svc.CaptureFromBalance(ctx, HoldInput{
		OrderID: uuid.New(), UserID: user, Currency: cur, Amount: dec("4.00"),
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("余额不足应 ErrInsufficientBalance，得 %v", err)
	}
}

func TestPostOrderPaidBalanceOnly(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	order := uuid.New()
	e.fund(t, ctx, user, cur, "5.00")
	if _, err := e.svc.HoldForOrder(ctx, HoldInput{
		OrderID: order, UserID: user, Currency: cur, Amount: dec("5.00"),
	}); err != nil {
		t.Fatal(err)
	}
	txnID, err := e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: order, UserID: user, Currency: cur,
		Total: dec("5.00"), Paid: dec("0"),
	})
	if err != nil || txnID == uuid.Nil {
		t.Fatalf("纯余额结算应成功: %v %v", txnID, err)
	}
	if !e.accountBalance(t, ctx, PlatformAccount(PlatformRevenue, RefMain), cur).Equal(dec("5.00")) {
		t.Fatalf("revenue 应为 5")
	}
	var st string
	if err := e.row(ctx, `SELECT status FROM balance_holds WHERE order_id = ?`, order).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != HoldCaptured {
		t.Fatalf("应为 captured，得 %s", st)
	}
}

func TestPostOrderPaidZeroTotal(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	order := uuid.New()
	var before int64
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	id, err := e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: order, UserID: user, Currency: cur, Total: dec("0"), Paid: dec("0"),
	})
	if err != nil || id != uuid.Nil {
		t.Fatalf("零总额应 Nil 无错: id=%v err=%v", id, err)
	}
	var after int64
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("不应新增交易")
	}
}

func TestPostOrderPaidMismatch(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	order := uuid.New()
	e.fund(t, ctx, user, cur, "10.00")
	if _, err := e.svc.HoldForOrder(ctx, HoldInput{
		OrderID: order, UserID: user, Currency: cur, Amount: dec("3.00"),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: order, UserID: user, Currency: cur,
		Total: dec("10.00"), Paid: dec("8.00"), Provider: "epay",
	})
	if !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("抵扣与冻结不一致应 ErrInvalidPosting，得 %v", err)
	}

	_, err = e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: uuid.New(), UserID: user, Currency: cur,
		Total: dec("5.00"), Paid: dec("6.00"), Provider: "epay",
	})
	if !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("Paid>Total 应 ErrInvalidPosting，得 %v", err)
	}
	_, err = e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: uuid.New(), UserID: user, Currency: cur,
		Total: dec("10.00"), Paid: dec("5.00"), Fee: dec("6.00"), Provider: "epay",
	})
	if !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("Fee>Paid 应 ErrInvalidPosting，得 %v", err)
	}
	_, err = e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: uuid.New(), UserID: user, Currency: cur,
		Total: dec("10.00"), Paid: dec("1.00"), Fee: dec("0"),
	})
	if !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("Paid>0 缺 Provider 应 ErrInvalidPosting，得 %v", err)
	}
}

func TestPostOrderPaidHoldNotUsed(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	order := uuid.New()
	e.fund(t, ctx, user, cur, "10.00")
	if _, err := e.svc.HoldForOrder(ctx, HoldInput{
		OrderID: order, UserID: user, Currency: cur, Amount: dec("3.00"),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.PostOrderPaid(ctx, OrderPaid{
		OrderID: order, UserID: user, Currency: cur,
		Total: dec("10.00"), Paid: dec("10.00"), Provider: "x",
	})
	if !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("有 held 冻结但全渠道付应 ErrInvalidPosting，得 %v", err)
	}
}
