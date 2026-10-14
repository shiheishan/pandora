package coinpayments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

const (
	testClientID     = "CLIENT1"
	testClientSecret = "TESTSECRET"
	testNotify1      = "https://panel.example.test/v1/webhooks/payments/cp"
	testNotify2      = "https://pay-cb.example.test/v1/webhooks/payments/cp"
	testFixedTime    = "2026-10-14T08:00:00Z"
)

func testSign(secret, method, fullURL, clientID, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("\ufeff"))
	mac.Write([]byte(strings.ToUpper(method)))
	mac.Write([]byte(fullURL))
	mac.Write([]byte(clientID))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func testProviderWithServer(t *testing.T, srv *httptest.Server) *Provider {
	t.Helper()
	p, err := New(Config{
		BaseURL:          srv.URL,
		ClientID:         testClientID,
		ClientSecret:     testClientSecret,
		NotifyURLs:       []string{testNotify1, testNotify2},
		AllowPrivateHost: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fixed, _ := time.Parse(time.RFC3339, testFixedTime)
	p.now = func() time.Time { return fixed }
	return p
}

func TestNewRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"缺 ClientID", Config{ClientSecret: "S", BaseURL: "https://api.example.test"}},
		{"缺 Secret", Config{ClientID: "C", BaseURL: "https://api.example.test"}},
		{"非法 ClientID", Config{ClientID: "bad/id", ClientSecret: "S", BaseURL: "https://api.example.test", AllowPrivateHost: true}},
		{"非 https", Config{ClientID: "CLIENT1", ClientSecret: "S", BaseURL: "http://api.example.test"}},
		{"URL 非法", Config{ClientID: "CLIENT1", ClientSecret: "S", BaseURL: "://bad", AllowPrivateHost: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.cfg); err == nil {
				t.Errorf("应当拒绝")
			}
		})
	}
}

func TestSignKnownShape(t *testing.T) {
	body := []byte(`{"a":1}`)
	got := sign(testClientSecret, "POST", "https://api.example.test/p?q=1", testClientID, "2026-10-14T08:00:00", body)
	want := testSign(testClientSecret, "POST", "https://api.example.test/p?q=1", testClientID, "2026-10-14T08:00:00", body)
	if got != want {
		t.Errorf("sign 不一致\n got: %s\nwant: %s", got, want)
	}
}

