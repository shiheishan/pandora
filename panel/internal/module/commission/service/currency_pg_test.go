package service

import (
	"errors"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 站点币种是 USD（缺省），订单是 CNY：佣金照样看得见、提得出，后台概览按币种列出；
// 不能转余额（钱包只显示站点币种），回 409 提示走提现。
func TestNonSiteCurrencyCommission(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	if site, err := e.ledger.DefaultCurrency(ctx); err != nil || site != money.USD {
		t.Fatalf("前提：站点币种应是 USD，得到 %s %v", site, err)
	}
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	if amt := accrueAvailableIn(t, e, ctx, referrer, buyer, "200.00", money.CNY); !amt.Equal(dec2("20.00")) {
		t.Fatalf("佣金 %s", amt)
	}
	uctx := userCtx(ctx, referrer)

	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	if mc.Summary.Currency != money.CNY || !mc.Summary.Available.Equal(dec2("20.00")) || !mc.Summary.TotalEarned.Equal(dec2("20.00")) {
		t.Fatalf("汇总应按 CNY 算: %+v", mc.Summary)
	}

	id, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "alipay a@example.test")
	if err != nil {
		t.Fatalf("CNY 佣金提现: %v", err)
	}
	var cur string
	if err := e.row(ctx, `SELECT currency FROM withdrawals WHERE id = ?`, id).Scan(&cur); err != nil || cur != "CNY" {
		t.Fatalf("提现币种 %s err=%v", cur, err)
	}
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("5.00")); !errors.Is(err, ErrTransferNotSiteCurrency) {
		t.Fatalf("非站点币种的佣金转余额应 409: %v", err)
	}
	if bal := accountBalanceIn(t, e, ctx, referrer, ledgersvc.UserBalance, money.CNY); !bal.IsZero() {
		t.Fatalf("CNY 余额 %s", bal)
	}

	// 打款走 CNY 科目
	actx := adminCtx(ctx, admin)
	if _, err := e.svc.ReviewWithdrawal(actx, id, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.MarkWithdrawalPaid(actx, id, "TX-CNY-1"); err != nil {
		t.Fatalf("CNY 打款: %v", err)
	}
	if bal := accountBalanceIn(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable, money.CNY); !bal.Equal(dec2("5.00")) {
		t.Fatalf("CNY 可提现佣金科目 %s", bal)
	}

	ov, err := e.svc.AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Currency != money.USD {
		t.Fatalf("概览顶层币种 %s", ov.Currency)
	}
	var cny *CurrencyOverview
	for i := range ov.ByCurrency {
		if ov.ByCurrency[i].Currency == money.CNY {
			cny = &ov.ByCurrency[i]
		}
	}
	if cny == nil || cny.TotalEarned.LessThan(dec2("20.00")) || cny.PaidOut.LessThan(dec2("15.00")) {
		t.Fatalf("概览缺 CNY 一组: %+v", ov.ByCurrency)
	}
}

// 两个币种都有可用额时提现不合并、不猜，回 409；转余额只认站点币种，照常能转。只剩一个币种有可用额时提现就用它。
func TestMultiCurrencyAmbiguous(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer := newUser(t, e, ctx)
	accrueAvailableIn(t, e, ctx, referrer, newUser(t, e, ctx), "200.00", money.USD)
	accrueAvailableIn(t, e, ctx, referrer, newUser(t, e, ctx), "300.00", money.CNY)
	uctx := userCtx(ctx, referrer)

	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("10.00"), "alipay a@example.test"); !errors.Is(err, ErrWithdrawCurrencyAmbiguous) {
		t.Fatalf("多币种提现应 409: %v", err)
	}
	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	if mc.Summary.Currency != money.USD || len(mc.Entries) != 2 {
		t.Fatalf("两个币种都有可用额时汇总取站点币种: %s，明细 %d 条", mc.Summary.Currency, len(mc.Entries))
	}
	// 转余额只动 USD；USD 转光后再转回「不足」（还有 CNY 可用，但站点币种已没有可用额，提示走提现）
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("20.00")); err != nil {
		t.Fatalf("多币种时转站点币种: %v", err)
	}
	if bal := accountBalanceIn(t, e, ctx, referrer, ledgersvc.UserBalance, money.USD); !bal.Equal(dec2("20.00")) {
		t.Fatalf("USD 余额 %s", bal)
	}
	if bal := accountBalanceIn(t, e, ctx, referrer, ledgersvc.UserBalance, money.CNY); !bal.IsZero() {
		t.Fatalf("CNY 余额 %s", bal)
	}
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("0.01")); !errors.Is(err, ErrTransferNotSiteCurrency) {
		t.Fatalf("只剩 CNY 可用时转余额应提示走提现: %v", err)
	}
	// USD 转光后只剩 CNY 可用，提现就用 CNY
	wid, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("30.00"), "alipay a@example.test")
	if err != nil {
		t.Fatalf("只剩 CNY 可用时提现: %v", err)
	}
	var wcur string
	if err := e.row(ctx, `SELECT currency FROM withdrawals WHERE id = ?`, wid).Scan(&wcur); err != nil || wcur != "CNY" {
		t.Fatalf("提现币种 %s err=%v", wcur, err)
	}

	// 另一个推荐人：USD 已解冻、CNY 还在冻结期，只有一个币种有可用额，就用它
	other := newUser(t, e, ctx)
	accrueAvailableIn(t, e, ctx, other, newUser(t, e, ctx), "200.00", money.USD)
	setConfig(t, e, ctx, admin, 10, 3, "10.00", "every_order")
	b := newUser(t, e, ctx)
	refer(e, b, other)
	if err := e.svc.OnOrderPaid(ctx, newPaidOrderIn(t, e, ctx, b, "300.00", money.CNY)); err != nil {
		t.Fatal(err)
	}
	id, err := e.svc.RequestWithdrawal(userCtx(ctx, other), other, dec2("20.00"), "alipay b@example.test")
	if err != nil {
		t.Fatalf("只有 USD 可用时应能提现: %v", err)
	}
	var cur string
	if err := e.row(ctx, `SELECT currency FROM withdrawals WHERE id = ?`, id).Scan(&cur); err != nil || cur != "USD" {
		t.Fatalf("提现币种 %s err=%v", cur, err)
	}
}
