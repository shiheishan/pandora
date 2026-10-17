package service

import (
	"errors"
	"strings"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestOverrideSetListDelete(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	a := newUser(t, e, ctx)
	e.refs.mu.Lock()
	storedEmail := e.refs.emails[a]
	e.refs.mu.Unlock()

	zero := 0
	got, err := e.svc.SetOverride(adminCtx(ctx, admin), OverrideInput{
		Email:       strings.ToUpper(storedEmail),
		RatePercent: &zero,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != storedEmail || got.RatePercent == nil || *got.RatePercent != 0 || got.Scope != nil {
		t.Fatalf("第一次设置: %+v", got)
	}

	list, err := e.svc.ListOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].UserID != a || list[0].RatePercent == nil || *list[0].RatePercent != 0 || list[0].Scope != nil {
		t.Fatalf("List 第一次: %+v", list)
	}

	scope := settingssvc.CommissionScopeFirstOrder
	got2, err := e.svc.SetOverride(adminCtx(ctx, admin), OverrideInput{
		Email: storedEmail,
		Scope: &scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got2.RatePercent != nil || got2.Scope == nil || *got2.Scope != settingssvc.CommissionScopeFirstOrder {
		t.Fatalf("第二次设置: %+v", got2)
	}

	list, err = e.svc.ListOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].RatePercent != nil || list[0].Scope == nil || *list[0].Scope != settingssvc.CommissionScopeFirstOrder {
		t.Fatalf("List 第二次: %+v", list)
	}

	var nSet int
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, ActionOverrideSet, a).Scan(&nSet); err != nil || nSet != 2 {
		t.Fatalf("ActionOverrideSet 审计 %d err=%v", nSet, err)
	}

	if err := e.svc.DeleteOverride(adminCtx(ctx, admin), a); err != nil {
		t.Fatal(err)
	}
	var nRm int
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, ActionOverrideRemoved, a).Scan(&nRm); err != nil || nRm != 1 {
		t.Fatalf("ActionOverrideRemoved 审计 %d err=%v", nRm, err)
	}
	list, err = e.svc.ListOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list == nil || len(list) != 0 {
		t.Fatalf("删除后 List 应为空切片，得 %v", list)
	}

	if err := e.svc.DeleteOverride(adminCtx(ctx, admin), a); !errors.Is(err, ErrOverrideNotFound) {
		t.Fatalf("再删应 NotFound: %v", err)
	}
}

func TestOverrideValidation(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)

	if _, err := e.svc.SetOverride(adminCtx(ctx, admin), OverrideInput{
		Email: "nobody-x@example.test", RatePercent: ptrInt(10),
	}); errCode(err) != errs.ValidationFailed {
		t.Fatalf("找不到邮箱: %v", err)
	}

	bad := 51
	if _, err := e.svc.SetOverride(adminCtx(ctx, admin), OverrideInput{
		Email: "any@example.test", RatePercent: &bad,
	}); errCode(err) != errs.ValidationFailed {
		t.Fatalf("比例 51: %v", err)
	}

	if _, err := e.svc.SetOverride(adminCtx(ctx, admin), OverrideInput{Email: "any@example.test"}); errCode(err) != errs.ValidationFailed {
		t.Fatalf("两项都 nil: %v", err)
	}
}

func ptrInt(n int) *int { return &n }

func TestOverrideRateZeroStopsCommission(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	buyer, a := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	e.refs.mu.Lock()
	emailA := e.refs.emails[a]
	e.refs.mu.Unlock()
	zero := 0
	if _, err := e.svc.SetOverride(adminCtx(ctx, admin), OverrideInput{Email: emailA, RatePercent: &zero}); err != nil {
		t.Fatal(err)
	}
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, o.OrderID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("应没有条目: %d err=%v", n, err)
	}
}