func TestCreatePayment(t *testing.T) {
	var currencyHits, createHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fullURL := srvURL(r)
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/currencies"):
			currencyHits.Add(1)
			fmt.Fprintf(w, `[{"id":"1001","type":"fiat","symbol":"CNY"}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/merchant/invoices":
			if !assertSigned(r, fullURL) {
				t.Error("签名头缺失或不一致")
				http.Error(w, "bad sig", http.StatusUnauthorized)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			// 同一个假服务收两次建单（第二次验币种缓存），invoiceId 依次是 ORD-CREATE、ORD-CREATE-2
			wantInvoice := "ORD-CREATE"
			if createHits.Add(1) == 2 {
				wantInvoice = "ORD-CREATE-2"
			}
			if req["currency"] != "1001" || req["invoiceId"] != wantInvoice {
				t.Errorf("请求体字段不符（要 currency=1001、invoiceId=%s）: %#v", wantInvoice, req)
			}
			amt, _ := req["amount"].(map[string]any)
			if amt["total"] != "10.30" {
				t.Errorf("amount.total = %v", amt["total"])
			}
			wh, _ := req["webhooks"].([]any)
			wh0, _ := wh[0].(map[string]any)
			if wh0["notificationsUrl"] != testNotify1 {
				t.Errorf("notificationsUrl = %v", wh0["notificationsUrl"])
			}
			fmt.Fprint(w, `{"invoices":[{"id":"CPINV1","link":"https://pay.example.test/l","checkoutLink":"https://pay.example.test/c/CPINV1"}]}`)
		default:
			t.Errorf("未预期的请求 %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := testProviderWithServer(t, srv)
	resp, err := p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "ORD-CREATE",
		Amount:     money.FromCents(1030),
		Currency:   money.CNY,
		Subject:    "测试商品",
		NotifyURL:  testNotify1,
		ReturnURL:  "https://panel.example.test/return",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if resp.ProviderRef != "CPINV1" || resp.RedirectURL != "https://pay.example.test/c/CPINV1" {
		t.Errorf("响应不符: %+v", resp)
	}
	if currencyHits.Load() != 1 {
		t.Errorf("币种接口应只调 1 次，实际 %d", currencyHits.Load())
	}

	_, err = p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "ORD-CREATE-2",
		Amount:     money.FromCents(1030),
		Currency:   money.CNY,
		NotifyURL:  testNotify1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if currencyHits.Load() != 1 {
		t.Errorf("第二次建单应走缓存，币种接口仍为 %d 次", currencyHits.Load())
	}
	if createHits.Load() != 2 {
		t.Errorf("建单接口应调 2 次，实际 %d", createHits.Load())
	}
}

func srvURL(r *http.Request) string {
	return "http://" + r.Host + r.URL.RequestURI()
}

func assertSigned(r *http.Request, fullURL string) bool {
	cid := r.Header.Get("X-CoinPayments-Client")
	ts := r.Header.Get("X-CoinPayments-Timestamp")
	sig := r.Header.Get("X-CoinPayments-Signature")
	if cid == "" || ts == "" || sig == "" {
		return false
	}
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	want := testSign(testClientSecret, r.Method, fullURL, cid, ts, body)
	return sig == want
}

func TestCreatePaymentRejectsHTTPNotify(t *testing.T) {
	p, err := New(Config{
		BaseURL:      "https://api.example.test",
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		NotifyURLs:   []string{testNotify1},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "X",
		Amount:     money.FromCents(100),
		Currency:   money.CNY,
		NotifyURL:  "http://panel.example.test/n",
	})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("want https error, got %v", err)
	}
}

func TestCreatePaymentCurrencyNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	p := testProviderWithServer(t, srv)
	_, err := p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "X",
		Amount:     money.FromCents(100),
		Currency:   money.CNY,
		NotifyURL:  testNotify1,
	})
	if err == nil || !strings.Contains(err.Error(), "找不到币种") {
		t.Fatalf("got %v", err)
	}
}

func webhookTS() string { return "2026-10-14T08:00:00" }

func signedInbound(notifyURL string, body []byte) provider.Inbound {
	sig := testSign(testClientSecret, http.MethodPost, notifyURL, testClientID, webhookTS(), body)
	h := http.Header{}
	h.Set("X-CoinPayments-Client", testClientID)
	h.Set("X-CoinPayments-Timestamp", webhookTS())
	h.Set("X-CoinPayments-Signature", sig)
	return provider.Inbound{Method: http.MethodPost, Header: h, Body: body}
}

func completedInvoiceJSON(status string, extra string) string {
	base := `{"id":"CPINV1","invoiceId":"ORD1","status":"` + status + `","amount":{"total":"10.30"},"currency":{"symbol":"CNY"},"payments":[]}`
	if extra != "" {
		base = strings.TrimSuffix(base, "}") + extra + "}"
	}
	return base
}

func TestParseNotificationCompleted(t *testing.T) {
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/invoices/CPINV1") {
			fetches.Add(1)
			fmt.Fprint(w, completedInvoiceJSON("completed", ""))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p := testProviderWithServer(t, srv)

	body := []byte(`{"id":1,"type":"InvoiceCompleted","invoice":{"id":"CPINV1","invoiceId":"ORD1"}}`)
	n, err := p.ParseNotification(context.Background(), signedInbound(testNotify2, body))
	if err != nil {
		t.Fatal(err)
	}
	if !n.SignatureVerified || n.Status != provider.StatusSucceeded {
		t.Errorf("n=%+v", n)
	}
	if n.EventID != "CPINV1:COMPLETED" || n.OutTradeNo != "ORD1" {
		t.Errorf("EventID/OutTradeNo: %q %q", n.EventID, n.OutTradeNo)
	}
	if strings.Contains(fmt.Sprint(n.Raw), testClientSecret) {
		t.Error("Raw 不应含密钥")
	}
}

func TestParseNotificationRejectsNoFetch(t *testing.T) {
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		fmt.Fprint(w, completedInvoiceJSON("completed", ""))
	}))
	defer srv.Close()
	p := testProviderWithServer(t, srv)
	body := []byte(`{"type":"InvoiceCompleted","invoice":{"id":"CPINV1"}}`)

	cases := []provider.Inbound{
		func() provider.Inbound {
			in := signedInbound(testNotify1, body)
			in.Header.Set("X-CoinPayments-Signature", "AAAA")
			return in
		}(),
		func() provider.Inbound {
			in := signedInbound(testNotify1, body)
			in.Header.Set("X-CoinPayments-Client", "WRONG")
			return in
		}(),
		func() provider.Inbound {
			in := signedInbound(testNotify1, body)
			in.Header.Set("X-CoinPayments-Timestamp", "2026-10-14T07:00:00")
			return in
		}(),
	}
	for i, in := range cases {
		before := fetches.Load()
		n, err := p.ParseNotification(context.Background(), in)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if n.SignatureVerified {
			t.Errorf("case %d: 应未验签", i)
		}
		if fetches.Load() != before {
			t.Errorf("case %d: 不应回查", i)
		}
	}
}

func TestParseNotificationPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"CPINV1","invoiceId":"ORD1","status":"completed","amount":{"total":"1.00"},"currency":{"symbol":"CNY"},"payments":[{"expectedAmount":"1.0","actualAmount":"0.5"}]}`)
	}))
	defer srv.Close()
	p := testProviderWithServer(t, srv)
	body := []byte(`{"type":"InvoiceCompleted","invoice":{"id":"CPINV1"}}`)
	n, err := p.ParseNotification(context.Background(), signedInbound(testNotify1, body))
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != provider.StatusPending || !strings.HasSuffix(n.EventID, ":PARTIAL") {
		t.Errorf("n=%+v", n)
	}
}

