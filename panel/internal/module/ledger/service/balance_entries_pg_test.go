package service

import (
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
