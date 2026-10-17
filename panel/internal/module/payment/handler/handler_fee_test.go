package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// settingsFake 在 fakeSvc 上实现 PaymentSettings / SetPaymentSettings。
type settingsFake struct {
	*fakeSvc
	cur    service.PaymentSettings
	setArg string
	setErr error
}

func (f *settingsFake) PaymentSettings(context.Context) (service.PaymentSettings, error) {
	return f.cur, nil
}

func (f *settingsFake) SetPaymentSettings(_ context.Context, notifyBaseURL string) error {
	f.setArg = notifyBaseURL
	if f.setErr != nil {
		return f.setErr
	}
	if notifyBaseURL != "" {
		f.cur.NotifyBaseURL = notifyBaseURL
		f.cur.EffectiveBase = notifyBaseURL
	}
	return nil
}

func TestPaymentMethodsAmountQuery(t *testing.T) {
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudiencePublic}

	f := &fakeSvc{}
	e := newPublicEngine(f, p)
	w := doJSON(e, http.MethodGet, "/v1/payment-methods?amount=1000", "")
	if w.Code != http.StatusOK {
		t.Fatalf("1000 status %d", w.Code)
	}
	if f.methodsAmount == nil || !f.methodsAmount.Equal(decimal.RequireFromString("10.00")) {
		t.Fatalf("amount 1000 want 10.00, got %v", f.methodsAmount)
	}

	f2 := &fakeSvc{}
	doJSON(newPublicEngine(f2, p), http.MethodGet, "/v1/payment-methods", "")
	if f2.methodsAmount != nil {
		t.Fatalf("no query want nil, got %v", f2.methodsAmount)
	}

	f3 := &fakeSvc{}
	doJSON(newPublicEngine(f3, p), http.MethodGet, "/v1/payment-methods?amount=0", "")
	if f3.methodsAmount != nil {
		t.Fatalf("amount=0 want nil, got %v", f3.methodsAmount)
	}

	for _, path := range []string{"/v1/payment-methods?amount=-1", "/v1/payment-methods?amount=abc", "/v1/payment-methods?amount=10000000001"} {
		w := doJSON(newPublicEngine(&fakeSvc{}, p), http.MethodGet, path, "")
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s status %d", path, w.Code)
		}
		env := parseErr(w.Body.Bytes())
		if env.Error.Code == "" {
			t.Fatalf("%s empty error code %s", path, w.Body)
		}
	}
}

func TestPaymentMethodsFeeShape(t *testing.T) {
	fee := decimal.RequireFromString("0.55")
	total := decimal.RequireFromString("10.55")
	f := &fakeSvc{methods: []service.PaymentMethod{
		{
			Provider: "p1", DisplayName: "P1", Method: "alipay",
			FeeFixed: decimal.RequireFromString("0.30"), FeePercent: decimal.RequireFromString("2.5"),
			Fee: &fee, Total: &total,
		},
		{
			Provider: "p2", DisplayName: "P2", Method: "",
			FeeFixed: decimal.Zero, FeePercent: decimal.Zero,
		},
	}}
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudiencePublic}
	w := doJSON(newPublicEngine(f, p), http.MethodGet, "/v1/payment-methods?amount=1000", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	raw := w.Body.String()
	if !strings.Contains(raw, `"fee_fixed":30`) || !strings.Contains(raw, `"fee_percent":"2.50"`) {
		t.Fatalf("fee fields %s", raw)
	}
	if !strings.Contains(raw, `"fee":55`) || !strings.Contains(raw, `"total":1055`) {
		t.Fatalf("fee/total %s", raw)
	}
	idx := strings.Index(raw, `"provider":"p2"`)
	if idx < 0 {
		t.Fatalf("p2 row missing %s", raw)
	}
	rest := raw[idx:]
	if strings.Contains(rest, `"fee":`) || strings.Contains(rest, `"total":`) {
		t.Fatalf("p2 should omit fee/total %s", rest)
	}
}

