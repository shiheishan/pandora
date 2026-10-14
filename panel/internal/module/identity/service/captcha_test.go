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

func TestCaptchaVerify(t *testing.T) {
	ctx := context.Background()

	t.Run("没开直接过", func(t *testing.T) {
		v := newCaptchaVerifier()
		if err := v.verify(ctx, settingssvc.Signup{}, ""); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("token空", func(t *testing.T) {
		v := newCaptchaVerifier()
		cfg := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile, CaptchaSecret: "sec"}
		err := v.verify(ctx, cfg, "  ")
		if err == nil {
			t.Fatal("expected error")
		}
		assertFieldErr(t, err, "captcha", "人机验证未通过")
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
			_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
		}))
		defer srv.Close()
		v := &captchaVerifier{client: srv.Client(), recaptchaURL: srv.URL, turnstileURL: srv.URL}
		cfg := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile, CaptchaSecret: "test-secret"}
		if err := v.verify(ctx, cfg, "tok"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("success false", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]bool{"success": false})
		}))
		defer srv.Close()
		v := &captchaVerifier{client: srv.Client(), turnstileURL: srv.URL}
		cfg := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile, CaptchaSecret: "s"}
		err := v.verify(ctx, cfg, "tok")
		assertFieldErr(t, err, "captcha", "人机验证未通过")
	})

	t.Run("v3分数低于阈值", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "score": 0.3})
		}))
		defer srv.Close()
		v := &captchaVerifier{client: srv.Client(), recaptchaURL: srv.URL}
		cfg := settingssvc.Signup{
			CaptchaProvider: settingssvc.CaptchaRecaptchaV3, CaptchaSecret: "s",
			RecaptchaV3Threshold: 0.5,
		}
		err := v.verify(ctx, cfg, "tok")
		assertFieldErr(t, err, "captcha", "人机验证未通过")
	})

	t.Run("v3分数等于阈值", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "score": 0.5})
		}))
		defer srv.Close()
		v := &captchaVerifier{client: srv.Client(), recaptchaURL: srv.URL}
		cfg := settingssvc.Signup{
			CaptchaProvider: settingssvc.CaptchaRecaptchaV3, CaptchaSecret: "s",
			RecaptchaV3Threshold: 0.5,
		}
		if err := v.verify(ctx, cfg, "tok"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("服务端500", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		v := &captchaVerifier{client: srv.Client(), turnstileURL: srv.URL}
		cfg := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile, CaptchaSecret: "s"}
		err := v.verify(ctx, cfg, "tok")
		assertFieldErr(t, err, "captcha", "暂时连不上")
	})

	t.Run("连不上", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		srv.Close()
		v := &captchaVerifier{client: srv.Client(), turnstileURL: srv.URL}
		cfg := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile, CaptchaSecret: "s"}
		err := v.verify(ctx, cfg, "tok")
		assertFieldErr(t, err, "captcha", "暂时连不上")
	})

	t.Run("密钥空", func(t *testing.T) {
		v := newCaptchaVerifier()
		cfg := settingssvc.Signup{CaptchaProvider: settingssvc.CaptchaTurnstile, CaptchaSecret: ""}
		err := v.verify(ctx, cfg, "tok")
		assertFieldErr(t, err, "captcha", "暂时连不上")
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
