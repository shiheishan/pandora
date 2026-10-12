package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// fakeSvc 只实现用例用到的方法；其余走嵌入的 nil 接口，被调到会 panic。
type fakeSvc struct {
	Service
	wallet     service.Wallet
	walletErr  error
	adjustRes  service.AdjustResult
	adjustErr  error
	lastAdjust service.Adjustment
}

func (f *fakeSvc) MyWallet(context.Context) (service.Wallet, error) {
	return f.wallet, f.walletErr
}

func (f *fakeSvc) AdjustBalance(_ context.Context, a service.Adjustment) (service.AdjustResult, error) {
	f.lastAdjust = a
	return f.adjustRes, f.adjustErr
}

// kitHooks 记录后台调账链路上的幂等作用域与 RecentReauth 是否执行。
type kitHooks struct {
	idempotencyScope string
	reauthCalled     bool
}

func pass(c *gin.Context) { c.Next() }

func newPublicEngine(svc Service, p *authn.Principal) *gin.Engine {
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
	New(svc).RegisterPublic(e.Group("/v1"), kit)
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
	New(svc).RegisterAdmin(e.Group("/v1"), kit)
	return e
}

func do(e *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func routesOf(e *gin.Engine) []string {
	var out []string
	for _, r := range e.Routes() {
		out = append(out, r.Method+" "+r.Path)
	}
	sort.Strings(out)
	return out
}

type errEnvelope struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	} `json:"error"`
}

func parseErr(body []byte) errEnvelope {
	var env errEnvelope
	_ = json.Unmarshal(body, &env)
	return env
}

func TestRoutes(t *testing.T) {
	pub := routesOf(newPublicEngine(&fakeSvc{}, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudiencePublic}))
	if strings.Join(pub, "\n") != "GET /v1/me/balance" {
		t.Fatalf("门户路由:\n%s", strings.Join(pub, "\n"))
	}
	adm := routesOf(newAdminEngine(&fakeSvc{}, nil, nil))
	if strings.Join(adm, "\n") != "POST /v1/users/:id/balance" {
		t.Fatalf("后台路由:\n%s", strings.Join(adm, "\n"))
	}
}