func TestParseNotificationTimedOutAndCancelled(t *testing.T) {
	for _, st := range []string{"timedOut", "cancelled"} {
		status := st
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"id":"CPINV1","invoiceId":"ORD1","status":"%s","amount":{"total":"1.00"},"currency":{"symbol":"CNY"}}`, status)
		}))
		defer srv.Close()
		p := testProviderWithServer(t, srv)
		body := []byte(`{"type":"X","invoice":{"id":"CPINV1"}}`)
		n, err := p.ParseNotification(context.Background(), signedInbound(testNotify1, body))
		if err != nil {
			t.Fatal(err)
		}
		if n.Status != provider.StatusFailed {
			t.Errorf("status %s => %s", status, n.Status)
		}
	}
}

func TestParseNotificationPaidIsPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"CPINV1","invoiceId":"ORD1","status":"paid","amount":{"total":"10.30"},"currency":{"symbol":"CNY"}}`)
	}))
	defer srv.Close()
	p := testProviderWithServer(t, srv)
	body := []byte(`{"type":"InvoicePaid","invoice":{"id":"CPINV1"}}`)
	n, err := p.ParseNotification(context.Background(), signedInbound(testNotify1, body))
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != provider.StatusPending {
		t.Errorf("paid 应为 pending, got %s", n.Status)
	}
}

