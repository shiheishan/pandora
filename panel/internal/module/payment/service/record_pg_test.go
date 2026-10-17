package service

import (
	"net/http"
	"sync"
	"testing"
	"time"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 同一签名回调连发 10 次只入账一笔。
func TestWebhookReplayRecordsOnce(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-replay-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	eventID := "evt-replay-1"
	payRef := "pay-replay-1"
	in := fakeInbound(eventID, "payment.succeeded", payRef, intent.OutTradeNo, 990)

	var lastBody []byte
	for i := 0; i < 10; i++ {
		ack, err := e.svc.HandleWebhook(ctx, code, in)
		if err != nil {
			t.Fatalf("第 %d 次回调: %v", i+1, err)
		}
		if ack.HTTPStatus != http.StatusOK {
			t.Fatalf("第 %d 次 HTTP %d", i+1, ack.HTTPStatus)
		}
		lastBody = ack.Body
	}

	if e.count(t, ctx, `SELECT count(*) FROM payments`) != 1 {
		t.Fatal("payments 应只有 1 行")
	}
	if e.count(t, ctx, `SELECT count(*) FROM payment_events WHERE processing_status = 'processed'`) != 1 {
		t.Fatal("processed 事件应只有 1 行")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("order_paid 应只有 1 笔")
	}
	if e.settler.calls.Load() != 1 {
		t.Fatalf("settler 应调用 1 次，实际 %d", e.settler.calls.Load())
	}
	var st string
	if err := e.row(ctx, `SELECT status FROM payment_intents WHERE id = ?`, intent.IntentID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != string(IntentSucceeded) {
		t.Fatalf("意图状态 %s", st)
	}
	if !ackAlreadyHandled(lastBody) {
		t.Fatalf("最后一次回执应 already_handled: %s", string(lastBody))
	}
	e.assertNoDrift(t, ctx)
}

// 两个 goroutine 同时投递同一回调，仍只记一笔 order_paid。
func TestWebhookConcurrentDuplicates(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-conc-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	eventID := "evt-conc-same"
	payRef := "pay-conc-same"
	in := fakeInbound(eventID, "payment.succeeded", payRef, intent.OutTradeNo, 990)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ack, err := e.svc.HandleWebhook(ctx, code, in)
			if err != nil {
				t.Error(err)
				return
			}
			if ack.HTTPStatus != http.StatusOK {
				t.Errorf("HTTP %d", ack.HTTPStatus)
			}
		}()
	}
	close(start)
	wg.Wait()

	if e.count(t, ctx, `SELECT count(*) FROM payments`) != 1 {
		t.Fatal("同一 payment_id 并发应只有 1 行 payments")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("应只有 1 笔 order_paid")
	}

	// 不同 event_id、同一 payment_id 并发：2 行事件、仍 1 行收款
	orderID2, orderNo2 := e.mkOrder(t, ctx, user)
	intent2 := e.createIntent(t, ctx, code, orderID2, orderNo2, user, "9.90")
	payRef2 := "pay-conc-dual"
	inA := fakeInbound("evt-conc-a", "payment.succeeded", payRef2, intent2.OutTradeNo, 990)
	inB := fakeInbound("evt-conc-b", "payment.succeeded", payRef2, intent2.OutTradeNo, 990)

	start2 := make(chan struct{})
	var wg2 sync.WaitGroup
	for _, in := range []provider.Inbound{inA, inB} {
		wg2.Add(1)
		go func(in provider.Inbound) {
			defer wg2.Done()
			<-start2
			_, err := e.svc.HandleWebhook(ctx, code, in)
			if err != nil {
				t.Error(err)
			}
		}(in)
	}
	close(start2)
	wg2.Wait()

	if e.count(t, ctx, `SELECT count(*) FROM payments WHERE provider_payment_id = ?`, payRef2) != 1 {
		t.Fatal("不同事件号同一 payment_id 应只有 1 行 payments")
	}
	if e.count(t, ctx, `
SELECT count(*) FROM payment_events e
JOIN payment_providers p ON p.id = e.provider_id
WHERE p.code = ? AND e.provider_payment_id = ?`, code, payRef2) != 2 {
		t.Fatal("应有 2 行 payment_events")
	}
}

