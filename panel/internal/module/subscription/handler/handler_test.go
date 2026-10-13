package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// fakeSvc 只实现用例用到的方法；其余走嵌入的 nil 接口，被调到会 panic。
type fakeSvc struct {
	Service
	mine        service.MySubscriptions
	renameID    uuid.UUID
	renameLabel *string
	moveFrom    *uuid.UUID
	moveTo      uuid.UUID
	extendDays  int
	extendOut   service.AdminExtendResult
	logFilter   service.ResetLogFilter
	logs        []service.ResetLogView
	limitSet    *int
	limitCalled bool
}

func (f *fakeSvc) MySubscriptions(context.Context) (service.MySubscriptions, error) {
	return f.mine, nil
}

func (f *fakeSvc) SetMyLabel(_ context.Context, id uuid.UUID, label *string) (service.LabelResult, error) {
	f.renameID, f.renameLabel = id, label
	return service.LabelResult{Label: label, ClientName: "Pandora · 基础版"}, nil
}

func (f *fakeSvc) MyTransferPacks(_ context.Context, from *uuid.UUID, to uuid.UUID) (int64, error) {
	f.moveFrom, f.moveTo = from, to
	return 42, nil
}

func (f *fakeSvc) AdminExtend(_ context.Context, id uuid.UUID, days int, _ string) (service.AdminExtendResult, error) {
	f.extendDays = days
	out := f.extendOut
	out.SubscriptionID = id
	return out, nil
}

func (f *fakeSvc) AdminSetDeviceLimit(_ context.Context, _ uuid.UUID, limit *int) error {
	f.limitCalled, f.limitSet = true, limit
	return nil
}

func (f *fakeSvc) ListTrafficResets(_ context.Context, fl service.ResetLogFilter) ([]service.ResetLogView, int64, error) {
	f.logFilter = fl
	return f.logs, int64(len(f.logs)), nil
}

func newEngine(svc Service, admin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: pass,
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	h := New(svc, nil, 0)
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

func decMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v body=%s", err, w.Body.String())
	}
	return got
}

