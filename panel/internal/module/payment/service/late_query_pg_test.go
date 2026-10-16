package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// queryStubAdapter 包一层替身：查单可注入结果，其余透传。
type queryStubAdapter struct {
	inner   provider.Provider
	queryFn func(context.Context, string) (provider.QueryResult, error)
}

func (a *queryStubAdapter) CreatePayment(ctx context.Context, req provider.CreateRequest) (provider.CreateResponse, error) {
	return a.inner.CreatePayment(ctx, req)
}

func (a *queryStubAdapter) ParseNotification(ctx context.Context, in provider.Inbound) (provider.Notification, error) {
	return a.inner.ParseNotification(ctx, in)
}

func (a *queryStubAdapter) NotificationAck(in provider.AckInput) provider.AckOutput {
	return a.inner.NotificationAck(in)
}

func (a *queryStubAdapter) QueryPayment(ctx context.Context, outTradeNo string) (provider.QueryResult, error) {
	if a.queryFn != nil {
		return a.queryFn(ctx, outTradeNo)
	}
	return a.inner.QueryPayment(ctx, outTradeNo)
}

func (e *env) useQueryStub(t *testing.T, queryFn func(context.Context, string) (provider.QueryResult, error)) {
	t.Helper()
	orig := e.svc.adapter
	e.svc.adapter = func(p providerRecord) (provider.Provider, error) {
		base, err := orig(p)
		if err != nil {
			return nil, err
		}
		return &queryStubAdapter{inner: base, queryFn: queryFn}, nil
	}
}

func (e *env) restoreDefaultAdapter(t *testing.T) {
	t.Helper()
	e.svc.adapter = e.adapterWithFake
}

func succeededQuery(payRef string) provider.QueryResult {
	return provider.QueryResult{
		Found:      true,
		PaymentRef: payRef,
		Amount:     dec("9.90"),
		Status:     provider.StatusSucceeded,
		Method:     "alipay",
	}
}

func (e *env) quarantineCase(t *testing.T, ctx context.Context, user uuid.UUID) (orderID uuid.UUID, caseID uuid.UUID) {
	t.Helper()
	code := "fake-late-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	e.settler.mu.Lock()
	e.settler.notPayable[orderID] = NotPayable(CaseReleasedOrder, "cancelled")
	e.settler.mu.Unlock()
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")
	in := fakeInbound("evt-late-"+uuid.New().String()[:8], "payment.succeeded",
		"pay-late-"+uuid.New().String()[:8], intent.OutTradeNo, 990)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("开挂账回调应 200: %s", string(ack.Body))
	}
	if err := e.row(ctx, `SELECT id FROM late_payment_cases WHERE order_id = ?`, orderID).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	return orderID, caseID
}

// ApplyLatePayment 转入余额、幂等与列表待处理合计。
func TestApplyLatePayment(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	_, caseID := e.quarantineCase(t, ctx, user)

	balBefore := e.accountBalance(t, ctx, ledgersvc.UserAccount(ledgersvc.UserBalance, user))
	susp := ledgersvc.PlatformAccount(ledgersvc.LatePaymentSuspense, ledgersvc.RefMain)

	txnID, err := e.svc.ApplyLatePayment(ctx, caseID, "核实后转入余额")
	if err != nil {
		t.Fatal(err)
	}
	if txnID == uuid.Nil {
		t.Fatal("应有 ledger txn")
	}

	balAfter := e.accountBalance(t, ctx, ledgersvc.UserAccount(ledgersvc.UserBalance, user))
	if !balAfter.Sub(balBefore).Equal(dec("9.90")) {
		t.Fatalf("用户余额 +9.90，得 %s", balAfter.Sub(balBefore))
	}
	if !e.accountBalance(t, ctx, susp).Equal(decimal.Zero) {
		t.Fatal("suspense 应归零")
	}

	var status string
	var appliedTxn uuid.UUID
	if err := e.row(ctx, `SELECT status, applied_txn_id FROM late_payment_cases WHERE id = ?`, caseID).
		Scan(&status, &appliedTxn); err != nil {
		t.Fatal(err)
	}
	if status != string(LateApplied) || appliedTxn != txnID {
		t.Fatalf("案状态=%s applied_txn=%s", status, appliedTxn)
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindLatePaymentApplied) != 1 {
		t.Fatal("应有 late_payment_applied")
	}

	_, err = e.svc.ApplyLatePayment(ctx, caseID, "再次申请转入")
	if !errors.Is(err, ErrCaseResolved) {
		t.Fatalf("已处理应 ErrCaseResolved: %v", err)
	}

	_, err = e.svc.ApplyLatePayment(ctx, caseID, "短")
	if err == nil || errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("原因过短应 Invalid: %v", err)
	}

	list, err := e.svc.ListLatePayments(ctx, "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range list.Cases {
		if c.ID == caseID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("列表应含本案")
	}
	if !list.Pending.Equal(decimal.Zero) {
		t.Fatalf("待处理金额应为 0，得 %s", list.Pending)
	}
	e.assertNoDrift(t, ctx)
}

