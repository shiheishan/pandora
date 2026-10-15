package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/promo/service"
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
	coupons      []service.Coupon
	listTotal    int64
	lastList     service.ListFilter
	lastLimit    int
	lastOffset   int
	createCode   string
	createSpec   service.Spec
	genPrefix    string
	genCount     int
	genSpec      service.Spec
	lastStatus   string
	lastStatusID uuid.UUID
	redemptions  []service.Redemption
	previewUser  uuid.UUID
	previewCode  string
	previewItem  service.PreviewItem
	previewOut   service.Preview
	previewErr   error
	genCodes     []string
}

func (f *fakeSvc) ListCoupons(_ context.Context, fl service.ListFilter, limit, offset int) ([]service.Coupon, int64, error) {
	f.lastList = fl
	f.lastLimit = limit
	f.lastOffset = offset
	if f.listTotal == 0 && len(f.coupons) > 0 {
		return f.coupons, int64(len(f.coupons)), nil
	}
	return f.coupons, f.listTotal, nil
}

func (f *fakeSvc) CreateCoupon(_ context.Context, code string, spec service.Spec) (uuid.UUID, string, error) {
	f.createCode = code
	f.createSpec = spec
	return uuid.New(), code, nil
}

func (f *fakeSvc) GenerateCoupons(_ context.Context, prefix string, count int, spec service.Spec) ([]string, error) {
	f.genPrefix = prefix
	f.genCount = count
	f.genSpec = spec
	if f.genCodes != nil {
		return f.genCodes, nil
	}
	out := make([]string, count)
	for i := range out {
		out[i] = prefix + "x"
	}
	return out, nil
}

func (f *fakeSvc) SetStatus(_ context.Context, id uuid.UUID, status string) error {
	f.lastStatusID = id
	f.lastStatus = status
	return nil
}

func (f *fakeSvc) Redemptions(context.Context, uuid.UUID) ([]service.Redemption, error) {
	return f.redemptions, nil
}

func (f *fakeSvc) Preview(_ context.Context, userID uuid.UUID, code string, item service.PreviewItem) (service.Preview, error) {
	f.previewUser = userID
	f.previewCode = code
	f.previewItem = item
	if f.previewErr != nil {
		return service.Preview{}, f.previewErr
	}
	return f.previewOut, nil
}

func newEngine(svc Service, admin bool, p *authn.Principal) *gin.Engine {
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
		Switch:       func(string) gin.HandlerFunc { return pass },
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

func decMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v body=%s", err, w.Body.String())
	}
	return got
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	got := decMap(t, w)
	errObj, _ := got["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("无 error 信封: %v", got)
	}
	code, _ := errObj["code"].(string)
	return code
}

func errFields(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	got := decMap(t, w)
	errObj, _ := got["error"].(map[string]any)
	fields, _ := errObj["fields"].(map[string]any)
	return fields
}

