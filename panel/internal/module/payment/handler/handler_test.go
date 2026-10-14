package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// fakeSvc 只实现用例用到的方法；其余走嵌入的 nil 接口，被调到会 panic。
type fakeSvc struct {
	Service
	methods       []service.PaymentMethod
	methodsAmount *decimal.Decimal
	webhookCode   string
	webhookIn     provider.Inbound
	webhookAck    provider.AckOutput
	webhookErr    error
	providers     []service.ProviderView
	createIn      service.CreateProviderInput
	createRes     service.ProviderWriteResult
	createErr     error
	updateErr     error
	lateStatus    string
	lateLimit     int
	lateOffset    int
	lateList      service.LateList
	lateListErr   error
	applyCaseID   uuid.UUID
	applyReason   string
	applyRes      uuid.UUID
	applyErr      error
}

func (f *fakeSvc) PaymentMethods(_ context.Context, amount *decimal.Decimal) ([]service.PaymentMethod, error) {
	f.methodsAmount = amount
	return f.methods, nil
}

func (f *fakeSvc) HandleWebhook(_ context.Context, code string, in provider.Inbound) (provider.AckOutput, error) {
	f.webhookCode = code
	f.webhookIn = in
	if f.webhookErr != nil {
		return provider.AckOutput{}, f.webhookErr
	}
	return f.webhookAck, nil
}

func (f *fakeSvc) ListProviders(context.Context) ([]service.ProviderView, error) {
	return f.providers, nil
}

func (f *fakeSvc) CreateProvider(_ context.Context, in service.CreateProviderInput) (service.ProviderWriteResult, error) {
	f.createIn = in
	return f.createRes, f.createErr
}

func (f *fakeSvc) UpdateProvider(context.Context, string, service.ProviderSettings) (service.ProviderWriteResult, error) {
	return service.ProviderWriteResult{}, f.updateErr
}

func (f *fakeSvc) ListLatePayments(_ context.Context, status string, limit, offset int) (service.LateList, error) {
	f.lateStatus = status
	f.lateLimit = limit
	f.lateOffset = offset
	return f.lateList, f.lateListErr
}

func (f *fakeSvc) ApplyLatePayment(_ context.Context, caseID uuid.UUID, reason string) (uuid.UUID, error) {
	f.applyCaseID = caseID
	f.applyReason = reason
	return f.applyRes, f.applyErr
}

// kitHooks 记录后台链路上的幂等作用域与 RecentReauth 是否执行。
type kitHooks struct {
	idempotencyScope string
	reauthCalled     bool
}

func pass(c *gin.Context) { c.Next() }

func newPublicEngine(svc Service, p *authn.Principal) *gin.Engine {
	return newPublicEngineWith(svc, p, nil)
}

func newPublicEngineWith(svc Service, p *authn.Principal, lim *ratelimit.Limiter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	if p != nil {
		e.Use(func(c *gin.Context) { authn.Set(c, *p); c.Next() })
	}
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: pass,
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	New(svc, lim).RegisterPublic(e.Group("/v1"), kit)
	return e
}

func newAdminEngine(svc Service, p *authn.Principal, hooks *kitHooks) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	if p != nil {
		e.Use(func(c *gin.Context) { authn.Set(c, *p); c.Next() })
	}
	var h kitHooks
	if hooks != nil {
		h = *hooks
	}
	rec := hooks
	if rec == nil {
		rec = &h
	}
	kit := route.Kit{
		RequireAuth: pass,
		RecentReauth: func(c *gin.Context) {
			rec.reauthCalled = true
			c.Next()
		},
		Idempotency: func(scope string) gin.HandlerFunc {
			return func(c *gin.Context) {
				rec.idempotencyScope = scope
				c.Next()
			}
		},
		Switch: func(string) gin.HandlerFunc { return pass },
	}
	New(svc, nil).RegisterAdmin(e.Group("/v1"), kit)
	return e
}

