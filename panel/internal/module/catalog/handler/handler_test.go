package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/catalog/service"
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

	portalUserID *uuid.UUID
	portalOffers []service.OfferPlan

	listPlans []service.PlanSummary

	createPlanIn  service.PlanInput
	createPlanOut service.PlanDetail
	createPlanErr error

	updateVersionCalled bool
	updateVersionIn     service.VersionInput
	updateVersionRV     int64

	updateCompleteIn service.WizardUpdate

	createPriceIn  service.PriceInput
	createPriceOut service.Price

	createCompleteOut service.WizardCreated

	setPackStatusIn struct {
		packID   uuid.UUID
		status   string
		expected *time.Time
	}
	setPackStatusOut service.TrafficPack

	activePacks []service.TrafficPack
}

func (f *fakeSvc) PortalCatalog(_ context.Context, userID *uuid.UUID) ([]service.OfferPlan, error) {
	f.portalUserID = userID
	return f.portalOffers, nil
}

func (f *fakeSvc) ListPlans(context.Context) ([]service.PlanSummary, error) {
	return f.listPlans, nil
}

func (f *fakeSvc) CreatePlan(_ context.Context, in service.PlanInput) (service.PlanDetail, error) {
	f.createPlanIn = in
	if f.createPlanErr != nil {
		return service.PlanDetail{}, f.createPlanErr
	}
	return f.createPlanOut, nil
}

func (f *fakeSvc) UpdateVersion(_ context.Context, _, _ uuid.UUID, _ int64, in service.VersionInput) (int64, error) {
	f.updateVersionCalled = true
	f.updateVersionIn = in
	return f.updateVersionRV, nil
}

func (f *fakeSvc) UpdatePlanComplete(_ context.Context, _ uuid.UUID, in service.WizardUpdate) (service.WizardUpdated, error) {
	f.updateCompleteIn = in
	return service.WizardUpdated{Plan: f.createPlanOut, Changed: []string{}}, nil
}

func (f *fakeSvc) CreatePrice(_ context.Context, _ uuid.UUID, in service.PriceInput) (service.Price, error) {
	f.createPriceIn = in
	return f.createPriceOut, nil
}

func (f *fakeSvc) CreatePlanComplete(_ context.Context, in service.WizardCreate) (service.WizardCreated, error) {
	_ = in
	return f.createCompleteOut, nil
}

func (f *fakeSvc) SetTrafficPackStatus(_ context.Context, packID uuid.UUID, status string, expected *time.Time) (service.TrafficPack, error) {
	f.setPackStatusIn.packID = packID
	f.setPackStatusIn.status = status
	f.setPackStatusIn.expected = expected
	return f.setPackStatusOut, nil
}