func TestMySubscriptionsShape(t *testing.T) {
	subID, planID, priceID := uuid.New(), uuid.New(), uuid.New()
	end := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	limit := int64(100)
	f := &fakeSvc{mine: service.MySubscriptions{
		UnattachedPackBytes: 7,
		Subscriptions: []service.MySubscription{{
			Summary: service.Summary{
				Subscription: service.Subscription{
					ID: subID, PlanID: planID, Status: service.StatusActive,
					PeriodStart: end.AddDate(0, -1, 0), PeriodEnd: end,
					Price: service.PriceSnapshot{PriceID: &priceID, Currency: money.CNY, Amount: decimal.RequireFromString("12.34"), Interval: "month", IntervalCount: 1},
				},
				State:    service.StateLive,
				PlanName: "基础版",
				Quotas:   []service.Quota{{Metric: service.MetricTraffic, Period: service.PeriodCycle, Limit: &limit, Consumed: 3}},
			},
			Renewable:    true,
			Changeable:   true,
			RenewalPrice: &service.RenewalPrice{ID: priceID, Currency: money.CNY, UnitAmount: decimal.RequireFromString("12.34"), BillingInterval: "month", IntervalCount: 1, Available: true},
			ClientName:   "Pandora · 基础版",
		}},
	}}
	w := do(newEngine(f, false), http.MethodGet, "/v1/me/subscriptions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	got := decMap(t, w)
	if got["unattached_pack_bytes"].(float64) != 7 {
		t.Fatalf("unattached: %v", got)
	}
	subs := got["subscriptions"].([]any)
	s := subs[0].(map[string]any)
	if s["amount"].(float64) != 1234 || s["price_id"] != priceID.String() || s["state"] != "live" {
		t.Fatalf("sub: %v", s)
	}
	rp := s["renewal_price"].(map[string]any)
	if rp["unit_amount"].(float64) != 1234 || rp["available"] != true {
		t.Fatalf("renewal_price: %v", rp)
	}
	q := s["quotas"].([]any)[0].(map[string]any)
	if q["granted_addon"].(float64) != 0 || q["limit"].(float64) != 100 {
		t.Fatalf("quota: %v", q)
	}
}

func TestMySubscriptionsEmptyIsArray(t *testing.T) {
	w := do(newEngine(&fakeSvc{}, false), http.MethodGet, "/v1/me/subscriptions", "")
	if !bytes.Contains(w.Body.Bytes(), []byte(`"subscriptions":[]`)) {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestRename(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, false)
	if w := do(e, http.MethodPatch, "/v1/me/subscriptions/not-a-uuid", `{"label":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("坏 id: %d", w.Code)
	}
	id := uuid.New()
	w := do(e, http.MethodPatch, "/v1/me/subscriptions/"+id.String(), `{"label":null}`)
	if w.Code != http.StatusOK || f.renameID != id || f.renameLabel != nil {
		t.Fatalf("清名: %d %v %v body=%s", w.Code, f.renameID, f.renameLabel, w.Body.String())
	}
	if got := decMap(t, w); got["client_name"] != "Pandora · 基础版" {
		t.Fatalf("body: %v", got)
	}
}

func TestTransfer(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, false)
	if w := do(e, http.MethodPost, "/v1/me/traffic-packs/transfer", `{"from_subscription_id":null,"to_subscription_id":"x"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏目标: %d body=%s", w.Code, w.Body.String())
	}
	to := uuid.New()
	w := do(e, http.MethodPost, "/v1/me/traffic-packs/transfer", `{"from_subscription_id":null,"to_subscription_id":"`+to.String()+`"}`)
	if w.Code != http.StatusOK || f.moveFrom != nil || f.moveTo != to {
		t.Fatalf("转移: %d body=%s", w.Code, w.Body.String())
	}
	if got := decMap(t, w); got["moved_bytes"].(float64) != 42 {
		t.Fatalf("body: %v", got)
	}
}

func TestAdminExtend(t *testing.T) {
	f := &fakeSvc{extendOut: service.AdminExtendResult{UserEmail: "a@example.com", Days: 7}}
	e := newEngine(f, true)
	id := uuid.New()
	if w := do(e, http.MethodPost, "/v1/subscriptions/"+id.String()+"/extend", `{"days":0,"reason":"补偿故障"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("天数 0: %d", w.Code)
	}
	w := do(e, http.MethodPost, "/v1/subscriptions/"+id.String()+"/extend", `{"days":7,"reason":"补偿故障时长"}`)
	if w.Code != http.StatusOK || f.extendDays != 7 {
		t.Fatalf("加时长: %d body=%s", w.Code, w.Body.String())
	}
	if got := decMap(t, w); got["subscription_id"] != id.String() || got["user_email"] != "a@example.com" {
		t.Fatalf("body: %v", got)
	}
}

func TestAdminDeviceLimit(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true)
	id := uuid.New().String()
	if w := do(e, http.MethodPost, "/v1/subscriptions/"+id+"/device-limit", `{"limit":1001}`); w.Code != http.StatusUnprocessableEntity || f.limitCalled {
		t.Fatalf("超限: %d", w.Code)
	}
	w := do(e, http.MethodPost, "/v1/subscriptions/"+id+"/device-limit", `{"limit":null}`)
	if w.Code != http.StatusOK || !f.limitCalled || f.limitSet != nil {
		t.Fatalf("清覆盖: %d body=%s", w.Code, w.Body.String())
	}
}

func TestTrafficResetsQuery(t *testing.T) {
	f := &fakeSvc{logs: []service.ResetLogView{{ID: uuid.New(), UserEmail: "a@example.com", Metric: service.MetricTraffic, Reason: service.ResetManual}}}
	e := newEngine(f, true)
	if w := do(e, http.MethodGet, "/v1/traffic-resets?reason=bogus", ""); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏原因: %d", w.Code)
	}
	w := do(e, http.MethodGet, "/v1/traffic-resets?reason=manual", "")
	if w.Code != http.StatusOK || f.logFilter.Reason != service.ResetManual || f.logFilter.Limit != 50 {
		t.Fatalf("列表: %d %+v", w.Code, f.logFilter)
	}
	got := decMap(t, w)
	if got["total"].(float64) != 1 || len(got["logs"].([]any)) != 1 {
		t.Fatalf("body: %v", got)
	}
	uid := uuid.New()
	if w := do(e, http.MethodGet, "/v1/users/"+uid.String()+"/traffic-resets", ""); w.Code != http.StatusOK || f.logFilter.UserID == nil || *f.logFilter.UserID != uid {
		t.Fatalf("按用户: %d %+v", w.Code, f.logFilter)
	}
}
