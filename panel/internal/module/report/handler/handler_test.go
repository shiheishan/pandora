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

	"github.com/aegispanel/aegis/internal/module/report/service"
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

type fakeSvc struct {
	Service

	overview            service.Overview
	overviewErr         error
	revenueSeries       service.RevenueSeries
	revenueErr          error
	lastRevenueCurrency string
	lastRevenueDays     int

	adjustments    []service.Adjustment
	adjustmentsErr error
	lastListCur    string

	createOut  service.Adjustment
	createErr  error
	lastCreate service.CreateAdjustmentInput

	reverseOut        service.Adjustment
	reverseErr        error
	lastReverseID     uuid.UUID
	lastReverseReason string

	nodeTraffic service.NodeTraffic
	userTraffic service.UserTraffic
	trafficErr  error
	lastTraffic service.TrafficQuery

	backlog service.NotificationBacklog
	tasks   service.Tasks
	system  service.SystemStatus

	activityPts []service.ActivityPoint
	activityErr error
	lastDays    int

	userTrafficCalled bool
}

func (f *fakeSvc) Overview(context.Context) (service.Overview, error) {
	return f.overview, f.overviewErr
}

func (f *fakeSvc) RevenueTimeseries(_ context.Context, currency string, days int) (service.RevenueSeries, error) {
	f.lastRevenueCurrency, f.lastRevenueDays = currency, days
	return f.revenueSeries, f.revenueErr
}

func (f *fakeSvc) ListAdjustments(_ context.Context, currency string) ([]service.Adjustment, error) {
	f.lastListCur = currency
	return f.adjustments, f.adjustmentsErr
}

func (f *fakeSvc) CreateAdjustment(_ context.Context, in service.CreateAdjustmentInput) (service.Adjustment, error) {
	f.lastCreate = in
	return f.createOut, f.createErr
}

func (f *fakeSvc) ReverseAdjustment(_ context.Context, id uuid.UUID, reason string) (service.Adjustment, error) {
	f.lastReverseID, f.lastReverseReason = id, reason
	return f.reverseOut, f.reverseErr
}

func (f *fakeSvc) NodeTraffic(_ context.Context, q service.TrafficQuery) (service.NodeTraffic, error) {
	f.lastTraffic = q
	return f.nodeTraffic, f.trafficErr
}

func (f *fakeSvc) UserTraffic(_ context.Context, q service.TrafficQuery) (service.UserTraffic, error) {
	f.userTrafficCalled = true
	f.lastTraffic = q
	return f.userTraffic, f.trafficErr
}

func (f *fakeSvc) NotificationBacklog(context.Context) (service.NotificationBacklog, error) {
	return f.backlog, nil
}

func (f *fakeSvc) Tasks(context.Context) (service.Tasks, error) {
	return f.tasks, nil
}

func (f *fakeSvc) SystemStatus(context.Context) (service.SystemStatus, error) {
	return f.system, nil
}

func (f *fakeSvc) ActivityTimeseries(_ context.Context, days int) ([]service.ActivityPoint, error) {
	f.lastDays = days
	return f.activityPts, f.activityErr
}

type kitHooks struct {
	idempotencyScope string
	reauthCalled     bool
}

func pass(c *gin.Context) { c.Next() }

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

