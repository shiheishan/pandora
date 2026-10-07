package billing

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func validProviderSettings() ProviderSettings {
	return ProviderSettings{
		DisplayName: "易支付", BaseURL: "https://pay.example.test/",
		Methods: []string{"wxpay", "alipay", "wxpay"}, MerchantID: "1001", Key: "k",
	}
}

func TestNormalizeProviderSettingsDefaultsAndDedupes(t *testing.T) {
	got, fields := normalizeProviderSettings(validProviderSettings(), false)
	if len(fields) != 0 {
		t.Fatalf("fields=%v", fields)
	}
	want := map[string]any{
		"base_url": "https://pay.example.test", "submit_path": "/submit.php", "api_path": "/api.php",
		"methods": []string{"wxpay", "alipay"}, "default_method": "wxpay", "allow_private_host": false,
	}
	if !reflect.DeepEqual(got.config, want) {
		t.Fatalf("config=%v, want %v", got.config, want)
	}
}

func TestNormalizeProviderSettingsRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ProviderSettings)
		devMode bool
		field   string
	}{
		{"empty name", func(s *ProviderSettings) { s.DisplayName = " " }, false, "display_name"},
		{"http in production", func(s *ProviderSettings) { s.BaseURL = "http://pay.example.test" }, false, "base_url"},
		{"query in base url", func(s *ProviderSettings) { s.BaseURL = "https://pay.example.test/?a=1" }, false, "base_url"},
		{"userinfo in base url", func(s *ProviderSettings) { s.BaseURL = "https://u:p@pay.example.test" }, false, "base_url"},
		{"private host in production", func(s *ProviderSettings) { s.AllowPrivateHost = true }, false, "allow_private_host"},
		{"unknown method", func(s *ProviderSettings) { s.Methods = []string{"alipay", "paypal"} }, false, "methods"},
		{"qq wallet is no longer offered", func(s *ProviderSettings) { s.Methods = []string{"alipay", "qqpay"} }, false, "methods"},
		{"no method", func(s *ProviderSettings) { s.Methods = nil }, false, "methods"},
		{"default outside methods", func(s *ProviderSettings) { s.Methods = []string{"alipay"}; s.DefaultMethod = "wxpay" }, false, "default_method"},
		{"bad submit path", func(s *ProviderSettings) { s.SubmitPath = "submit.php?x" }, false, "submit_path"},
		{"protocol-relative api path", func(s *ProviderSettings) { s.APIPath = "//evil.example.test/api.php" }, false, "api_path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validProviderSettings()
			tc.mutate(&in)
			_, fields := normalizeProviderSettings(in, tc.devMode)
			if fields[tc.field] == "" {
				t.Fatalf("fields=%v, want an error on %s", fields, tc.field)
			}
		})
	}
	// 开发环境允许内网 http 地址
	in := validProviderSettings()
	in.BaseURL, in.AllowPrivateHost = "http://127.0.0.1:8080", true
	if _, fields := normalizeProviderSettings(in, true); len(fields) != 0 {
		t.Fatalf("dev private host fields=%v", fields)
	}
}

func wantFieldError(t *testing.T, err error, field string) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields[field] == "" {
		t.Fatalf("err=%v, want a validation error on %s", err, field)
	}
}

// 校验都在开事务之前：pool 为空，走到数据库就会 panic
func TestCreateProviderValidatesBeforeTouchingTheDatabase(t *testing.T) {
	s := NewPaymentService(nil, nil, nil, nil, "https://panel.example.test", false)
	ctx := context.Background()
	actor := ProviderActor{Kind: "admin", ID: "71000000-0000-7000-8000-000000000011"}
	base := CreateProviderInput{Code: "epay2", Adapter: "epay", ProviderSettings: validProviderSettings()}

	in := base
	in.Code = OfflineProviderCode
	_, err := s.CreateProvider(ctx, "t", actor, in)
	wantFieldError(t, err, "code")

	in = base
	in.Code = "Bad Code"
	_, err = s.CreateProvider(ctx, "t", actor, in)
	wantFieldError(t, err, "code")

	in = base
	in.Adapter = "demo_hmac"
	_, err = s.CreateProvider(ctx, "t", actor, in)
	wantFieldError(t, err, "adapter")

	in = base
	in.Key = ""
	_, err = s.CreateProvider(ctx, "t", actor, in)
	wantFieldError(t, err, "key")

	// 试构造：生产环境下指向回环地址的渠道被适配器拒绝（与运行时同一个构造函数）
	in = base
	in.BaseURL = "https://127.0.0.1"
	_, err = s.CreateProvider(ctx, "t", actor, in)
	wantFieldError(t, err, "base_url")
	var he *httpx.Error
	if errors.As(err, &he) && !strings.Contains(he.Fields["base_url"], "非公网") {
		t.Fatalf("trial build message=%q", he.Fields["base_url"])
	}
}

func TestResolvePaymentMethod(t *testing.T) {
	configured := map[string]any{"methods": []any{"alipay", "wxpay"}, "default_method": "wxpay"}
	legacy := map[string]any{"default_method": "alipay"}
	cases := []struct {
		name      string
		cfg       map[string]any
		requested string
		want      string
		reject    bool
	}{
		{"chosen in methods", configured, "alipay", "alipay", false},
		{"empty takes default", configured, "", "wxpay", false},
		{"outside methods", configured, "qqpay", "", true},
		{"default outside methods falls back to first", map[string]any{"methods": []any{"wxpay"}, "default_method": "alipay"}, "", "wxpay", false},
		{"stored qqpay is dropped", map[string]any{"methods": []any{"qqpay", "wxpay"}, "default_method": "qqpay"}, "", "wxpay", false},
		{"stored qqpay cannot be chosen", map[string]any{"methods": []any{"qqpay", "wxpay"}}, "qqpay", "", true},
		{"legacy qqpay default leaves nothing named", map[string]any{"default_method": "qqpay"}, "", "", false},
		{"legacy default only", legacy, "alipay", "alipay", false},
		{"legacy rejects other", legacy, "wxpay", "", true},
		{"unconfigured accepts empty", map[string]any{}, "", "", false},
		{"unconfigured rejects named", nil, "alipay", "", true},
		{"empty methods array uses default", map[string]any{"methods": []any{}, "default_method": "alipay"}, "", "alipay", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePaymentMethod(tc.cfg, tc.requested)
			if tc.reject {
				wantFieldError(t, err, "method")
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}
