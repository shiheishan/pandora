package service

import (
	"context"
	"testing"
)

func TestUserBalanceEntries(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	e.fund(t, ctx, user, "10.00")

	_, err := e.svc.AdjustBalance(asAdmin(ctx, admin), Adjustment{
		UserID: user,
		Amount: dec("-3.00"),
		Reason: "测试扣减原因",
	})
	if err != nil {
		t.Fatal(err)
	}

	orderID := e.newOrder(t, ctx, user)
	_, err = e.svc.HoldForOrder(ctx, HoldInput{
		OrderID: orderID,
		UserID:  user,
		Amount:  dec("2.00"),
	})
	if err != nil {
		t.Fatal(err)
	}

	page, err := e.svc.UserBalanceEntries(ctx, user, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Entries) != 3 {
		t.Fatalf("条数不对: total=%d len=%d", page.Total, len(page.Entries))
	}
	if page.Entries[0].Kind != string(KindBalanceHold) || !page.Entries[0].Delta.Equal(dec("-2.00")) ||
		!page.Entries[0].BalanceAfter.Equal(dec("5.00")) || page.Entries[0].OrderID == nil || *page.Entries[0].OrderID != orderID {
		t.Fatalf("冻结行: %+v", page.Entries[0])
	}
	if page.Entries[1].Kind != string(KindBalanceAdjusted) || !page.Entries[1].Delta.Equal(dec("-3.00")) ||
		!page.Entries[1].BalanceAfter.Equal(dec("7.00")) || page.Entries[1].Memo != "测试扣减原因" || page.Entries[1].OrderID != nil {
		t.Fatalf("调账行: %+v", page.Entries[1])
	}
	if page.Entries[2].Kind != string(KindBalanceTopup) || !page.Entries[2].Delta.Equal(dec("10.00")) ||
		!page.Entries[2].BalanceAfter.Equal(dec("10.00")) {
		t.Fatalf("充值行: %+v", page.Entries[2])
	}
	if !page.Entries[0].BalanceAfter.Equal(e.balanceOf(t, ctx, user)) {
		t.Fatalf("最新变动后余额应等于当前余额")
	}

	wallet, err := e.svc.MyWallet(asUser(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if len(wallet.History) != len(page.Entries) {
		t.Fatalf("与钱包明细条数不一致: wallet=%d admin=%d", len(wallet.History), len(page.Entries))
	}
	for i := range wallet.History {
		if wallet.History[i].Kind != page.Entries[i].Kind || !wallet.History[i].Delta.Equal(page.Entries[i].Delta) {
			t.Fatalf("第 %d 条与钱包不一致: wallet=%+v admin=%+v", i, wallet.History[i], page.Entries[i])
		}
	}

	other := e.mkUser(t, ctx)
	empty, err := e.svc.UserBalanceEntries(ctx, other, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total != 0 || len(empty.Entries) != 0 {
		t.Fatalf("无往来用户: %+v", empty)
	}
}

// 早开事务、晚拿锁：T1（扣减）先开事务，T2（充值）后开但先记账提交，T1 等 T2 的钱到了才扣得动。
// T1 分录的 created_at（事务开始时刻）早于 T2，按 created_at 排 T1 会排在 T2 前面，变动后余额算成负数；
// 按分录 id（插入时、拿到科目锁之后生成的 uuidv7）排，顺序与真实记账一致，每行都是出现过的非负值。
// 单 goroutine 顺序执行：T1 在 tx.Do 里先发一条语句，再在事务外跑完 T2，然后 T1 才去锁科目扣钱。
func TestUserBalanceEntriesEarlyTxLateLock(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e, ctx := newCommitEnv(t)
	admin := e.mkUser(t, ctx)
	user := e.mkUser(t, ctx)
	e.fund(t, ctx, user, "1.00")

	err := e.svc.tx.Do(ctx, func(t1 context.Context) error {
		if err := e.exec(t1, `SELECT 1`); err != nil {
			return err
		}
		// T2：事务外独立提交的充值（T1 还没锁科目，不互等）
		e.fund(t, ctx, user, "5.00")
		// T1：读已提交，锁科目后看得到 T2 的钱，扣 4.00（只靠开户的 1.00 扣不动）
		_, err := e.svc.AdjustBalance(asAdmin(t1, admin), Adjustment{UserID: user, Amount: dec("-4.00"), Reason: "早开事务晚拿锁"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// 先证明场景造出来了：扣减分录的 created_at 早于后一笔充值
	var inverted bool
	if err := e.row(ctx, `
SELECT (SELECT e.created_at FROM ledger_entries e JOIN ledger_transactions t ON t.id = e.transaction_id
         WHERE t.kind = 'balance_adjusted' AND e.account_id = a.id)
     < (SELECT max(e.created_at) FROM ledger_entries e JOIN ledger_transactions t ON t.id = e.transaction_id
         WHERE t.kind = 'balance_topup' AND e.account_id = a.id)
  FROM ledger_accounts a WHERE a.account_type = 'user_balance' AND a.owner_user_id = ?`, user).Scan(&inverted); err != nil {
		t.Fatal(err)
	}
	if !inverted {
		t.Fatal("没造出「扣减分录 created_at 早于充值」的情形")
	}

	page, err := e.svc.UserBalanceEntries(ctx, user, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 {
		t.Fatalf("应 3 条，得 %d", page.Total)
	}
	want := []struct {
		kind         Kind
		delta, after string
	}{
		{KindBalanceAdjusted, "-4.00", "2.00"},
		{KindBalanceTopup, "5.00", "6.00"},
		{KindBalanceTopup, "1.00", "1.00"},
	}
	for i, w := range want {
		got := page.Entries[i]
		if got.Kind != string(w.kind) || !got.Delta.Equal(dec(w.delta)) || !got.BalanceAfter.Equal(dec(w.after)) {
			t.Fatalf("第 %d 条: want %+v got %+v", i, w, got)
		}
		if got.BalanceAfter.IsNegative() {
			t.Fatalf("第 %d 条变动后余额为负: %s", i, got.BalanceAfter)
		}
	}
	wallet, err := e.svc.MyWallet(asUser(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	for i := range wallet.History {
		if wallet.History[i].Kind != page.Entries[i].Kind || !wallet.History[i].Delta.Equal(page.Entries[i].Delta) {
			t.Fatalf("第 %d 条与钱包顺序不一致: wallet=%+v admin=%+v", i, wallet.History[i], page.Entries[i])
		}
	}
}
