package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
)

type fakeSettings struct {
	site       service.Site
	policy     service.Registration
	mailOK     bool
	switches   []service.Switch
	enabled    map[string]bool
	enabledErr error

	setSiteVal      service.Site
	setSwitchCode   string
	setSwitchEn     bool
	setSwitchReason string
	setSiteErr      error

	supportRules service.SupportRules
}

func (f *fakeSettings) Site(ctx context.Context) (service.Site, error) {
	return f.site, nil
}

func (f *fakeSettings) SetSite(ctx context.Context, u service.SiteUpdate) error {
	if f.setSiteErr != nil {
		return f.setSiteErr
	}
	v := f.site
	if u.Name != nil {
		v.Name = *u.Name
	}
	if u.Timezone != nil {
		v.Timezone = *u.Timezone
	}
	f.setSiteVal = v
	f.site = v
	return nil
}

func (f *fakeSettings) RegistrationPolicy(ctx context.Context) (service.Registration, error) {
	return f.policy, nil
}

func (f *fakeSettings) MailConfigured(ctx context.Context) (bool, error) {
	return f.mailOK, nil
}

func (f *fakeSettings) Switches(ctx context.Context) ([]service.Switch, error) {
	return f.switches, nil
}

func (f *fakeSettings) SetSwitch(ctx context.Context, code string, enabled bool, reason string) error {
	f.setSwitchCode = code
	f.setSwitchEn = enabled
	f.setSwitchReason = reason
	return nil
}

func (f *fakeSettings) SwitchEnabled(ctx context.Context, code string) (bool, error) {
	if f.enabledErr != nil {
		return false, f.enabledErr
	}
	if f.enabled == nil {
		return true, nil
	}
	v, ok := f.enabled[code]
	if !ok {
		return true, nil
	}
	return v, nil
}

func (f *fakeSettings) SupportRules(ctx context.Context) (service.SupportRules, error) {
	return f.supportRules, nil
}

func (f *fakeSettings) SetSupportRules(ctx context.Context, v service.SupportRules) error {
	f.supportRules = v
	return nil
}

func (f *fakeSettings) SignupWithoutSecret(ctx context.Context) (service.Signup, error) {
	return service.Signup{EmailSuffixes: []string{}}, nil
}

func (f *fakeSettings) SetSignup(ctx context.Context, u service.SignupUpdate) error {
	return nil
}

func testKit(h *Handler) route.Kit {
	return route.Kit{
		// 后台不分权限：带 X-Test-Auth: 1 就当已登录的后台主体
		RequireAuth: func(c *gin.Context) {
			if c.GetHeader("X-Test-Auth") != "1" {
				httpx.Fail(c, errs.New(errs.Unauthorized))
				return
			}
			c.Next()
		},
		RecentReauth: func(c *gin.Context) {
			if c.GetHeader("X-Test-Reauth") != "1" {
				httpx.Fail(c, errs.New(errs.ReauthRequired))
				return
			}
			c.Next()
		},
		Switch: h.Switch,
	}
}

func do(e http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error.Code
}

func TestSiteConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeSettings{
		policy: service.Registration{
			Mode:              service.RegistrationOpen,
			EmailVerification: true,
		},
		mailOK: true,
	}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterPublic(e.Group("/v1"), testKit(h))

	rec := do(e, http.MethodGet, "/v1/site-config", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Fatalf("keys %d: %v", len(m), m)
	}
	if m["registration_mode"] != service.RegistrationOpen {
		t.Fatalf("registration_mode %v", m["registration_mode"])
	}
	if m["email_verification"] != true {
		t.Fatalf("email_verification %v", m["email_verification"])
	}
	if m["password_reset"] != true {
		t.Fatalf("password_reset %v", m["password_reset"])
	}
}

func TestGetSiteRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeSettings{site: service.Site{Name: "N", Timezone: "Asia/Shanghai"}}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))

	rec := do(e, http.MethodGet, "/v1/settings/site", "", nil)
	if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthorized" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}

	rec = do(e, http.MethodGet, "/v1/settings/site", "", map[string]string{"X-Test-Auth": "1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var sr siteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &sr); err != nil {
		t.Fatal(err)
	}
	if sr.Timezone != "Asia/Shanghai" || sr.Name != "N" {
		t.Fatalf("site %+v", sr)
	}
}

func TestSetSite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeSettings{
		site: service.Site{Name: "Keep", Timezone: "UTC"},
	}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))
	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}

	rec := do(e, http.MethodPost, "/v1/settings/site", `{"timezone":"Asia/Tokyo"}`, map[string]string{"X-Test-Auth": "1"})
	if rec.Code != http.StatusForbidden || errCode(t, rec) != "reauth_required" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}

	rec = do(e, http.MethodPost, "/v1/settings/site", `{"timezone":"Asia/Tokyo"}`, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fake.setSiteVal.Name != "Keep" || fake.setSiteVal.Timezone != "Asia/Tokyo" {
		t.Fatalf("setSite %+v", fake.setSiteVal)
	}
	var sr siteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &sr); err != nil || sr != (siteResponse{Name: "Keep", Timezone: "Asia/Tokyo"}) {
		t.Fatalf("set site response %s", rec.Body.String())
	}

	// 只改站点名：时区保持
	rec = do(e, http.MethodPost, "/v1/settings/site", `{"name":"夜海加速"}`, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fake.setSiteVal.Name != "夜海加速" || fake.setSiteVal.Timezone != "Asia/Tokyo" {
		t.Fatalf("setSite name only %+v", fake.setSiteVal)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sr); err != nil || sr != (siteResponse{Name: "夜海加速", Timezone: "Asia/Tokyo"}) {
		t.Fatalf("set name response %s", rec.Body.String())
	}

	rec = do(e, http.MethodPost, "/v1/settings/site", `{"timezone":"x","extra":1}`, hdrs)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "bad_request" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}
}

func TestListSwitchesJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeSettings{
		switches: []service.Switch{
			{Code: "a", Enabled: true, Essential: false, Reason: nil},
		},
	}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))

	rec := do(e, http.MethodGet, "/v1/switches", "", map[string]string{"X-Test-Auth": "1"})
	if !strings.Contains(rec.Body.String(), `"reason":null`) {
		t.Fatalf("body %s", rec.Body.String())
	}

	fake.switches = nil
	rec = do(e, http.MethodGet, "/v1/switches", "", map[string]string{"X-Test-Auth": "1"})
	if !strings.Contains(rec.Body.String(), `"switches":[]`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestSetSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeSettings{}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))
	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}
	rec := do(e, http.MethodPost, "/v1/switches/billing.checkout", `{"enabled":false,"reason":"维护"}`, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out setSwitchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Enabled {
		t.Fatalf("resp %+v", out)
	}
	if fake.setSwitchCode != service.SwitchBillingCheckout || fake.setSwitchEn || fake.setSwitchReason != "维护" {
		t.Fatalf("setSwitch %q %v %q", fake.setSwitchCode, fake.setSwitchEn, fake.setSwitchReason)
	}
}

func TestSwitchMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeSettings{enabled: map[string]bool{service.SwitchBillingCheckout: false}}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	okHandler := func(c *gin.Context) { httpx.OK(c, gin.H{"ok": true}) }
	e.POST("/v1/orders", h.Switch(service.SwitchBillingCheckout), okHandler)

	rec := do(e, http.MethodPost, "/v1/orders", "", nil)
	if rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "service_unavailable" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}
	var errBody struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
	if errBody.Error.Message != "下单与支付暂停中，请稍后再试" {
		t.Fatalf("message %q", errBody.Error.Message)
	}

	fake.enabledErr = context.Canceled
	rec = do(e, http.MethodPost, "/v1/orders", "", nil)
	if rec.Code != http.StatusInternalServerError || errCode(t, rec) != "internal_error" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}

	fake.enabledErr = nil
	fake.enabled[service.SwitchBillingCheckout] = true
	rec = do(e, http.MethodPost, "/v1/orders", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestAdminWritesGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeSettings{
		site:    service.Site{Timezone: "UTC"},
		enabled: map[string]bool{service.SwitchAdminWrites: false},
	}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors(), h.AdminWritesGate())
	h.RegisterAdmin(e.Group("/admin-x/v1"), testKit(h))
	e.POST("/admin-x/v1/auth/login", func(c *gin.Context) { httpx.OK(c, gin.H{}) })
	e.POST("/admin-x/v1/auth/staff", func(c *gin.Context) { httpx.OK(c, gin.H{}) })

	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}
	rec := do(e, http.MethodPost, "/admin-x/v1/settings/site", `{"timezone":"X"}`, hdrs)
	if rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "service_unavailable" {
		t.Fatalf("post site status %d", rec.Code)
	}
	var errBody struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
	if errBody.Error.Message != "管理端只读模式" {
		t.Fatalf("message %q", errBody.Error.Message)
	}

	rec = do(e, http.MethodGet, "/admin-x/v1/settings/site", "", map[string]string{"X-Test-Auth": "1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("get site status %d", rec.Code)
	}

	rec = do(e, http.MethodPost, "/admin-x/v1/switches/admin.writes", `{"enabled":true,"reason":""}`, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("post switch status %d body %s", rec.Code, rec.Body.String())
	}

	rec = do(e, http.MethodPost, "/admin-x/v1/auth/login", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth login status %d", rec.Code)
	}

	// /auth/ 下没登记的写接口照样受只读模式约束
	rec = do(e, http.MethodPost, "/admin-x/v1/auth/staff", "", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("auth staff status %d", rec.Code)
	}

	fake.enabled[service.SwitchAdminWrites] = true
	rec = do(e, http.MethodPost, "/admin-x/v1/settings/site", `{"timezone":"Y"}`, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("post site when writes on status %d", rec.Code)
	}
}

func TestSupportRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeSettings{}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))
	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}

	rec := do(e, http.MethodPost, "/v1/settings/support", `{"wait_for_reply":true,"auto_close_enabled":true,"auto_close_hours":48,"max_open_tickets":8}`, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("post status %d body %s", rec.Code, rec.Body.String())
	}

	rec = do(e, http.MethodGet, "/v1/settings/support", "", map[string]string{"X-Test-Auth": "1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("get status %d", rec.Code)
	}
	var out supportRulesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.WaitForReply || !out.AutoClose || out.AutoCloseHours != 48 || out.MaxOpenTickets != 8 {
		t.Fatalf("rules %+v", out)
	}

	rec = do(e, http.MethodPost, "/v1/settings/support", `{"wait_for_reply":true}`, hdrs)
	if rec.Code != http.StatusUnprocessableEntity || errCode(t, rec) != "validation_failed" {
		t.Fatalf("status %d code %s body %s", rec.Code, errCode(t, rec), rec.Body.String())
	}
}
