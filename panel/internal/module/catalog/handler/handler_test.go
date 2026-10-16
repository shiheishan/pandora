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

	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// fakeSvc 记录调用参数；未实现的方法被调到会 panic。
type fakeSvc struct {
	Service

	portalUserID *uuid.UUID
	portalOffers []service.OfferPlan

	listFilter service.PlanFilter
	listPlans  []service.PlanSummary

	getPlanID  uuid.UUID
	getPlanOut service.PlanDetail

	createIn  service.PlanCreate
	createOut service.PlanDetail
	createErr error

	updatePricesIn service.PricesUpdate
	updateUsageIn  service.UsageUpdate
}

func (f *fakeSvc) PortalCatalog(_ context.Context, userID *uuid.UUID) ([]service.OfferPlan, error) {
	f.portalUserID = userID
	return f.portalOffers, nil
}

func (f *fakeSvc) ListPlans(_ context.Context, filter service.PlanFilter) ([]service.PlanSummary, error) {
	f.listFilter = filter
	return f.listPlans, nil
}

func (f *fakeSvc) GetPlan(_ context.Context, planID uuid.UUID) (service.PlanDetail, error) {
	f.getPlanID = planID
	return f.getPlanOut, nil
}

func (f *fakeSvc) CreatePlan(_ context.Context, in service.PlanCreate) (service.PlanDetail, error) {
	f.createIn = in
	if f.createErr != nil {
		return service.PlanDetail{}, f.createErr
	}
	return f.createOut, nil
}

func (f *fakeSvc) UpdateBasic(context.Context, uuid.UUID, service.BasicUpdate) (service.PlanDetail, error) {
	panic("UpdateBasic")
}
func (f *fakeSvc) UpdateSales(context.Context, uuid.UUID, service.SalesUpdate) (service.PlanDetail, error) {
	panic("UpdateSales")
}
func (f *fakeSvc) UpdateReset(context.Context, uuid.UUID, service.ResetUpdate) (service.PlanDetail, error) {
	panic("UpdateReset")
}
func (f *fakeSvc) UpdatePrices(_ context.Context, _ uuid.UUID, in service.PricesUpdate) (service.PlanDetail, error) {
	f.updatePricesIn = in
	return f.getPlanOut, nil
}
func (f *fakeSvc) SetPlanPools(context.Context, uuid.UUID, service.PoolsUpdate) (service.PlanDetail, error) {
	panic("SetPlanPools")
}
func (f *fakeSvc) UpdateUsage(_ context.Context, _ uuid.UUID, in service.UsageUpdate) (service.PlanDetail, error) {
	f.updateUsageIn = in
	return f.getPlanOut, nil
}
func (f *fakeSvc) PublishVersion(context.Context, uuid.UUID, uuid.UUID, int64) (service.PlanDetail, error) {
	panic("PublishVersion")
}
func (f *fakeSvc) ArchivePlan(context.Context, uuid.UUID, int64) (service.PlanDetail, error) {
	panic("ArchivePlan")
}

type fakeSubs struct {
	counts map[uuid.UUID]int64
	ids    []uuid.UUID
}

func (f *fakeSubs) LiveSubscriptionCounts(_ context.Context, planIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	f.ids = planIDs
	return f.counts, nil
}

type fakeNodes struct {
	counts map[uuid.UUID]int64
	ids    []uuid.UUID
}

func (f *fakeNodes) ServingNodeCounts(_ context.Context, planIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	f.ids = planIDs
	return f.counts, nil
}

func newPublicEngine(h *Handler, p *authn.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	if p != nil {
		e.Use(func(c *gin.Context) { authn.Set(c, *p); c.Next() })
	}
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{RequireAuth: pass, RecentReauth: pass, Idempotency: func(string) gin.HandlerFunc { return pass }}
	h.RegisterPublic(e.Group("/v1"), kit)
	return e
}

