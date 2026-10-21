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

func (f *fakeSettings) SubscriptionFetchLimit(ctx context.Context) (int, error) {
	return 60, nil
}

type fakeExtras struct {
	fakeSettings
	extras     service.SubscriptionExtras
	fetchLimit int
	got        *service.SubscriptionExtrasUpdate
	setErr     error
}

func (f *fakeExtras) SubscriptionExtras(ctx context.Context) (service.SubscriptionExtras, error) {
	return f.extras, nil
}

func (f *fakeExtras) SubscriptionFetchLimit(ctx context.Context) (int, error) {
	return f.fetchLimit, nil
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
	if u.FetchLimitPerHour != nil {
		f.fetchLimit = *u.FetchLimitPerHour
	}
	f.extras = v
	return nil
}

func TestGetSubscriptionExtrasRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeExtras{extras: service.SubscriptionExtras{}, fetchLimit: 60}
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
	if _, ok := m["fetch_limit_per_hour"]; !ok {
		t.Fatalf("missing fetch_limit_per_hour: %s", rec.Body.String())
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
		fetchLimit: 60,
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
	if !resp.InfoNodes || !resp.ProtocolPrefix || resp.FetchLimitPerHour != 60 {
		t.Fatalf("resp %+v", resp)
	}
}

func TestSetSubscriptionExtrasFetchLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeExtras{fetchLimit: 60}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))
	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}

	rec := do(e, http.MethodPost, "/v1/settings/subscription", `{"fetch_limit_per_hour":120}`, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var resp subscriptionExtrasResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.FetchLimitPerHour != 120 {
		t.Fatalf("resp fetch %d", resp.FetchLimitPerHour)
	}
	if fake.got == nil || fake.got.FetchLimitPerHour == nil || *fake.got.FetchLimitPerHour != 120 {
		t.Fatalf("got %+v", fake.got)
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

// 只读模式下订阅附加设置被挡。
func TestAdminWritesGateSubscriptionExtras(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeExtras{}
	fake.site = service.Site{Timezone: "UTC"}
	fake.enabled = map[string]bool{service.SwitchAdminWrites: false}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors(), h.AdminWritesGate())
	h.RegisterAdmin(e.Group("/admin-x/v1"), testKit(h))
	hdrs := map[string]string{"X-Test-Auth": "1", "X-Test-Reauth": "1"}

	rec := do(e, http.MethodPost, "/admin-x/v1/settings/subscription", `{"info_nodes":true}`, hdrs)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("只读模式下保存订阅附加设置该被挡：status %d", rec.Code)
	}
}
