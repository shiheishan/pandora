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

func portalCSP(t *testing.T, r captchaProviderReader) string {
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

// 关人机验证（或读设置失败）时门户入口页 CSP 只比不挂选项时多一个 frame-src 'self'（订阅域名探测页）；
// 开了只再多出所选那家的来源。入口页 CSP 不含任何订阅域名：connect-src 只有 'self'。
func TestPortalCaptchaCSP(t *testing.T) {
	base := portalCSP(t, fakeSignup{})
	plainRec := httptest.NewRecorder()
	webapp.Handler(fstest.MapFS{"index.html": {Data: []byte(`<!doctype html>`)}}).
		ServeHTTP(plainRec, httptest.NewRequest(http.MethodGet, "/", nil))
	plain := plainRec.Header().Get("Content-Security-Policy")
	if want := strings.Replace(plain, "script-src 'self'; ", "script-src 'self'; frame-src 'self'; ", 1); base != want {
		t.Fatalf("captcha off CSP:\n got %q\nwant %q", base, want)
	}
	if !strings.Contains(base, "connect-src 'self';") || strings.Contains(base, "https://") {
		t.Fatalf("入口页 CSP 不应放行任何外部来源（含订阅域名）: %q", base)
	}
	if got := portalCSP(t, fakeSignup{err: errors.New("db down")}); got != base {
		t.Fatalf("settings error must not widen CSP: %q", got)
	}

	cloudflare := "https://challenges.cloudflare.com"
	for _, p := range []string{settingssvc.CaptchaRecaptchaV2, settingssvc.CaptchaRecaptchaV3} {
		got := portalCSP(t, fakeSignup{s: settingssvc.Signup{CaptchaProvider: p}})
		if !strings.Contains(got, "script-src 'self' https://www.google.com/recaptcha/ https://www.gstatic.com/recaptcha/;") {
			t.Fatalf("%s script-src: %q", p, got)
		}
		if !strings.Contains(got, "frame-src 'self' https://www.google.com/recaptcha/ https://recaptcha.google.com/recaptcha/;") {
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

	got := portalCSP(t, fakeSignup{s: settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile}})
	if !strings.Contains(got, "script-src 'self' "+cloudflare+";") || !strings.Contains(got, "frame-src 'self' "+cloudflare+";") {
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
