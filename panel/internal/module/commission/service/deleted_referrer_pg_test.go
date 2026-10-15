package service

import (
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
)

// 已注销的邀请人等同比例 0：跳过、仍占他那一级，下一级不往上挪。
// 第 2 级注销：第 1、3 级照拿；直接邀请人（第 1 级）注销：整单按全局比例算总额，第 2、3 级照拿自己那一级。
func TestDeletedReferrerSkippedButKeepsLevel(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 50, 30, 20)
	buyer, a, b, c := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	refer(e, b, c)

	// 第 2 级 b 已注销
	e.refs.markDeleted(b)
	o1 := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o1); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o1.OrderID)
	if len(rows) != 2 || rows[0].Referrer != a || rows[0].Level != 1 || !rows[0].Amount.Equal(dec2("5.00")) ||
		rows[1].Referrer != c || rows[1].Level != 3 || !rows[1].Amount.Equal(dec2("2.00")) {
		t.Fatalf("b 已注销时条目不对: %+v", rows)
	}
	if bal := accountBalance(t, e, ctx, b, ledgersvc.UserCommissionPending); !bal.IsZero() {
		t.Fatalf("已注销的 b 不应有待结佣金: %s", bal)
	}

	// 直接邀请人 a 也已注销（b 仍注销）：只剩第 3 级 c
	e.refs.markDeleted(a)
	o2 := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
		t.Fatal(err)
	}
	rows = entriesOf(t, e, ctx, o2.OrderID)
	if len(rows) != 1 || rows[0].Referrer != c || rows[0].Level != 3 || !rows[0].Amount.Equal(dec2("2.00")) || rows[0].RateBP != 1000 {
		t.Fatalf("a、b 都已注销时条目不对: %+v", rows)
	}
	if bal := accountBalance(t, e, ctx, a, ledgersvc.UserCommissionPending); !bal.Equal(dec2("5.00")) {
		t.Fatalf("a 待结只应有注销前那一单的 5.00: %s", bal)
	}
}

// 只有直接邀请人这一级已注销：第 2、3 级照拿自己那一级，不往上挪；没开多级分销时直接邀请人注销就没有佣金。
func TestDeletedDirectReferrer(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	buyer, a, b, c := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	refer(e, b, c)
	e.refs.markDeleted(a)

	o1 := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o1); err != nil {
		t.Fatal(err)
	}
	if rows := entriesOf(t, e, ctx, o1.OrderID); len(rows) != 0 {
		t.Fatalf("没开多级时直接邀请人已注销不应有条目: %+v", rows)
	}

	setDistribution(t, e, ctx, admin, true, 50, 30, 20)
	o2 := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o2.OrderID)
	if len(rows) != 2 || rows[0].Referrer != b || rows[0].Level != 2 || !rows[0].Amount.Equal(dec2("3.00")) ||
		rows[1].Referrer != c || rows[1].Level != 3 || !rows[1].Amount.Equal(dec2("2.00")) {
		t.Fatalf("a 已注销时条目不对: %+v", rows)
	}
	if bal := accountBalance(t, e, ctx, a, ledgersvc.UserCommissionPending); !bal.IsZero() {
		t.Fatalf("已注销的 a 不应有待结佣金: %s", bal)
	}
}
