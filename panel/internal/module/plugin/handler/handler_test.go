package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/aegis/internal/module/plugin/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeSvc struct {
	Service
	hooks       []service.Hook
	lastSave    service.SaveHookInput
	deleteCode  string
	deliverCode string
	deliverLim  int
	testCode    string
	saveSecret  string
	saveErr     error
	testErr     error
	deliveries  []service.Delivery
}

func (f *fakeSvc) List(context.Context) ([]service.Hook, error) {
	return f.hooks, nil
}

func (f *fakeSvc) SaveHook(_ context.Context, in service.SaveHookInput) (string, error) {
	f.lastSave = in
	if f.saveErr != nil {
		return "", f.saveErr
	}
	return f.saveSecret, nil
}

func (f *fakeSvc) DeleteHook(_ context.Context, code string) error {
	f.deleteCode = code
	return nil
}

func (f *fakeSvc) Deliveries(_ context.Context, code string, limit int) ([]service.Delivery, error) {
	f.deliverCode = code
	f.deliverLim = limit
	return f.deliveries, nil
}

func (f *fakeSvc) TestHook(_ context.Context, code string) (service.TestResult, error) {
	f.testCode = code
	if f.testErr != nil {
		return service.TestResult{}, f.testErr
	}
	return service.TestResult{DurationMS: 12, ResponseCode: 200}, nil
}

func newEngine(svc Service, kit route.Kit) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	e.Use(func(c *gin.Context) {
		authn.Set(c, authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin})
		c.Next()
	})
	if kit.RequireAuth == nil {
		pass := func(c *gin.Context) { c.Next() }
		kit = route.Kit{
			RequireAuth:  pass,
			RecentReauth: pass,
			Idempotency:  func(string) gin.HandlerFunc { return pass },
		}
	}
	if kit.RecentReauth == nil {
		kit.RecentReauth = kit.RequireAuth
	}
	if kit.Idempotency == nil {
		kit.Idempotency = func(string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } }
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

func decMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v body=%s", err, w.Body.String())
	}
	return got
}

func errFields(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	got := decMap(t, w)
	errObj, _ := got["error"].(map[string]any)
	fields, _ := errObj["fields"].(map[string]any)
	return fields
}

