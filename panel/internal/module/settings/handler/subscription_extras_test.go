package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
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
	if u.LinkBaseURLs != nil {
		v.LinkBaseURLs = *u.LinkBaseURLs
	}
	f.extras = v
	return nil
}

func TestGetSubscriptionExtrasRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeExtras{extras: service.SubscriptionExtras{LinkBaseURLs: []string{}}}
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
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatal("invalid json")
	}
	if !containsJSONArray(rec.Body.String(), "link_base_urls") {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func containsJSONArray(body string, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return false
	}
	raw, ok := m[key]
	if !ok {
		return false
	}
	var arr []json.RawMessage
	return json.Unmarshal(raw, &arr) == nil
}

func TestSetSubscriptionExtras(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeExtras{
		extras: service.SubscriptionExtras{
			InfoNodes:      false,
			ProtocolPrefix: false,
			LinkBaseURLs:   []string{"https://a.example.com"},
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
	if len(resp.LinkBaseURLs) != 1 || resp.LinkBaseURLs[0] != "https://a.example.com" {
		t.Fatalf("link_base_urls %v", resp.LinkBaseURLs)
	}
}

func TestSetSubscriptionExtrasValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeExtras{
		setErr: errs.Invalid(map[string]string{"link_base_urls": "x"}),
	}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))
	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}

	rec := do(e, http.MethodPost, "/v1/settings/subscription", `{"link_base_urls":["http://a.example.com"]}`, hdrs)
	if rec.Code != http.StatusUnprocessableEntity || errCode(t, rec) != "validation_failed" {
		t.Fatalf("status %d code %s body %s", rec.Code, errCode(t, rec), rec.Body.String())
	}
}
