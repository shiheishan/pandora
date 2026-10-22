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
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// fakeSvc 只实现用例用到的方法；其余走嵌入的 nil 接口，被调到会 panic。
type fakeSvc struct {
	Service
	mine        service.MySubscriptions
	renameID    uuid.UUID
	renameLabel *string
	extendDays  int
	extendOut   service.AdminExtendResult
	grantBytes  int64
	grantOut    service.AdminTrafficGrantResult
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

func (f *fakeSvc) AdminExtend(_ context.Context, id uuid.UUID, days int, _ string) (service.AdminExtendResult, error) {
	f.extendDays = days
	out := f.extendOut
	out.SubscriptionID = id
	return out, nil
}

func (f *fakeSvc) AdminGrantTraffic(_ context.Context, id uuid.UUID, bytes int64, _ string) (service.AdminTrafficGrantResult, error) {
	f.grantBytes = bytes
	out := f.grantOut
	out.SubscriptionID = id
	return out, nil
}

func (f *fakeSvc) AdminSetDeviceLimit(_ context.Context, _ uuid.UUID, limit *int) error {
	f.limitCalled, f.limitSet = true, limit
	return nil
}

func (f *fakeSvc) AdminSetPeriodEnd(_ context.Context, _ uuid.UUID, _ time.Time, _ string) (service.AdminSetPeriodEndResult, error) {
	return service.AdminSetPeriodEndResult{}, nil
}

func (f *fakeSvc) AdminSetQuotaLimit(_ context.Context, _ uuid.UUID, _, _ string, _ *int64, _ string) error {
	return nil
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
	resetPrice := decimal.RequireFromString("5.00")
	f := &fakeSvc{mine: service.MySubscriptions{
		Subscriptions: []service.MySubscription{{
			Summary: service.Summary{
				Subscription: service.Subscription{
					ID: subID, PlanID: planID, Status: service.StatusActive,
					PeriodStart: end.AddDate(0, -1, 0), PeriodEnd: end,
					Price: service.PriceSnapshot{PriceID: &priceID, Amount: decimal.RequireFromString("12.34"), Interval: "month", IntervalCount: 1},
				},
				State:        service.StateLive,
				PlanName:     "基础版",
				PlanKind:     "subscription",
				TrafficLimit: &limit,
				TrafficBonus: 20,
				TrafficUsed:  3,
				ResetsUsed:   1,
				ResetPrice:   &resetPrice,
				Quotas:       []service.Quota{{Metric: service.MetricTraffic, Period: service.PeriodCycle, Limit: &limit, Bonus: 20, Consumed: 3}},
			},
			Renewable:    true,
			Stackable:    false,
			Changeable:   true,
			RenewalPrice: &service.RenewalPrice{ID: priceID, UnitAmount: decimal.RequireFromString("12.34"), BillingInterval: "month", IntervalCount: 1, Available: true},
			ClientName:   "Pandora · 基础版",
		}},
	}}
	w := do(newEngine(f, false), http.MethodGet, "/v1/me/subscriptions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	got := decMap(t, w)
	if _, ok := got["unattached_pack_bytes"]; ok {
		t.Fatalf("unexpected unattached_pack_bytes: %v", got)
	}
	subs := got["subscriptions"].([]any)
	s := subs[0].(map[string]any)
	if _, ok := s["currency"]; ok {
		t.Fatalf("unexpected currency: %v", s)
	}
	if s["amount"].(float64) != 1234 || s["price_id"] != priceID.String() || s["state"] != "live" {
		t.Fatalf("sub: %v", s)
	}
	if s["plan_kind"] != "subscription" {
		t.Fatalf("plan_kind: %v", s["plan_kind"])
	}
	if s["bonus_bytes"].(float64) != 20 {
		t.Fatalf("bonus_bytes: %v", s["bonus_bytes"])
	}
	if s["resets_used"].(float64) != 1 {
		t.Fatalf("resets_used: %v", s["resets_used"])
	}
	if s["reset_price"].(float64) != 500 {
		t.Fatalf("reset_price: %v", s["reset_price"])
	}
	if s["stackable"] != false {
		t.Fatalf("stackable: %v", s["stackable"])
	}
	if s["validity_days"] != nil {
		t.Fatalf("validity_days: %v", s["validity_days"])
	}
	rp := s["renewal_price"].(map[string]any)
	if _, ok := rp["currency"]; ok {
		t.Fatalf("unexpected currency in renewal_price: %v", rp)
	}
	if rp["unit_amount"].(float64) != 1234 || rp["available"] != true {
		t.Fatalf("renewal_price: %v", rp)
	}
	q := s["quotas"].([]any)[0].(map[string]any)
	if _, legacy := q["granted_addon"]; legacy || q["limit"].(float64) != 100 {
		t.Fatalf("quota: %v", q)
	}
	if q["bonus_bytes"].(float64) != 20 {
		t.Fatalf("quota bonus_bytes: %v", q)
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

func TestAdminGrantTraffic(t *testing.T) {
	uid := uuid.New()
	f := &fakeSvc{grantOut: service.AdminTrafficGrantResult{
		UserID:       uid,
		UserEmail:    "a@example.com",
		GrantedBytes: 1024,
		BonusBytes:   2048,
	}}
	e := newEngine(f, true)
	id := uuid.New()
	if w := do(e, http.MethodPost, "/v1/subscriptions/"+id.String()+"/traffic-pack", `{"bytes":0,"reason":"补偿"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("流量 0: %d", w.Code)
	}
	w := do(e, http.MethodPost, "/v1/subscriptions/"+id.String()+"/traffic-pack", `{"bytes":1024,"reason":"补偿流量"}`)
	if w.Code != http.StatusOK || f.grantBytes != 1024 {
		t.Fatalf("加流量: %d body=%s", w.Code, w.Body.String())
	}
	got := decMap(t, w)
	if got["subscription_id"] != id.String() || got["user_email"] != "a@example.com" || got["granted_bytes"].(float64) != 1024 || got["bonus_bytes"].(float64) != 2048 {
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
