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

// 意图存有 provider_ref 时，回调渠道单号不一致整笔拒收，一致时成功入账。
func TestWebhookProviderRefMismatchRejected(t *testing.T) {
	cases := []struct {
		adapter string
	}{
		{"btcpay"},
		{"coinpayments"},
	}
	for _, tc := range cases {
		t.Run(tc.adapter, func(t *testing.T) {
			e, ctx := newEnv(t)
			defer e.restoreDefaultAdapter(t)
			user := e.mkUser(t, ctx)
			code := "fake-ref-" + uuid.New().String()[:8]
			_ = e.mkFakeProvider(t, ctx, code)
			if err := e.exec(ctx, "UPDATE payment_providers SET adapter = ? WHERE code = ?", tc.adapter, code); err != nil {
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

			// 推一条签名正确、金额 990 分、payment_id 为 INV-OTHER 的回调 -> 409，未入账
			inWrong := fakeInbound("evt-ref-wrong-"+uuid.New().String()[:8], "payment.succeeded",
				"INV-OTHER", intent.OutTradeNo, 990)
			ackWrong, err := e.svc.HandleWebhook(ctx, code, inWrong)
			if err != nil {
				t.Fatal(err)
			}
			if ackWrong.HTTPStatus == http.StatusOK {
				t.Fatal("渠道单号不符时不应 200")
			}
			if ackWrong.HTTPStatus != http.StatusConflict {
				t.Fatalf("期望 409，得 %d body=%s", ackWrong.HTTPStatus, string(ackWrong.Body))
			}
			if e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, orderID) != 0 {
				t.Fatal("不应有 payments")
			}
			if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 0 {
				t.Fatal("不应有 order_paid")
			}

			// 再推 payment_id 为 INV-OURS 的同额成功回调 -> 200，入账 1 行
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
		})
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
func TestQueryRejectsProviderRefMismatch(t *testing.T) {
	e, ctx := newEnv(t)
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

	_, err := e.svc.Query(ctx, orderID)
	if !errors.Is(err, ErrProviderRefMismatch) {
		t.Fatalf("期望 ErrProviderRefMismatch，得 %v", err)
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, orderID) != 0 {
		t.Fatal("不应有 payments")
	}
}