func TestMyBalanceShape(t *testing.T) {
	at1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	at2 := time.Date(2026, 3, 2, 11, 30, 0, 0, time.UTC)
	f := &fakeSvc{wallet: service.Wallet{
		Currency: money.CNY,
		Balance:  decimal.RequireFromString("12.34"),
		History: []service.HistoryItem{
			{Kind: "balance_topup", Delta: decimal.RequireFromString("10.00"), Memo: "充值", At: at1},
			{Kind: "balance_hold", Delta: decimal.RequireFromString("-5.50"), Memo: "冻结", At: at2},
		},
	}}
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudiencePublic}
	w := do(newPublicEngine(f, p), http.MethodGet, "/v1/me/balance", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var got struct {
		Balance  int64  `json:"balance"`
		Currency string `json:"currency"`
		History  []struct {
			Kind  string `json:"kind"`
			Delta int64  `json:"delta"`
			Memo  string `json:"memo"`
			At    string `json:"at"`
		} `json:"history"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Balance != 1234 || got.Currency != "CNY" {
		t.Fatalf("余额/币种 %+v", got)
	}
	if len(got.History) != 2 || got.History[0].Delta != 1000 || got.History[0].Kind != "balance_topup" ||
		got.History[1].Delta != -550 || got.History[1].Kind != "balance_hold" {
		t.Fatalf("history %+v", got.History)
	}
	if _, err := time.Parse(time.RFC3339, got.History[0].At); err != nil {
		t.Fatalf("at 不是 RFC3339: %q %v", got.History[0].At, err)
	}
	if _, err := time.Parse(time.RFC3339, got.History[1].At); err != nil {
		t.Fatalf("at 不是 RFC3339: %q %v", got.History[1].At, err)
	}
}

func TestMyBalanceEmptyHistory(t *testing.T) {
	f := &fakeSvc{wallet: service.Wallet{Currency: money.CNY, Balance: decimal.Zero, History: nil}}
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudiencePublic}
	w := do(newPublicEngine(f, p), http.MethodGet, "/v1/me/balance", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"history":[]`) {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
}

func TestAdjustBalanceOK(t *testing.T) {
	uid := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	f := &fakeSvc{adjustRes: service.AdjustResult{Balance: decimal.RequireFromString("30.5")}}
	var hooks kitHooks
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	body := `{"amount":-2000,"currency":"CNY","reason":"补偿测试原因"}`
	w := do(newAdminEngine(f, p, &hooks), http.MethodPost, "/v1/users/"+uid.String()+"/balance", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	wantAmt := decimal.RequireFromString("-20")
	if f.lastAdjust.UserID != uid || !f.lastAdjust.Amount.Equal(wantAmt) ||
		f.lastAdjust.Currency != "CNY" || f.lastAdjust.Reason != "补偿测试原因" {
		t.Fatalf("入参 %+v", f.lastAdjust)
	}
	var res struct {
		Balance int64 `json:"balance"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.Balance != 3050 {
		t.Fatalf("响应 %s err=%v", w.Body, err)
	}
	if hooks.idempotencyScope != AdjustIdempotencyScope || !hooks.reauthCalled {
		t.Fatalf("中间件 scope=%q reauth=%v", hooks.idempotencyScope, hooks.reauthCalled)
	}
}

func TestAdjustBalanceBadUserID(t *testing.T) {
	w := do(newAdminEngine(&fakeSvc{}, nil, nil), http.MethodPost, "/v1/users/not-a-uuid/balance",
		`{"amount":100,"currency":"CNY","reason":"原因够长"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d", w.Code)
	}
	env := parseErr(w.Body.Bytes())
	if env.Error.Code != "not_found" {
		t.Fatalf("code %q", env.Error.Code)
	}
}

func TestAdjustBalanceUnknownField(t *testing.T) {
	uid := uuid.NewString()
	w := do(newAdminEngine(&fakeSvc{}, nil, nil), http.MethodPost, "/v1/users/"+uid+"/balance",
		`{"amount":1,"reason":"xxxxx","x":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
	env := parseErr(w.Body.Bytes())
	if env.Error.Code != "bad_request" {
		t.Fatalf("code %q", env.Error.Code)
	}
}

func TestAdjustBalanceConflict(t *testing.T) {
	f := &fakeSvc{adjustErr: errs.Newf(errs.Conflict, "余额不足，无法扣减这么多")}
	uid := uuid.NewString()
	w := do(newAdminEngine(f, nil, nil), http.MethodPost, "/v1/users/"+uid+"/balance",
		`{"amount":-100,"currency":"CNY","reason":"扣减测试原因"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d", w.Code)
	}
	env := parseErr(w.Body.Bytes())
	if env.Error.Code != "conflict" {
		t.Fatalf("code %q", env.Error.Code)
	}
}

func TestAdjustBalanceValidationFailedNoFields(t *testing.T) {
	f := &fakeSvc{adjustErr: errs.Newf(errs.ValidationFailed, "调整金额不能为零")}
	uid := uuid.NewString()
	w := do(newAdminEngine(f, nil, nil), http.MethodPost, "/v1/users/"+uid+"/balance",
		`{"amount":100,"currency":"CNY","reason":"非零金额原因"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", w.Code)
	}
	env := parseErr(w.Body.Bytes())
	if env.Error.Code != "validation_failed" {
		t.Fatalf("code %q", env.Error.Code)
	}
	if len(env.Error.Fields) != 0 {
		t.Fatalf("不应有 fields: %v", env.Error.Fields)
	}
	if strings.Contains(w.Body.String(), `"fields"`) {
		t.Fatalf("响应含 fields: %s", w.Body)
	}
}
