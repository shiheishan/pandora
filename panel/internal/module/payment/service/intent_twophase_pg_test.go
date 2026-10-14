package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

// createStubAdapter 包一层替身：CreatePayment 可注入，其余透传。
type createStubAdapter struct {
	inner    provider.Provider
	createFn func(context.Context, provider.CreateRequest) (provider.CreateResponse, error)
}

func (a *createStubAdapter) CreatePayment(ctx context.Context, req provider.CreateRequest) (provider.CreateResponse, error) {
	if a.createFn != nil {
		return a.createFn(ctx, req)
	}
	return a.inner.CreatePayment(ctx, req)
}

func (a *createStubAdapter) ParseNotification(ctx context.Context, in provider.Inbound) (provider.Notification, error) {
	return a.inner.ParseNotification(ctx, in)
}

func (a *createStubAdapter) NotificationAck(in provider.AckInput) provider.AckOutput {
	return a.inner.NotificationAck(in)
}

func (a *createStubAdapter) QueryPayment(ctx context.Context, outTradeNo string) (provider.QueryResult, error) {
	return a.inner.QueryPayment(ctx, outTradeNo)
}

func (e *env) useCreateStub(t *testing.T, createFn func(context.Context, provider.CreateRequest) (provider.CreateResponse, error)) {
	t.Helper()
	orig := e.svc.adapter
	e.svc.adapter = func(p providerRecord) (provider.Provider, error) {
		base, err := orig(p)
		if err != nil {
			return nil, err
		}
		return &createStubAdapter{inner: base, createFn: createFn}, nil
	}
}

func (e *env) intentInput(code string, orderID uuid.UUID, orderNo string, user uuid.UUID) CreateIntentInput {
	return CreateIntentInput{
		OrderID:      orderID,
		OrderNo:      orderNo,
		UserID:       user,
		Amount:       dec("9.90"),
		Currency:     money.CNY,
		ProviderCode: code,
		Title:        "测试订单",
		ClientIP:     "203.0.113.1",
	}
}