// 回调金额与意图不一致时整笔拒收。
func TestWebhookAmountMismatchRejected(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-amt-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	for _, cents := range []int64{991, 989} {
		in := fakeInbound("evt-amt-"+uuid.New().String()[:8], "payment.succeeded",
			"pay-amt-"+uuid.New().String()[:8], intent.OutTradeNo, cents)
		ack, err := e.svc.HandleWebhook(ctx, code, in)
		if err != nil {
			t.Fatal(err)
		}
		if ack.HTTPStatus == http.StatusOK {
			t.Fatalf("金额 %d 分不应 200", cents)
		}
		if ack.HTTPStatus != http.StatusConflict {
			t.Fatalf("期望 409，得 %d body=%s", ack.HTTPStatus, string(ack.Body))
		}
	}

	if e.count(t, ctx, `SELECT count(*) FROM payments`) != 0 {
		t.Fatal("不应有 payments")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 0 {
		t.Fatal("不应有 order_paid")
	}
}

// 验签失败仍留证据，同事件号真签名后可入账。
func TestWebhookBadSignatureKeepsEvidence(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-bad-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	eventID := "evt-badsig-1"
	payRef := "pay-badsig-1"
	bad := unsignedInbound(eventID, "payment.succeeded", payRef, intent.OutTradeNo, 990)
	ackBad, err := e.svc.HandleWebhook(ctx, code, bad)
	if err != nil {
		t.Fatal(err)
	}
	if ackBad.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("未签名应 401，得 %d", ackBad.HTTPStatus)
	}

	var sigOK bool
	var status string
	var evID string
	if err := e.row(ctx, `
SELECT signature_verified, processing_status, provider_event_id
FROM payment_events WHERE signature_verified = false`).
		Scan(&sigOK, &status, &evID); err != nil {
		t.Fatal(err)
	}
	if sigOK {
		t.Fatal("signature_verified 应为 false")
	}
	if status != "ignored" {
		t.Fatalf("processing_status=%s", status)
	}
	if len(evID) < 11 || evID[:11] != "unverified:" {
		t.Fatalf("provider_event_id=%q", evID)
	}

	good := fakeInbound(eventID, "payment.succeeded", payRef, intent.OutTradeNo, 990)
	ackGood, err := e.svc.HandleWebhook(ctx, code, good)
	if err != nil {
		t.Fatal(err)
	}
	if ackGood.HTTPStatus != http.StatusOK {
		t.Fatalf("真签名应 200: %s", string(ackGood.Body))
	}
	if e.count(t, ctx, `SELECT count(*) FROM payment_events WHERE processing_status = 'processed'`) != 1 {
		t.Fatal("应有 1 条 processed")
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments`) != 1 {
		t.Fatal("应入账 1 笔")
	}
}

// Settler 返回 NotPayable 时开挂账并记 late_payment_suspense。
func TestWebhookNotPayableQuarantines(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-np-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	e.settler.mu.Lock()
	e.settler.notPayable[orderID] = NotPayable(CaseReleasedOrder, "cancelled")
	e.settler.mu.Unlock()
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	eventID := "evt-np-1"
	payRef := "pay-np-1"
	in := fakeInbound(eventID, "payment.succeeded", payRef, intent.OutTradeNo, 990)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("应 200: %s", string(ack.Body))
	}

	var kind, ostatus, lstatus string
	var amt string
	if err := e.row(ctx, `
SELECT case_kind, order_status, status, amount::text FROM late_payment_cases WHERE order_id = ?`, orderID).
		Scan(&kind, &ostatus, &lstatus, &amt); err != nil {
		t.Fatal(err)
	}
	if kind != string(CaseReleasedOrder) || ostatus != "cancelled" || lstatus != string(LateSuspense) {
		t.Fatalf("挂账行 kind=%s status=%s order=%s", kind, lstatus, ostatus)
	}
	if !decimal.RequireFromString(amt).Equal(dec("9.90")) {
		t.Fatalf("amount=%s", amt)
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindLatePaymentSuspense) != 1 {
		t.Fatal("应有 late_payment_suspense")
	}
	// 桩 Settler 在返回 NotPayable 前已记了一笔 order_paid，保存点必须把它撤回
	if e.settler.calls.Load() != 1 {
		t.Fatalf("settler 应调用 1 次，实际 %d", e.settler.calls.Load())
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 0 {
		t.Fatal("不应 order_paid：Settler 的写入应在保存点上撤回")
	}
	susp := ledgersvc.PlatformAccount(ledgersvc.LatePaymentSuspense, ledgersvc.RefMain)
	bal := e.accountBalance(t, ctx, susp)
	if !bal.Equal(dec("9.90")) {
		t.Fatalf("suspense 贷方余额 %s", bal)
	}

	var ist string
	if err := e.row(ctx, `SELECT status FROM payment_intents WHERE id = ?`, intent.IntentID).Scan(&ist); err != nil {
		t.Fatal(err)
	}
	if ist != string(IntentSucceeded) {
		t.Fatalf("意图 %s", ist)
	}

	ack2, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack2.HTTPStatus != http.StatusOK {
		t.Fatal(ack2.HTTPStatus)
	}
	if e.count(t, ctx, `SELECT count(*) FROM late_payment_cases WHERE order_id = ?`, orderID) != 1 {
		t.Fatal("重放不应新开挂账案")
	}
	e.assertNoDrift(t, ctx)
}

// 渠道报手续费时挂账：渠道净收款与手续费各记一条借方，挂账科目贷方记全额。
func TestWebhookNotPayableQuarantinesWithFee(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-npfee-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	e.settler.mu.Lock()
	e.settler.notPayable[orderID] = NotPayable(CaseReleasedOrder, "cancelled")
	e.settler.mu.Unlock()
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	in := fakeInboundFee("evt-npfee-1", "payment.succeeded", "pay-npfee-1", intent.OutTradeNo, 990, 60)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("应 200: %s", string(ack.Body))
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindLatePaymentSuspense) != 1 {
		t.Fatal("应有 late_payment_suspense")
	}
	cash := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.ChannelCash, code))
	fee := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.PlatformFeeExpense, code))
	susp := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.LatePaymentSuspense, ledgersvc.RefMain))
	if !cash.Equal(dec("9.30")) || !fee.Equal(dec("0.60")) || !susp.Equal(dec("9.90")) {
		t.Fatalf("渠道净收款 %s（应 9.30）、手续费 %s（应 0.60）、挂账 %s（应 9.90）", cash, fee, susp)
	}
	var feeAmt string
	if err := e.row(ctx, `SELECT fee_amount::text FROM payments WHERE provider_payment_id = ?`, "pay-npfee-1").Scan(&feeAmt); err != nil {
		t.Fatal(err)
	}
	if !dec(feeAmt).Equal(dec("0.60")) {
		t.Fatalf("payments.fee_amount=%s", feeAmt)
	}
	e.assertNoDrift(t, ctx)
}

// 手续费大于金额：验签通过、金额与意图一致，仍整笔拒收，不调 Settler、不留收款与分录。
func TestWebhookFeeAboveAmountRejected(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-feehi-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	in := fakeInboundFee("evt-feehi-1", "payment.succeeded", "pay-feehi-1", intent.OutTradeNo, 990, 991)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusConflict {
		t.Fatalf("期望 409，得 %d body=%s", ack.HTTPStatus, string(ack.Body))
	}
	if e.settler.calls.Load() != 0 {
		t.Fatalf("不应调用 Settler，实际 %d 次", e.settler.calls.Load())
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments`) != 0 {
		t.Fatal("不应有 payments")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 0 || e.ledgerTxnCount(t, ctx, ledgersvc.KindLatePaymentSuspense) != 0 {
		t.Fatal("不应有分录")
	}
}

// 金额校验：手续费可以等于金额，大于金额或为负都拒收。
func TestValidateRecordAmountsFee(t *testing.T) {
	cases := []struct {
		amount, fee string
		ok          bool
	}{
		{"9.90", "0", true},
		{"9.90", "0.60", true},
		{"9.90", "9.90", true},
		{"9.90", "9.91", false},
		{"9.90", "-0.01", false},
	}
	for _, c := range cases {
		err := validateRecordAmounts(recordInput{Amount: dec(c.amount), Fee: dec(c.fee)})
		if c.ok != (err == nil) {
			t.Fatalf("金额 %s 手续费 %s：err=%v", c.amount, c.fee, err)
		}
		if err != nil {
			if e := errs.As(err); e.Code != errs.BadRequest || e.Message != "支付金额或手续费不正确" {
				t.Fatalf("金额 %s 手续费 %s：%+v", c.amount, c.fee, e)
			}
		}
	}
}

// RecordOffline 入账、幂等与 NotPayable 不开挂账。
func TestRecordOffline(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	orderID, orderNo := e.mkOrder(t, ctx, user)

	res, err := e.svc.RecordOffline(ctx, OfflineInput{
		OrderID: orderID, OrderNo: orderNo, UserID: user,
		Amount:    dec("9.90"),
		Reference: "BANK-001", Reason: "线下转账",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Processed {
		t.Fatal("应 Processed")
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments WHERE provider_payment_id = 'offline:BANK-001'`) != 1 {
		t.Fatal("payments 一行")
	}
	var intentNull bool
	if err := e.row(ctx, `SELECT payment_intent_id IS NULL FROM payments WHERE order_id = ?`, orderID).Scan(&intentNull); err != nil || !intentNull {
		t.Fatal("payment_intent_id 应为空")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("order_paid 一笔")
	}

	res2, err := e.svc.RecordOffline(ctx, OfflineInput{
		OrderID: orderID, OrderNo: orderNo, UserID: user,
		Amount:    dec("9.90"),
		Reference: "BANK-001", Reason: "线下转账",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.AlreadyHandled {
		t.Fatal("同凭证应 AlreadyHandled")
	}

	orderBad, orderNoBad := e.mkOrder(t, ctx, user)
	e.settler.mu.Lock()
	e.settler.notPayable[orderBad] = NotPayable(CaseReleasedOrder, "cancelled")
	e.settler.mu.Unlock()
	_, err = e.svc.RecordOffline(ctx, OfflineInput{
		OrderID: orderBad, OrderNo: orderNoBad, UserID: user,
		Amount:    dec("1.00"),
		Reference: "BANK-002", Reason: "线下",
	})
	if err == nil {
		t.Fatal("NotPayable 线下应失败")
	}
	if errs.As(err).Code != errs.Conflict {
		t.Fatalf("期望 Conflict，得 %s", errs.As(err).Code)
	}
	if e.count(t, ctx, `SELECT count(*) FROM late_payment_cases`) != 0 {
		t.Fatal("线下 NotPayable 不开挂账")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("线下 NotPayable 时 Settler 的写入应在保存点上撤回，order_paid 仍只有第一笔")
	}

	_, err = e.svc.RecordOffline(ctx, OfflineInput{
		OrderID: orderID, OrderNo: orderNo, UserID: user,
		Amount:    dec("1.00"),
		Reference: "", Reason: "",
	})
	if err == nil {
		t.Fatal("空 Reference 应失败")
	}
	if errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("期望 ValidationFailed，得 %s", errs.As(err).Code)
	}
}

// 同参复用意图、改金额轮换单号、CloseOrderIntents。
func TestCreateIntentReuseAndRotate(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-reuse-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)

	in := CreateIntentInput{
		OrderID: orderID, OrderNo: orderNo, UserID: user,
		Amount: dec("9.90"), ProviderCode: code,
		Title: "测试订单", ClientIP: "203.0.113.2",
	}
	r1, err := e.svc.CreateIntent(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.svc.CreateIntent(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Reused || r2.IntentID != r1.IntentID {
		t.Fatal("第二次应复用同一 IntentID")
	}

	in.Amount = dec("10.00")
	r3, err := e.svc.CreateIntent(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if r3.IntentID == r1.IntentID {
		t.Fatal("改金额应新意图")
	}
	wantOut := orderNo + "-2"
	if r3.OutTradeNo != wantOut {
		t.Fatalf("out_trade_no=%s want %s", r3.OutTradeNo, wantOut)
	}
	var oldSt string
	if err := e.row(ctx, `SELECT status FROM payment_intents WHERE id = ?`, r1.IntentID).Scan(&oldSt); err != nil {
		t.Fatal(err)
	}
	if oldSt != string(IntentCancelled) {
		t.Fatalf("旧意图 %s", oldSt)
	}

	n, err := e.svc.CloseOrderIntents(ctx, orderID, IntentExpired)
	if err != nil || n != 1 {
		t.Fatalf("Close expired: n=%d err=%v", n, err)
	}
	_, err = e.svc.CloseOrderIntents(ctx, orderID, IntentSucceeded)
	if err == nil {
		t.Fatal("Close 到 succeeded 应报错")
	}
}

// 渠道停收、停用与订单过期拒收。
func TestCreateIntentRejects(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	base := CreateIntentInput{
		OrderID: orderID, OrderNo: orderNo, UserID: user,
		Amount: dec("9.90"),
		Title:  "测试", ClientIP: "203.0.113.3",
	}

	paused := "fake-pause-" + uuid.New().String()[:8]
	pPaused := e.mkFakeProvider(t, ctx, paused)
	ok, err := e.svc.repo.UpdateProviderFields(ctx, pPaused.ID, map[string]any{"accepting_new": false})
	if err != nil || !ok {
		t.Fatal(err)
	}
	base.ProviderCode = paused
	_, err = e.svc.CreateIntent(ctx, base)
	if err != ErrProviderPaused {
		t.Fatalf("暂停收单: %v", err)
	}

	dis := "fake-dis-" + uuid.New().String()[:8]
	pDis := e.mkFakeProvider(t, ctx, dis)
	ok, err = e.svc.repo.UpdateProviderFields(ctx, pDis.ID, map[string]any{"enabled": false})
	if err != nil || !ok {
		t.Fatal(err)
	}
	base.ProviderCode = dis
	_, err = e.svc.CreateIntent(ctx, base)
	if err != ErrProviderDisabled {
		t.Fatalf("停用: %v", err)
	}

	lapseCode := "fake-lapse-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, lapseCode)
	base.ProviderCode = lapseCode
	past := time.Now().Add(-time.Minute)
	base.ExpiresAt = &past
	_, err = e.svc.CreateIntent(ctx, base)
	if errs.As(err).Code != errs.OrderLapsed {
		t.Fatalf("过期订单: %s", errs.As(err).Code)
	}
}