func TestParseNotificationCurrency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"CPINV1","invoiceId":"ORD1","status":"completed","amount":{"total":"10.30"},"currency":{"symbol":"USD"}}`)
	}))
	defer srv.Close()
	p := testProviderWithServer(t, srv)
	body := []byte(`{"type":"InvoiceCompleted","invoice":{"id":"CPINV1"}}`)
	n, err := p.ParseNotification(context.Background(), signedInbound(testNotify1, body))
	if err != nil {
		t.Fatal(err)
	}
	if n.Currency != money.USD {
		t.Errorf("currency %s", n.Currency)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"CPINV1","invoiceId":"ORD1","status":"completed","amount":{"total":"1.00"},"currency":{"symbol":"BTC"}}`)
	}))
	defer srv2.Close()
	p2 := testProviderWithServer(t, srv2)
	if _, err := p2.ParseNotification(context.Background(), signedInbound(testNotify1, body)); err == nil {
		t.Error("BTC 应报错")
	}
}

func TestParseNotificationBreakdownTotal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"CPINV1","invoiceId":"ORD1","status":"completed","amount":{"breakdown":{"total":10.3}},"currency":{"symbol":"CNY"}}`)
	}))
	defer srv.Close()
	p := testProviderWithServer(t, srv)
	body := []byte(`{"type":"InvoiceCompleted","invoice":{"id":"CPINV1"}}`)
	n, err := p.ParseNotification(context.Background(), signedInbound(testNotify1, body))
	if err != nil {
		t.Fatal(err)
	}
	if !n.Amount.Equal(money.FromCents(1030)) {
		t.Errorf("amount %s", n.Amount)
	}
}

func TestParseNotificationReplayStableEventID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, completedInvoiceJSON("completed", ""))
	}))
	defer srv.Close()
	p := testProviderWithServer(t, srv)
	body := []byte(`{"type":"InvoiceCompleted","invoice":{"id":"CPINV1"}}`)
	var first string
	for i := 0; i < 3; i++ {
		n, err := p.ParseNotification(context.Background(), signedInbound(testNotify1, body))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = n.EventID
		} else if n.EventID != first {
			t.Fatalf("EventID 不稳定: %q vs %q", n.EventID, first)
		}
	}
}

func TestNotificationAck(t *testing.T) {
	p, _ := New(Config{ClientID: testClientID, ClientSecret: testClientSecret, BaseURL: "https://api.example.test", AllowPrivateHost: true})
	ok := p.NotificationAck(provider.AckInput{SignatureValid: true})
	if ok.HTTPStatus != 200 || !strings.Contains(string(ok.Body), "true") {
		t.Errorf("ok ack: %+v", ok)
	}
	bad := p.NotificationAck(provider.AckInput{SignatureValid: false})
	if bad.HTTPStatus != 401 {
		t.Errorf("bad sig: %d", bad.HTTPStatus)
	}
	errAck := p.NotificationAck(provider.AckInput{SignatureValid: true, HandlerError: fmt.Errorf("x")})
	if errAck.HTTPStatus != 500 {
		t.Errorf("handler err: %d", errAck.HTTPStatus)
	}
}

func TestQueryPayment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "ORD1" {
			t.Errorf("q=%q", r.URL.Query().Get("q"))
		}
		fmt.Fprint(w, `{"items":[{"id":"CP-A","invoiceId":"OTHER","status":"completed","amount":{"total":"1.00"},"currency":{"symbol":"CNY"}},`+
			`{"id":"CP-B","invoiceId":"ORD1","status":"completed","amount":{"total":"10.30"},"currency":{"symbol":"CNY"}}]}`)
	}))
	defer srv.Close()
	p := testProviderWithServer(t, srv)
	res, err := p.QueryPayment(context.Background(), "ORD1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found || res.PaymentRef != "CP-B" || res.Status != provider.StatusSucceeded {
		t.Errorf("res=%+v", res)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[]}`)
	}))
	defer srv2.Close()
	p2 := testProviderWithServer(t, srv2)
	res2, err := p2.QueryPayment(context.Background(), "ORD1")
	if err != nil || res2.Found {
		t.Fatalf("empty: err=%v found=%v", err, res2.Found)
	}

	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv3.Close()
	p3 := testProviderWithServer(t, srv3)
	if _, err := p3.QueryPayment(context.Background(), "ORD1"); err == nil {
		t.Error("500 应报错")
	}
}
