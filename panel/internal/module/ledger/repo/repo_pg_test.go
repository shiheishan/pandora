package repo

// 本文件通过 Repo API 证明科目、分录、余额与冻结的读写行为与库约束一致（schema_pg_test 已覆盖纯 SQL 底线）。

import (
	"bytes"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/ledger/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

func platformKey(typ, ref string) AccountKey {
	return AccountKey{Type: typ, Currency: "CNY", OwnerRef: ref}
}

func userKey(typ string, user uuid.UUID) AccountKey {
	return AccountKey{Type: typ, Currency: "CNY", OwnerUserID: &user, OwnerRef: ""}
}

func TestEnsureAndLockAccounts(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)
	g := d.Conn(ctx)

	user := mkUser(t, g)
	keys := []AccountKey{
		platformKey("channel_cash", "alipay"),
		userKey("user_balance", user),
	}
	if err := r.EnsureAccounts(ctx, keys); err != nil {
		t.Fatalf("第一次 EnsureAccounts: %v", err)
	}
	if err := r.EnsureAccounts(ctx, keys); err != nil {
		t.Fatalf("第二次 EnsureAccounts: %v", err)
	}

	var n int64
	if err := g.Raw(`SELECT count(*) FROM ledger_accounts WHERE
		(account_type = 'channel_cash' AND currency = 'CNY' AND owner_user_id IS NULL AND owner_ref = 'alipay')
		OR (account_type = 'user_balance' AND currency = 'CNY' AND owner_user_id = ? AND owner_ref = '')`, user).
		Row().Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("匹配科目应为 2 行，got %d", n)
	}

	rows, err := r.LockAccounts(ctx, keys)
	if err != nil {
		t.Fatalf("LockAccounts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("LockAccounts 应返回 2 行，got %d", len(rows))
	}
	if bytes.Compare(rows[0].ID[:], rows[1].ID[:]) >= 0 {
		t.Fatalf("LockAccounts 应按 id 升序")
	}
	for _, row := range rows {
		if !row.Balance.IsZero() {
			t.Fatalf("新建科目余额应为 0，got %s", row.Balance)
		}
	}
}

func TestEnsureAccountsUnknownUser(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)

	ghost := uuid.New()
	err := r.EnsureAccounts(ctx, []AccountKey{userKey("user_balance", ghost)})
	if !errors.Is(err, ErrForeignKey) {
		t.Fatalf("未知用户: want ErrForeignKey, got %v", err)
	}
}

func TestSetBalancesAndUserBalances(t *testing.T) {
	d := testkit.DB(t)

	t.Run("读写余额", func(t *testing.T) {
		ctx := testkit.TxCtx(t, d)
		r := New(d)
		g := d.Conn(ctx)
		user := mkUser(t, g)
		keys := []AccountKey{userKey("user_balance", user)}
		if err := r.EnsureAccounts(ctx, keys); err != nil {
			t.Fatal(err)
		}
		locked, err := r.LockAccounts(ctx, keys)
		if err != nil || len(locked) != 1 {
			t.Fatalf("LockAccounts: %v, n=%d", err, len(locked))
		}
		want := decimal.RequireFromString("12.34")
		if err := r.SetBalances(ctx, map[uuid.UUID]decimal.Decimal{locked[0].ID: want}); err != nil {
			t.Fatal(err)
		}
		m, err := r.UserBalances(ctx, "user_balance", "CNY", []uuid.UUID{user})
		if err != nil {
			t.Fatal(err)
		}
		if got := m[user]; !got.Equal(want) {
			t.Fatalf("UserBalances: want %s, got %s", want, got)
		}
	})

	t.Run("负余额", func(t *testing.T) {
		ctx := testkit.TxCtx(t, d)
		r := New(d)
		g := d.Conn(ctx)
		user := mkUser(t, g)
		keys := []AccountKey{userKey("user_balance", user)}
		if err := r.EnsureAccounts(ctx, keys); err != nil {
			t.Fatal(err)
		}
		locked, err := r.LockAccounts(ctx, keys)
		if err != nil || len(locked) != 1 {
			t.Fatal(err)
		}
		err = r.SetBalances(ctx, map[uuid.UUID]decimal.Decimal{
			locked[0].ID: decimal.RequireFromString("-1"),
		})
		if !errors.Is(err, ErrCheck) {
			t.Fatalf("负余额: want ErrCheck, got %v", err)
		}
	})

	t.Run("非法精度", func(t *testing.T) {
		ctx := testkit.TxCtx(t, d)
		r := New(d)
		g := d.Conn(ctx)
		user := mkUser(t, g)
		keys := []AccountKey{userKey("user_balance", user)}
		if err := r.EnsureAccounts(ctx, keys); err != nil {
			t.Fatal(err)
		}
		locked, err := r.LockAccounts(ctx, keys)
		if err != nil || len(locked) != 1 {
			t.Fatal(err)
		}
		err = r.SetBalances(ctx, map[uuid.UUID]decimal.Decimal{
			locked[0].ID: decimal.RequireFromString("1.001"),
		})
		if !errors.Is(err, money.ErrPrecision) {
			t.Fatalf("1.001: want money.ErrPrecision, got %v", err)
		}
	})
}