func TestPostRoutesReauthAndIdempotency(t *testing.T) {
	f := &fakeSvc{createOut: sampleAdjustment()}
	var hooks kitHooks
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	e := newAdminEngine(f, p, &hooks)

	body := `{"currency":"CNY","amount":1,"reason":"补录线下收款测试"}`
	w := do(e, http.MethodPost, "/v1/revenue/adjustments", body)
	if w.Code != http.StatusOK {
		t.Fatalf("create status %d %s", w.Code, w.Body)
	}
	if hooks.idempotencyScope != CreateAdjustmentScope || !hooks.reauthCalled {
		t.Fatalf("create middleware scope=%q reauth=%v", hooks.idempotencyScope, hooks.reauthCalled)
	}

	hooks = kitHooks{}
	id := uuid.New()
	w = do(e, http.MethodPost, "/v1/revenue/adjustments/"+id.String()+"/reverse", `{"reason":"撤销理由够长测试"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reverse status %d %s", w.Code, w.Body)
	}
	if hooks.idempotencyScope != ReverseAdjustmentScope || !hooks.reauthCalled {
		t.Fatalf("reverse middleware scope=%q reauth=%v", hooks.idempotencyScope, hooks.reauthCalled)
	}
}

func TestGetRoutesNoReauth(t *testing.T) {
	f := &fakeSvc{overview: service.Overview{}}
	var hooks kitHooks
	e := newAdminEngine(f, nil, &hooks)
	w := do(e, http.MethodGet, "/v1/overview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("overview %d", w.Code)
	}
	if hooks.reauthCalled {
		t.Fatal("GET 不应触发 RecentReauth")
	}
}

func TestCreateAdjustmentOK(t *testing.T) {
	f := &fakeSvc{createOut: sampleAdjustment()}
	w := do(newAdminEngine(f, nil, nil), http.MethodPost, "/v1/revenue/adjustments",
		`{"currency":"CNY","amount":1234,"reason":"补录线下收款","effective_on":"2026-03-01"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	want := decimal.RequireFromString("12.34")
	if !f.lastCreate.Amount.Equal(want) || f.lastCreate.EffectiveOn != "2026-03-01" || f.lastCreate.Currency != "CNY" {
		t.Fatalf("入参 %+v", f.lastCreate)
	}
}

func TestCreateAdjustmentUnknownField(t *testing.T) {
	w := do(newAdminEngine(&fakeSvc{}, nil, nil), http.MethodPost, "/v1/revenue/adjustments",
		`{"currency":"CNY","amount":1234,"reason":"补录线下收款","effective_on":"2026-03-01","x":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
}

func TestReverseAdjustmentBadID(t *testing.T) {
	w := do(newAdminEngine(&fakeSvc{}, nil, nil), http.MethodPost, "/v1/revenue/adjustments/not-uuid/reverse",
		`{"reason":"撤销理由够长测试"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d", w.Code)
	}
}

func TestReverseAdjustmentOK(t *testing.T) {
	id := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	f := &fakeSvc{reverseOut: sampleAdjustment()}
	w := do(newAdminEngine(f, nil, nil), http.MethodPost, "/v1/revenue/adjustments/"+id.String()+"/reverse",
		`{"reason":"撤销理由够长测试"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.lastReverseID != id || f.lastReverseReason != "撤销理由够长测试" {
		t.Fatalf("reverse 入参 id=%v reason=%q", f.lastReverseID, f.lastReverseReason)
	}
}

func TestUserTrafficRejectsIdentity(t *testing.T) {
	f := &fakeSvc{}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/dashboard/traffic/users?identity=x", "")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.userTrafficCalled {
		t.Fatal("不应调用 UserTraffic")
	}
}

func TestRevenueTimeseriesQuery(t *testing.T) {
	f := &fakeSvc{revenueSeries: service.RevenueSeries{Currency: money.USD, Days: 30}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/revenue/timeseries?currency=USD&days=30", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if f.lastRevenueCurrency != "USD" || f.lastRevenueDays != 30 {
		t.Fatalf("query %+v %d", f.lastRevenueCurrency, f.lastRevenueDays)
	}
}

func TestStatsTimeseriesBadDays(t *testing.T) {
	w := do(newAdminEngine(&fakeSvc{}, nil, nil), http.MethodGet, "/v1/stats/timeseries?days=abc", "")
	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
}

func TestReverseAdjustmentConflict(t *testing.T) {
	f := &fakeSvc{reverseErr: errs.Newf(errs.Conflict, "该调整已撤销")}
	id := uuid.NewString()
	w := do(newAdminEngine(f, nil, nil), http.MethodPost, "/v1/revenue/adjustments/"+id+"/reverse",
		`{"reason":"撤销理由够长测试"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d", w.Code)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Error.Code != "conflict" {
		t.Fatalf("code %q", env.Error.Code)
	}
}

func sampleAdjustment() service.Adjustment {
	return service.Adjustment{
		ID: uuid.New(), Currency: money.CNY, Amount: decimal.New(1234, -2),
		Reason: "补录线下收款", EffectiveOn: "2026-03-01", CreatedBy: uuid.New(),
		CreatedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestRoutesRegistered(t *testing.T) {
	adm := routesOf(newAdminEngine(&fakeSvc{}, nil, nil))
	want := []string{
		"GET /v1/dashboard/backlog/notifications",
		"GET /v1/dashboard/tasks",
		"GET /v1/dashboard/traffic/nodes",
		"GET /v1/dashboard/traffic/users",
		"GET /v1/overview",
		"GET /v1/revenue/adjustments",
		"GET /v1/revenue/timeseries",
		"GET /v1/stats/timeseries",
		"GET /v1/system/status",
		"POST /v1/revenue/adjustments",
		"POST /v1/revenue/adjustments/:id/reverse",
	}
	if strings.Join(adm, "\n") != strings.Join(want, "\n") {
		t.Fatalf("路由:\n%s", strings.Join(adm, "\n"))
	}
}
