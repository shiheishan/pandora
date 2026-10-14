package service

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/shopspring/decimal"
)

func validProviderSettings() ProviderSettings {
	return ProviderSettings{
		DisplayName: "易支付",
		BaseURL:     "https://pay.example.test/",
		Methods:     []string{"wxpay", "alipay", "wxpay"},
		MerchantID:  "1001",
		Key:         "k",
	}
}

func TestNormalizeSettingsDefaults(t *testing.T) {
	got, fields := normalizeSettings(validProviderSettings(), false)
	if len(fields) != 0 {
		t.Fatalf("fields=%v", fields)
	}
	if got.baseURL != "https://pay.example.test" {
		t.Fatalf("baseURL=%q", got.baseURL)
	}
	if got.submitPath != defaultSubmitPath || got.apiPath != defaultAPIPath {
		t.Fatalf("paths submit=%q api=%q", got.submitPath, got.apiPath)
	}
	if len(got.methods) != 2 || got.methods[0] != "wxpay" || got.methods[1] != "alipay" {
		t.Fatalf("methods=%v", got.methods)
	}
	if got.defaultMethod != "wxpay" {
		t.Fatalf("defaultMethod=%q", got.defaultMethod)
	}
	if !got.minAmount.Equal(defaultMinAmount) || got.minAmountSet {
		t.Fatalf("minAmount=%s set=%v", got.minAmount, got.minAmountSet)
	}
}

func TestNormalizeSettingsRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ProviderSettings)
		allow  bool
		field  string
	}{
		{"qqpay method", func(s *ProviderSettings) { s.Methods = []string{"alipay", "qqpay"} }, false, "methods"},
		{"default outside methods", func(s *ProviderSettings) {
			s.Methods = []string{"alipay"}
			s.DefaultMethod = "wxpay"
		}, false, "default_method"},
		{"http without private", func(s *ProviderSettings) { s.BaseURL = "http://pay.example.test" }, false, "base_url"},
		{"private host in production", func(s *ProviderSettings) {
			s.BaseURL = "http://127.0.0.1:8080"
			s.AllowPrivateHost = true
		}, false, "allow_private_host"},
		{"min_amount too small", func(s *ProviderSettings) { s.MinAmount = decimal.RequireFromString("0.001") }, false, "min_amount"},
		{"min_amount too large", func(s *ProviderSettings) { s.MinAmount = decimal.RequireFromString("1000.01") }, false, "min_amount"},
		{"display_name nul", func(s *ProviderSettings) { s.DisplayName = "a\x00b" }, false, "display_name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validProviderSettings()
			tc.mutate(&in)
			_, fields := normalizeSettings(in, tc.allow)
			if fields[tc.field] == "" {
				t.Fatalf("fields=%v, want error on %s", fields, tc.field)
			}
		})
	}
	in := validProviderSettings()
	in.BaseURL, in.AllowPrivateHost = "http://127.0.0.1:8080", true
	if _, fields := normalizeSettings(in, true); len(fields) != 0 {
		t.Fatalf("dev private host fields=%v", fields)
	}
}

func wantInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.ValidationFailed || e.Fields[field] == "" {
		t.Fatalf("err=%v, want validation on %s", err, field)
	}
}

func TestResolveMethod(t *testing.T) {
	configured := providerRecord{
		Adapter:       AdapterEpay,
		Methods:       []string{"alipay", "wxpay"},
		DefaultMethod: "wxpay",
	}
	legacy := providerRecord{Adapter: AdapterEpay, DefaultMethod: "alipay"}
	cases := []struct {
		name      string
		p         providerRecord
		requested string
		want      string
		reject    bool
	}{
		{"chosen in methods", configured, "alipay", "alipay", false},
		{"empty takes default", configured, "", "wxpay", false},
		{"outside methods", configured, "qqpay", "", true},
		{"default outside falls back first", providerRecord{
			Adapter: AdapterEpay, Methods: []string{"wxpay"}, DefaultMethod: "alipay",
		}, "", "wxpay", false},
		{"stored qqpay dropped", providerRecord{
			Adapter: AdapterEpay, Methods: []string{"qqpay", "wxpay"}, DefaultMethod: "qqpay",
		}, "", "wxpay", false},
		{"legacy default only", legacy, "alipay", "alipay", false},
		{"legacy rejects other", legacy, "wxpay", "", true},
		{"unconfigured empty", providerRecord{Adapter: AdapterDemo}, "", "", false},
		{"unconfigured rejects named", providerRecord{Adapter: AdapterDemo}, "alipay", "", true},
		{"epay no methods", providerRecord{Adapter: AdapterEpay}, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMethod(tc.p, tc.requested)
			if tc.reject {
				wantInvalidField(t, err, "method")
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestProviderMethods(t *testing.T) {
	fromMethods := providerRecord{Methods: []string{"wxpay", "alipay", "qqpay"}}
	got := providerMethods(fromMethods)
	if len(got) != 2 || got[0] != "wxpay" || got[1] != "alipay" {
		t.Fatalf("got=%v", got)
	}
	fromDefault := providerRecord{DefaultMethod: "alipay", Methods: nil}
	got = providerMethods(fromDefault)
	if len(got) != 1 || got[0] != "alipay" {
		t.Fatalf("got=%v", got)
	}
	empty := providerMethods(providerRecord{})
	if len(empty) != 0 {
		t.Fatalf("got=%v", empty)
	}
}
