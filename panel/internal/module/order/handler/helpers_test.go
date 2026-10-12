package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/order/service"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// fakeSvc 只实现用例设置了的方法：每个方法有一个可选的函数字段，没设置时走嵌入的 nil 接口（被调到会 panic）。
type fakeSvc struct {
	Service
	quote         func(service.QuoteInput) (service.QuoteResult, error)
	createNew     func(service.NewOrderInput) (service.Created, error)
	createRenewal func(service.RenewalOrderInput) (service.Created, error)
	createPack    func(service.PackOrderInput) (service.Created, error)
	createTopup   func(service.TopupInput) (service.TopupCreated, error)
	pay           func(service.PayInput) (paymentsvc.IntentResult, error)
	query         func(uuid.UUID) (service.QueryResult, error)
	adminQuery    func(uuid.UUID) (service.QueryResult, error)
	markPaid      func(service.MarkPaidInput) (service.MarkPaidResult, error)
	cancel        func(service.CancelInput) (service.CancelResult, error)
	myOrders      func(service.ListFilter) (service.MyOrderList, error)
	myOrder       func(uuid.UUID) (service.MyOrderDetail, error)
	adminOrders   func(service.ListFilter) (service.AdminOrderList, error)
	adminOrder    func(uuid.UUID) (service.AdminOrderDetail, error)
	adminPayments func(uuid.UUID) (paymentsvc.OrderHistory, error)
}

func (f *fakeSvc) Quote(_ context.Context, in service.QuoteInput) (service.QuoteResult, error) {
	return f.quote(in)
}

func (f *fakeSvc) CreateNewOrder(_ context.Context, in service.NewOrderInput) (service.Created, error) {
	return f.createNew(in)
}

func (f *fakeSvc) CreateRenewalOrder(_ context.Context, in service.RenewalOrderInput) (service.Created, error) {
	return f.createRenewal(in)
}

func (f *fakeSvc) CreatePackOrder(_ context.Context, in service.PackOrderInput) (service.Created, error) {
	return f.createPack(in)
}

func (f *fakeSvc) CreateTopup(_ context.Context, in service.TopupInput) (service.TopupCreated, error) {
	return f.createTopup(in)
}

func (f *fakeSvc) Pay(_ context.Context, in service.PayInput) (paymentsvc.IntentResult, error) {
	return f.pay(in)
}

func (f *fakeSvc) Query(_ context.Context, id uuid.UUID) (service.QueryResult, error) {
	return f.query(id)
}

func (f *fakeSvc) AdminQuery(_ context.Context, id uuid.UUID) (service.QueryResult, error) {
	return f.adminQuery(id)
}

func (f *fakeSvc) MarkPaid(_ context.Context, in service.MarkPaidInput) (service.MarkPaidResult, error) {
	return f.markPaid(in)
}

func (f *fakeSvc) Cancel(_ context.Context, in service.CancelInput) (service.CancelResult, error) {
	return f.cancel(in)
}

func (f *fakeSvc) MyOrders(_ context.Context, lf service.ListFilter) (service.MyOrderList, error) {
	return f.myOrders(lf)
}

func (f *fakeSvc) MyOrder(_ context.Context, id uuid.UUID) (service.MyOrderDetail, error) {
	return f.myOrder(id)
}

func (f *fakeSvc) AdminOrders(_ context.Context, lf service.ListFilter) (service.AdminOrderList, error) {
	return f.adminOrders(lf)
}

func (f *fakeSvc) AdminOrder(_ context.Context, id uuid.UUID) (service.AdminOrderDetail, error) {
	return f.adminOrder(id)
}

func (f *fakeSvc) AdminOrderPayments(_ context.Context, id uuid.UUID) (paymentsvc.OrderHistory, error) {
	return f.adminPayments(id)
}

// kitRec 记下路由链路上用到的幂等作用域、开关与重认证。
type kitRec struct {
	scopes   []string
	switches []string
	reauth   int
}

func pass(c *gin.Context) { c.Next() }

// newEngine 搭一个挂了 order 路由的 gin 引擎并放入登录主体；admin 为真挂后台路由（后台主体）。
func newEngine(svc Service, admin bool, rec *kitRec) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	audience := authn.AudiencePublic
	if admin {
		audience = authn.AudienceAdmin
	}
	p := authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: audience}
	e.Use(func(c *gin.Context) { authn.Set(c, p); c.Next() })
	if rec == nil {
		rec = &kitRec{}
	}
	kit := route.Kit{
		RequireAuth: pass,
		RecentReauth: func(c *gin.Context) {
			rec.reauth++
			c.Next()
		},
		Idempotency: func(scope string) gin.HandlerFunc {
			return func(c *gin.Context) {
				rec.scopes = append(rec.scopes, scope)
				c.Next()
			}
		},
		Switch: func(code string) gin.HandlerFunc {
			return func(c *gin.Context) {
				rec.switches = append(rec.switches, code)
				c.Next()
			}
		},
	}
	h := New(svc, nil)
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

// obj 把响应体解成 map，方便看字段在不在、是不是 null。
func obj(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v %s", err, w.Body)
	}
	return m
}

// sub 取嵌套对象。
func sub(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("字段 %s 不是对象: %#v", key, m[key])
	}
	return v
}

// mustStatus 核对状态码。
func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("状态 %d，期望 %d：%s", w.Code, want, w.Body)
	}
}

// errBody 是错误信封。
type errBody struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	} `json:"error"`
}

func parseErr(t *testing.T, w *httptest.ResponseRecorder) errBody {
	t.Helper()
	var b errBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("错误响应不是 JSON: %v %s", err, w.Body)
	}
	return b
}

// mustErr 核对错误状态与码。
func mustErr(t *testing.T, w *httptest.ResponseRecorder, status int, code string) errBody {
	t.Helper()
	mustStatus(t, w, status)
	b := parseErr(t, w)
	if b.Error.Code != code {
		t.Fatalf("错误码 %q，期望 %q：%s", b.Error.Code, code, w.Body)
	}
	return b
}

// num 取数字字段（JSON 数字解成 float64），字段缺失即失败。
func num(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("缺字段 %s", key)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("字段 %s 不是数字: %#v", key, v)
	}
	return f
}

// mustAbsent 核对 omitempty 的字段不出现。
func mustAbsent(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; ok {
			t.Fatalf("字段 %s 应省略，实际为 %#v", k, m[k])
		}
	}
}

// mustNull 核对 nullable 的字段在且为 null。
func mustNull(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			t.Fatalf("字段 %s 应在（null），实际缺失", k)
		}
		if v != nil {
			t.Fatalf("字段 %s 应为 null，实际为 %#v", k, v)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// mustContain 核对切片含某值。
func mustContain(t *testing.T, got []string, want string) {
	t.Helper()
	for _, g := range got {
		if g == want {
			return
		}
	}
	t.Fatalf("%v 里没有 %q", got, want)
}