func TestInsertTransactionAndHistory(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)
	g := d.Conn(ctx)

	user := mkUser(t, g)
	keys := []AccountKey{
		platformKey("channel_cash", "alipay"),
		userKey("user_balance", user),
	}
	if err := r.EnsureAccounts(ctx, keys); err != nil {
		t.Fatal(err)
	}
	accs, err := r.LockAccounts(ctx, keys)
	if err != nil || len(accs) != 2 {
		t.Fatalf("LockAccounts: %v", err)
	}
	var cashID, balID uuid.UUID
	for _, a := range accs {
		if a.AccountType == "channel_cash" {
			cashID = a.ID
		} else {
			balID = a.ID
		}
	}
	amt := decimal.RequireFromString("10")
	txn := &model.Transaction{
		Kind:      "balance_topup",
		Currency:  "CNY",
		Memo:      "充值",
		ActorKind: "system",
	}
	entries := []model.Entry{
		{AccountID: cashID, Currency: "CNY", Direction: "debit", Amount: amt},
		{AccountID: balID, Currency: "CNY", Direction: "credit", Amount: amt},
	}
	if err := r.InsertTransaction(ctx, txn, entries); err != nil {
		t.Fatalf("InsertTransaction: %v", err)
	}
	if txn.ID == uuid.Nil {
		t.Fatal("交易 ID 应回填")
	}
	for _, e := range entries {
		if e.TransactionID != txn.ID {
			t.Fatalf("分录 TransactionID 应等于交易 ID")
		}
	}

	rows, err := r.History(ctx, user, "user_balance", "CNY", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("History: want 1 row, got %d", len(rows))
	}
	if rows[0].Kind != "balance_topup" || rows[0].Direction != "credit" ||
		!rows[0].Amount.Equal(amt) || rows[0].Memo != "充值" {
		t.Fatalf("History 行不符合预期: %+v", rows[0])
	}

	emptyCtx := testkit.TxCtx(t, d)
	if err := r.InsertTransaction(emptyCtx, &model.Transaction{
		Kind: "balance_topup", Currency: "CNY", ActorKind: "system",
	}, nil); err == nil {
		t.Fatal("空分录应报错")
	}
}