func newAdminEngine(h *Handler, p *authn.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	if p != nil {
		e.Use(func(c *gin.Context) { authn.Set(c, *p); c.Next() })
	}
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: pass,
		Idempotency:  func(string) gin.HandlerFunc { return pass },
	}
	h.RegisterAdmin(e.Group("/v1"), kit)
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

func routePaths(e *gin.Engine) []string {
	var out []string
	for _, r := range e.Routes() {
		out = append(out, r.Method+" "+r.Path)
	}
	sort.Strings(out)
	return out
}

func mustUUID(s string) uuid.UUID { return uuid.MustParse(s) }

func samplePlanDetail() service.PlanDetail {
	planID := mustUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	verID := mustUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	pub := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	reset := decimal.RequireFromString("5.00")
	traffic := int64(1 << 30)
	return service.PlanDetail{
		Plan: service.Plan{
			ID:               planID,
			Code:             "c1",
			Name:             "套餐",
			Kind:             service.KindSubscription,
			Status:           service.PlanActive,
			Visibility:       service.VisibilityPublic,
			AllowNewPurchase: true,
			AllowRenewal:     true,
			AllowUpgrade:     true,
			ResetPrice:       &reset,
			ResetLimit:       intPtr(2),
			RowVersion:       3,
			VisibleGroupIDs:  []uuid.UUID{},
		},
		PlanUsage: service.PlanUsage{
			Version:      intPtr(1),
			TrafficBytes: &traffic,
			MaxDevices:   intPtr(3),
			PoolIDs:      []uuid.UUID{},
		},
		Versions: []service.Version{
			{
				ID: verID, PlanID: planID, Version: 1, Status: service.VersionPublished,
				FrozenAt: &pub, MaxDevices: intPtr(3),
				Entitlements: []service.Entitlement{},
				Quotas: []service.Quota{
					{Metric: service.MetricTraffic, Limit: &traffic, Unit: service.UnitBytes, Period: service.PeriodCycle},
				},
			},
		},
		Prices: []service.Price{
			{
				ID: mustUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc"), PlanID: planID,
				PlanKind: service.KindSubscription, UnitAmount: decimal.RequireFromString("15.00"),
				BillingInterval: service.IntervalMonth, IntervalCount: 1, Status: service.PriceActive,
			},
		},
	}
}

func intPtr(n int) *int { return &n }

func sampleOfferPlan() service.OfferPlan {
	planID := mustUUID("11111111-1111-4111-8111-111111111111")
	priceID := mustUUID("22222222-2222-4222-8222-222222222222")
	traffic := int64(1073741824)
	reset := decimal.RequireFromString("3.00")
	return service.OfferPlan{
		Plan: service.Plan{
			ID: planID, Name: "基础", Kind: service.KindOneTime, Recommended: true,
			ResetPrice: &reset, ResetLimit: intPtr(5),
		},
		Version: service.Version{
			MaxDevices: intPtr(2),
			Quotas: []service.Quota{
				{Metric: service.MetricTraffic, Limit: &traffic, Unit: service.UnitBytes, Period: service.PeriodTotal},
			},
		},
		Prices: []service.Price{
			{
				ID: priceID, UnitAmount: decimal.RequireFromString("9.90"),
				BillingInterval: service.IntervalOneTime, IntervalCount: 1,
				ValidityDays: intPtr(30),
			},
		},
	}
}

