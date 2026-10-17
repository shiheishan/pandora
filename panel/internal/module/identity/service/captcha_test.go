package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// testCaptchaHost 是用例里的面板对外主机名（虚构）。
const testCaptchaHost = "panel.example.test"

// captchaServer 起一个假的服务商校验接口，回给定的 JSON。
func captchaServer(t *testing.T, resp map[string]any) (*captchaVerifier, func()) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(resp)
	}))
	v := &captchaVerifier{client: srv.Client(), recaptchaURL: srv.URL, turnstileURL: srv.URL, expectedHost: testCaptchaHost}
	return v, srv.Close
}

func TestCaptchaHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://Panel.Example.Test":       "panel.example.test",
		"https://panel.example.test:8443/": "panel.example.test",
		"http://127.0.0.1:9000":            "127.0.0.1",
		"https://panel.example.test./x":    "panel.example.test",
		"":                                 "",
		"::bad":                            "",
	} {
		if got := captchaHost(in); got != want {
			t.Errorf("captchaHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCaptchaVerify(t *testing.T) {
	ctx := context.Background()
	v3 := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaRecaptchaV3, CaptchaSecret: "s", RecaptchaV3Threshold: 0.5}
	ts := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile, CaptchaSecret: "s"}

	t.Run("没开直接过", func(t *testing.T) {
		v := newCaptchaVerifier("https://" + testCaptchaHost)
		if err := v.verify(ctx, settingssvc.Signup{}, "", captchaActionRegister); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("token空", func(t *testing.T) {
		v := newCaptchaVerifier("https://" + testCaptchaHost)
		assertFieldErr(t, v.verify(ctx, ts, "  ", captchaActionRegister), "captcha", "人机验证未通过")
	})

	t.Run("turnstile成功", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			vals, _ := url.ParseQuery(string(body))
			if vals.Get("secret") != "test-secret" || vals.Get("response") != "tok" {
				t.Errorf("form: %v", vals)
			}
			if vals.Get("remoteip") != "" {
				t.Error("unexpected remoteip")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "hostname": testCaptchaHost})
		}))
		defer srv.Close()
		v := &captchaVerifier{client: srv.Client(), recaptchaURL: srv.URL, turnstileURL: srv.URL, expectedHost: testCaptchaHost}
		cfg := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile, CaptchaSecret: "test-secret"}
		if err := v.verify(ctx, cfg, "tok", captchaActionRegister); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("success false", func(t *testing.T) {
		v, done := captchaServer(t, map[string]any{"success": false, "hostname": testCaptchaHost})
		defer done()
		assertFieldErr(t, v.verify(ctx, ts, "tok", captchaActionRegister), "captcha", "人机验证未通过")
	})

	t.Run("hostname不符", func(t *testing.T) {
		v, done := captchaServer(t, map[string]any{"success": true, "hostname": "evil.example.test"})
		defer done()
		assertFieldErr(t, v.verify(ctx, ts, "tok", captchaActionRegister), "captcha", "人机验证未通过")
	})

	t.Run("hostname缺失", func(t *testing.T) {
		v, done := captchaServer(t, map[string]any{"success": true})
		defer done()
		assertFieldErr(t, v.verify(ctx, ts, "tok", captchaActionRegister), "captcha", "人机验证未通过")
	})

	t.Run("hostname大小写与末尾点不影响", func(t *testing.T) {
		v, done := captchaServer(t, map[string]any{"success": true, "hostname": "Panel.Example.Test."})
		defer done()
		if err := v.verify(ctx, ts, "tok", captchaActionRegister); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("对外地址取不到主机名就不放行", func(t *testing.T) {
		v, done := captchaServer(t, map[string]any{"success": true, "hostname": ""})
		defer done()
		v.expectedHost = ""
		assertFieldErr(t, v.verify(ctx, ts, "tok", captchaActionRegister), "captcha", "暂时连不上")
	})

	t.Run("v3 action不符", func(t *testing.T) {
		v, done := captchaServer(t, map[string]any{"success": true, "score": 0.9, "hostname": testCaptchaHost, "action": captchaActionPasswordReset})
		defer done()
		assertFieldErr(t, v.verify(ctx, v3, "tok", captchaActionRegister), "captcha", "人机验证未通过")
	})

	t.Run("v3 action一致", func(t *testing.T) {
		for _, a := range []string{captchaActionRegister, captchaActionPasswordReset, captchaActionMailLogin} {
			v, done := captchaServer(t, map[string]any{"success": true, "score": 0.9, "hostname": testCaptchaHost, "action": a})
			if err := v.verify(ctx, v3, "tok", a); err != nil {
				done()
				t.Fatalf("%s: %v", a, err)
			}
			done()
		}
	})

	t.Run("v3分数低于阈值", func(t *testing.T) {
		v, done := captchaServer(t, map[string]any{"success": true, "score": 0.3, "hostname": testCaptchaHost, "action": captchaActionRegister})
		defer done()
		assertFieldErr(t, v.verify(ctx, v3, "tok", captchaActionRegister), "captcha", "人机验证未通过")
	})

	t.Run("v3分数等于阈值", func(t *testing.T) {
		v, done := captchaServer(t, map[string]any{"success": true, "score": 0.5, "hostname": testCaptchaHost, "action": captchaActionRegister})
		defer done()
		if err := v.verify(ctx, v3, "tok", captchaActionRegister); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("v2不核action", func(t *testing.T) {
		v, done := captchaServer(t, map[string]any{"success": true, "hostname": testCaptchaHost})
		defer done()
		v2 := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaRecaptchaV2, CaptchaSecret: "s"}
		if err := v.verify(ctx, v2, "tok", captchaActionRegister); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("服务端500", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		v := &captchaVerifier{client: srv.Client(), turnstileURL: srv.URL, expectedHost: testCaptchaHost}
		assertFieldErr(t, v.verify(ctx, ts, "tok", captchaActionRegister), "captcha", "暂时连不上")
	})

	t.Run("连不上", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		srv.Close()
		v := &captchaVerifier{client: srv.Client(), turnstileURL: srv.URL, expectedHost: testCaptchaHost}
		assertFieldErr(t, v.verify(ctx, ts, "tok", captchaActionRegister), "captcha", "暂时连不上")
	})

	t.Run("密钥空", func(t *testing.T) {
		v := newCaptchaVerifier("https://" + testCaptchaHost)
		cfg := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile, CaptchaSecret: ""}
		assertFieldErr(t, v.verify(ctx, cfg, "tok", captchaActionRegister), "captcha", "暂时连不上")
	})
}

func assertFieldErr(t *testing.T, err error, field, substr string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	e, ok := err.(*errs.Error)
	if !ok {
		t.Fatalf("want *errs.Error, got %T: %v", err, err)
	}
	if e.Code != errs.ValidationFailed {
		t.Fatalf("code %v", e.Code)
	}
	msg, ok := e.Fields[field]
	if !ok {
		t.Fatalf("fields: %v", e.Fields)
	}
	if !strings.Contains(msg, substr) {
		t.Fatalf("msg %q want contains %q", msg, substr)
	}
}
