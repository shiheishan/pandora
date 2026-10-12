package demo

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

const testSecret = "demo-secret-key-for-tests"

func testProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New(Config{Secret: []byte(testSecret)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func signedInbound(t *testing.T, body string, secret []byte) provider.Inbound {
	t.Helper()
	// 默认与 TestParseNotificationValidSignature 一致
	eventID := "ev-1"
	orderNo := "ORD-1"
	amount := int64(990)
	currency := "CNY"
	sig := Sign(secret, eventID, orderNo, amount, currency)
	return provider.Inbound{
		Method: http.MethodPost,
		Header: http.Header{"X-Aegis-Signature": {sig}},
		Body:   []byte(body),
	}
}

func TestParseNotificationValidSignature(t *testing.T) {
	p := testProvider(t)
	body := `{"event_id":"ev-1","event_type":"payment.succeeded","payment_id":"pay-1","order_no":"ORD-1","amount":990,"currency":"CNY","fee":0,"method":"alipay"}`
	in := signedInbound(t, body, []byte(testSecret))

	n, err := p.ParseNotification(context.Background(), in)
	if err != nil {
		t.Fatalf("ParseNotification: %v", err)
	}
	if !n.SignatureVerified {
		t.Fatal("SignatureVerified 应为 true")
	}
	if !n.Amount.Equal(money.FromCents(990)) {
		t.Errorf("amount = %s", n.Amount)
	}
}

func TestParseNotificationTamperedAmount(t *testing.T) {
	p := testProvider(t)
	body := `{"event_id":"ev-1","event_type":"payment.succeeded","payment_id":"pay-1","order_no":"ORD-1","amount":991,"currency":"CNY","fee":0}`
	// 签名仍按 990 造，与 body 不一致
	sig := Sign([]byte(testSecret), "ev-1", "ORD-1", 990, "CNY")
	in := provider.Inbound{
		Method: http.MethodPost,
		Header: http.Header{"X-Aegis-Signature": {sig}},
		Body:   []byte(body),
	}
	n, err := p.ParseNotification(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if n.SignatureVerified {
		t.Error("篡改金额后验签应失败")
	}
}

func TestParseNotificationWrongSecret(t *testing.T) {
	p := testProvider(t)
	body := `{"event_id":"ev-1","event_type":"payment.succeeded","payment_id":"pay-1","order_no":"ORD-1","amount":990,"currency":"CNY","fee":0}`
	sig := Sign([]byte("other-secret"), "ev-1", "ORD-1", 990, "CNY")
	in := provider.Inbound{
		Method: http.MethodPost,
		Header: http.Header{"X-Aegis-Signature": {sig}},
		Body:   []byte(body),
	}
	n, err := p.ParseNotification(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if n.SignatureVerified {
		t.Error("错误密钥应验签失败")
	}
}

func TestParseNotificationMissingEventID(t *testing.T) {
	p := testProvider(t)
	body := `{"event_type":"payment.succeeded","order_no":"ORD-1","amount":990,"currency":"CNY"}`
	_, err := p.ParseNotification(context.Background(), provider.Inbound{Body: []byte(body)})
	if err == nil {
		t.Fatal("缺少 event_id 应报错")
	}
}

func TestNotificationAckUnauthorized(t *testing.T) {
	p := testProvider(t)
	out := p.NotificationAck(provider.AckInput{SignatureValid: false})
	if out.HTTPStatus != http.StatusUnauthorized {
		t.Errorf("status = %d", out.HTTPStatus)
	}
	if !strings.Contains(string(out.Body), "unauthorized") {
		t.Errorf("body = %s", out.Body)
	}
}

func TestNotificationAckConflict(t *testing.T) {
	p := testProvider(t)
	out := p.NotificationAck(provider.AckInput{
		SignatureValid: true,
		HandlerError:   errors.New("x"),
	})
	if out.HTTPStatus != http.StatusConflict {
		t.Errorf("status = %d", out.HTTPStatus)
	}
	if !strings.Contains(string(out.Body), "conflict") {
		t.Errorf("body = %s", out.Body)
	}
}

func TestNotificationAckOK(t *testing.T) {
	p := testProvider(t)
	out := p.NotificationAck(provider.AckInput{
		SignatureValid: true,
		Processed:      true,
		PaymentID:      "pid-1",
	})
	if out.HTTPStatus != http.StatusOK {
		t.Errorf("status = %d", out.HTTPStatus)
	}
	if !strings.Contains(string(out.Body), "processed") {
		t.Errorf("body = %s", out.Body)
	}
}

func TestQueryPaymentNotSupported(t *testing.T) {
	p := testProvider(t)
	_, err := p.QueryPayment(context.Background(), "X")
	if !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("err = %v", err)
	}
}
