package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/google/uuid"
)

// refStubAdapter 包一层替身：创建支付时注入固定的 ProviderRef，其余透传。
type refStubAdapter struct {
	inner provider.Provider
	ref   string
}

func (a *refStubAdapter) CreatePayment(ctx context.Context, req provider.CreateRequest) (provider.CreateResponse, error) {
	resp, err := a.inner.CreatePayment(ctx, req)
	if err != nil {
		return resp, err
	}
	resp.ProviderRef = a.ref
	return resp, nil
}

func (a *refStubAdapter) ParseNotification(ctx context.Context, in provider.Inbound) (provider.Notification, error) {
	return a.inner.ParseNotification(ctx, in)
}

func (a *refStubAdapter) NotificationAck(in provider.AckInput) provider.AckOutput {
	return a.inner.NotificationAck(in)
}

func (a *refStubAdapter) QueryPayment(ctx context.Context, outTradeNo string) (provider.QueryResult, error) {
	return a.inner.QueryPayment(ctx, outTradeNo)
}

func (e *env) useRefStub(t *testing.T, ref string) {
	t.Helper()
	orig := e.svc.adapter
	e.svc.adapter = func(p providerRecord) (provider.Provider, error) {
		base, err := orig(p)
		if err != nil {
			return nil, err
		}
		return &refStubAdapter{inner: base, ref: ref}, nil
	}
}

// 证据用例真提交（newCommitEnv）：要证明入账事务真的回滚了（原事件不占去重键）、证据是另开事务落下的。
func TestWebhookProviderRefMismatchRejectedBTCPay(t *testing.T) {
	e, ctx := newCommitEnv(t)
	defer e.restoreDefaultAdapter(t)
	user := e.mkUser(t, ctx)
	code := "fake-ref-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	if err := e.exec(ctx, "UPDATE payment_providers SET adapter = ? WHERE code = ?", "btcpay", code); err != nil {
		t.Fatal(err)
	}
	e.useRefStub(t, "INV-OURS")
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	var savedRef *string
	if err := e.row(ctx, `SELECT provider_ref FROM payment_intents WHERE id = ?`, intent.IntentID).Scan(&savedRef); err != nil {
		t.Fatal(err)
	}
	if savedRef == nil || *savedRef != "INV-OURS" {
		t.Fatalf("库里 provider_ref 应为 INV-OURS，得 %v", savedRef)
	}

	wrongEvent := "evt-ref-wrong-" + uuid.New().String()[:8]
	inWrong := fakeInbound(wrongEvent, "payment.succeeded", "INV-OTHER", intent.OutTradeNo, 990)
	// 同一回调推两次（渠道重投）：每次都重新核对、都 409；原事件行随入账事务回滚，证据按原事件号去重只留 1 条
	for i := 1; i <= 2; i++ {
		ackWrong, err := e.svc.HandleWebhook(ctx, code, inWrong)
		if err != nil {
			t.Fatal(err)
		}
		if ackWrong.HTTPStatus != http.StatusConflict {
			t.Fatalf("第 %d 次：期望 409，得 %d body=%s", i, ackWrong.HTTPStatus, string(ackWrong.Body))
		}
		if e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, orderID) != 0 {
			t.Fatal("不应有 payments")
		}
		if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 0 {
			t.Fatal("不应有 order_paid")
		}
		if n := e.count(t, ctx, `SELECT count(*) FROM payment_events WHERE provider_event_id = ? AND provider_id = (SELECT id FROM payment_providers WHERE code = ?)`, wrongEvent, code); n != 0 {
			t.Fatalf("第 %d 次：原事件行应随入账事务回滚，得 %d 行", i, n)
		}
		if n := e.count(t, ctx, `SELECT count(*) FROM payment_events WHERE provider_event_id = ? AND processing_status = 'ignored' AND raw_payload->>'channel_ref' = 'INV-OTHER' AND raw_payload->>'reject' = 'provider_ref_mismatch' AND provider_id = (SELECT id FROM payment_providers WHERE code = ?)`, "ref_mismatch:"+wrongEvent, code); n != 1 {
			t.Fatalf("第 %d 次：应恰有 1 条 ref_mismatch 证据，得 %d", i, n)
		}
	}

	inRight := fakeInbound("evt-ref-right-"+uuid.New().String()[:8], "payment.succeeded",
		"INV-OURS", intent.OutTradeNo, 990)
	ackRight, err := e.svc.HandleWebhook(ctx, code, inRight)
	if err != nil {
		t.Fatal(err)
	}
	if ackRight.HTTPStatus != http.StatusOK {
		t.Fatalf("渠道单号一致时应 200: %s", string(ackRight.Body))
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, orderID) != 1 {
		t.Fatal("应入账 1 行 payments")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("应有 1 笔 order_paid")
	}
}