func TestOverrideScopeFirstOrder(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	buyer, a := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	e.refs.mu.Lock()
	emailA := e.refs.emails[a]
	e.refs.mu.Unlock()
	scope := settingssvc.CommissionScopeFirstOrder
	if _, err := e.svc.SetOverride(adminCtx(ctx, admin), OverrideInput{Email: emailA, Scope: &scope}); err != nil {
		t.Fatal(err)
	}

	o1 := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o1); err != nil {
		t.Fatal(err)
	}
	var n1 int
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, o1.OrderID).Scan(&n1); err != nil || n1 != 1 {
		t.Fatalf("第一单应有条目: %d err=%v", n1, err)
	}

	o2 := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
		t.Fatal(err)
	}
	var n2 int
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, o2.OrderID).Scan(&n2); err != nil || n2 != 0 {
		t.Fatalf("第二单不应有条目: %d err=%v", n2, err)
	}

	c, buyer2 := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer2, c)
	for i := 0; i < 2; i++ {
		ox := newPaidOrder(t, e, ctx, buyer2, "50.00")
		if err := e.svc.OnOrderPaid(ctx, ox); err != nil {
			t.Fatal(err)
		}
		var nx int
		if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, ox.OrderID).Scan(&nx); err != nil || nx != 1 {
			t.Fatalf("buyer2 第 %d 单应有条目: %d err=%v", i+1, nx, err)
		}
	}
}

// 单独比例 0 = 这个人不拿佣金，不论他在第几级（用户 10-14 定）：跳过他、继续往上找，他占的那一级不由别人补位。
func TestOverrideRateZeroSkipsAtEveryLevel(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 50, 30, 20)
	buyer, a, b, c := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	refer(e, b, c)
	e.refs.mu.Lock()
	emailA, emailB := e.refs.emails[a], e.refs.emails[b]
	e.refs.mu.Unlock()
	zero := 0

	// 第 2 级 b 被排除：a 拿第 1 级、c 拿第 3 级，b 什么都没有
	if _, err := e.svc.SetOverride(adminCtx(ctx, admin), OverrideInput{Email: emailB, RatePercent: &zero}); err != nil {
		t.Fatal(err)
	}
	o1 := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o1); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o1.OrderID)
	if len(rows) != 2 || rows[0].Referrer != a || rows[0].Level != 1 || !rows[0].Amount.Equal(dec2("5.00")) ||
		rows[1].Referrer != c || rows[1].Level != 3 || !rows[1].Amount.Equal(dec2("2.00")) {
		t.Fatalf("b 被排除时条目不对: %+v", rows)
	}
	if bal := accountBalance(t, e, ctx, b, ledgersvc.UserCommissionPending); !bal.IsZero() {
		t.Fatalf("b 不应有待结佣金: %s", bal)
	}

	// 直接邀请人 a 被排除：整单按全局比例算总额，b、c 照拿第 2、3 级
	if err := e.svc.DeleteOverride(adminCtx(ctx, admin), b); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetOverride(adminCtx(ctx, admin), OverrideInput{Email: emailA, RatePercent: &zero}); err != nil {
		t.Fatal(err)
	}
	o2 := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
		t.Fatal(err)
	}
	rows = entriesOf(t, e, ctx, o2.OrderID)
	if len(rows) != 2 || rows[0].Referrer != b || rows[0].Level != 2 || !rows[0].Amount.Equal(dec2("3.00")) ||
		rows[1].Referrer != c || rows[1].Level != 3 || !rows[1].Amount.Equal(dec2("2.00")) {
		t.Fatalf("a 被排除时条目不对: %+v", rows)
	}
	for _, r := range rows {
		if r.RateBP != 1000 {
			t.Fatalf("a 被排除时按全局比例，rate_bp 应为 1000，得 %d", r.RateBP)
		}
	}
	if bal := accountBalance(t, e, ctx, a, ledgersvc.UserCommissionPending); !bal.Equal(dec2("5.00")) {
		t.Fatalf("a 待结只应有第一单的 5.00: %s", bal)
	}
}