func TestRegisteredRoutes(t *testing.T) {
	h := New(&fakeSvc{})
	pub := routePaths(newPublicEngine(h, nil))
	if len(pub) != 1 || pub[0] != "GET /v1/plans" {
		t.Fatalf("public routes %v", pub)
	}

	admin := routePaths(newAdminEngine(h, nil))
	want := []string{
		"GET /v1/plans",
		"GET /v1/plans/:id",
		"POST /v1/plans",
		"PUT /v1/plans/:id/basic",
		"PUT /v1/plans/:id/sales",
		"PUT /v1/plans/:id/reset",
		"PUT /v1/plans/:id/prices",
		"PUT /v1/plans/:id/pools",
		"PUT /v1/plans/:id/usage",
		"POST /v1/plans/:id/versions/:versionID/publish",
		"POST /v1/plans/:id/archive",
	}
	for _, w := range want {
		if !containsRoute(admin, w) {
			t.Fatalf("缺路由 %s，实际 %v", w, admin)
		}
	}
	forbidden := []string{
		"/v1/traffic-packs", "/v1/plans/:id/reset-prices",
		"POST /v1/plans/:id/prices",
	}
	for _, bad := range forbidden {
		for _, r := range admin {
			if strings.Contains(r, bad) {
				t.Fatalf("不应存在路由 %s（命中 %s）", bad, r)
			}
		}
	}
}

func containsRoute(routes []string, want string) bool {
	for _, r := range routes {
		if r == want {
			return true
		}
	}
	return false
}

