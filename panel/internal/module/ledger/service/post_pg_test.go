package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

func TestPostTopupUpdatesBalances(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	e.fund(t, ctx, user, cur, "10.00")

	if !e.balanceOf(t, ctx, user, cur).Equal(dec("10.00")) {
		t.Fatalf("用户余额应为 10，实际 %s", e.balanceOf(t, ctx, user, cur))
	}
	ch := PlatformAccount(ChannelCash, "test")
	if !e.accountBalance(t, ctx, ch, cur).Equal(dec("10.00")) {
		t.Fatalf("channel_cash/test 应为 10，实际 %s", e.accountBalance(t, ctx, ch, cur))
	}

	var txnID uuid.UUID
	if err := e.row(ctx, `SELECT id FROM ledger_transactions WHERE kind = 'balance_topup' ORDER BY occurred_at DESC LIMIT 1`).Scan(&txnID); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM ledger_entries e JOIN ledger_transactions t ON t.id = e.transaction_id WHERE t.id = ?`, txnID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("分录数应为 2，实际 %d", n)
	}
	e.assertNoDrift(t, ctx)
}

func TestPostActorFromContext(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	amt := dec("1.00")
	post := func(c context.Context) uuid.UUID {
		id, err := e.svc.Post(c, Posting{
			Kind:     KindBalanceTopup,
			Currency: cur,
			Legs: []Leg{
				{Account: PlatformAccount(ChannelCash, "test"), Direction: Debit, Amount: amt},
				{Account: UserAccount(UserBalance, user), Direction: Credit, Amount: amt},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	sysID := post(ctx)
	var kind string
	var actorID sql.NullString
	if err := e.row(ctx, `SELECT actor_kind, actor_id::text FROM ledger_transactions WHERE id = ?`, sysID).Scan(&kind, &actorID); err != nil {
		t.Fatal(err)
	}
	if kind != "system" || actorID.Valid {
		t.Fatalf("无主体应为 system/NULL，得 %s %v", kind, actorID)
	}

	uid := uuid.New()
	userTxn := post(asUser(ctx, uid))
	if err := e.row(ctx, `SELECT actor_kind, actor_id::text FROM ledger_transactions WHERE id = ?`, userTxn).Scan(&kind, &actorID); err != nil {
		t.Fatal(err)
	}
	if kind != "user" || !actorID.Valid || actorID.String != uid.String() {
		t.Fatalf("门户用户 actor 不对: %s %v", kind, actorID)
	}

	admin := uuid.New()
	adminTxn := post(asAdmin(ctx, admin))
	if err := e.row(ctx, `SELECT actor_kind, actor_id::text FROM ledger_transactions WHERE id = ?`, adminTxn).Scan(&kind, &actorID); err != nil {
		t.Fatal(err)
	}
	if kind != "admin" || !actorID.Valid || actorID.String != admin.String() {
		t.Fatalf("管理员 actor 不对: %s %v", kind, actorID)
	}
}

func TestPostInsufficientBalance(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	e.fund(t, ctx, user, cur, "5.00")

	var before int64
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	_, err := e.svc.Post(ctx, Posting{
		Kind:     KindCommissionToBalance,
		Currency: cur,
		Memo:     "测试扣减原因说明",
		Legs: []Leg{
			{Account: UserAccount(UserBalance, user), Direction: Debit, Amount: dec("10.00")},
			{Account: PlatformAccount(Suspense, RefManualAdjust), Direction: Credit, Amount: dec("10.00")},
		},
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("期望 ErrInsufficientBalance，得 %v", err)
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("5.00")) {
		t.Fatalf("余额应仍为 5")
	}
	var after int64
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("不应新增交易: before=%d after=%d", before, after)
	}
}

func TestPostUnknownUser(t *testing.T) {
	e, ctx := newEnv(t)
	ghost := uuid.New()
	cur := money.USD
	_, err := e.svc.Post(ctx, Posting{
		Kind:     KindBalanceTopup,
		Currency: cur,
		Legs: []Leg{
			{Account: PlatformAccount(ChannelCash, "test"), Direction: Debit, Amount: dec("1.00")},
			{Account: UserAccount(UserBalance, ghost), Direction: Credit, Amount: dec("1.00")},
		},
	})
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("期望 ErrUserNotFound，得 %v", err)
	}
	if codeOf(err) != errs.NotFound {
		t.Fatalf("code 应为 not_found，得 %s", codeOf(err))
	}
}

func TestPostDuplicateBySource(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	orderID := uuid.New()
	cur := money.USD
	leg := []Leg{
		{Account: PlatformAccount(ChannelCash, "p"), Direction: Debit, Amount: dec("1.00")},
		{Account: UserAccount(UserBalance, user), Direction: Credit, Amount: dec("1.00")},
	}
	p := Posting{
		Kind:       KindBalanceTopup,
		Currency:   cur,
		SourceType: SourceOrder,
		SourceID:   orderID,
		Legs:       leg,
	}
	if _, err := e.svc.Post(ctx, p); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Post(ctx, p)
	if !errors.Is(err, ErrDuplicatePosting) {
		t.Fatalf("第二笔 balance_topup 应 ErrDuplicatePosting，得 %v", err)
	}
	var cnt int64
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'balance_topup' AND source_id = ?`, orderID).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("同一来源应只有 1 笔交易，得 %d", cnt)
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("1.00")) {
		t.Fatalf("余额应为 1.00，得 %s", e.balanceOf(t, ctx, user, cur))
	}
}