func (f *fakeSvc) ActiveTrafficPacks(context.Context) ([]service.TrafficPack, error) {
	return f.activePacks, nil
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

func (f *fakeNodes) ServingNodeCounts(_ context.Context, versionIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	f.ids = versionIDs
	return f.counts, nil
}

type fakeSales struct {
	counts map[uuid.UUID]int64
	ids    []uuid.UUID
}

func (f *fakeSales) PackSoldCounts(_ context.Context, packIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	f.ids = packIDs
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

func mustParseUUID(s string) uuid.UUID { return uuid.MustParse(s) }

func sampleOfferPlan() service.OfferPlan {
	planID := mustParseUUID("11111111-1111-4111-8111-111111111111")
	priceID := mustParseUUID("22222222-2222-4222-8222-222222222222")
	resetDay := 1
	traffic := int64(1 << 30)
	verNum := 3
	return service.OfferPlan{
		Plan: service.Plan{
			ID:           planID,
			Code:         "basic",
			Name:         "基础",
			AllowRenewal: true,
			AllowUpgrade: true,
			Highlights:   nil,
		},
		Version: service.Version{
			Version:            verNum,
			QuotaResetStrategy: service.ResetFixedDay,
			QuotaResetDay:      &resetDay,
			Quotas: []service.Quota{
				{Metric: service.MetricTraffic, Limit: &traffic, Unit: "bytes", Period: service.PeriodCycle},
			},
		},
		Prices: []service.Price{
			{
				ID:              priceID,
				Currency:        money.CNY,
				UnitAmount:      decimal.RequireFromString("9.90"),
				BillingInterval: "month",
				IntervalCount:   1,
			},
		},
	}
}

func samplePlanDetail() service.PlanDetail {
	id := mustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	verID := mustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	return service.PlanDetail{
		Plan: service.Plan{
			ID:         id,
			Code:       "c1",
			Name:       "套餐",
			Status:     service.PlanDraft,
			Visibility: service.VisibilityPublic,
			Highlights: []string{},
		},
		Versions: []service.Version{
			{ID: verID, PlanID: id, Version: 1, Status: service.VersionDraft, Entitlements: []service.Entitlement{}, Quotas: []service.Quota{}},
		},
		Prices: []service.Price{},
	}
}

// 1. 门户 GET /v1/plans 匿名
func TestPortalPlansAnonymous(t *testing.T) {
	f := &fakeSvc{portalOffers: []service.OfferPlan{sampleOfferPlan()}}
	e := newPublicEngine(New(f), nil)
	w := doJSON(e, http.MethodGet, "/v1/plans", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.portalUserID != nil {
		t.Fatalf("PortalCatalog userID 应为 nil，得到 %v", f.portalUserID)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var plans []map[string]any
	if err := json.Unmarshal(body["plans"], &plans); err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans len %d", len(plans))
	}
	prices, ok := plans[0]["prices"].([]any)
	if !ok || len(prices) != 1 {
		t.Fatalf("prices %v", plans[0]["prices"])
	}
	p0, _ := prices[0].(map[string]any)
	if p0["unit_amount"] != float64(990) && p0["unit_amount"] != int64(990) {
		t.Fatalf("unit_amount %v", p0["unit_amount"])
	}
	if plans[0]["quota_reset_day"] != float64(1) && plans[0]["quota_reset_day"] != int64(1) {
		t.Fatalf("quota_reset_day %v", plans[0]["quota_reset_day"])
	}
	quotas, ok := plans[0]["quotas"].([]any)
	if !ok || len(quotas) != 1 {
		t.Fatalf("quotas %v", plans[0]["quotas"])
	}
	if plans[0]["highlights"] == nil {
		t.Fatal("highlights 不应为 null")
	}
}

// 2. 门户 GET /v1/plans 已登录与空列表
func TestPortalPlansLoggedInAndEmpty(t *testing.T) {
	uid := uuid.New()
	p := &authn.Principal{UserID: uid, SessionID: "s", Audience: authn.AudiencePublic}
	f := &fakeSvc{portalOffers: []service.OfferPlan{}}
	e := newPublicEngine(New(f), p)
	w := doJSON(e, http.MethodGet, "/v1/plans", "")
	if w.Code != http.StatusOK || w.Body.String() != `{"plans":[]}` {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if f.portalUserID == nil || *f.portalUserID != uid {
		t.Fatalf("PortalCatalog userID %+v", f.portalUserID)
	}
}

// 3. 门户 GET /v1/traffic-packs
func TestPortalTrafficPacks(t *testing.T) {
	packID := mustParseUUID("33333333-3333-4333-8333-333333333333")
	f := &fakeSvc{
		activePacks: []service.TrafficPack{
			{
				ID:           packID,
				Name:         "加量包",
				TrafficBytes: 5e9,
				Currency:     money.CNY,
				UnitAmount:   decimal.RequireFromString("5.00"),
				Recommended:  true,
				Status:       service.PackActive,
			},
		},
	}
	e := newPublicEngine(New(f), nil)
	w := doJSON(e, http.MethodGet, "/v1/traffic-packs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var resp map[string][]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp["packs"]) != 1 {
		t.Fatalf("packs %v", resp["packs"])
	}
	pk := resp["packs"][0]
	wantKeys := []string{"id", "name", "traffic_bytes", "currency", "unit_amount", "recommended"}
	for _, k := range wantKeys {
		if _, ok := pk[k]; !ok {
			t.Fatalf("缺字段 %s: %v", k, pk)
		}
	}
	if len(pk) != len(wantKeys) {
		t.Fatalf("多余字段 %v", pk)
	}
	if pk["id"] != packID.String() || pk["unit_amount"] != float64(500) && pk["unit_amount"] != int64(500) {
		t.Fatalf("pack 字段 %v", pk)
	}
}

// 4. 后台 GET /v1/plans 端口计数与 product_id
func TestAdminListPlansCounters(t *testing.T) {
	planID := mustParseUUID("44444444-4444-4444-8444-444444444444")
	verID := mustParseUUID("55555555-5555-4555-8555-555555555555")
	cur := &verID
	f := &fakeSvc{
		listPlans: []service.PlanSummary{
			{Plan: service.Plan{ID: planID, Code: "p", Name: "P", CurrentVersionID: cur, Highlights: []string{}}},
		},
	}
	h := New(f)
	subs := &fakeSubs{counts: map[uuid.UUID]int64{planID: 7}}
	nodes := &fakeNodes{counts: map[uuid.UUID]int64{verID: 42}}
	h.UseSubscriptionCounter(subs)
	h.UseNodeCounter(nodes)
	e := newAdminEngine(h, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin})
	w := doJSON(e, http.MethodGet, "/v1/plans", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var body struct {
		Plans []struct {
			ProductID           string `json:"product_id"`
			ActiveSubscriptions int    `json:"active_subscriptions"`
			NodeCount           int    `json:"node_count"`
		} `json:"plans"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Plans[0].ProductID != planID.String() || body.Plans[0].ActiveSubscriptions != 7 || body.Plans[0].NodeCount != 42 {
		t.Fatalf("%+v", body.Plans[0])
	}

	// 未装端口时为 0
	h2 := New(&fakeSvc{listPlans: f.listPlans})
	w2 := doJSON(newAdminEngine(h2, nil), http.MethodGet, "/v1/plans", "")
	if err := json.Unmarshal(w2.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Plans[0].ActiveSubscriptions != 0 || body.Plans[0].NodeCount != 0 {
		t.Fatalf("无端口 %+v", body.Plans[0])
	}
}

// 5. 后台 POST /v1/plans
func TestAdminCreatePlan(t *testing.T) {
	detail := samplePlanDetail()
	f := &fakeSvc{createPlanOut: detail}
	e := newAdminEngine(New(f), nil)

	w := doJSON(e, http.MethodPost, "/v1/plans", `{"code":"x","name":"n","visibility":"public","visible_group_ids":["not-uuid"]}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏 uuid status %d %s", w.Code, w.Body)
	}
	var env struct {
		Error struct {
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Error.Fields["visible_group_ids"] == "" {
		t.Fatalf("fields %v", env.Error.Fields)
	}

	w = doJSON(e, http.MethodPost, "/v1/plans", `{"code":"x","name":"n","visibility":"public"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status %d %s", w.Code, w.Body)
	}
	if !f.createPlanIn.AllowRenewal {
		t.Fatal("allow_renewal 缺省应为 true")
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	plan, _ := created["plan"].(map[string]any)
	vers, ok := plan["versions"].([]any)
	if !ok {
		t.Fatalf("plan.versions %v", plan["versions"])
	}
	if len(vers) == 0 {
		t.Fatal("versions 应为数组")
	}
}

// 6. 后台 PUT /v1/plans/:id/versions/:versionID 与 pool_ids
func TestAdminUpdateVersionPoolIDs(t *testing.T) {
	planID := samplePlanDetail().Plan.ID
	verID := samplePlanDetail().Versions[0].ID
	f := &fakeSvc{updateVersionRV: 9}
	e := newAdminEngine(New(f), nil)
	path := "/v1/plans/" + planID.String() + "/versions/" + verID.String()

	w := doJSON(e, http.MethodPut, path, `{"expected_row_version":1,"pool_ids":[]}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("pool_ids [] status %d %s", w.Code, w.Body)
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
	if f.updateVersionCalled {
		t.Fatal("不应调用 UpdateVersion")
	}

	f.updateVersionCalled = false
	w = doJSON(e, http.MethodPut, path, `{"expected_row_version":1}`)
	if w.Code != http.StatusOK || !f.updateVersionCalled {
		t.Fatalf("omit pool_ids status %d called %v", w.Code, f.updateVersionCalled)
	}
	var okBody struct {
		OK         bool  `json:"ok"`
		RowVersion int64 `json:"row_version"`
	}
	json.Unmarshal(w.Body.Bytes(), &okBody)
	if !okBody.OK || okBody.RowVersion != 9 {
		t.Fatalf("%+v", okBody)
	}

	f.updateVersionCalled = false
	w = doJSON(e, http.MethodPut, path, `{"expected_row_version":1,"pool_ids":null}`)
	if w.Code != http.StatusOK || !f.updateVersionCalled {
		t.Fatalf("pool_ids null status %d", w.Code)
	}
}

// 7. 后台 PUT /v1/plans/:id/complete
func TestAdminUpdatePlanCompleteOptionalFields(t *testing.T) {
	planID := samplePlanDetail().Plan.ID
	f := &fakeSvc{createPlanOut: samplePlanDetail()}
	e := newAdminEngine(New(f), nil)
	path := "/v1/plans/" + planID.String() + "/complete"

	body := `{"expected_row_version":1,"code":"c","name":"n","visibility":"public","visible_group_ids":[],` +
		`"max_devices":null,"throttle_kbps":512,"pool_ids":["` + planID.String() + `"]}`
	w := doJSON(e, http.MethodPut, path, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if !f.updateCompleteIn.MaxDevices.Set || f.updateCompleteIn.MaxDevices.Value != nil {
		t.Fatalf("max_devices null %+v", f.updateCompleteIn.MaxDevices)
	}
	if !f.updateCompleteIn.ThrottleKbps.Set || f.updateCompleteIn.ThrottleKbps.Value == nil || *f.updateCompleteIn.ThrottleKbps.Value != 512 {
		t.Fatalf("throttle %+v", f.updateCompleteIn.ThrottleKbps)
	}
	if f.updateCompleteIn.Prices != nil {
		t.Fatal("prices 缺省应为 nil")
	}

	f.updateCompleteIn = service.WizardUpdate{}
	w = doJSON(e, http.MethodPut, path, `{"expected_row_version":1,"code":"c","name":"n","visibility":"public","visible_group_ids":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if f.updateCompleteIn.MaxDevices.Set || f.updateCompleteIn.ThrottleKbps.Set {
		t.Fatalf("未带 optional 不应 Set: max=%+v throttle=%+v", f.updateCompleteIn.MaxDevices, f.updateCompleteIn.ThrottleKbps)
	}
}

// 8. 后台 POST /v1/plans/:id/prices
func TestAdminCreatePrice(t *testing.T) {
	planID := samplePlanDetail().Plan.ID
	priceID := uuid.New()
	f := &fakeSvc{
		createPriceOut: service.Price{
			ID: priceID, Currency: money.CNY, UnitAmount: decimal.RequireFromString("19.99"),
			BillingInterval: "month", IntervalCount: 1, Status: service.PriceActive,
		},
	}
	e := newAdminEngine(New(f), nil)
	path := "/v1/plans/" + planID.String() + "/prices"
	w := doJSON(e, http.MethodPost, path, `{"currency":"CNY","unit_amount":1999,"billing_interval":"month","interval_count":1}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if !f.createPriceIn.UnitAmount.Equal(decimal.RequireFromString("19.99")) {
		t.Fatalf("service 金额 %s", f.createPriceIn.UnitAmount)
	}
	var resp struct {
		Price struct {
			UnitAmount int64 `json:"unit_amount"`
		} `json:"price"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Price.UnitAmount != 1999 {
		t.Fatalf("响应 unit_amount %d", resp.Price.UnitAmount)
	}
}

// 9. 后台 POST /v1/plans/complete
func TestAdminCreatePlanComplete(t *testing.T) {
	detail := samplePlanDetail()
	verID := detail.Versions[0].ID
	priceID := uuid.New()
	f := &fakeSvc{
		createCompleteOut: service.WizardCreated{
			Plan:      detail,
			VersionID: verID,
			PriceIDs:  []uuid.UUID{priceID},
			Published: false,
		},
	}
	e := newAdminEngine(New(f), nil)
	w := doJSON(e, http.MethodPost, "/v1/plans/complete", `{"code":"w","name":"向导","visibility":"public","visible_group_ids":[]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var resp struct {
		VersionID string   `json:"version_id"`
		PriceIDs  []string `json:"price_ids"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.VersionID != verID.String() || len(resp.PriceIDs) != 1 || resp.PriceIDs[0] != priceID.String() {
		t.Fatalf("%+v", resp)
	}
}

// 10. 非法路径 id → 404
func TestInvalidPlanIDPath(t *testing.T) {
	e := newAdminEngine(New(&fakeSvc{}), nil)
	w := doJSON(e, http.MethodGet, "/v1/plans/not-a-uuid", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d", w.Code)
	}
}

// 11. 未知 JSON 字段（与 identity/auth 样板一致：400 bad_request）
func TestUnknownJSONField(t *testing.T) {
	e := newAdminEngine(New(&fakeSvc{}), nil)
	w := doJSON(e, http.MethodPost, "/v1/plans", `{"code":"x","name":"n","visibility":"public","tenant_id":"t"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status %d %s", w.Code, w.Body)
	}
}

// 12. 后台 POST /v1/traffic-packs/:id/status 与 sold_count
func TestAdminSetTrafficPackStatus(t *testing.T) {
	packID := mustParseUUID("66666666-6666-4666-8666-666666666666")
	exp := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	f := &fakeSvc{
		setPackStatusOut: service.TrafficPack{
			ID: packID, Name: "p", TrafficBytes: 1, Currency: money.CNY,
			UnitAmount: decimal.Zero, Status: service.PackArchived, UpdatedAt: exp,
		},
	}
	h := New(f)
	h.UsePackSales(&fakeSales{counts: map[uuid.UUID]int64{packID: 99}})
	e := newAdminEngine(h, nil)
	path := "/v1/traffic-packs/" + packID.String() + "/status"
	body := `{"status":"archived","expected_updated_at":"2026-03-01T12:00:00Z"}`
	w := doJSON(e, http.MethodPost, path, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.setPackStatusIn.status != "archived" || f.setPackStatusIn.packID != packID {
		t.Fatalf("透传 %+v", f.setPackStatusIn)
	}
	if f.setPackStatusIn.expected == nil || !f.setPackStatusIn.expected.Equal(exp) {
		t.Fatalf("expected_updated_at %v", f.setPackStatusIn.expected)
	}
	var resp struct {
		Pack struct {
			SoldCount int64 `json:"sold_count"`
		} `json:"pack"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Pack.SoldCount != 99 {
		t.Fatalf("sold_count %d", resp.Pack.SoldCount)
	}
}

// 13. service.ErrSoldOut → 409
func TestServiceErrSoldOutMapsTo409(t *testing.T) {
	f := &fakeSvc{createPlanErr: service.ErrSoldOut}
	e := newAdminEngine(New(f), nil)
	w := doJSON(e, http.MethodPost, "/v1/plans", `{"code":"x","name":"n","visibility":"public"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	json.Unmarshal(w.Body.Bytes(), &env)
	if env.Error.Code != string(errs.Conflict) {
		t.Fatalf("code %s", env.Error.Code)
	}
}