func TestCreatePlanMappingSubscription(t *testing.T) {
	pool := mustUUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	f := &fakeSvc{createOut: samplePlanDetail()}
	e := newAdminEngine(New(f), nil)
	body := `{"kind":"subscription","name":"x","prices":[{"billing_interval":"month","interval_count":1,"unit_amount":1500}],` +
		`"traffic_bytes":1073741824,"max_devices":3,"pool_ids":["` + pool.String() + `"],` +
		`"reset":{"price":500,"limit":2},"publish":true}`
	w := doJSON(e, http.MethodPost, "/v1/plans", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.createIn.Kind != service.KindSubscription || len(f.createIn.Tiers) != 1 {
		t.Fatalf("Tiers %+v", f.createIn.Tiers)
	}
	if !f.createIn.Tiers[0].UnitAmount.Equal(decimal.RequireFromString("15.00")) {
		t.Fatalf("金额 %s", f.createIn.Tiers[0].UnitAmount)
	}
	if f.createIn.Usage.TrafficBytes == nil || *f.createIn.Usage.TrafficBytes != 1073741824 {
		t.Fatalf("traffic %+v", f.createIn.Usage.TrafficBytes)
	}
	if f.createIn.Usage.MaxDevices == nil || *f.createIn.Usage.MaxDevices != 3 {
		t.Fatalf("devices %+v", f.createIn.Usage.MaxDevices)
	}
	if len(f.createIn.PoolIDs) != 1 || f.createIn.PoolIDs[0] != pool {
		t.Fatalf("pools %+v", f.createIn.PoolIDs)
	}
	if f.createIn.Reset == nil || !f.createIn.Reset.Price.Equal(decimal.RequireFromString("5.00")) {
		t.Fatalf("reset %+v", f.createIn.Reset)
	}
	if !f.createIn.Sales.AllowNewPurchase || !f.createIn.Sales.AllowUpgrade || !f.createIn.Sales.AllowRenewal {
		t.Fatalf("sales %+v", f.createIn.Sales)
	}
	if !f.createIn.Publish {
		t.Fatal("publish 应为 true")
	}
}

func TestCreatePlanMappingOneTime(t *testing.T) {
	f := &fakeSvc{createOut: samplePlanDetail()}
	e := newAdminEngine(New(f), nil)
	w := doJSON(e, http.MethodPost, "/v1/plans", `{"kind":"one_time","name":"ot","unit_amount":990,"validity_days":30}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.createIn.OneTime == nil || !f.createIn.OneTime.UnitAmount.Equal(decimal.RequireFromString("9.90")) {
		t.Fatalf("one_time %+v", f.createIn.OneTime)
	}
	if f.createIn.OneTime.ValidityDays == nil || *f.createIn.OneTime.ValidityDays != 30 {
		t.Fatalf("validity %+v", f.createIn.OneTime.ValidityDays)
	}
	if f.createIn.Sales.AllowRenewal {
		t.Fatal("一次性 allow_renewal 缺省应为 false")
	}
}

func TestCreatePlanInvalidPoolIDs(t *testing.T) {
	f := &fakeSvc{}
	e := newAdminEngine(New(f), nil)
	w := doJSON(e, http.MethodPost, "/v1/plans", `{"kind":"subscription","name":"x","prices":[{"billing_interval":"month","interval_count":1,"unit_amount":100}],"pool_ids":["bad"]}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", w.Code)
	}
	var env struct {
		Error struct {
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Fields["pool_ids"] == "" {
		t.Fatalf("fields %v", env.Error.Fields)
	}
	if f.createIn.Name != "" {
		t.Fatal("service 不应被调用")
	}
}

func TestUpdatePricesMapping(t *testing.T) {
	planID := samplePlanDetail().ID
	f := &fakeSvc{getPlanOut: samplePlanDetail()}
	e := newAdminEngine(New(f), nil)
	path := "/v1/plans/" + planID.String() + "/prices"

	w := doJSON(e, http.MethodPut, path, `{"expected_row_version":1,"prices":[{"billing_interval":"month","interval_count":1,"unit_amount":null}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if len(f.updatePricesIn.Tiers) != 1 || f.updatePricesIn.Tiers[0].UnitAmount != nil {
		t.Fatalf("tiers %+v", f.updatePricesIn.Tiers)
	}

	f.updatePricesIn = service.PricesUpdate{}
	w = doJSON(e, http.MethodPut, path, `{"expected_row_version":1,"unit_amount":880,"validity_days":7}`)
	if w.Code != http.StatusOK {
		t.Fatalf("one_time status %d", w.Code)
	}
	if f.updatePricesIn.OneTime == nil || !f.updatePricesIn.OneTime.UnitAmount.Equal(decimal.RequireFromString("8.80")) {
		t.Fatalf("one_time %+v", f.updatePricesIn.OneTime)
	}

	w = doJSON(e, http.MethodPut, path, `{"expected_row_version":1}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty prices status %d", w.Code)
	}
	var env struct {
		Error struct {
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Error.Fields["prices"] == "" {
		t.Fatalf("fields %v", env.Error.Fields)
	}
}

func TestUpdateUsageMapping(t *testing.T) {
	planID := samplePlanDetail().ID
	f := &fakeSvc{getPlanOut: samplePlanDetail()}
	e := newAdminEngine(New(f), nil)
	w := doJSON(e, http.MethodPut, "/v1/plans/"+planID.String()+"/usage",
		`{"expected_row_version":3,"traffic_bytes":null,"max_devices":5}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.updateUsageIn.ExpectedRowVersion != 3 {
		t.Fatalf("rv %d", f.updateUsageIn.ExpectedRowVersion)
	}
	if f.updateUsageIn.Usage.TrafficBytes != nil {
		t.Fatalf("traffic %+v", f.updateUsageIn.Usage.TrafficBytes)
	}
	if f.updateUsageIn.Usage.MaxDevices == nil || *f.updateUsageIn.Usage.MaxDevices != 5 {
		t.Fatalf("devices %+v", f.updateUsageIn.Usage.MaxDevices)
	}
}

func TestGetPlanResponseShape(t *testing.T) {
	detail := samplePlanDetail()
	f := &fakeSvc{getPlanOut: detail}
	h := New(f)
	h.UseSubscriptionCounter(&fakeSubs{counts: map[uuid.UUID]int64{detail.ID: 11}})
	h.UseNodeCounter(&fakeNodes{counts: map[uuid.UUID]int64{detail.ID: 4}})
	w := doJSON(newAdminEngine(h, nil), http.MethodGet, "/v1/plans/"+detail.ID.String(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if err := json.Unmarshal(body["plan"], &plan); err != nil {
		t.Fatal(err)
	}
	if plan["kind"] != service.KindSubscription {
		t.Fatalf("kind %v", plan["kind"])
	}
	prices, _ := plan["prices"].([]any)
	p0, _ := prices[0].(map[string]any)
	if p0["unit_amount"] != float64(1500) && p0["unit_amount"] != int64(1500) {
		t.Fatalf("unit_amount %v", p0["unit_amount"])
	}
	reset, _ := plan["reset"].(map[string]any)
	if reset["price"] != float64(500) && reset["price"] != int64(500) {
		t.Fatalf("reset.price %v", reset["price"])
	}
	vers, _ := plan["versions"].([]any)
	v0, _ := vers[0].(map[string]any)
	if v0["published_at"] == nil {
		t.Fatalf("published_at %v", v0["published_at"])
	}
	poolIDs, ok := plan["pool_ids"].([]any)
	if !ok || poolIDs == nil {
		t.Fatalf("pool_ids %v", plan["pool_ids"])
	}
	if plan["active_subscriptions"] != float64(11) && plan["active_subscriptions"] != int64(11) {
		t.Fatalf("subs %v", plan["active_subscriptions"])
	}
	if plan["node_count"] != float64(4) && plan["node_count"] != int64(4) {
		t.Fatalf("nodes %v", plan["node_count"])
	}

	h2 := New(&fakeSvc{getPlanOut: detail})
	w2 := doJSON(newAdminEngine(h2, nil), http.MethodGet, "/v1/plans/"+detail.ID.String(), "")
	var resp struct {
		Plan struct {
			ActiveSubscriptions int64 `json:"active_subscriptions"`
			NodeCount           int64 `json:"node_count"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Plan.ActiveSubscriptions != 0 || resp.Plan.NodeCount != 0 {
		t.Fatalf("无计数端口 %+v", resp.Plan)
	}
}

func TestPortalPlansResponse(t *testing.T) {
	f := &fakeSvc{portalOffers: []service.OfferPlan{sampleOfferPlan()}}
	e := newPublicEngine(New(f), nil)
	w := doJSON(e, http.MethodGet, "/v1/plans", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	plans, _ := resp["plans"].([]any)
	p0, _ := plans[0].(map[string]any)
	for _, k := range []string{"kind", "traffic_bytes"} {
		if _, ok := p0[k]; !ok {
			t.Fatalf("缺 %s: %v", k, p0)
		}
	}
	prices, _ := p0["prices"].([]any)
	pr0, _ := prices[0].(map[string]any)
	if _, ok := pr0["validity_days"]; !ok {
		t.Fatalf("prices[0] %v", pr0)
	}
	if _, ok := p0["reset"]; !ok {
		t.Fatal("缺 reset")
	}
	for _, bad := range []string{"highlights", "currency"} {
		if _, ok := p0[bad]; ok {
			t.Fatalf("不应有 %s", bad)
		}
	}
	if _, ok := pr0["currency"]; ok {
		t.Fatal("价格不应有 currency")
	}
}

func TestInvalidPlanIDPath(t *testing.T) {
	e := newAdminEngine(New(&fakeSvc{}), nil)
	w := doJSON(e, http.MethodGet, "/v1/plans/not-a-uuid", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d", w.Code)
	}
}

func TestUnknownJSONField(t *testing.T) {
	e := newAdminEngine(New(&fakeSvc{}), nil)
	w := doJSON(e, http.MethodPost, "/v1/plans", `{"kind":"subscription","name":"n","visibility":"public","tenant_id":"t"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
}

func TestServiceConflictMapsTo409(t *testing.T) {
	f := &fakeSvc{createErr: service.ErrNewPurchaseClosed}
	w := doJSON(newAdminEngine(New(f), nil), http.MethodPost, "/v1/plans",
		`{"kind":"subscription","name":"n","prices":[{"billing_interval":"month","interval_count":1,"unit_amount":100}]}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d", w.Code)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Error.Code != string(errs.Conflict) {
		t.Fatalf("code %s", env.Error.Code)
	}
}