func TestWebhookProviderRefMismatchCoinPaymentsLogsOnly(t *testing.T) {
	e, ctx := newEnv(t)
	defer e.restoreDefaultAdapter(t)
	user := e.mkUser(t, ctx)
	code := "fake-ref-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	if err := e.exec(ctx, "UPDATE payment_providers SET adapter = ? WHERE code = ?", "coinpayments", code); err != nil {
		t.Fatal(err)
	}
	e.useRefStub(t, "INV-OURS")
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	inWrong := fakeInbound("evt-ref-wrong-"+uuid.New().String()[:8], "payment.succeeded",
		"INV-OTHER", intent.OutTradeNo, 990)
	ack, err := e.svc.HandleWebhook(ctx, code, inWrong)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("CoinPayments 单号不符临时口径应 200: %d %s", ack.HTTPStatus, string(ack.Body))
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, orderID) != 1 {
		t.Fatal("应入账 1 行 payments")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("应有 1 笔 order_paid")
	}
	if e.count(t, ctx, `SELECT count(*) FROM payment_events WHERE provider_event_id LIKE 'ref_mismatch:%' AND provider_id = (SELECT id FROM payment_providers WHERE code = ?)`, code) != 0 {
		t.Fatal("不应有 ref_mismatch 证据行")
	}
}

// 意图 provider_ref 为 NULL（如易支付）时，允许任意渠道流水号正常入账。
func TestRecordAllowsNilProviderRef(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-ref-nil-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	var savedRef *string
	if err := e.row(ctx, `SELECT provider_ref FROM payment_intents WHERE id = ?`, intent.IntentID).Scan(&savedRef); err != nil {
		t.Fatal(err)
	}
	if savedRef != nil {
		t.Fatalf("provider_ref 应为 NULL，得 %v", *savedRef)
	}

	in := fakeInbound("evt-nilref-"+uuid.New().String()[:8], "payment.succeeded",
		"ANY-PAYMENT-ID", intent.OutTradeNo, 990)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("应 200: %s", string(ack.Body))
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, orderID) != 1 {
		t.Fatal("应入账 1 行 payments")
	}
}

// 查单结果流水号与意图 provider_ref 不一致时拒绝入账并返回 ErrProviderRefMismatch。
// 真提交：查单路径同样是入账事务回滚、另开事务落证据。
func TestQueryRejectsProviderRefMismatch(t *testing.T) {
	e, ctx := newCommitEnv(t)
	defer e.restoreDefaultAdapter(t)
	user := e.mkUser(t, ctx)
	code := "fake-query-ref-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	if err := e.exec(ctx, "UPDATE payment_providers SET adapter = ? WHERE code = ?", "btcpay", code); err != nil {
		t.Fatal(err)
	}
	e.useRefStub(t, "INV-OURS")
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	var savedRef *string
	if err := e.row(ctx, `SELECT provider_ref FROM payment_intents WHERE id = ?`, intent.IntentID).Scan(&savedRef); err != nil {
		t.Fatal(err)
	}
	if savedRef == nil || *savedRef != "INV-OURS" {
		t.Fatalf("库里 provider_ref 应为 INV-OURS，得 %v", savedRef)
	}

	e.useQueryStub(t, func(_ context.Context, _ string) (provider.QueryResult, error) {
		return succeededQuery("INV-OTHER"), nil
	})

	// 查单的事件号是「渠道单号:RECONCILED」；查两次都拒收，证据只留 1 条
	const queryEvent = "INV-OTHER:RECONCILED"
	for i := 1; i <= 2; i++ {
		_, err := e.svc.Query(ctx, orderID)
		if !errors.Is(err, ErrProviderRefMismatch) {
			t.Fatalf("第 %d 次：期望 ErrProviderRefMismatch，得 %v", i, err)
		}
		if e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, orderID) != 0 {
			t.Fatal("不应有 payments")
		}
		if n := e.count(t, ctx, `SELECT count(*) FROM payment_events WHERE provider_event_id = ? AND provider_id = (SELECT id FROM payment_providers WHERE code = ?)`, queryEvent, code); n != 0 {
			t.Fatalf("第 %d 次：原事件行应随入账事务回滚，得 %d 行", i, n)
		}
		if n := e.count(t, ctx, `SELECT count(*) FROM payment_events WHERE provider_event_id = ? AND processing_status = 'ignored' AND raw_payload->>'channel_ref' = 'INV-OTHER' AND provider_id = (SELECT id FROM payment_providers WHERE code = ?)`, "ref_mismatch:"+queryEvent, code); n != 1 {
			t.Fatalf("第 %d 次：应恰有 1 条 ref_mismatch 证据，得 %d", i, n)
		}
	}
}