func decFixed(t *testing.T, s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestListCouponsShape(t *testing.T) {
	planA := uuid.New()
	fixedID := uuid.New()
	pctID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	f := &fakeSvc{
		coupons: []service.Coupon{
			{
				ID: fixedID, Code: "FIX10", Name: "定额", Status: service.StatusActive,
				Face:            service.Face{Kind: service.KindFixed, AmountOff: decFixed(t, "10.50")},
				Currency:        "CNY",
				MinOrder:        decimal.Zero,
				MaxPerUser:      1,
				PlanIDs:         nil,
				CreatedAt:       now,
				DiscountedTotal: decFixed(t, "21.00"),
			},
			{
				ID: pctID, Code: "VIP20", Name: "百分比", Status: service.StatusActive,
				Face:            service.Face{Kind: service.KindPercent, PercentBP: 2000},
				Currency:        "CNY",
				MaxDiscount:     ptrDec(decFixed(t, "5.00")),
				MinOrder:        decimal.Zero,
				MaxPerUser:      1,
				PlanIDs:         []uuid.UUID{planA},
				Intervals:       []string{"year"},
				CreatedAt:       now,
				DiscountedTotal: decimal.Zero,
			},
		},
		listTotal: 2,
	}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin})

	w := do(e, http.MethodGet, "/v1/coupons?status=active&q=vip&limit=50", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	got := decMap(t, w)
	if got["total"] != float64(2) {
		t.Fatalf("total %v", got["total"])
	}
	coupons, _ := got["coupons"].([]any)
	if len(coupons) != 2 {
		t.Fatalf("coupons len %d", len(coupons))
	}
	c0, _ := coupons[0].(map[string]any)
	c1, _ := coupons[1].(map[string]any)
	if c0["discount_value"] != float64(1050) || c0["discounted_total"] != float64(2100) {
		t.Fatalf("第一张金额 %v %v", c0["discount_value"], c0["discounted_total"])
	}
	plans0, _ := c0["applicable_plan_ids"].([]any)
	if plans0 == nil || len(plans0) != 0 {
		t.Fatalf("applicable_plan_ids 应为 [] 非 null: %v", c0["applicable_plan_ids"])
	}
	ivs0, ok0 := c0["applicable_intervals"].([]any)
	if !ok0 || ivs0 == nil || len(ivs0) != 0 {
		t.Fatalf("c0 applicable_intervals 应为 [] 非 null: %v", c0["applicable_intervals"])
	}
	if c0["max_discount"] != nil {
		t.Fatalf("max_discount 应为 null: %v", c0["max_discount"])
	}
	if c1["discount_value"] != float64(2000) || c1["max_discount"] != float64(500) {
		t.Fatalf("第二张 %v %v", c1["discount_value"], c1["max_discount"])
	}
	ivs1, ok1 := c1["applicable_intervals"].([]any)
	if !ok1 || len(ivs1) != 1 || ivs1[0] != "year" {
		t.Fatalf("c1 applicable_intervals 期望 [year]: %v", c1["applicable_intervals"])
	}
	if f.lastList.Query != "vip" || f.lastList.Status != "active" || f.lastLimit != 50 {
		t.Fatalf("ListFilter/limit %+v limit=%d", f.lastList, f.lastLimit)
	}

	w = do(e, http.MethodGet, "/v1/coupons?status=bogus", "")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bogus status %d", w.Code)
	}
}

func ptrDec(d decimal.Decimal) *decimal.Decimal { return &d }

