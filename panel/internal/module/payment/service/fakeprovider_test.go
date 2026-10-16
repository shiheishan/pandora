// 本文件是 payment 包测试用的支付渠道替身：HMAC-SHA256 验签的 JSON 回调，支持手续费，
// 事件号由回调给出；经 Service.adapter 注入，产品代码里没有它。

package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// fakeCodePrefix 是测试渠道代码的前缀：注入钩子按它认替身。
const fakeCodePrefix = "fake-"

// fakeSecret 是替身渠道的验签密钥（虚构）。
const fakeSecret = "payment-pg-test-secret"

// fakeConfig 是测试替身渠道配置。
type fakeConfig struct {
	Secret      []byte
	CheckoutURL string
}

// fakeProvider 是测试替身渠道适配器。
type fakeProvider struct{ cfg fakeConfig }

var _ provider.Provider = (*fakeProvider)(nil)

func newFakeProvider(cfg fakeConfig) (*fakeProvider, error) {
	if len(cfg.Secret) == 0 {
		return nil, errors.New("fake: 缺少验签密钥")
	}
	return &fakeProvider{cfg: cfg}, nil
}

func (p *fakeProvider) CreatePayment(ctx context.Context, req provider.CreateRequest) (provider.CreateResponse, error) {
	if !req.Amount.IsPositive() {
		return provider.CreateResponse{}, errors.New("fake: 金额必须为正")
	}
	cents, err := money.ToCents(req.Amount)
	if err != nil {
		return provider.CreateResponse{}, fmt.Errorf("fake: 金额非法: %w", err)
	}

	base := p.cfg.CheckoutURL
	if base == "" {
		base = "about:blank"
	}
	q := url.Values{
		"out_trade_no": {req.OutTradeNo},
		"amount":       {strconv.FormatInt(cents, 10)},
	}
	redirect := base + "?" + q.Encode()
	return provider.CreateResponse{
		HTTPMethod:  http.MethodGet,
		RedirectURL: redirect,
		FormFields: map[string]string{
			"out_trade_no": req.OutTradeNo,
			"amount":       strconv.FormatInt(cents, 10),
		},
	}, nil
}

type fakeNotifyBody struct {
	EventID   string          `json:"event_id"`
	EventType string          `json:"event_type"`
	PaymentID string          `json:"payment_id"`
	OrderNo   string          `json:"order_no"`
	Amount    int64           `json:"amount"`
	Fee       int64           `json:"fee"`
	Method    string          `json:"method"`
	Raw       json.RawMessage `json:"raw"`
}

func (p *fakeProvider) ParseNotification(ctx context.Context, in provider.Inbound) (provider.Notification, error) {
	body := in.Body
	if len(body) > 1<<20 {
		return provider.Notification{}, errors.New("fake: 报文过大")
	}

	var b fakeNotifyBody
	if err := json.Unmarshal(body, &b); err != nil {
		return provider.Notification{}, fmt.Errorf("fake: 报文不是合法 JSON: %w", err)
	}
	if b.EventID == "" {
		return provider.Notification{}, errors.New("fake: 缺少 event_id")
	}
	if b.OrderNo == "" {
		return provider.Notification{}, errors.New("fake: 缺少 order_no")
	}

	canonical := fakeCanonical(b.EventID, b.EventType, b.PaymentID, b.OrderNo, b.Amount, b.Fee)
	sigHex := strings.TrimSpace(in.Header.Get("X-Aegis-Signature"))
	sig, _ := hex.DecodeString(sigHex)
	mac := hmac.New(sha256.New, p.cfg.Secret)
	mac.Write([]byte(canonical))
	expected := mac.Sum(nil)
	verified := len(sig) > 0 && hmac.Equal(expected, sig)

	status := provider.StatusPending
	switch b.EventType {
	case "payment.succeeded":
		status = provider.StatusSucceeded
	case "payment.failed":
		status = provider.StatusFailed
	case "payment.refunded":
		status = provider.StatusRefunded
	}

	raw := map[string]string{}
	if len(b.Raw) > 0 {
		var m map[string]any
		if json.Unmarshal(b.Raw, &m) == nil {
			for k, v := range m {
				raw[k] = fmt.Sprint(v)
			}
		}
	}

	return provider.Notification{
		EventID:           b.EventID,
		PaymentRef:        b.PaymentID,
		OutTradeNo:        b.OrderNo,
		Amount:            money.FromCents(b.Amount),
		Fee:               money.FromCents(b.Fee),
		Status:            status,
		Method:            b.Method,
		Raw:               raw,
		SignatureVerified: verified,
	}, nil
}

func (p *fakeProvider) NotificationAck(in provider.AckInput) provider.AckOutput {
	const ct = "application/json; charset=utf-8"

	if !in.SignatureValid {
		return provider.AckOutput{
			HTTPStatus:  http.StatusUnauthorized,
			ContentType: ct,
			Body: []byte(`{"error":{"code":"unauthorized",` +
				`"message":"回调签名校验失败"}}`),
		}
	}

	if in.HandlerError != nil {
		return provider.AckOutput{
			HTTPStatus:  http.StatusConflict,
			ContentType: ct,
			Body:        []byte(`{"error":{"code":"conflict","message":"回调处理失败"}}`),
		}
	}

	body, _ := json.Marshal(map[string]any{
		"processed":       in.Processed,
		"already_handled": in.AlreadyHandled,
		"payment_id":      in.PaymentID,
	})
	return provider.AckOutput{HTTPStatus: http.StatusOK, ContentType: ct, Body: body}
}

func (p *fakeProvider) QueryPayment(ctx context.Context, outTradeNo string) (provider.QueryResult, error) {
	return provider.QueryResult{}, fmt.Errorf("fake: %w", provider.ErrNotSupported)
}

func fakeCanonical(eventID, eventType, paymentID, orderNo string, amountCents, feeCents int64) string {
	return strings.Join([]string{
		eventID, eventType, paymentID, orderNo,
		strconv.FormatInt(amountCents, 10), strconv.FormatInt(feeCents, 10),
	}, "|")
}

func fakeSign(secret []byte, eventID, eventType, paymentID, orderNo string, amountCents, feeCents int64) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(fakeCanonical(eventID, eventType, paymentID, orderNo, amountCents, feeCents)))
	return hex.EncodeToString(mac.Sum(nil))
}

// adapterWithFake 是测试用的 adapter 钩子：fake- 前缀的渠道走替身，其余走 buildAdapter。
func (e *env) adapterWithFake(p providerRecord) (provider.Provider, error) {
	if strings.HasPrefix(p.Code, fakeCodePrefix) {
		return newFakeProvider(fakeConfig{Secret: []byte(p.Creds.Key), CheckoutURL: p.BaseURL})
	}
	return e.svc.buildAdapter(p)
}
