package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/webapp"
)

type fakeSignup struct {
	s   settingssvc.Signup
	err error
}

func (f fakeSignup) SignupCaptchaProvider(context.Context) (string, error) {
	return f.s.CaptchaProvider, f.err
}

func (fakeSignup) SignupGeneration() uint64 { return 0 }

func portalCSPHeader(t *testing.T, r captchaProviderReader) string {
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
	base := portalCSPHeader(t, fakeSignup{})
	plainRec := httptest.NewRecorder()
	webapp.Handler(fstest.MapFS{"index.html": {Data: []byte(`<!doctype html>`)}}).
		ServeHTTP(plainRec, httptest.NewRequest(http.MethodGet, "/", nil))
	if plain := plainRec.Header().Get("Content-Security-Policy"); base != plain {
		t.Fatalf("captcha off changed CSP:\n got %q\nwant %q", base, plain)
	}
	if got := portalCSPHeader(t, fakeSignup{err: errors.New("db down")}); got != base {
		t.Fatalf("settings error must not widen CSP: %q", got)
	}

	cloudflare := "https://challenges.cloudflare.com"
	for _, p := range []string{settingssvc.CaptchaRecaptchaV2, settingssvc.CaptchaRecaptchaV3} {
		got := portalCSPHeader(t, fakeSignup{s: settingssvc.Signup{CaptchaProvider: p}})
		if !strings.Contains(got, "script-src 'self' https://www.google.com/recaptcha/ https://www.gstatic.com/recaptcha/;") {
			t.Fatalf("%s script-src: %q", p, got)
		}
		if !strings.Contains(got, "frame-src https://www.google.com/recaptcha/ https://recaptcha.google.com/recaptcha/;") {
			t.Fatalf("%s frame-src: %q", p, got)
		}
		// 只放行 /recaptcha/ 路径，不放行整个域名（www.google.com 上有 JSONP 端点）
		for _, wide := range []string{"https://www.google.com ", "https://www.google.com;", "https://www.gstatic.com ", "https://www.gstatic.com;"} {
			if strings.Contains(got, wide) {
				t.Fatalf("%s must not allow the whole host %q: %q", p, wide, got)
			}
		}
		if strings.Contains(got, cloudflare) {
			t.Fatalf("%s must not allow Turnstile: %q", p, got)
		}
	}

	got := portalCSPHeader(t, fakeSignup{s: settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile}})
	if !strings.Contains(got, "script-src 'self' "+cloudflare+";") || !strings.Contains(got, "frame-src "+cloudflare+";") {
		t.Fatalf("turnstile CSP: %q", got)
	}
	if strings.Contains(got, "google.com") || strings.Contains(got, "gstatic.com") {
		t.Fatalf("turnstile must not allow reCAPTCHA: %q", got)
	}
}

// 入口页 CSP 读设置的缓存：成功缓存 30 秒、读失败缓存 5 秒；本进程保存了注册设置（gen 变了）立刻重读。
func TestPortalCSPProviderCache(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	reads, provider, fail := 0, settingssvc.CaptchaTurnstile, false
	var gen uint64
	c := &providerCache{
		read: func(context.Context) (string, error) {
			reads++
			if fail {
				return "", errors.New("db down")
			}
			return provider, nil
		},
		gen:    func() uint64 { return gen },
		now:    func() time.Time { return now },
		ttl:    portalCSPCacheTTL,
		errTTL: portalCSPErrorTTL,
	}
	f := portalCaptchaCSPWith(c)
	ctx := context.Background()
	if x := f(ctx); len(x.Script) == 0 || reads != 1 {
		t.Fatalf("first: %+v reads=%d", x, reads)
	}
	provider = ""
	now = now.Add(29 * time.Second)
	if x := f(ctx); len(x.Script) == 0 || reads != 1 {
		t.Fatalf("within ttl should use cache: %+v reads=%d", x, reads)
	}
	now = now.Add(2 * time.Second)
	if x := f(ctx); len(x.Script) != 0 || reads != 2 {
		t.Fatalf("after ttl should re-read: %+v reads=%d", x, reads)
	}

	// 保存了注册设置：缓存期内也立刻重读
	provider = settingssvc.CaptchaRecaptchaV3
	gen++
	if x := f(ctx); len(x.Script) == 0 || reads != 3 {
		t.Fatalf("after save should re-read at once: %+v reads=%d", x, reads)
	}

	// 读失败缓存 5 秒，期间不放行任何来源也不再读库；过期后重读
	fail = true
	gen++
	if x := f(ctx); len(x.Script) != 0 || reads != 4 {
		t.Fatalf("error: %+v reads=%d", x, reads)
	}
	now = now.Add(4 * time.Second)
	if x := f(ctx); len(x.Script) != 0 || reads != 4 {
		t.Fatalf("errors should be cached briefly: %+v reads=%d", x, reads)
	}
	fail = false
	now = now.Add(2 * time.Second)
	if x := f(ctx); len(x.Script) == 0 || reads != 5 {
		t.Fatalf("after error ttl should re-read: %+v reads=%d", x, reads)
	}
}

type fakeProbes []string

func (f fakeProbes) ProbeOrigins(context.Context) []string { return f }

// 订阅域名进门户入口页的 connect-src：没有时与原来逐字一致；不合形状的（带空格、分号、http）滤掉。
func TestPortalCSPProbeOrigins(t *testing.T) {
	render := func(p probeOriginReader) string {
		t.Helper()
		h := webapp.Handler(fstest.MapFS{"index.html": {Data: []byte(`<!doctype html>`)}},
			webapp.WithExtraSources(portalCSP(fakeSignup{}, p)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Header().Get("Content-Security-Policy")
	}
	base := portalCSPHeader(t, fakeSignup{})
	if got := render(fakeProbes(nil)); got != base {
		t.Fatalf("no sub domains changed CSP:\n got %q\nwant %q", got, base)
	}
	if got := render(nil); got != base {
		t.Fatalf("nil reader changed CSP: %q", got)
	}
	got := render(fakeProbes{"https://sub-a.example.test", "https://sub-b.example.test:8443", "http://plain.example.test", "https://x.example.test; script-src *", "https://Upper.example.test"})
	if !strings.Contains(got, "connect-src 'self' https://sub-a.example.test https://sub-b.example.test:8443;") {
		t.Fatalf("connect-src: %q", got)
	}
	for _, bad := range []string{"plain.example.test", "script-src *", "Upper"} {
		if strings.Contains(got, bad) {
			t.Fatalf("CSP must not contain %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, "script-src 'self';") {
		t.Fatalf("script-src must stay self-only: %q", got)
	}
}