func TestCreateCouponMapping(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin})
	w := do(e, http.MethodPost, "/v1/coupons", `{"code":" summer ","discount_type":"fixed","discount_value":1050,"min_order_amount":5000,"max_redemptions_per_user":2}`)
	if w.Code != http.StatusOK {
		t.Fatalf("fixed create status %d %s", w.Code, w.Body)
	}
	got := decMap(t, w)
	if got["code"] == nil || got["id"] == nil {
		t.Fatalf("响应缺 code/id: %v", got)
	}
	if f.createCode != " summer " {
		t.Fatalf("createCode %q", f.createCode)
	}
	if f.createSpec.Face.Kind != service.KindFixed || !f.createSpec.Face.AmountOff.Equal(decFixed(t, "10.50")) {
		t.Fatalf("Face fixed %v", f.createSpec.Face)
	}
	if f.createSpec.Currency != "" || !f.createSpec.MinOrder.Equal(decFixed(t, "50.00")) || f.createSpec.MaxPerUser != 2 {
		t.Fatalf("Spec %+v", f.createSpec)
	}
	if f.createSpec.Name != "SUMMER" {
		t.Fatalf("Name %q", f.createSpec.Name)
	}
	if len(f.createSpec.Intervals) != 0 {
		t.Fatalf("不带 applicable_intervals 应为 nil 或空: %v", f.createSpec.Intervals)
	}

	f3 := &fakeSvc{}
	e3 := newEngine(f3, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin})
	w = do(e3, http.MethodPost, "/v1/coupons", `{"code":"MONTHONLY","discount_type":"fixed","discount_value":1000,"applicable_intervals":["month"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("applicable_intervals create status %d %s", w.Code, w.Body)
	}
	if len(f3.createSpec.Intervals) != 1 || f3.createSpec.Intervals[0] != "month" {
		t.Fatalf("createSpec Intervals 期望 [month], got %v", f3.createSpec.Intervals)
	}

	f2 := &fakeSvc{}
	e2 := newEngine(f2, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin})
	w = do(e2, http.MethodPost, "/v1/coupons", `{"code":"A","discount_type":"percent","discount_value":2000,"max_discount":500}`)
	if w.Code != http.StatusOK {
		t.Fatalf("percent status %d %s", w.Code, w.Body)
	}
	if f2.createSpec.Face.PercentBP != 2000 || f2.createSpec.MaxDiscount == nil || !f2.createSpec.MaxDiscount.Equal(decFixed(t, "5.00")) {
		t.Fatalf("percent spec %+v", f2.createSpec)
	}
	if f2.createSpec.MaxPerUser != 1 {
		t.Fatalf("MaxPerUser %d", f2.createSpec.MaxPerUser)
	}

	w = do(e2, http.MethodPost, "/v1/coupons", `{"code":"A","discount_type":"fixed","discount_value":1,"bogus":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status %d", w.Code)
	}

	w = do(e2, http.MethodPost, "/v1/coupons", `{"code":"A","discount_type":"x","discount_value":1}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad type status %d", w.Code)
	}

	w = do(e2, http.MethodPost, "/v1/coupons", `{"code":"A","discount_type":"fixed","discount_value":1,"valid_until":"not-a-time"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad time status %d", w.Code)
	}
	fields := errFields(t, w)
	if fields["valid_until"] == nil {
		t.Fatalf("fields %v", fields)
	}

	w = do(e2, http.MethodPost, "/v1/coupons", `{"code":"   ","discount_type":"fixed","discount_value":1}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty code status %d", w.Code)
	}
	fields = errFields(t, w)
	if fields["code"] == nil {
		t.Fatalf("fields %v", fields)
	}
}

func TestGenerateCouponsDefaults(t *testing.T) {
	f := &fakeSvc{genCodes: []string{"vip1", "vip2", "vip3"}}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin})
	w := do(e, http.MethodPost, "/v1/coupons/batch", `{"name":"双11","count":3,"prefix":"vip","discount_type":"percent","discount_value":1000}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	got := decMap(t, w)
	if got["count"] != float64(3) || got["name"] != "双11" {
		t.Fatalf("count/name %v", got)
	}
	codes, _ := got["codes"].([]any)
	if len(codes) != 3 {
		t.Fatalf("codes %v", got["codes"])
	}
	if f.genPrefix != "vip" || f.genCount != 3 {
		t.Fatalf("prefix/count %q %d", f.genPrefix, f.genCount)
	}
	if f.genSpec.MaxRedemptions == nil || *f.genSpec.MaxRedemptions != 1 {
		t.Fatalf("MaxRedemptions %v", f.genSpec.MaxRedemptions)
	}
}

func TestSetStatusAndRedemptions(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin})

	w := do(e, http.MethodPost, "/v1/coupons/not-a-uuid/status", `{"status":"paused"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("bad id status %d", w.Code)
	}

	cid := uuid.New()
	w = do(e, http.MethodPost, "/v1/coupons/"+cid.String()+"/status", `{"status":"paused"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("set status %d %s", w.Code, w.Body)
	}
	got := decMap(t, w)
	if got["ok"] != true {
		t.Fatalf("ok %v", got)
	}
	if f.lastStatus != "paused" || f.lastStatusID != cid {
		t.Fatalf("SetStatus %s %s", f.lastStatus, f.lastStatusID)
	}

	f.redemptions = []service.Redemption{{
		UserID: uuid.New(), Email: "u@example.test", OrderNo: "o1",
		Discount: decFixed(t, "3.30"), Currency: "CNY", At: time.Now(), Released: true,
	}}
	w = do(e, http.MethodGet, "/v1/coupons/"+cid.String()+"/redemptions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("redemptions %d %s", w.Code, w.Body)
	}
	got = decMap(t, w)
	reds, _ := got["redemptions"].([]any)
	if len(reds) != 1 {
		t.Fatalf("reds %v", got)
	}
	r0, _ := reds[0].(map[string]any)
	if r0["discount"] != float64(330) || r0["reverted"] != true || r0["email"] != "u@example.test" {
		t.Fatalf("row %v", r0)
	}

	f.redemptions = []service.Redemption{}
	w = do(e, http.MethodGet, "/v1/coupons/"+cid.String()+"/redemptions", "")
	got = decMap(t, w)
	reds, _ = got["redemptions"].([]any)
	if reds == nil || len(reds) != 0 {
		t.Fatalf("空 redemptions 应为 []: %v", got["redemptions"])
	}
}

func TestPreview(t *testing.T) {
	planA := uuid.New()
	priceB := uuid.New()
	packA := uuid.New()
	userID := uuid.New()
	p := &authn.Principal{UserID: userID, SessionID: "s", Audience: authn.AudiencePublic}

	f := &fakeSvc{
		previewOut: service.Preview{
			Subtotal: decFixed(t, "30.00"),
			Discount: decFixed(t, "3.00"),
			Payable:  decFixed(t, "27.00"),
			Currency: "CNY",
			Applied: &service.Applied{
				Code: "X",
				Face: service.Face{Kind: service.KindPercent, PercentBP: 1000},
			},
		},
	}
	e := newEngine(f, false, p)
	body := `{"plan_id":"` + planA.String() + `","price_id":"` + priceB.String() + `","coupon_code":"x"}`
	w := do(e, http.MethodPost, "/v1/coupons/preview", body)
	if w.Code != http.StatusOK {
		t.Fatalf("preview %d %s", w.Code, w.Body)
	}
	got := decMap(t, w)
	if got["subtotal"] != float64(3000) || got["discount"] != float64(300) || got["payable"] != float64(2700) {
		t.Fatalf("金额 %v", got)
	}
	coupon, _ := got["coupon"].(map[string]any)
	if coupon["discount_type"] != "percent" || coupon["discount_value"] != float64(1000) {
		t.Fatalf("coupon %v", coupon)
	}
	if f.previewUser != userID || f.previewItem.PlanID == nil || *f.previewItem.PlanID != planA ||
		f.previewItem.PriceID == nil || *f.previewItem.PriceID != priceB || f.previewItem.PackID != nil {
		t.Fatalf("PreviewItem/user %v %v", f.previewItem, f.previewUser)
	}

	f.previewOut.Applied = nil
	f.previewOut.Discount = decimal.Zero
	w = do(e, http.MethodPost, "/v1/coupons/preview", body)
	got = decMap(t, w)
	if got["coupon"] != nil {
		t.Fatalf("coupon 应为 null: %v", got["coupon"])
	}

	w = do(e, http.MethodPost, "/v1/coupons/preview", `{}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty %d", w.Code)
	}
	fields := errFields(t, w)
	if fields["plan_id"] == nil || fields["price_id"] == nil {
		t.Fatalf("fields %v", fields)
	}

	w = do(e, http.MethodPost, "/v1/coupons/preview", `{"pack_id":"bad"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad pack %d", w.Code)
	}
	fields = errFields(t, w)
	if fields["pack_id"] == nil {
		t.Fatalf("fields %v", fields)
	}

	w = do(e, http.MethodPost, "/v1/coupons/preview", `{"pack_id":"`+packA.String()+`","plan_id":"`+planA.String()+`"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("both %d", w.Code)
	}
	fields = errFields(t, w)
	if fields["pack_id"] != "流量包与套餐只能二选一" {
		t.Fatalf("pack_id msg %v", fields["pack_id"])
	}

	eNoAuth := newEngine(&fakeSvc{}, false, nil)
	w = do(eNoAuth, http.MethodPost, "/v1/coupons/preview", body)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 %d", w.Code)
	}

	f.previewErr = service.ErrCouponUsedUp
	w = do(e, http.MethodPost, "/v1/coupons/preview", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("used up status %d %s", w.Code, w.Body)
	}
	if errCode(t, w) != "conflict" {
		t.Fatalf("code %s", errCode(t, w))
	}
}
