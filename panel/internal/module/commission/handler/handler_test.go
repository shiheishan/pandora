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

	"github.com/aegispanel/aegis/internal/module/commission/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// fakeSvc 只实现用例用到的方法；其余走嵌入的 nil 接口，被调到会 panic。
type fakeSvc struct {
	Service
	my          service.MyCommission
	withdrawals []service.AdminWithdrawal
	overview    service.Overview
	gotAmount   decimal.Decimal
	gotPayout   string
	gotMethod   string
	gotConfig   service.ConfigUpdate
	gotApprove  bool
	gotReason   string
	reviewPage  service.ReviewEntryPage
	gotCursor   string
	gotLimit    int
	overrides   []service.UserOverride
	gotOverride service.OverrideInput
	gotDeleted  uuid.UUID
	gotRef      string
	gotPaidUSDT *decimal.Decimal
}

func (f *fakeSvc) MyCommission(context.Context, uuid.UUID) (service.MyCommission, error) {
	return f.my, nil
}

func (f *fakeSvc) RequestWithdrawal(_ context.Context, _ uuid.UUID, amount decimal.Decimal, method, payout string) (uuid.UUID, error) {
	f.gotAmount, f.gotMethod, f.gotPayout = amount, method, payout
	return uuid.New(), nil
}

func (f *fakeSvc) TransferToBalance(_ context.Context, _ uuid.UUID, amount decimal.Decimal) (uuid.UUID, error) {
	f.gotAmount = amount
	return uuid.New(), nil
}

func (f *fakeSvc) AdminOverview(context.Context) (service.Overview, error) {
	return f.overview, nil
}

func (f *fakeSvc) AdminWithdrawals(context.Context, string) ([]service.AdminWithdrawal, error) {
	return f.withdrawals, nil
}

func (f *fakeSvc) ReviewWithdrawal(_ context.Context, _ uuid.UUID, approve bool, reason string) (string, error) {
	f.gotApprove, f.gotReason = approve, reason
	if approve {
		return service.WithdrawalApproved, nil
	}
	return service.WithdrawalRejected, nil
}

func (f *fakeSvc) SetConfig(_ context.Context, u service.ConfigUpdate) error {
	f.gotConfig = u
	return nil
}

func (f *fakeSvc) MarkWithdrawalPaid(_ context.Context, _ uuid.UUID, reference string, paidUSDT *decimal.Decimal) error {
	f.gotRef, f.gotPaidUSDT = reference, paidUSDT
	return nil
}

func (f *fakeSvc) ListOverrides(context.Context) ([]service.UserOverride, error) {
	return f.overrides, nil
}

func (f *fakeSvc) SetOverride(_ context.Context, in service.OverrideInput) (service.UserOverride, error) {
	f.gotOverride = in
	return service.UserOverride{UserID: uuid.New(), Email: in.Email, RatePercent: in.RatePercent, Scope: in.Scope, UpdatedAt: time.Now().UTC()}, nil
}

func (f *fakeSvc) DeleteOverride(_ context.Context, userID uuid.UUID) error {
	f.gotDeleted = userID
	return nil
}

func pass(c *gin.Context) { c.Next() }

// newEngine 构造挂了 public 或 admin 路由的引擎；scopes 记下每条路由挂的幂等作用域（按注册顺序）。
func newEngine(svc Service, admin bool, scopes *[]string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	aud := authn.AudiencePublic
	if admin {
		aud = authn.AudienceAdmin
	}
	p := authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: aud}
	e.Use(func(c *gin.Context) { authn.Set(c, p); c.Next() })
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: pass,
		Idempotency: func(scope string) gin.HandlerFunc {
			if scopes != nil {
				*scopes = append(*scopes, scope)
			}
			return pass
		},
		Switch: func(string) gin.HandlerFunc { return pass },
	}
	h := New(svc)
	if admin {
		h.RegisterAdmin(e.Group("/v1"), kit)
	} else {
		h.RegisterPublic(e.Group("/v1"), kit)
	}
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

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Error.Code
}

func TestRoutes(t *testing.T) {
	var pubScopes, admScopes []string
	pub := routesOf(newEngine(&fakeSvc{}, false, &pubScopes))
	wantPub := []string{"GET /v1/me/commission", "POST /v1/me/commission/transfer", "POST /v1/me/withdrawals"}
	if strings.Join(pub, ",") != strings.Join(wantPub, ",") {
		t.Fatalf("public 路由 %v", pub)
	}
	if strings.Join(pubScopes, ",") != WithdrawalRequestScope+","+TransferScope {
		t.Fatalf("public 幂等作用域 %v", pubScopes)
	}
	adm := routesOf(newEngine(&fakeSvc{}, true, &admScopes))
	wantAdm := []string{
		"DELETE /v1/commission/overrides/:user_id",
		"GET /v1/commission/entries", "GET /v1/commission/overrides", "GET /v1/commission/overview", "GET /v1/withdrawals",
		"POST /v1/commission/config", "POST /v1/commission/entries/:id/review", "POST /v1/commission/overrides",
		"POST /v1/withdrawals/:id/paid", "POST /v1/withdrawals/:id/review",
	}
	if strings.Join(adm, ",") != strings.Join(wantAdm, ",") {
		t.Fatalf("admin 路由 %v", adm)
	}
	if strings.Join(admScopes, ",") != MarkPaidScope {
		t.Fatalf("admin 幂等作用域 %v", admScopes)
	}
}

