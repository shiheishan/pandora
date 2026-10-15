package service

import (
	"context"
	"net/http"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// setFee 给渠道设用户手续费（元、百分比）。
func (e *env) setFee(t *testing.T, ctx context.Context, code, fixed, percent string) {
	t.Helper()
	if err := e.exec(ctx, `UPDATE payment_providers SET fee_fixed = ?, fee_percent = ? WHERE code = ?`, fixed, percent, code); err != nil {
		t.Fatal(err)
	}
}

func TestCreateIntentAddsSurcharge(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-sc-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	e.setFee(t, ctx, code, "0.30", "2")
	orderID, orderNo := e.mkOrder(t, ctx, user)

	res := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")
	if !res.Amount.Equal(dec("10.40")) || !res.Surcharge.Equal(dec("0.50")) {
		t.Fatalf("意图金额 %s surcharge %s", res.Amount, res.Surcharge)
	}

	var dbAmt, dbSur string
	if err := e.row(ctx, `SELECT amount::text, surcharge_amount::text FROM payment_intents WHERE id = ?`, res.IntentID).
		Scan(&dbAmt, &dbSur); err != nil {
		t.Fatal(err)
	}
	if !dec(dbAmt).Equal(dec("10.40")) || !dec(dbSur).Equal(dec("0.50")) {
		t.Fatalf("库里 amount=%s surcharge_amount=%s", dbAmt, dbSur)
	}

	res2 := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")
	if !res2.Reused || !res2.Amount.Equal(dec("10.40")) || !res2.Surcharge.Equal(dec("0.50")) {
		t.Fatalf("复用 Reused=%v Amount=%s Surcharge=%s", res2.Reused, res2.Amount, res2.Surcharge)
	}
}

func TestWebhookSettlesWithSurcharge(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-sc-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	e.setFee(t, ctx, code, "0.30", "2")
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	payRef := "pay-sc-settle-1"
	in := fakeInbound("evt-sc-settle-1", "payment.succeeded", payRef, intent.OutTradeNo, 1040)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("应 200: %s", string(ack.Body))
	}

	if !e.settler.last.Amount.Equal(dec("9.90")) || !e.settler.last.Surcharge.Equal(dec("0.50")) {
		t.Fatalf("settler last Amount=%s Surcharge=%s", e.settler.last.Amount, e.settler.last.Surcharge)
	}

	var payAmt, paySur string
	if err := e.row(ctx, `SELECT amount::text, surcharge_amount::text FROM payments WHERE provider_payment_id = ?`, payRef).
		Scan(&payAmt, &paySur); err != nil {
		t.Fatal(err)
	}
	if !dec(payAmt).Equal(dec("10.40")) || !dec(paySur).Equal(dec("0.50")) {
		t.Fatalf("payments amount=%s surcharge_amount=%s", payAmt, paySur)
	}

	cash := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.ChannelCash, code), money.CNY)
	rev := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefMain), money.CNY)
	surRev := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefSurcharge), money.CNY)
	if !cash.Equal(dec("10.40")) || !rev.Equal(dec("9.90")) || !surRev.Equal(dec("0.50")) {
		t.Fatalf("渠道现金 %s、主营收入 %s、手续费收入 %s", cash, rev, surRev)
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("应有 order_paid")
	}
	e.assertNoDrift(t, ctx)
}

func TestWebhookNotPayableSurcharge(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-sc-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	e.setFee(t, ctx, code, "0.30", "2")
	orderID, orderNo := e.mkOrder(t, ctx, user)
	e.settler.mu.Lock()
	e.settler.notPayable[orderID] = NotPayable(CaseReleasedOrder, "cancelled")
	e.settler.mu.Unlock()
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	in := fakeInbound("evt-sc-np-1", "payment.succeeded", "pay-sc-np-1", intent.OutTradeNo, 1040)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("应 200: %s", string(ack.Body))
	}

	var amt string
	if err := e.row(ctx, `SELECT amount::text FROM late_payment_cases WHERE order_id = ?`, orderID).Scan(&amt); err != nil {
		t.Fatal(err)
	}
	if !dec(amt).Equal(dec("9.90")) {
		t.Fatalf("late_payment_cases.amount=%s", amt)
	}

	susp := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.LatePaymentSuspense, ledgersvc.RefMain), money.CNY)
	surRev := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefSurcharge), money.CNY)
	cash := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.ChannelCash, code), money.CNY)
	if !susp.Equal(dec("9.90")) || !surRev.Equal(dec("0.50")) || !cash.Equal(dec("10.40")) {
		t.Fatalf("挂账 %s、手续费收入 %s、渠道现金 %s", susp, surRev, cash)
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 0 {
		t.Fatal("不应有 order_paid")
	}
	e.assertNoDrift(t, ctx)
}