func TestListShape(t *testing.T) {
	id1, id2 := uuid.New(), uuid.New()
	f := &fakeSvc{hooks: []service.Hook{
		{
			ID: id1, Code: "a", Name: "A", Description: "d", Enabled: true,
			Events: []string{"order.paid"}, EndpointURL: "https://example.com/hook",
			HasSecret: true, TimeoutMS: 5000, MaxAttempts: 5,
			QueuedCount: 1, FailedCount: 2, SentCount7d: 3, LastSentAt: "2026-10-01 12:00",
		},
		{
			ID: id2, Code: "b", Name: "B", Description: "", Enabled: false,
			Events: nil, EndpointURL: "https://example.test/x",
			HasSecret: false, TimeoutMS: 1000, MaxAttempts: 1,
			QueuedCount: 0, FailedCount: 0, SentCount7d: 0, LastSentAt: "",
		},
	}}
	e := newEngine(f, route.Kit{})
	w := do(e, http.MethodGet, "/v1/plugin-hooks", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	got := decMap(t, w)
	hooks, _ := got["hooks"].([]any)
	if len(hooks) != 2 {
		t.Fatalf("hooks len %d", len(hooks))
	}
	h0, _ := hooks[0].(map[string]any)
	if h0["id"] != id1.String() || h0["code"] != "a" || h0["last_sent_at"] != "2026-10-01 12:00" {
		t.Fatalf("hook0 %v", h0)
	}
	if _, ok := h0["timeout_ms"].(float64); !ok {
		t.Fatalf("timeout_ms type %T", h0["timeout_ms"])
	}
	h1, _ := hooks[1].(map[string]any)
	if _, ok := h1["last_sent_at"]; ok {
		t.Fatalf("应有 last_sent_at 缺席: %v", h1)
	}
	ev1, _ := h1["events"].([]any)
	if ev1 == nil || len(ev1) != 0 {
		t.Fatalf("events %v", h1["events"])
	}
	events, _ := got["events"].([]any)
	if len(events) != len(service.Events) {
		t.Fatalf("events len %d want %d", len(events), len(service.Events))
	}
	for _, row := range events {
		m, _ := row.(map[string]any)
		if len(m) != 2 || m["name"] == nil || m["desc"] == nil {
			t.Fatalf("event row %v", m)
		}
	}
}

func TestListEmpty(t *testing.T) {
	f := &fakeSvc{hooks: nil}
	e := newEngine(f, route.Kit{})
	w := do(e, http.MethodGet, "/v1/plugin-hooks", "")
	got := decMap(t, w)
	hooks, _ := got["hooks"].([]any)
	if hooks == nil {
		t.Fatal("hooks null")
	}
	if len(hooks) != 0 {
		t.Fatalf("hooks %v", hooks)
	}
}

func TestSaveGeneratedSecret(t *testing.T) {
	f := &fakeSvc{saveSecret: "whsec_x"}
	e := newEngine(f, route.Kit{})
	body := `{"code":"myhook","name":"n","description":"d","enabled":true,"events":["order.created"],"endpoint_url":"https://example.com","secret":"","timeout_ms":5000,"max_attempts":5}`
	w := do(e, http.MethodPost, "/v1/plugin-hooks", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	got := decMap(t, w)
	if got["saved"] != true || got["secret"] != "whsec_x" {
		t.Fatalf("%v", got)
	}
	if got["secret_hint"] != "签名密钥只显示这一次，请立刻填进插件那边的配置" {
		t.Fatalf("hint %v", got["secret_hint"])
	}
	if f.lastSave.Code != "myhook" || f.lastSave.Name != "n" || !f.lastSave.Enabled || f.lastSave.EndpointURL != "https://example.com" {
		t.Fatalf("SaveHookInput %+v", f.lastSave)
	}
}

func TestSaveNoSecret(t *testing.T) {
	f := &fakeSvc{saveSecret: ""}
	e := newEngine(f, route.Kit{})
	w := do(e, http.MethodPost, "/v1/plugin-hooks", `{"code":"x","name":"y","description":"","enabled":false,"events":[],"endpoint_url":"https://example.test","secret":"","timeout_ms":0,"max_attempts":0}`)
	got := decMap(t, w)
	if len(got) != 1 || got["saved"] != true {
		t.Fatalf("%v", got)
	}
}

func TestSaveValidationError(t *testing.T) {
	f := &fakeSvc{saveErr: errs.Invalid(map[string]string{"code": "插件标识必填，小写字母开头"})}
	e := newEngine(f, route.Kit{})
	w := do(e, http.MethodPost, "/v1/plugin-hooks", `{"code":"","name":"n","description":"","enabled":true,"events":[],"endpoint_url":"https://example.com","secret":"","timeout_ms":5000,"max_attempts":5}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", w.Code)
	}
	if errFields(t, w)["code"] == nil {
		t.Fatal("fields code")
	}
}

func TestDeleteShape(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, route.Kit{})
	w := do(e, http.MethodDelete, "/v1/plugin-hooks/the-code", "")
	got := decMap(t, w)
	if got["deleted"] != true {
		t.Fatalf("%v", got)
	}
	if f.deleteCode != "the-code" {
		t.Fatalf("code %q", f.deleteCode)
	}
}

func TestDeliveriesShape(t *testing.T) {
	f := &fakeSvc{deliveries: []service.Delivery{
		{Event: "e1", Status: "failed", Attempts: 2, ResponseCode: 0, ErrorMessage: "x", CreatedAt: "2026-10-01 00:00:00", SentAt: "", DurationMS: nil},
		{Event: "e2", Status: "sent", Attempts: 1, ResponseCode: 201, ErrorMessage: "", CreatedAt: "2026-10-02 00:00:00", SentAt: "2026-10-02 00:00:01", DurationMS: intPtr(9)},
	}}
	e := newEngine(f, route.Kit{})
	w := do(e, http.MethodGet, "/v1/plugin-hooks/c/deliveries", "")
	got := decMap(t, w)
	del, _ := got["deliveries"].([]any)
	if len(del) != 2 {
		t.Fatalf("len %d", len(del))
	}
	d0, _ := del[0].(map[string]any)
	if d0["duration_ms"] != nil {
		t.Fatalf("duration_ms %v", d0["duration_ms"])
	}
	d1, _ := del[1].(map[string]any)
	if _, ok := d1["duration_ms"].(float64); !ok {
		t.Fatalf("duration_ms %v", d1["duration_ms"])
	}
	if f.deliverLim != 50 || f.deliverCode != "c" {
		t.Fatalf("deliver %q lim %d", f.deliverCode, f.deliverLim)
	}

	f.deliveries = []service.Delivery{}
	w = do(e, http.MethodGet, "/v1/plugin-hooks/c/deliveries", "")
	got = decMap(t, w)
	del, _ = got["deliveries"].([]any)
	if del == nil || len(del) != 0 {
		t.Fatalf("empty deliveries %v", got["deliveries"])
	}
}

func intPtr(n int) *int { return &n }

func TestTestShape(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, route.Kit{})
	w := do(e, http.MethodPost, "/v1/plugin-hooks/x/test", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	got := decMap(t, w)
	if got["duration_ms"] != float64(12) || got["response_code"] != float64(200) || got["sent"] != true {
		t.Fatalf("%v", got)
	}

	f.testErr = errs.Newf(errs.ValidationFailed, "对方返回 HTTP 500，不是 2xx")
	w = do(e, http.MethodPost, "/v1/plugin-hooks/x/test", "")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", w.Code)
	}
}

func TestWriteRoutesNeedReauth(t *testing.T) {
	pass := func(c *gin.Context) { c.Next() }
	reject := func(c *gin.Context) { c.AbortWithStatus(http.StatusForbidden) }
	k := route.Kit{RequireAuth: pass, RecentReauth: reject, Idempotency: func(string) gin.HandlerFunc { return pass }}
	f := &fakeSvc{hooks: []service.Hook{}, deliveries: []service.Delivery{}}
	e := newEngine(f, k)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/v1/plugin-hooks", http.StatusForbidden},
		{http.MethodDelete, "/v1/plugin-hooks/x", http.StatusForbidden},
		{http.MethodPost, "/v1/plugin-hooks/x/test", http.StatusForbidden},
		{http.MethodGet, "/v1/plugin-hooks", http.StatusOK},
		{http.MethodGet, "/v1/plugin-hooks/x/deliveries", http.StatusOK},
	} {
		w := do(e, tc.method, tc.path, "{}")
		if w.Code != tc.want {
			t.Fatalf("%s %s got %d want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}

func TestRoutesNeedAuth(t *testing.T) {
	reject := func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }
	pass := func(c *gin.Context) { c.Next() }
	k := route.Kit{RequireAuth: reject, RecentReauth: pass, Idempotency: func(string) gin.HandlerFunc { return pass }}
	f := &fakeSvc{}
	e := newEngine(f, k)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/plugin-hooks"},
		{http.MethodPost, "/v1/plugin-hooks"},
		{http.MethodDelete, "/v1/plugin-hooks/x"},
		{http.MethodGet, "/v1/plugin-hooks/x/deliveries"},
		{http.MethodPost, "/v1/plugin-hooks/x/test"},
	} {
		w := do(e, tc.method, tc.path, "{}")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s got %d", tc.method, tc.path, w.Code)
		}
	}
}