func TestBeginIntentLeavesPlaceholder(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-2p-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	in := e.intentInput(code, orderID, orderNo, user)

	var draft IntentDraft
	if err := e.txm.Do(ctx, func(ctx context.Context) error {
		var beginErr error
		draft, beginErr = e.svc.BeginIntent(ctx, in)
		return beginErr
	}); err != nil {
		t.Fatal(err)
	}

	if e.count(t, ctx, `SELECT count(*) FROM payment_intents WHERE order_id = ? AND status = 'requires_action' AND redirect_url = ''`, orderID) != 1 {
		t.Fatal("BeginIntent 后应有占位意图")
	}

	res, err := e.svc.CompleteIntent(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if res.RedirectURL == "" || res.Reused {
		t.Fatalf("CompleteIntent 结果 %+v", res)
	}
	if e.count(t, ctx, `SELECT count(*) FROM payment_intents WHERE order_id = ? AND status = 'requires_action' AND redirect_url <> ''`, orderID) != 1 {
		t.Fatal("CompleteIntent 后应补上收银台")
	}
}

func TestCompleteIntentGatewayFailureCancelsPlaceholder(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-2p-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	in := e.intentInput(code, orderID, orderNo, user)

	e.useCreateStub(t, func(context.Context, provider.CreateRequest) (provider.CreateResponse, error) {
		return provider.CreateResponse{}, errors.New("渠道不可用")
	})
	_, err := e.svc.CreateIntent(ctx, in)
	if err == nil || errs.As(err).Code != errs.Unavailable {
		t.Fatalf("应 Unavailable: %v", err)
	}
	if e.count(t, ctx, `SELECT count(*) FROM payment_intents WHERE order_id = ? AND status = 'cancelled'`, orderID) != 1 {
		t.Fatal("占位应 cancelled")
	}
	if e.count(t, ctx, `SELECT count(*) FROM payment_intents WHERE order_id = ? AND status = 'requires_action'`, orderID) != 0 {
		t.Fatal("不应有在途意图")
	}

	e.restoreDefaultAdapter(t)
	res, err := e.svc.CreateIntent(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.OutTradeNo, "-2") {
		t.Fatalf("out_trade_no=%s 应以 -2 结尾", res.OutTradeNo)
	}
}

func TestBeginIntentConflictsWhileCreating(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-2p-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	in := e.intentInput(code, orderID, orderNo, user)

	var firstDraft IntentDraft
	if err := e.txm.Do(ctx, func(ctx context.Context) error {
		var beginErr error
		firstDraft, beginErr = e.svc.BeginIntent(ctx, in)
		return beginErr
	}); err != nil {
		t.Fatal(err)
	}

	_, err := e.svc.CreateIntent(ctx, in)
	if err == nil || errs.As(err).Code != errs.PaymentInProgress {
		t.Fatalf("建单窗口内应 payment_in_progress: %v", err)
	}

	if err := e.exec(ctx, `UPDATE payment_intents SET created_at = now() - interval '1 minute' WHERE id = ?`, firstDraft.intentID); err != nil {
		t.Fatal(err)
	}

	res, err := e.svc.CreateIntent(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.OutTradeNo, "-2") {
		t.Fatalf("新意图 out_trade_no=%s", res.OutTradeNo)
	}

	_, err = e.svc.CompleteIntent(ctx, firstDraft)
	if err == nil || errs.As(err).Code != errs.Conflict {
		t.Fatalf("旧草稿 CompleteIntent 应 Conflict: %v", err)
	}
	var newRedirect string
	if err := e.row(ctx, `SELECT redirect_url FROM payment_intents WHERE out_trade_no = ?`, res.OutTradeNo).Scan(&newRedirect); err != nil {
		t.Fatal(err)
	}
	if newRedirect == "" {
		t.Fatal("新意图 redirect_url 不应被旧草稿改掉")
	}
}

func TestCompleteIntentStoresProviderRef(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-2p-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	in := e.intentInput(code, orderID, orderNo, user)

	e.useCreateStub(t, func(ctx context.Context, req provider.CreateRequest) (provider.CreateResponse, error) {
		fp, err := newFakeProvider(fakeConfig{Secret: []byte(fakeSecret), CheckoutURL: "https://checkout.example.test"})
		if err != nil {
			return provider.CreateResponse{}, err
		}
		resp, err := fp.CreatePayment(ctx, req)
		if err != nil {
			return resp, err
		}
		resp.ProviderRef = "INV-" + req.OutTradeNo
		return resp, nil
	})

	res, err := e.svc.CreateIntent(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	wantRef := "INV-" + res.OutTradeNo
	var gotRef *string
	if err := e.row(ctx, `SELECT provider_ref FROM payment_intents WHERE id = ?`, res.IntentID).Scan(&gotRef); err != nil {
		t.Fatal(err)
	}
	if gotRef == nil || *gotRef != wantRef {
		t.Fatalf("provider_ref=%v want %s", gotRef, wantRef)
	}
	e.restoreDefaultAdapter(t)
}

func TestCompleteIntentRejectsBadResponse(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-2p-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	in := e.intentInput(code, orderID, orderNo, user)

	e.useCreateStub(t, func(context.Context, provider.CreateRequest) (provider.CreateResponse, error) {
		return provider.CreateResponse{HTTPMethod: "GET", RedirectURL: ""}, nil
	})
	_, err := e.svc.CreateIntent(ctx, in)
	if err == nil || errs.As(err).Code != errs.Unavailable {
		t.Fatalf("应 Unavailable: %v", err)
	}
	if e.count(t, ctx, `SELECT count(*) FROM payment_intents WHERE order_id = ? AND status = 'cancelled'`, orderID) != 1 {
		t.Fatal("不合格响应应作废占位")
	}
	e.restoreDefaultAdapter(t)
}