func doJSON(e *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func doRaw(e *gin.Engine, method, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

type errEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseErr(body []byte) errEnvelope {
	var env errEnvelope
	_ = json.Unmarshal(body, &env)
	return env
}

func TestPaymentMethodsLabelsAndCurrencies(t *testing.T) {
	f := &fakeSvc{methods: []service.PaymentMethod{
		{Provider: "epay", DisplayName: "易支付", Method: "alipay", Currencies: nil},
		{Provider: "demo", DisplayName: "演示渠道", Method: "custom_pay", Currencies: []string{"CNY"}},
	}}
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudiencePublic}
	w := doJSON(newPublicEngine(f, p), http.MethodGet, "/v1/payment-methods", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var got struct {
		Methods []struct {
			Provider   string   `json:"provider"`
			Method     string   `json:"method"`
			Label      string   `json:"label"`
			Currencies []string `json:"currencies"`
		} `json:"methods"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Methods) != 2 || got.Methods[0].Label != "支付宝" || len(got.Methods[0].Currencies) != 0 {
		t.Fatalf("alipay 行 %+v", got.Methods[0])
	}
	if got.Methods[1].Label != "演示渠道" {
		t.Fatalf("未知方式 label %+v", got.Methods[1])
	}
}

func TestWebhookGETPreservesQueryAndPlainSuccess(t *testing.T) {
	f := &fakeSvc{webhookAck: provider.AckOutput{
		HTTPStatus:  http.StatusOK,
		ContentType: "text/plain; charset=utf-8",
		Body:        []byte("success"),
	}}
	e := newPublicEngine(f, nil)
	w := doRaw(e, http.MethodGet, "/v1/webhooks/payments/epay_demo?trade_no=1&sign=abc", nil)
	if w.Code != http.StatusOK || w.Body.String() != "success" {
		t.Fatalf("status %d body %q", w.Code, w.Body)
	}
	if w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("content-type %q", w.Header().Get("Content-Type"))
	}
	if f.webhookCode != "epay_demo" || f.webhookIn.Method != http.MethodGet {
		t.Fatalf("code/method %q %q", f.webhookCode, f.webhookIn.Method)
	}
	if f.webhookIn.Query.Get("trade_no") != "1" || f.webhookIn.Query.Get("sign") != "abc" {
		t.Fatalf("query %+v", f.webhookIn.Query)
	}
}

func TestWebhookPOSTBodyAndCustomAck(t *testing.T) {
	body := []byte(`{"paid":true}`)
	ackBody := []byte(`{"error":"denied"}`)
	f := &fakeSvc{webhookAck: provider.AckOutput{
		HTTPStatus:  http.StatusUnauthorized,
		ContentType: "application/json",
		Body:        ackBody,
	}}
	w := doRaw(newPublicEngine(f, nil), http.MethodPost, "/v1/webhooks/payments/p1", body)
	if w.Code != http.StatusUnauthorized || !bytes.Equal(w.Body.Bytes(), ackBody) {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if !bytes.Equal(f.webhookIn.Body, body) {
		t.Fatalf("body %+v", f.webhookIn.Body)
	}
}

func TestWebhookNotFoundEnvelope(t *testing.T) {
	f := &fakeSvc{webhookErr: errs.Newf(errs.NotFound, "未知的支付渠道")}
	w := doRaw(newPublicEngine(f, nil), http.MethodGet, "/v1/webhooks/payments/missing", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d", w.Code)
	}
	env := parseErr(w.Body.Bytes())
	if env.Error.Code != "not_found" {
		t.Fatalf("code %q body %s", env.Error.Code, w.Body)
	}
}

func TestListProvidersShape(t *testing.T) {
	pid := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	f := &fakeSvc{providers: []service.ProviderView{{
		ID: pid, Code: "ep", Adapter: "epay", DisplayName: "E",
		Enabled: true, AcceptingNew: false, HasCredentials: true,
		MinAmount: decimal.RequireFromString("1.00"),
		Today:     map[money.Currency]decimal.Decimal{money.CNY: decimal.RequireFromString("9.90")},
		Methods:   nil,
	}}}
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	w := doJSON(newAdminEngine(f, p, nil), http.MethodGet, "/v1/payment-providers", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	raw := w.Body.String()
	if !strings.Contains(raw, `"min_amount":100`) || !strings.Contains(raw, `"CNY":990`) {
		t.Fatalf("金额 %s", raw)
	}
	if !strings.Contains(raw, `"success_rate_24h":null`) || !strings.Contains(raw, `"last_callback_at":null`) {
		t.Fatalf("可空指标 %s", raw)
	}
	if !strings.Contains(raw, `"methods":[]`) {
		t.Fatalf("methods %s", raw)
	}
}

func TestCreateProviderMiddlewareAndMinAmount(t *testing.T) {
	id := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	f := &fakeSvc{createRes: service.ProviderWriteResult{ID: id, Code: "new1", CredentialsChanged: true}}
	var hooks kitHooks
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	body := `{"code":"new1","adapter":"epay","display_name":"新渠道","min_amount":100}`
	w := doJSON(newAdminEngine(f, p, &hooks), http.MethodPost, "/v1/payment-providers", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	want := decimal.RequireFromString("1.00")
	if !f.createIn.MinAmount.Equal(want) {
		t.Fatalf("min_amount %s", f.createIn.MinAmount)
	}
	if hooks.idempotencyScope != CreateProviderScope || !hooks.reauthCalled {
		t.Fatalf("中间件 scope=%q reauth=%v", hooks.idempotencyScope, hooks.reauthCalled)
	}

	w2 := doJSON(newAdminEngine(&fakeSvc{}, p, nil), http.MethodPost, "/v1/payment-providers",
		`{"code":"x","adapter":"epay","display_name":"n","unknown":1}`)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("未知字段 status %d", w2.Code)
	}
}

func TestUpdateProviderRejectsCodeField(t *testing.T) {
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	w := doJSON(newAdminEngine(&fakeSvc{}, p, nil), http.MethodPut, "/v1/payment-providers/ep1",
		`{"code":"hack","display_name":"n"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	env := parseErr(w.Body.Bytes())
	if env.Error.Code != "bad_request" {
		t.Fatalf("code %q", env.Error.Code)
	}
}

func TestListLatePaymentsOmitResolvedAndDefaultLimit(t *testing.T) {
	cid := uuid.MustParse("44444444-4444-4444-8444-444444444444")
	uid := uuid.MustParse("55555555-5555-4555-8555-555555555555")
	recv := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	f := &fakeSvc{lateList: service.LateList{
		Cases: []service.LateCase{{
			ID: cid, Kind: service.CaseReleasedOrder, Status: service.LateSuspense,
			Amount: decimal.RequireFromString("9.90"), Currency: money.CNY,
			OrderNo: "O1", OrderStatus: "cancelled", UserID: uid, UserEmail: "u@example.com",
			ReceivedAt: recv, ResolvedAt: nil, ResolutionReason: nil,
		}},
		Total: 1,
	}}
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	w := doJSON(newAdminEngine(f, p, nil), http.MethodGet, "/v1/late-payments?status=suspense", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.lateLimit != 25 {
		t.Fatalf("limit %d", f.lateLimit)
	}
	raw := w.Body.String()
	if strings.Contains(raw, `"resolved_at"`) {
		t.Fatalf("不应含 resolved_at: %s", raw)
	}
	if !strings.Contains(raw, `"amount":990`) {
		t.Fatalf("amount %s", raw)
	}
}

func TestApplyLatePaymentUUIDAndMiddleware(t *testing.T) {
	w := doJSON(newAdminEngine(&fakeSvc{}, nil, nil), http.MethodPost,
		"/v1/late-payments/not-uuid/apply-to-balance", `{"reason":"人工核对入账"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d", w.Code)
	}

	caseID := uuid.MustParse("66666666-6666-4666-8666-666666666666")
	txnID := uuid.MustParse("77777777-7777-4777-8777-777777777777")
	f := &fakeSvc{applyRes: txnID}
	var hooks kitHooks
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	w2 := doJSON(newAdminEngine(f, p, &hooks), http.MethodPost,
		"/v1/late-payments/"+caseID.String()+"/apply-to-balance", `{"reason":"人工核对入账"}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("status %d %s", w2.Code, w2.Body)
	}
	var res struct {
		LedgerTxnID string `json:"ledger_txn_id"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &res); err != nil || res.LedgerTxnID != txnID.String() {
		t.Fatalf("响应 %s err=%v", w2.Body, err)
	}
	if f.applyCaseID != caseID || f.applyReason != "人工核对入账" {
		t.Fatalf("入参 id=%s reason=%q", f.applyCaseID, f.applyReason)
	}
	if hooks.idempotencyScope != ApplyLateScope || !hooks.reauthCalled {
		t.Fatalf("中间件 scope=%q reauth=%v", hooks.idempotencyScope, hooks.reauthCalled)
	}
}

func TestWebhookRateLimitedPerIP(t *testing.T) {
	_, rdb := testkit.Redis(t)
	f := &fakeSvc{webhookAck: provider.AckOutput{HTTPStatus: http.StatusOK, ContentType: "text/plain", Body: []byte("success")}}
	e := newPublicEngineWith(f, nil, ratelimit.New(rdb))
	hit := func(ip string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/payments/p1", bytes.NewReader([]byte("x")))
		req.RemoteAddr = ip + ":40000"
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		return w.Code
	}
	for i := 0; i < WebhookPerIPPerMin; i++ {
		if code := hit("203.0.113.7"); code != http.StatusOK {
			t.Fatalf("第 %d 次 status %d", i+1, code)
		}
	}
	f.webhookCode = ""
	if code := hit("203.0.113.7"); code != http.StatusTooManyRequests {
		t.Fatalf("超限应 429，实际 %d", code)
	}
	if f.webhookCode != "" {
		t.Fatal("超限后不应再进 service")
	}
	if code := hit("203.0.113.8"); code != http.StatusOK {
		t.Fatalf("别的 IP 不受影响，实际 %d", code)
	}
}
