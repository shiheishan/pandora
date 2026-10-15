package service

import (
	"errors"
	"strings"
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
	got, fields := normalizeSettings(validProviderSettings(), AdapterEpay, false)
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
			_, fields := normalizeSettings(in, AdapterEpay, tc.allow)
			if fields[tc.field] == "" {
				t.Fatalf("fields=%v, want error on %s", fields, tc.field)
			}
		})
	}
	in := validProviderSettings()
	in.BaseURL, in.AllowPrivateHost = "http://127.0.0.1:8080", true
	if _, fields := normalizeSettings(in, AdapterEpay, true); len(fields) != 0 {
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
		{"unconfigured empty", providerRecord{Adapter: AdapterOffline}, "", "", false},
		{"unconfigured rejects named", providerRecord{Adapter: AdapterOffline}, "alipay", "", true},
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

func TestNormalizeSettingsCrypto(t *testing.T) {
	btcpayIn := ProviderSettings{
		DisplayName:   "BTCPay",
		BaseURL:       "https://btcpay.example.test",
		SubmitPath:    "/x",
		Methods:       []string{"alipay"},
		MerchantID:    "STORE1",
		Key:           "fake-api-key",
		WebhookSecret: "fake-hook-secret",
	}
	got, fields := normalizeSettings(btcpayIn, AdapterBTCPay, false)
	if len(fields) != 0 {
		t.Fatalf("btcpay fields=%v", fields)
	}
	if got.submitPath != "" || got.apiPath != "" || len(got.methods) != 0 || got.defaultMethod != "" {
		t.Fatalf("btcpay paths/methods not cleared: submit=%q api=%q methods=%v default=%q",
			got.submitPath, got.apiPath, got.methods, got.defaultMethod)
	}

	btcpayHTTP := btcpayIn
	btcpayHTTP.BaseURL = "http://btcpay.example.test"
	_, fields = normalizeSettings(btcpayHTTP, AdapterBTCPay, false)
	if fields["base_url"] == "" {
		t.Fatalf("http without private fields=%v", fields)
	}

	cpIn := ProviderSettings{
		DisplayName: "CoinPayments",
		MerchantID:  "CLIENT1",
		Key:         "fake-secret",
	}
	got, fields = normalizeSettings(cpIn, AdapterCoinPayments, false)
	if len(fields) != 0 {
		t.Fatalf("coinpayments fields=%v", fields)
	}
	if got.baseURL != "https://a-api.coinpayments.net" {
		t.Fatalf("coinpayments default baseURL=%q", got.baseURL)
	}
}

func TestNormalizeSettingsFee(t *testing.T) {
	in := validProviderSettings()
	got, fields := normalizeSettings(in, AdapterEpay, false)
	if len(fields) != 0 {
		t.Fatalf("fields=%v", fields)
	}
	if got.feeFixedSet || got.feePercentSet {
		t.Fatalf("nil fees should not set: fixed=%v percent=%v", got.feeFixedSet, got.feePercentSet)
	}

	ff := decimal.RequireFromString("0.30")
	fp := decimal.RequireFromString("2.5")
	in.FeeFixed = &ff
	in.FeePercent = &fp
	got, fields = normalizeSettings(in, AdapterEpay, false)
	if len(fields) != 0 {
		t.Fatalf("valid fee fields=%v", fields)
	}
	if !got.feeFixedSet || !got.feePercentSet || !got.feeFixed.Equal(ff) || !got.feePercent.Equal(fp) {
		t.Fatalf("fee got fixed=%s/%v percent=%s/%v", got.feeFixed, got.feeFixedSet, got.feePercent, got.feePercentSet)
	}

	badFixed := []string{"1000.01", "-0.01", "0.001"}
	for _, s := range badFixed {
		in := validProviderSettings()
		d := decimal.RequireFromString(s)
		in.FeeFixed = &d
		_, fields := normalizeSettings(in, AdapterEpay, false)
		if fields["fee_fixed"] == "" {
			t.Fatalf("fee_fixed %s fields=%v", s, fields)
		}
	}
	badPercent := []string{"100.01", "0.001"}
	for _, s := range badPercent {
		in := validProviderSettings()
		d := decimal.RequireFromString(s)
		in.FeePercent = &d
		_, fields := normalizeSettings(in, AdapterEpay, false)
		if fields["fee_percent"] == "" {
			t.Fatalf("fee_percent %s fields=%v", s, fields)
		}
	}
}

func TestNormalizeSettingsWebhookSecretLimit(t *testing.T) {
	in := validProviderSettings()
	in.WebhookSecret = strings.Repeat("a", 257)
	_, fields := normalizeSettings(in, AdapterBTCPay, false)
	if fields["webhook_secret"] == "" {
		t.Fatalf("fields=%v", fields)
	}
}