func TestCreateProviderFeeAndWebhookSecret(t *testing.T) {
	id := uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	f := &fakeSvc{createRes: service.ProviderWriteResult{ID: id, Code: "b1"}}
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	body := `{"code":"b1","adapter":"btcpay","display_name":"B","fee_fixed":30,"fee_percent":"1.5","webhook_secret":"fake-hook"}`
	w := doJSON(newAdminEngine(f, p, nil), http.MethodPost, "/v1/payment-providers", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	wantFixed := decimal.RequireFromString("0.30")
	if f.createIn.FeeFixed == nil || !f.createIn.FeeFixed.Equal(wantFixed) {
		t.Fatalf("fee_fixed %v", f.createIn.FeeFixed)
	}
	if f.createIn.FeePercent == nil || !f.createIn.FeePercent.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("fee_percent %v", f.createIn.FeePercent)
	}
	if f.createIn.WebhookSecret != "fake-hook" {
		t.Fatalf("webhook_secret %q", f.createIn.WebhookSecret)
	}

	w2 := doJSON(newAdminEngine(&fakeSvc{}, p, nil), http.MethodPost, "/v1/payment-providers",
		`{"code":"x","adapter":"btcpay","display_name":"n","fee_fixed":-1}`)
	if w2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("负 fee_fixed status %d", w2.Code)
	}
	w3 := doJSON(newAdminEngine(&fakeSvc{}, p, nil), http.MethodPost, "/v1/payment-providers",
		`{"code":"x","adapter":"btcpay","display_name":"n","fee_percent":"1e2"}`)
	if w3.Code != http.StatusUnprocessableEntity {
		t.Fatalf("1e2 status %d", w3.Code)
	}
}

func TestListProvidersFeeAndNotifyURL(t *testing.T) {
	pid := uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	notify := "https://panel.example.test/v1/webhooks/payments/b1"
	f := &fakeSvc{providers: []service.ProviderView{{
		ID: pid, Code: "b1", Adapter: "btcpay", DisplayName: "B",
		Enabled: true, MinAmount: decimal.RequireFromString("1.00"),
		FeeFixed: decimal.RequireFromString("0.30"), FeePercent: decimal.RequireFromString("1.5"),
		HasWebhookSecret: true, NotifyURL: notify,
	}}}
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	w := doJSON(newAdminEngine(f, p, nil), http.MethodGet, "/v1/payment-providers", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	raw := w.Body.String()
	for _, want := range []string{
		`"fee_fixed":30`, `"fee_percent":"1.50"`, `"has_webhook_secret":true`, notify,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %q in %s", want, raw)
		}
	}
}

func TestPaymentSettingsRoutes(t *testing.T) {
	sf := &settingsFake{
		fakeSvc: &fakeSvc{},
		cur: service.PaymentSettings{
			NotifyBaseURL: "",
			EffectiveBase: "https://site.example.test",
			PublicBaseURL: "https://site.example.test",
		},
	}
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	w := doJSON(newAdminEngine(sf, p, nil), http.MethodGet, "/v1/payment-settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET status %d %s", w.Code, w.Body)
	}
	raw := w.Body.String()
	for _, k := range []string{"notify_base_url", "effective_base", "public_base_url"} {
		if !strings.Contains(raw, k) {
			t.Fatalf("GET missing %s: %s", k, raw)
		}
	}

	var hooks kitHooks
	sf2 := &settingsFake{
		fakeSvc: &fakeSvc{},
		cur: service.PaymentSettings{
			EffectiveBase: "https://site.example.test",
			PublicBaseURL: "https://site.example.test",
		},
	}
	w2 := doJSON(newAdminEngine(sf2, p, &hooks), http.MethodPut, "/v1/payment-settings",
		`{"notify_base_url":"https://cb.example.test"}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("PUT status %d %s", w2.Code, w2.Body)
	}
	if sf2.setArg != "https://cb.example.test" || !hooks.reauthCalled {
		t.Fatalf("setArg=%q reauth=%v", sf2.setArg, hooks.reauthCalled)
	}
	if !strings.Contains(w2.Body.String(), "https://cb.example.test") {
		t.Fatalf("PUT body %s", w2.Body)
	}

	sf3 := &settingsFake{
		fakeSvc: &fakeSvc{},
		setErr:  errs.Invalid(map[string]string{"notify_base_url": "x"}),
	}
	w3 := doJSON(newAdminEngine(sf3, p, nil), http.MethodPut, "/v1/payment-settings",
		`{"notify_base_url":"bad"}`)
	if w3.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid PUT status %d", w3.Code)
	}

	w4 := doJSON(newAdminEngine(&settingsFake{fakeSvc: &fakeSvc{}}, p, nil), http.MethodPut,
		"/v1/payment-settings", `{"notify_base_url":"https://ok.example.test","unknown":1}`)
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status %d", w4.Code)
	}
}
