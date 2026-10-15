package service

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestCreateBTCPayProvider(t *testing.T) {
	e, ctx := newEnv(t)
	code := "btc-" + uuid.New().String()[:8]
	feeFixed := decimal.RequireFromString("0.30")
	feePercent := decimal.RequireFromString("1.5")
	_, err := e.svc.CreateProvider(ctx, CreateProviderInput{
		Code:    code,
		Adapter: AdapterBTCPay,
		ProviderSettings: ProviderSettings{
			DisplayName:   "BTCPay 测试",
			BaseURL:       "https://btcpay.example.test",
			MerchantID:    "STORE1",
			Key:           "fake-api-key",
			WebhookSecret: "fake-hook-secret",
			FeeFixed:      &feeFixed,
			FeePercent:    &feePercent,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	views, err := e.svc.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := findProvider(t, views, code)
	if v.Adapter != AdapterBTCPay || !v.HasCredentials || !v.HasWebhookSecret {
		t.Fatalf("view %+v", v)
	}
	if !v.FeeFixed.Equal(feeFixed) || !v.FeePercent.Equal(feePercent) {
		t.Fatalf("fees fixed=%s percent=%s", v.FeeFixed, v.FeePercent)
	}
	wantNotify := "https://panel.example.test/v1/webhooks/payments/" + code
	if v.NotifyURL != wantNotify {
		t.Fatalf("NotifyURL=%q want %q", v.NotifyURL, wantNotify)
	}
	if len(v.Methods) != 0 {
		t.Fatalf("methods=%v", v.Methods)
	}
	if v.AcceptingNew {
		t.Fatalf("AcceptingNew should be false")
	}
}

func TestCreateBTCPayRequiresWebhookSecret(t *testing.T) {
	e, ctx := newEnv(t)
	code := "btc-" + uuid.New().String()[:8]
	_, err := e.svc.CreateProvider(ctx, CreateProviderInput{
		Code:    code,
		Adapter: AdapterBTCPay,
		ProviderSettings: ProviderSettings{
			DisplayName: "BTCPay",
			BaseURL:     "https://btcpay.example.test",
			MerchantID:  "STORE1",
			Key:         "fake-api-key",
		},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	e2 := errs.As(err)
	if e2.Code != errs.ValidationFailed || e2.Fields["webhook_secret"] == "" {
		t.Fatalf("err=%v", err)
	}
}

func TestNotifyURLUsesCallbackPrefix(t *testing.T) {
	e, ctx := newEnv(t)
	if err := e.settings.SetPaymentCallback(ctx, "https://pay-cb.example.test"); err != nil {
		t.Fatal(err)
	}
	code := "cp-" + uuid.New().String()[:8]
	_, err := e.svc.CreateProvider(ctx, CreateProviderInput{
		Code:    code,
		Adapter: AdapterCoinPayments,
		ProviderSettings: ProviderSettings{
			DisplayName: "CoinPayments",
			MerchantID:  "CLIENT1",
			Key:         "fake-secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	views, err := e.svc.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := findProvider(t, views, code)
	prefix := "https://pay-cb.example.test/v1/webhooks/payments/"
	if !strings.HasPrefix(v.NotifyURL, prefix) {
		t.Fatalf("NotifyURL=%q want prefix %q", v.NotifyURL, prefix)
	}
	if v.BaseURL != "https://a-api.coinpayments.net" {
		t.Fatalf("BaseURL=%q", v.BaseURL)
	}
}

func TestUpdateProviderKeepsFeeWhenNil(t *testing.T) {
	e, ctx := newEnv(t)
	code := "epay-" + uuid.New().String()[:8]
	feeFixed := decimal.RequireFromString("0.50")
	_, err := e.svc.CreateProvider(ctx, CreateProviderInput{
		Code:    code,
		Adapter: AdapterEpay,
		ProviderSettings: ProviderSettings{
			DisplayName: "易支付",
			BaseURL:     "https://pay.example.test",
			Methods:     []string{"alipay", "wxpay"},
			MerchantID:  "1001",
			Key:         "fake-key",
			FeeFixed:    &feeFixed,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	feePercent := decimal.RequireFromString("2")
	_, err = e.svc.UpdateProvider(ctx, code, ProviderSettings{
		DisplayName: "易支付",
		BaseURL:     "https://pay.example.test",
		Methods:     []string{"alipay", "wxpay"},
		FeePercent:  &feePercent,
	})
	if err != nil {
		t.Fatal(err)
	}
	views, err := e.svc.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := findProvider(t, views, code)
	wantFixed := decimal.RequireFromString("0.50")
	wantPercent := decimal.RequireFromString("2.00")
	if !v.FeeFixed.Equal(wantFixed) || !v.FeePercent.Equal(wantPercent) {
		t.Fatalf("fees fixed=%s percent=%s", v.FeeFixed, v.FeePercent)
	}
}
