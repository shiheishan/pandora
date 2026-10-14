package btcpay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

const (
	testStoreID = "STORE1"
	testAPIKey  = "TESTKEY"
	testSecret  = "TESTSECRET"
)

func testConfig(baseURL string) Config {
	return Config{
		BaseURL:          baseURL,
		StoreID:          testStoreID,
		APIKey:           testAPIKey,
		WebhookSecret:    testSecret,
		AllowPrivateHost: true,
	}
}

func webhookSig(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestNewRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "缺 StoreID",
			cfg:  Config{BaseURL: "https://btcpay.example.test", APIKey: "k", WebhookSecret: "s"},
		},
		{
			name: "缺 APIKey",
			cfg:  Config{BaseURL: "https://btcpay.example.test", StoreID: "S1", WebhookSecret: "s"},
		},
		{
			name: "缺 WebhookSecret",
			cfg:  Config{BaseURL: "https://btcpay.example.test", StoreID: "S1", APIKey: "k"},
		},
		{
			name: "StoreID 含斜杠",
			cfg: Config{
				BaseURL: "https://btcpay.example.test", StoreID: "a/b", APIKey: "k", WebhookSecret: "s",
			},
		},
		{
			name: "http 且不允许内网",
			cfg: Config{
				BaseURL: "http://btcpay.example.test", StoreID: "S1", APIKey: "k", WebhookSecret: "s",
			},
		},
		{
			name: "地址带查询串",
			cfg: Config{
				BaseURL: "https://btcpay.example.test?x=1", StoreID: "S1", APIKey: "k", WebhookSecret: "s",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil {
				t.Fatal("期望报错")
			}
		})
	}
}

func TestCreatePayment(t *testing.T) {
	var gotAuth string
	var gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"INV1","checkoutLink":"https://btcpay.example.test/i/INV1","amount":"10.30","currency":"CNY","status":"New"}`))
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "ORD1",
		Amount:     decimal.RequireFromString("10.30"),
		Currency:   money.CNY,
		Subject:    "套餐",
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotAuth != "token TESTKEY" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/api/v1/stores/STORE1/invoices" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["amount"] != "10.30" || gotBody["currency"] != "CNY" {
		t.Errorf("body amount/currency = %v", gotBody)
	}
	meta, _ := gotBody["metadata"].(map[string]any)
	if meta == nil || meta["orderId"] != "ORD1" {
		t.Errorf("metadata.orderId = %v", gotBody)
	}

	if resp.HTTPMethod != http.MethodGet {
		t.Errorf("HTTPMethod = %s", resp.HTTPMethod)
	}
	if resp.ProviderRef != "INV1" {
		t.Errorf("ProviderRef = %s", resp.ProviderRef)
	}
	wantURL := "https://btcpay.example.test/i/INV1"
	if resp.RedirectURL != wantURL {
		t.Errorf("RedirectURL = %s want %s", resp.RedirectURL, wantURL)
	}
}

func TestCreatePaymentRejectsEchoMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"INV1","checkoutLink":"https://btcpay.example.test/i/INV1","amount":"10.00","currency":"CNY","status":"New"}`))
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "ORD1",
		Amount:     decimal.RequireFromString("10.30"),
		Currency:   money.CNY,
	})
	if err == nil {
		t.Fatal("期望金额回显不一致时报错")
	}
}

func TestCreatePaymentRejectsBadCheckoutLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"INV1","checkoutLink":"javascript:alert(1)","amount":"10.30","currency":"CNY","status":"New"}`))
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "ORD1",
		Amount:     decimal.RequireFromString("10.30"),
		Currency:   money.CNY,
	})
	if err == nil {
		t.Fatal("期望非法 checkoutLink 报错")
	}
}

func TestCreatePaymentDoesNotFollowRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.test/", http.StatusFound)
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "ORD1",
		Amount:     decimal.RequireFromString("10.30"),
		Currency:   money.CNY,
	})
	if err == nil {
		t.Fatal("期望 302 不跟随并报错")
	}
}

func invoiceJSON(id, status, additional, amount, currency, orderID string) string {
	b, _ := json.Marshal(map[string]any{
		"id": id, "storeId": testStoreID, "amount": amount, "currency": currency,
		"status": status, "additionalStatus": additional,
		"metadata": map[string]string{"orderId": orderID},
	})
	return string(b)
}

func webhookBody(storeID, invoiceID, typ string) []byte {
	b, _ := json.Marshal(map[string]any{
		"deliveryId": "d1", "webhookId": "w1", "isRedelivery": false,
		"type": typ, "storeId": storeID, "invoiceId": invoiceID,
	})
	return b
}

func newWebhookTestServer(t *testing.T, invoiceBody string) (*httptest.Server, *int) {
	var fetches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/stores/") && strings.Contains(r.URL.Path, "/invoices/") {
			fetches++
			_, _ = w.Write([]byte(invoiceBody))
			return
		}
		http.NotFound(w, r)
	}))
	return srv, &fetches
}

func assertNoSecrets(t *testing.T, n provider.Notification) {
	for k, v := range n.Raw {
		if strings.Contains(v, testAPIKey) || strings.Contains(v, testSecret) {
			t.Errorf("Raw[%s] 含密钥: %s", k, v)
		}
	}
	if strings.Contains(n.PaymentRef, testAPIKey) {
		t.Error("PaymentRef 含 API Key")
	}
}

func TestParseNotificationSettled(t *testing.T) {
	inv := invoiceJSON("INV1", "Settled", "None", "10.30", "CNY", "ORD1")
	srv, fetches := newWebhookTestServer(t, inv)
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	body := webhookBody(testStoreID, "INV1", "InvoiceSettled")
	n, err := p.ParseNotification(context.Background(), provider.Inbound{
		Method: http.MethodPost,
		Header: http.Header{"BTCPay-Sig": {webhookSig(body, testSecret)}},
		Body:   body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if *fetches != 1 {
		t.Fatalf("回查次数 = %d", *fetches)
	}
	if !n.SignatureVerified || n.Status != provider.StatusSucceeded {
		t.Fatalf("verified=%v status=%s", n.SignatureVerified, n.Status)
	}
	if n.EventID != "INV1:SETTLED" || n.OutTradeNo != "ORD1" {
		t.Fatalf("EventID=%s OutTradeNo=%s", n.EventID, n.OutTradeNo)
	}
	if !n.Amount.Equal(decimal.RequireFromString("10.30")) {
		t.Fatalf("amount=%s", n.Amount)
	}
	assertNoSecrets(t, n)
}

func TestParseNotificationBadSignatureNoFetch(t *testing.T) {
	inv := invoiceJSON("INV1", "Settled", "None", "10.30", "CNY", "ORD1")
	srv, fetches := newWebhookTestServer(t, inv)
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	body := webhookBody(testStoreID, "INV1", "InvoiceSettled")
	cases := []struct {
		name string
		sig  string
	}{
		{"签名错误", "sha256=" + strings.Repeat("00", 32)},
		{"缺签名头", ""},
		{"sha256 后非 hex", "sha256=not-hex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			*fetches = 0
			n, err := p.ParseNotification(context.Background(), provider.Inbound{
				Method: http.MethodPost,
				Header: http.Header{"BTCPay-Sig": {tc.sig}},
				Body:   body,
			})
			if err != nil {
				t.Fatal(err)
			}
			if n.SignatureVerified {
				t.Fatal("期望 SignatureVerified=false")
			}
			if *fetches != 0 {
				t.Fatalf("回查次数 = %d", *fetches)
			}
		})
	}
}

func TestParseNotificationForeignStoreNoFetch(t *testing.T) {
	inv := invoiceJSON("INV1", "Settled", "None", "10.30", "CNY", "ORD1")
	srv, fetches := newWebhookTestServer(t, inv)
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	body := webhookBody("OTHER", "INV1", "InvoiceSettled")
	n, err := p.ParseNotification(context.Background(), provider.Inbound{
		Method: http.MethodPost,
		Header: http.Header{"BTCPay-Sig": {webhookSig(body, testSecret)}},
		Body:   body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.SignatureVerified {
		t.Fatal("期望 SignatureVerified=false")
	}
	if *fetches != 0 {
		t.Fatalf("回查次数 = %d", *fetches)
	}
}

func TestParseNotificationMarkedIsPending(t *testing.T) {
	inv := invoiceJSON("INV1", "Settled", "Marked", "10.30", "CNY", "ORD1")
	srv, _ := newWebhookTestServer(t, inv)
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	body := webhookBody(testStoreID, "INV1", "InvoiceSettled")
	n, err := p.ParseNotification(context.Background(), provider.Inbound{
		Method: http.MethodPost,
		Header: http.Header{"BTCPay-Sig": {webhookSig(body, testSecret)}},
		Body:   body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != provider.StatusPending {
		t.Fatalf("status=%s", n.Status)
	}
	if !strings.HasSuffix(n.EventID, ":MARKED") {
		t.Fatalf("EventID=%s", n.EventID)
	}
}

func TestParseNotificationPartialExpiredFails(t *testing.T) {
	inv := invoiceJSON("INV1", "Expired", "PaidPartial", "10.30", "CNY", "ORD1")
	srv, _ := newWebhookTestServer(t, inv)
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	body := webhookBody(testStoreID, "INV1", "InvoiceExpired")
	n, err := p.ParseNotification(context.Background(), provider.Inbound{
		Method: http.MethodPost,
		Header: http.Header{"BTCPay-Sig": {webhookSig(body, testSecret)}},
		Body:   body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != provider.StatusFailed {
		t.Fatalf("status=%s", n.Status)
	}
	if n.Raw["additional_status"] != "PaidPartial" {
		t.Fatalf("additional_status=%q", n.Raw["additional_status"])
	}
}

func TestParseNotificationInvalidFails(t *testing.T) {
	inv := invoiceJSON("INV1", "Invalid", "Invalid", "10.30", "CNY", "ORD1")
	srv, _ := newWebhookTestServer(t, inv)
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	body := webhookBody(testStoreID, "INV1", "InvoiceInvalid")
	n, err := p.ParseNotification(context.Background(), provider.Inbound{
		Method: http.MethodPost,
		Header: http.Header{"BTCPay-Sig": {webhookSig(body, testSecret)}},
		Body:   body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != provider.StatusFailed {
		t.Fatalf("status=%s", n.Status)
	}
}

func TestParseNotificationCurrency(t *testing.T) {
	t.Run("USD", func(t *testing.T) {
		inv := invoiceJSON("INV1", "Settled", "None", "10.30", "USD", "ORD1")
		srv, _ := newWebhookTestServer(t, inv)
		defer srv.Close()
		p, _ := New(testConfig(srv.URL))
		body := webhookBody(testStoreID, "INV1", "InvoiceSettled")
		n, err := p.ParseNotification(context.Background(), provider.Inbound{
			Method: http.MethodPost,
			Header: http.Header{"BTCPay-Sig": {webhookSig(body, testSecret)}},
			Body:   body,
		})
		if err != nil {
			t.Fatal(err)
		}
		if n.Currency != money.USD {
			t.Fatalf("currency=%s", n.Currency)
		}
	})
	t.Run("BTC", func(t *testing.T) {
		inv := invoiceJSON("INV1", "Settled", "None", "10.30", "BTC", "ORD1")
		srv, _ := newWebhookTestServer(t, inv)
		defer srv.Close()
		p, _ := New(testConfig(srv.URL))
		body := webhookBody(testStoreID, "INV1", "InvoiceSettled")
		_, err := p.ParseNotification(context.Background(), provider.Inbound{
			Method: http.MethodPost,
			Header: http.Header{"BTCPay-Sig": {webhookSig(body, testSecret)}},
			Body:   body,
		})
		if err == nil {
			t.Fatal("期望 BTC 币种报错")
		}
	})
}

func TestParseNotificationBadAmount(t *testing.T) {
	inv := invoiceJSON("INV1", "Settled", "None", "10.301", "CNY", "ORD1")
	srv, _ := newWebhookTestServer(t, inv)
	defer srv.Close()
	p, _ := New(testConfig(srv.URL))
	body := webhookBody(testStoreID, "INV1", "InvoiceSettled")
	_, err := p.ParseNotification(context.Background(), provider.Inbound{
		Method: http.MethodPost,
		Header: http.Header{"BTCPay-Sig": {webhookSig(body, testSecret)}},
		Body:   body,
	})
	if err == nil {
		t.Fatal("期望三位小数金额报错")
	}
}

func TestParseNotificationReplayStableEventID(t *testing.T) {
	inv := invoiceJSON("INV1", "Settled", "None", "10.30", "CNY", "ORD1")
	srv, _ := newWebhookTestServer(t, inv)
	defer srv.Close()
	p, _ := New(testConfig(srv.URL))
	body := webhookBody(testStoreID, "INV1", "InvoiceSettled")
	in := provider.Inbound{
		Method: http.MethodPost,
		Header: http.Header{"BTCPay-Sig": {webhookSig(body, testSecret)}},
		Body:   body,
	}
	n1, err := p.ParseNotification(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := p.ParseNotification(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if n1.EventID != n2.EventID {
		t.Fatalf("EventID 不稳定: %s vs %s", n1.EventID, n2.EventID)
	}
}

func TestNotificationAck(t *testing.T) {
	p, err := New(testConfig("https://btcpay.example.test"))
	if err != nil {
		t.Fatal(err)
	}

	ok := p.NotificationAck(provider.AckInput{SignatureValid: true, HandlerError: nil})
	if ok.HTTPStatus != http.StatusOK || !strings.Contains(string(ok.Body), "true") {
		t.Fatalf("OK ack: %d %s", ok.HTTPStatus, ok.Body)
	}

	unauth := p.NotificationAck(provider.AckInput{SignatureValid: false})
	if unauth.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d", unauth.HTTPStatus)
	}

	fail := p.NotificationAck(provider.AckInput{SignatureValid: true, HandlerError: io.EOF})
	if fail.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("fail status=%d", fail.HTTPStatus)
	}
	if !strings.Contains(string(fail.Body), "false") {
		t.Fatalf("fail body=%s", fail.Body)
	}
}

func TestQueryPayment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stores/STORE1/invoices" {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(r.URL.RawQuery, "orderId=ORD1") {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "token TESTKEY" {
			t.Errorf("auth=%s", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`[` +
			invoiceJSON("INV0", "New", "None", "1.00", "CNY", "OTHER") + `,` +
			invoiceJSON("INV1", "Settled", "None", "10.30", "CNY", "ORD1") + `]`))
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	qr, err := p.QueryPayment(context.Background(), "ORD1")
	if err != nil {
		t.Fatal(err)
	}
	if !qr.Found || qr.Status != provider.StatusSucceeded || qr.PaymentRef != "INV1" {
		t.Fatalf("result=%+v", qr)
	}

	srvEmpty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srvEmpty.Close()
	p2, _ := New(testConfig(srvEmpty.URL))
	qr2, err := p2.QueryPayment(context.Background(), "ORD1")
	if err != nil {
		t.Fatal(err)
	}
	if qr2.Found {
		t.Fatal("期望 Found=false")
	}

	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv500.Close()
	p3, _ := New(testConfig(srv500.URL))
	_, err = p3.QueryPayment(context.Background(), "ORD1")
	if err == nil {
		t.Fatal("期望 HTTP 500 报错")
	}
}
