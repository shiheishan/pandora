package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/webapp"
)

type fakeSignup struct {
	s   settingssvc.Signup
	err error
}

func (f fakeSignup) Signup(context.Context) (settingssvc.Signup, error) { return f.s, f.err }

func portalCSP(t *testing.T, r signupReader) string {
	t.Helper()
	h := webapp.Handler(fstest.MapFS{"index.html": {Data: []byte(`<!doctype html>`)}},
		webapp.WithExtraSources(portalCaptchaCSP(r)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	return rec.Header().Get("Content-Security-Policy")
}

// 关人机验证（或读设置失败）时门户入口页 CSP 与不挂选项时逐字一致；开了只多出所选那家的来源。
func TestPortalCaptchaCSP(t *testing.T) {
	base := portalCSP(t, fakeSignup{})
	plainRec := httptest.NewRecorder()
	webapp.Handler(fstest.MapFS{"index.html": {Data: []byte(`<!doctype html>`)}}).
		ServeHTTP(plainRec, httptest.NewRequest(http.MethodGet, "/", nil))
	if plain := plainRec.Header().Get("Content-Security-Policy"); base != plain {
		t.Fatalf("captcha off changed CSP:\n got %q\nwant %q", base, plain)
	}
	if got := portalCSP(t, fakeSignup{err: errors.New("db down")}); got != base {
		t.Fatalf("settings error must not widen CSP: %q", got)
	}

	google := []string{"https://www.google.com", "https://www.gstatic.com"}
	cloudflare := "https://challenges.cloudflare.com"
	for _, p := range []string{settingssvc.CaptchaRecaptchaV2, settingssvc.CaptchaRecaptchaV3} {
		got := portalCSP(t, fakeSignup{s: settingssvc.Signup{CaptchaProvider: p}})
		if !strings.Contains(got, "script-src 'self' "+strings.Join(google, " ")+";") {
			t.Fatalf("%s script-src: %q", p, got)
		}
		if !strings.Contains(got, "frame-src https://www.google.com;") {
			t.Fatalf("%s frame-src: %q", p, got)
		}
		if strings.Contains(got, cloudflare) {
			t.Fatalf("%s must not allow Turnstile: %q", p, got)
		}
	}

	got := portalCSP(t, fakeSignup{s: settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile}})
	if !strings.Contains(got, "script-src 'self' "+cloudflare+";") || !strings.Contains(got, "frame-src "+cloudflare+";") {
		t.Fatalf("turnstile CSP: %q", got)
	}
	if strings.Contains(got, "google.com") || strings.Contains(got, "gstatic.com") {
		t.Fatalf("turnstile must not allow reCAPTCHA: %q", got)
	}
}