func TestPostRejectsInvalid(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD
	_, err := e.svc.Post(ctx, Posting{
		Kind:     KindBalanceTopup,
		Currency: cur,
		Legs: []Leg{
			{Account: PlatformAccount(ChannelCash, "test"), Direction: Debit, Amount: dec("10.00")},
			{Account: UserAccount(UserBalance, user), Direction: Credit, Amount: dec("9.00")},
		},
	})
	if !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("借贷不平应 ErrInvalidPosting，得 %v", err)
	}
	e.fund(t, ctx, user, cur, "1.00")
}

func TestPostRejectsDedicatedKinds(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	cur := money.USD

	var before int64
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	baseLegs := []Leg{
		{Account: PlatformAccount(ChannelCash, "test"), Direction: Debit, Amount: dec("1.00")},
		{Account: UserAccount(UserBalance, user), Direction: Credit, Amount: dec("1.00")},
	}
	dedicated := []Kind{
		KindBalanceHold,
		KindBalanceRelease,
		KindOrderPaid,
		KindBalanceAdjusted,
	}
	for _, k := range dedicated {
		p := Posting{Kind: k, Currency: cur, Legs: baseLegs}
		if k == KindBalanceAdjusted {
			p.Memo = "五个字以上原因"
		}
		_, err := e.svc.Post(ctx, p)
		if !errors.Is(err, ErrInvalidPosting) {
			t.Fatalf("kind %s 应 ErrInvalidPosting，得 %v", k, err)
		}
	}

	var after int64
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("专用 kind 不应落库: before=%d after=%d", before, after)
	}
}

func TestBalancesBatch(t *testing.T) {
	e, ctx := newEnv(t)
	u1 := e.mkUser(t, ctx)
	u2 := e.mkUser(t, ctx)
	u3 := e.mkUser(t, ctx)
	cur := money.USD
	e.fund(t, ctx, u1, cur, "1.00")
	e.fund(t, ctx, u2, cur, "2.00")

	m, err := e.svc.Balances(ctx, []uuid.UUID{u1, u2, u3}, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Fatalf("应 3 个键，得 %d", len(m))
	}
	if !m[u1].Equal(dec("1.00")) || !m[u2].Equal(dec("2.00")) || !m[u3].Equal(dec("0")) {
		t.Fatalf("批量余额不对: %v", m)
	}

	empty, err := e.svc.Balances(ctx, nil, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("空切片应空 map，得 %v", empty)
	}
}

func TestMyWallet(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	cur := money.USD
	e.fund(t, ctx, user, cur, "10.00")

	_, err := e.svc.AdjustBalance(asAdmin(ctx, admin), Adjustment{
		UserID:   user,
		Amount:   dec("-3.00"),
		Currency: "USD",
		Reason:   "测试扣减原因",
	})
	if err != nil {
		t.Fatal(err)
	}

	w, err := e.svc.MyWallet(asUser(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if w.Currency != cur || !w.Balance.Equal(dec("7.00")) {
		t.Fatalf("钱包余额/币种不对: %s %s", w.Currency, w.Balance)
	}
	if len(w.History) != 2 {
		t.Fatalf("明细应为 2 条，得 %d", len(w.History))
	}
	h0 := w.History[0]
	if h0.Kind != string(KindBalanceAdjusted) || !h0.Delta.Equal(dec("-3.00")) || h0.Memo != "测试扣减原因" {
		t.Fatalf("最新明细不对: %+v", h0)
	}
	if !w.History[1].Delta.Equal(dec("10.00")) {
		t.Fatalf("第二条应为 +10: %+v", w.History[1])
	}

	_, err = e.svc.MyWallet(ctx)
	if codeOf(err) != errs.Unauthorized {
		t.Fatalf("无主体应 unauthorized，得 %s %v", codeOf(err), err)
	}
}