// Query 补记漏掉的回调，并与后续回调幂等。
func TestQueryReconcilesMissedCallback(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-qrec-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	payRef := "pay-q1"
	e.useQueryStub(t, func(_ context.Context, _ string) (provider.QueryResult, error) {
		return succeededQuery(payRef), nil
	})

	res, err := e.svc.Query(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if res.ChannelStatus != ChannelPaid || !res.Reconciled {
		t.Fatalf("查单 %+v", res)
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("应补记 order_paid")
	}

	res2, err := e.svc.Query(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.AlreadyRecorded {
		t.Fatal("第二次应 AlreadyRecorded")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("仍应 1 笔 order_paid")
	}

	evt := "evt-after-q-" + uuid.New().String()[:8]
	in := fakeInbound(evt, "payment.succeeded", payRef, intent.OutTradeNo, 990)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatal(ack.HTTPStatus)
	}
	if !ackAlreadyHandled(ack.Body) {
		t.Fatalf("回调应 already_handled: %s", string(ack.Body))
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("回调后仍 1 笔")
	}
}

// Query 与同一 payment_id 的回调并发只记一笔。
func TestQueryCallbackRace(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-race-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	payRef := "pay-race-1"
	e.useQueryStub(t, func(_ context.Context, _ string) (provider.QueryResult, error) {
		return succeededQuery(payRef), nil
	})

	in := fakeInbound("evt-race-1", "payment.succeeded", payRef, intent.OutTradeNo, 990)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var qErr, wErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, qErr = e.svc.Query(ctx, orderID)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, wErr = e.svc.HandleWebhook(ctx, code, in)
	}()
	close(start)
	wg.Wait()

	if qErr != nil {
		t.Fatalf("Query: %v", qErr)
	}
	if wErr != nil {
		t.Fatalf("Webhook: %v", wErr)
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments`) != 1 {
		t.Fatal("应 1 行 payments")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("应 1 笔 order_paid")
	}
	if e.settler.calls.Load() != 1 {
		t.Fatalf("settler 应 1 次，实际 %d", e.settler.calls.Load())
	}
}

// 从未发起支付不可查单；替身渠道报不支持查单。
func TestQueryNeverPaidAndUnsupported(t *testing.T) {
	e, ctx := newEnv(t)

	neverOrder := uuid.Must(uuid.NewV7())
	_, err := e.svc.Query(ctx, neverOrder)
	if !errors.Is(err, ErrNeverPaid) {
		t.Fatalf("从未支付: %v", err)
	}

	user := e.mkUser(t, ctx)
	code := "fake-unsup-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	_ = e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")
	e.restoreDefaultAdapter(t)

	_, err = e.svc.Query(ctx, orderID)
	if err == nil || errs.As(err).Code != errs.Conflict {
		t.Fatalf("替身不支持查单应 Conflict: %v", err)
	}
}

// ReconcileDue 认领到期意图；替身查单失败则停查且下轮不再认领。
func TestReconcileDueClaimsAndStops(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-patrol-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")
	e.restoreDefaultAdapter(t)

	if err := e.exec(ctx, `UPDATE payment_intents SET next_query_at = now() - interval '1 minute' WHERE id = ?`, intent.IntentID); err != nil {
		t.Fatal(err)
	}

	stats, err := e.svc.ReconcileDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Queried != 1 || stats.Failed != 1 {
		t.Fatalf("首轮 stats=%+v", stats)
	}

	var attempts int
	if err := e.row(ctx, `SELECT query_attempts FROM payment_intents WHERE id = ?`, intent.IntentID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts < defaultPatrol.MaxAttempts {
		t.Fatalf("停查后 attempts 应 >= %d，得 %d", defaultPatrol.MaxAttempts, attempts)
	}

	stats2, err := e.svc.ReconcileDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Queried != 0 {
		t.Fatalf("停查后不应再认领: %+v", stats2)
	}
}