func TestMyCommissionShape(t *testing.T) {
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	f := &fakeSvc{my: service.MyCommission{
		Summary: service.Summary{Currency: "CNY", MinWithdraw: decimal.RequireFromString("100"), Scope: "every_order", RatePercent: 10},
		Entries: []service.EntryView{{
			Amount: decimal.RequireFromString("12.34"), Base: decimal.RequireFromString("123.45"), RateBP: 1000,
			Currency: "CNY", Status: "pending", FrozenUntil: now, CreatedAt: now, OrderNo: "N1", From: "al***@x.test",
		}},
		Withdrawals: []service.WithdrawalView{},
		Transfers:   []service.TransferView{},
	}}
	w := do(newEngine(f, false, nil), http.MethodGet, "/v1/me/commission", "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态 %d %s", w.Code, w.Body)
	}
	var resp struct {
		Summary     map[string]any   `json:"summary"`
		Entries     []map[string]any `json:"entries"`
		Withdrawals json.RawMessage  `json:"withdrawals"`
		Transfers   json.RawMessage  `json:"transfers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary["min_withdraw"] != float64(10000) || resp.Summary["scope"] != "every_order" {
		t.Fatalf("summary %v", resp.Summary)
	}
	if resp.Entries[0]["amount"] != float64(1234) || resp.Entries[0]["base"] != float64(12345) || resp.Entries[0]["rate_percent"] != float64(10) {
		t.Fatalf("entry %v", resp.Entries[0])
	}
	if string(resp.Withdrawals) != "[]" || string(resp.Transfers) != "[]" {
		t.Fatalf("空列表应为 []：%s %s", resp.Withdrawals, resp.Transfers)
	}
}

func TestRequestWithdrawal(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, false, nil)
	w := do(e, http.MethodPost, "/v1/me/withdrawals", `{"amount":0,"method":"alipay","payout_detail":"alipay"}`)
	if w.Code != http.StatusUnprocessableEntity || errCode(t, w) != "validation_failed" {
		t.Fatalf("金额 0：%d %s", w.Code, w.Body)
	}
	w = do(e, http.MethodPost, "/v1/me/withdrawals", `{"amount":1234,"payout_detail":"alipay a@b.test"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"method"`) {
		t.Fatalf("缺提现方式：%d %s", w.Code, w.Body)
	}
	w = do(e, http.MethodPost, "/v1/me/withdrawals", `{"amount":1234,"method":" alipay ","payout_detail":"alipay a@b.test"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("状态 %d %s", w.Code, w.Body)
	}
	if !f.gotAmount.Equal(decimal.RequireFromString("12.34")) || f.gotMethod != "alipay" || f.gotPayout != "alipay a@b.test" {
		t.Fatalf("收到 %s %q %q", f.gotAmount, f.gotMethod, f.gotPayout)
	}
}

func TestTransfer(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, false, nil)
	if w := do(e, http.MethodPost, "/v1/me/commission/transfer", `{"amount":-1}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("负数：%d", w.Code)
	}
	w := do(e, http.MethodPost, "/v1/me/commission/transfer", `{"amount":500}`)
	var resp struct {
		LedgerTxnID string `json:"ledger_txn_id"`
		Amount      int64  `json:"amount"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if w.Code != http.StatusOK || resp.Amount != 500 || resp.LedgerTxnID == "" || !f.gotAmount.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestReviewWithdrawal(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, nil)
	id := uuid.New().String()
	if w := do(e, http.MethodPost, "/v1/withdrawals/"+id+"/review", `{"action":"maybe"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法 action：%d", w.Code)
	}
	if w := do(e, http.MethodPost, "/v1/withdrawals/not-a-uuid/review", `{"action":"approve"}`); w.Code != http.StatusNotFound {
		t.Fatalf("非 UUID：%d", w.Code)
	}
	w := do(e, http.MethodPost, "/v1/withdrawals/"+id+"/review", `{"action":"reject","reason":"信息不全"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"rejected"`) || f.gotApprove || f.gotReason != "信息不全" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestSetConfigOnlyGivenFields(t *testing.T) {
	f := &fakeSvc{}
	w := do(newEngine(f, true, nil), http.MethodPost, "/v1/commission/config", `{"min_withdraw":5000}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	u := f.gotConfig
	if u.MinWithdraw == nil || !u.MinWithdraw.Equal(decimal.RequireFromString("50")) || u.RatePercent != nil || u.FreezeDays != nil || u.Scope != nil {
		t.Fatalf("config %+v", u)
	}
}

func TestListWithdrawalsShape(t *testing.T) {
	f := &fakeSvc{withdrawals: []service.AdminWithdrawal{{
		ID: uuid.New(), UserID: uuid.New(), Email: "a@b.test", Amount: decimal.RequireFromString("30"),
		Currency: "CNY", Status: "requested", PayoutDetail: "alipay", RequestedAt: time.Now().UTC(),
		EarnedTotal: decimal.RequireFromString("50.5"),
	}}}
	w := do(newEngine(f, true, nil), http.MethodGet, "/v1/withdrawals", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var resp struct {
		Withdrawals []map[string]any `json:"withdrawals"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	row := resp.Withdrawals[0]
	if row["amount"] != float64(3000) || row["earned_total"] != float64(5050) || row["completed_at"] != nil || row["payout_detail"] != "alipay" {
		t.Fatalf("row %v", row)
	}
	if _, ok := row["completed_at"]; !ok {
		t.Fatal("completed_at 字段应输出为 null")
	}
}

// 审核、打款、改配置、佣金复核、单独佣金设置的增删必须挂 RecentReauth：把它换成直接回 428 的桩，这六条应被挡下、四条只读的不受影响。
func TestAdminWritesRequireRecentReauth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	p := authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	e.Use(func(c *gin.Context) { authn.Set(c, p); c.Next() })
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: func(c *gin.Context) { c.AbortWithStatus(http.StatusPreconditionRequired) },
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	New(&fakeSvc{}).RegisterAdmin(e.Group("/v1"), kit)

	id := uuid.New().String()
	guarded := map[string]string{
		"/v1/withdrawals/" + id + "/review":        `{"action":"approve"}`,
		"/v1/withdrawals/" + id + "/paid":          `{"payout_reference":"TX-1"}`,
		"/v1/commission/config":                    `{"rate_percent":10}`,
		"/v1/commission/entries/" + id + "/review": `{"action":"void","reason":"同源刷单确认"}`,
		"/v1/commission/overrides":                 `{"email":"a@b.test","rate_percent":10}`,
	}
	for path, body := range guarded {
		if w := do(e, http.MethodPost, path, body); w.Code != http.StatusPreconditionRequired {
			t.Errorf("POST %s 应要求近期重认证，得到 %d", path, w.Code)
		}
	}
	if w := do(e, http.MethodDelete, "/v1/commission/overrides/"+id, ""); w.Code != http.StatusPreconditionRequired {
		t.Errorf("DELETE 单独佣金设置应要求近期重认证，得到 %d", w.Code)
	}
	for _, path := range []string{"/v1/commission/overview", "/v1/withdrawals", "/v1/commission/entries?review=required", "/v1/commission/overrides"} {
		if w := do(e, http.MethodGet, path, ""); w.Code == http.StatusPreconditionRequired {
			t.Errorf("GET %s 是只读的，不该要求重认证", path)
		}
	}
}

// 概览：顶层是站点币种那一组，by_currency 按币种列出。
func TestOverviewShape(t *testing.T) {
	f := &fakeSvc{overview: service.Overview{
		Currency: "USD", Pending: decimal.RequireFromString("1.5"), Entries: 3, NeedReview: 1,
		MinWithdraw: decimal.RequireFromString("100"), Scope: "every_order",
		ByCurrency: []service.CurrencyOverview{
			{Currency: "CNY", Pending: decimal.RequireFromString("20"), Entries: 2, NeedReview: 1},
			{Currency: "USD", Pending: decimal.RequireFromString("1.5"), Entries: 1},
		},
	}}
	w := do(newEngine(f, true, nil), http.MethodGet, "/v1/commission/overview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var resp struct {
		Currency   string           `json:"currency"`
		Pending    int64            `json:"pending"`
		NeedReview int64            `json:"need_review"`
		ByCurrency []map[string]any `json:"by_currency"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Currency != "USD" || resp.Pending != 150 || resp.NeedReview != 1 || len(resp.ByCurrency) != 2 {
		t.Fatalf("概览 %s", w.Body)
	}
	if resp.ByCurrency[0]["currency"] != "CNY" || resp.ByCurrency[0]["pending"] != float64(2000) || resp.ByCurrency[0]["need_review"] != float64(1) {
		t.Fatalf("by_currency %v", resp.ByCurrency[0])
	}
}