func TestHolds(t *testing.T) {
	d := testkit.DB(t)

	t.Run("插入与查询", func(t *testing.T) {
		ctx := testkit.TxCtx(t, d)
		r := New(d)
		g := d.Conn(ctx)
		user := mkUser(t, g)
		order := uuid.New()

		keys := []AccountKey{
			userKey("user_balance", user),
			userKey("user_balance_hold", user),
		}
		if err := r.EnsureAccounts(ctx, keys); err != nil {
			t.Fatal(err)
		}
		accs, err := r.LockAccounts(ctx, keys)
		if err != nil || len(accs) != 2 {
			t.Fatal(err)
		}
		var balID, holdAccID uuid.UUID
		for _, a := range accs {
			if a.AccountType == "user_balance" {
				balID = a.ID
			} else {
				holdAccID = a.ID
			}
		}
		amt := decimal.RequireFromString("5")
		src := "order"
		holdTxn := &model.Transaction{
			Kind:       "balance_hold",
			Currency:   "CNY",
			SourceType: &src,
			SourceID:   &order,
			ActorKind:  "system",
		}
		if err := r.InsertTransaction(ctx, holdTxn, []model.Entry{
			{AccountID: balID, Currency: "CNY", Direction: "debit", Amount: amt},
			{AccountID: holdAccID, Currency: "CNY", Direction: "credit", Amount: amt},
		}); err != nil {
			t.Fatal(err)
		}

		h := &model.Hold{
			OrderID:   order,
			UserID:    user,
			Currency:  "CNY",
			Amount:    amt,
			Status:    "held",
			HoldTxnID: holdTxn.ID,
		}
		if err := r.InsertHold(ctx, h); err != nil {
			t.Fatalf("InsertHold: %v", err)
		}
		locked, found, err := r.LockHoldByOrder(ctx, order)
		if err != nil || !found {
			t.Fatalf("LockHoldByOrder: err=%v found=%v", err, found)
		}
		if locked.Status != "held" || locked.ID != h.ID {
			t.Fatalf("冻结状态或 ID 不对: %+v", locked)
		}
	})

	t.Run("同单重复冻结", func(t *testing.T) {
		ctx := testkit.TxCtx(t, d)
		r := New(d)
		g := d.Conn(ctx)
		user := mkUser(t, g)
		order := uuid.New()
		src := "order"
		txnID, err := insertTxn(g, "balance_hold", "CNY", &src, &order)
		if err != nil {
			t.Fatal(err)
		}
		amt := decimal.RequireFromString("1")
		h1 := &model.Hold{
			OrderID: order, UserID: user, Currency: "CNY", Amount: amt,
			Status: "held", HoldTxnID: txnID,
		}
		if err := r.InsertHold(ctx, h1); err != nil {
			t.Fatal(err)
		}
		h2 := &model.Hold{
			OrderID: order, UserID: user, Currency: "CNY", Amount: amt,
			Status: "held", HoldTxnID: txnID,
		}
		err = r.InsertHold(ctx, h2)
		if !errors.Is(err, ErrDuplicate) {
			t.Fatalf("重复冻结: want ErrDuplicate, got %v", err)
		}
	})

	t.Run("无冻结订单", func(t *testing.T) {
		ctx := testkit.TxCtx(t, d)
		r := New(d)
		_, found, err := r.HoldByOrder(ctx, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		if found {
			t.Fatal("不存在的订单应 found=false")
		}
	})

	t.Run("释放与扣款状态", func(t *testing.T) {
		ctx := testkit.TxCtx(t, d)
		r := New(d)
		g := d.Conn(ctx)
		user := mkUser(t, g)
		order := uuid.New()
		src := "order"

		keys := []AccountKey{
			userKey("user_balance", user),
			userKey("user_balance_hold", user),
		}
		if err := r.EnsureAccounts(ctx, keys); err != nil {
			t.Fatal(err)
		}
		accs, err := r.LockAccounts(ctx, keys)
		if err != nil || len(accs) != 2 {
			t.Fatal(err)
		}
		var balID, holdAccID uuid.UUID
		for _, a := range accs {
			if a.AccountType == "user_balance" {
				balID = a.ID
			} else {
				holdAccID = a.ID
			}
		}
		amt := decimal.RequireFromString("3")
		holdTxn := &model.Transaction{
			Kind: "balance_hold", Currency: "CNY", SourceType: &src, SourceID: &order, ActorKind: "system",
		}
		if err := r.InsertTransaction(ctx, holdTxn, []model.Entry{
			{AccountID: balID, Currency: "CNY", Direction: "debit", Amount: amt},
			{AccountID: holdAccID, Currency: "CNY", Direction: "credit", Amount: amt},
		}); err != nil {
			t.Fatal(err)
		}
		h := &model.Hold{
			OrderID: order, UserID: user, Currency: "CNY", Amount: amt,
			Status: "held", HoldTxnID: holdTxn.ID,
		}
		if err := r.InsertHold(ctx, h); err != nil {
			t.Fatal(err)
		}

		relTxn := &model.Transaction{
			Kind: "balance_release", Currency: "CNY", SourceType: &src, SourceID: &order, ActorKind: "system",
		}
		if err := r.InsertTransaction(ctx, relTxn, []model.Entry{
			{AccountID: holdAccID, Currency: "CNY", Direction: "debit", Amount: amt},
			{AccountID: balID, Currency: "CNY", Direction: "credit", Amount: amt},
		}); err != nil {
			t.Fatal(err)
		}

		ok, err := r.MarkHoldReleased(ctx, h.ID, relTxn.ID)
		if err != nil || !ok {
			t.Fatalf("第一次 MarkHoldReleased: ok=%v err=%v", ok, err)
		}
		ok, err = r.MarkHoldReleased(ctx, h.ID, relTxn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("已释放后再 MarkHoldReleased 应为 false")
		}

		// 已 released 的冻结不能再 captured；扣款交易 ID 仅作参数，不会写入。
		ok, err = r.MarkHoldCaptured(ctx, h.ID, relTxn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("已 released 的冻结 MarkHoldCaptured 应为 false")
		}
	})
}

func TestTransactionExists(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)
	g := d.Conn(ctx)

	src := "order"
	order := uuid.New()
	if _, err := insertTxn(g, "order_paid", "CNY", &src, &order); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		kind   string
		source uuid.UUID
		want   bool
	}{
		{"同 kind 同来源", "order_paid", order, true},
		{"别的 kind", "balance_hold", order, false},
		{"别的来源", "order_paid", uuid.New(), false},
	}
	for _, c := range cases {
		got, err := r.TransactionExists(ctx, c.kind, "order", c.source)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("%s: want %v, got %v", c.name, c.want, got)
		}
	}
}