func TestWebhookAmountWithoutSurchargeRejected(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-sc-" + uuid.New().String()[:8]
	rec := e.mkFakeProvider(t, ctx, code)
	e.setFee(t, ctx, code, "0.30", "2")
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	in := fakeInbound("evt-sc-amt-1", "payment.succeeded", "pay-sc-amt-1", intent.OutTradeNo, 990)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusConflict {
		t.Fatalf("期望 409，得 %d body=%s", ack.HTTPStatus, string(ack.Body))
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments WHERE provider_id = ?`, rec.ID) != 0 {
		t.Fatal("不应有 payments")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 0 {
		t.Fatal("不应有 order_paid")
	}
}

func TestPaymentMethodsSurcharge(t *testing.T) {
	e, ctx := newEnv(t)
	code := "fake-sc-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	e.setFee(t, ctx, code, "0.30", "2")

	amt := dec("9.90")
	withAmt, err := e.svc.PaymentMethods(ctx, &amt)
	if err != nil {
		t.Fatal(err)
	}
	var found *PaymentMethod
	for i := range withAmt {
		if withAmt[i].Provider == code {
			found = &withAmt[i]
			break
		}
	}
	if found == nil {
		t.Fatal("未找到该渠道")
	}
	if found.Fee == nil || found.Total == nil {
		t.Fatal("带 amount 时应有 Fee 与 Total")
	}
	if !found.Fee.Equal(dec("0.50")) || !found.Total.Equal(dec("10.40")) {
		t.Fatalf("Fee=%s Total=%s", found.Fee, found.Total)
	}
	if !found.FeeFixed.Equal(dec("0.30")) || !found.FeePercent.Equal(dec("2")) {
		t.Fatalf("FeeFixed=%s FeePercent=%s", found.FeeFixed, found.FeePercent)
	}

	noAmt, err := e.svc.PaymentMethods(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range noAmt {
		if noAmt[i].Provider == code {
			if noAmt[i].Fee != nil || noAmt[i].Total != nil {
				t.Fatalf("无 amount 时 Fee/Total 应为 nil，得 %v %v", noAmt[i].Fee, noAmt[i].Total)
			}
			return
		}
	}
	t.Fatal("未找到该渠道")
}

func TestZeroFeeUnchanged(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-sc-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)

	res := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")
	if !res.Amount.Equal(dec("9.90")) || !res.Surcharge.Equal(decimal.Zero) {
		t.Fatalf("Amount=%s Surcharge=%s", res.Amount, res.Surcharge)
	}

	payRef := "pay-sc-zero-1"
	in := fakeInbound("evt-sc-zero-1", "payment.succeeded", payRef, res.OutTradeNo, 990)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("应 200: %s", string(ack.Body))
	}

	surRev := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefSurcharge), money.CNY)
	if !surRev.Equal(decimal.Zero) {
		t.Fatalf("手续费收入科目 %s", surRev)
	}
	var paySur string
	if err := e.row(ctx, `SELECT surcharge_amount::text FROM payments WHERE provider_payment_id = ?`, payRef).Scan(&paySur); err != nil {
		t.Fatal(err)
	}
	if !dec(paySur).Equal(decimal.Zero) {
		t.Fatalf("payments.surcharge_amount=%s", paySur)
	}
}

func TestPaymentSettingsPrefix(t *testing.T) {
	e, ctx := newEnv(t)

	ps, err := e.svc.PaymentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ps.NotifyBaseURL != "" || ps.EffectiveBase != "https://panel.example.test" {
		t.Fatalf("初始 NotifyBaseURL=%q EffectiveBase=%q", ps.NotifyBaseURL, ps.EffectiveBase)
	}

	if err := e.svc.SetPaymentSettings(ctx, "https://pay-cb.example.test/"); err != nil {
		t.Fatal(err)
	}
	ps, err = e.svc.PaymentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ps.NotifyBaseURL != "https://pay-cb.example.test" || ps.EffectiveBase != "https://pay-cb.example.test" {
		t.Fatalf("设置后 NotifyBaseURL=%q EffectiveBase=%q", ps.NotifyBaseURL, ps.EffectiveBase)
	}

	err = e.svc.SetPaymentSettings(ctx, "https://a.example.test?x=1")
	if err == nil {
		t.Fatal("带查询串应失败")
	}
	if ve := errs.As(err); ve.Fields["notify_base_url"] == "" {
		t.Fatalf("应有 notify_base_url 字段: %+v", ve)
	}

	cands, err := e.svc.notifyURLCandidates(ctx, "fake-x")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://pay-cb.example.test/v1/webhooks/payments/fake-x",
		"https://panel.example.test/v1/webhooks/payments/fake-x",
	}
	if len(cands) != len(want) {
		t.Fatalf("candidates=%v", cands)
	}
	for i := range want {
		if cands[i] != want[i] {
			t.Fatalf("candidates[%d]=%q want %q", i, cands[i], want[i])
		}
	}

	if err := e.svc.SetPaymentSettings(ctx, ""); err != nil {
		t.Fatal(err)
	}
	ps, err = e.svc.PaymentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ps.EffectiveBase != "https://panel.example.test" {
		t.Fatalf("清除后 EffectiveBase=%q", ps.EffectiveBase)
	}
	cands, err = e.svc.notifyURLCandidates(ctx, "fake-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0] != "https://panel.example.test/v1/webhooks/payments/fake-x" {
		t.Fatalf("清除后 candidates=%v", cands)
	}
}
