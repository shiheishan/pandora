package repo

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/ledger/model"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

func TestBalanceEntries(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)
	g := d.Conn(ctx)

	user := mkUser(t, g)
	keys := []AccountKey{
		platformKey("channel_cash", "alipay"),
		userKey("user_balance", user),
		userKey("user_balance_hold", user),
	}
	if err := r.EnsureAccounts(ctx, keys); err != nil {
		t.Fatal(err)
	}
	accs, err := r.LockAccounts(ctx, keys)
	if err != nil || len(accs) != 3 {
		t.Fatalf("LockAccounts: %v", err)
	}
	var cashID, balID, holdAccID uuid.UUID
	for _, a := range accs {
		switch a.AccountType {
		case "channel_cash":
			cashID = a.ID
		case "user_balance":
			balID = a.ID
		case "user_balance_hold":
			holdAccID = a.ID
		}
	}

	amt10 := decimal.RequireFromString("10")
	if err := r.InsertTransaction(ctx, &model.Transaction{
		Kind: "balance_topup", Memo: "充值", ActorKind: "system",
	}, []model.Entry{
		{AccountID: cashID, Direction: "debit", Amount: amt10},
		{AccountID: balID, Direction: "credit", Amount: amt10},
	}); err != nil {
		t.Fatal(err)
	}

	amt3 := decimal.RequireFromString("3")
	src := "order"
	orderID := uuid.New()
	if err := r.InsertTransaction(ctx, &model.Transaction{
		Kind: "balance_hold", SourceType: &src, SourceID: &orderID, ActorKind: "system",
	}, []model.Entry{
		{AccountID: balID, Direction: "debit", Amount: amt3},
		{AccountID: holdAccID, Direction: "credit", Amount: amt3},
	}); err != nil {
		t.Fatal(err)
	}

	amt5 := decimal.RequireFromString("5")
	if err := r.InsertTransaction(ctx, &model.Transaction{
		Kind: "balance_topup", Memo: "再充", ActorKind: "system",
	}, []model.Entry{
		{AccountID: cashID, Direction: "debit", Amount: amt5},
		{AccountID: balID, Direction: "credit", Amount: amt5},
	}); err != nil {
		t.Fatal(err)
	}

	rows, total, err := r.BalanceEntries(ctx, user, "user_balance", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 2 {
		t.Fatalf("第一页: total=%d len=%d", total, len(rows))
	}
	if rows[0].Kind != "balance_topup" || !rows[0].CreditsThrough.Equal(decimal.RequireFromString("15")) ||
		!rows[0].DebitsThrough.Equal(amt3) {
		t.Fatalf("最新一行: %+v", rows[0])
	}
	if rows[1].Kind != "balance_hold" || !rows[1].CreditsThrough.Equal(amt10) || !rows[1].DebitsThrough.Equal(amt3) {
		t.Fatalf("冻结行: %+v", rows[1])
	}
	if rows[1].SourceType == nil || *rows[1].SourceType != "order" {
		t.Fatalf("冻结来源: %+v", rows[1].SourceType)
	}

	rows, total, err = r.BalanceEntries(ctx, user, "user_balance", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 1 {
		t.Fatalf("第三页首条: total=%d len=%d", total, len(rows))
	}
	if rows[0].Kind != "balance_topup" || !rows[0].CreditsThrough.Equal(amt10) || !rows[0].DebitsThrough.IsZero() {
		t.Fatalf("最早充值: %+v", rows[0])
	}

	rows, total, err = r.BalanceEntries(ctx, user, "user_balance", 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 0 {
		t.Fatalf("翻过头: total=%d len=%d", total, len(rows))
	}

	other := mkUser(t, g)
	rows, total, err = r.BalanceEntries(ctx, other, "user_balance", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(rows) != 0 || rows == nil {
		t.Fatalf("无科目用户: total=%d rows=%v", total, rows)
	}
}
