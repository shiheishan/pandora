package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

func (f *fakeSettings) SubscriptionExtras(ctx context.Context) (service.SubscriptionExtras, error) {
	return service.SubscriptionExtras{}, nil
}

func (f *fakeSettings) SetSubscriptionExtras(ctx context.Context, u service.SubscriptionExtrasUpdate) error {
	return nil
}

type fakeExtras struct {
	fakeSettings
	extras service.SubscriptionExtras
	got    *service.SubscriptionExtrasUpdate
	setErr error
}

func (f *fakeExtras) SubscriptionExtras(ctx context.Context) (service.SubscriptionExtras, error) {
	return f.extras, nil
}

func (f *fakeExtras) SetSubscriptionExtras(ctx context.Context, u service.SubscriptionExtrasUpdate) error {
	f.got = &u
	if f.setErr != nil {
		return f.setErr
	}
	v := f.extras
	if u.InfoNodes != nil {
		v.InfoNodes = *u.InfoNodes
	}
	if u.ProtocolPrefix != nil {
		v.ProtocolPrefix = *u.ProtocolPrefix
	}
	f.extras = v
	return nil
}

func TestGetSubscriptionExtrasRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeExtras{extras: service.SubscriptionExtras{}}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))

	rec := do(e, http.MethodGet, "/v1/settings/subscription", "", nil)
	if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthorized" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}

	rec = do(e, http.MethodGet, "/v1/settings/subscription", "", map[string]string{"X-Test-Auth": "1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("json: %v", err)
	}
	if _, ok := m["info_nodes"]; !ok {
		t.Fatalf("missing info_nodes: %s", rec.Body.String())
	}
	if _, ok := m["protocol_prefix"]; !ok {
		t.Fatalf("missing protocol_prefix: %s", rec.Body.String())
	}
	if _, ok := m["link_base_urls"]; ok {
		t.Fatalf("unexpected link_base_urls: %s", rec.Body.String())
	}
}

func TestSetSubscriptionExtras(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeExtras{
		extras: service.SubscriptionExtras{
			InfoNodes:      false,
			ProtocolPrefix: false,
		},
	}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))
	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}

	rec := do(e, http.MethodPost, "/v1/settings/subscription", `{"info_nodes":true,"protocol_prefix":true}`, map[string]string{"X-Test-Auth": "1"})
	if rec.Code != http.StatusForbidden || errCode(t, rec) != "reauth_required" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}

	rec = do(e, http.MethodPost, "/v1/settings/subscription", `{"info_nodes":true,"protocol_prefix":true}`, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fake.got == nil || fake.got.InfoNodes == nil || !*fake.got.InfoNodes || fake.got.ProtocolPrefix == nil || !*fake.got.ProtocolPrefix {
		t.Fatalf("got %+v", fake.got)
	}
	var resp subscriptionExtrasResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.InfoNodes || !resp.ProtocolPrefix {
		t.Fatalf("resp %+v", resp)
	}
}

func TestSetSubscriptionExtrasValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeExtras{}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))
	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}

	rec := do(e, http.MethodPost, "/v1/settings/subscription", `{"link_base_urls":["https://a.example.com"]}`, hdrs)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "bad_request" {
		t.Fatalf("status %d code %s body %s", rec.Code, errCode(t, rec), rec.Body.String())
	}
}

// 只读模式下订阅模板预览照旧放行（只渲染示例节点、不写库），保存与恢复缺省、附加设置仍被挡。
func TestAdminWritesGateSubscriptionTemplates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeExtras{}
	fake.site = service.Site{Timezone: "UTC"}
	fake.enabled = map[string]bool{service.SwitchAdminWrites: false}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors(), h.AdminWritesGate())
	h.RegisterAdmin(e.Group("/admin-x/v1"), testKit(h))
	ok := func(c *gin.Context) { httpx.OK(c, gin.H{}) }
	e.POST("/admin-x/v1/subscription-templates/:format/preview", ok)
	e.PUT("/admin-x/v1/subscription-templates/:format", ok)
	e.DELETE("/admin-x/v1/subscription-templates/:format", ok)
	hdrs := map[string]string{"X-Test-Auth": "1", "X-Test-Reauth": "1"}

	if rec := do(e, http.MethodPost, "/admin-x/v1/subscription-templates/clash/preview", `{"content":""}`, hdrs); rec.Code != http.StatusOK {
		t.Fatalf("只读模式下预览 status %d body %s", rec.Code, rec.Body.String())
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPut, "/admin-x/v1/subscription-templates/clash", `{"content":"x"}`},
		{http.MethodDelete, "/admin-x/v1/subscription-templates/clash", ""},
		{http.MethodPost, "/admin-x/v1/settings/subscription", `{"info_nodes":true}`},
	} {
		rec := do(e, c.method, c.path, c.body, hdrs)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("只读模式下 %s %s 该被挡：status %d", c.method, c.path, rec.Code)
		}
	}
}
