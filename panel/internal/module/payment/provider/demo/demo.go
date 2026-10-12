// Package demo 是一个用 HMAC-SHA256 验签的参考适配器，供集成测试与本地联调。
package demo

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

// Config 是 demo 渠道配置。
type Config struct {
	// Secret 是与调用方共享的验签密钥。
	Secret []byte
	// CheckoutURL 是模拟收银台地址，可为空。
	CheckoutURL string
}

// Provider 是 demo 渠道适配器。
type Provider struct{ cfg Config }

var _ provider.Provider = (*Provider)(nil)

// New 构造 demo 适配器。
func New(cfg Config) (*Provider, error) {
	if len(cfg.Secret) == 0 {
		return nil, errors.New("demo: 缺少验签密钥")
	}
	return &Provider{cfg: cfg}, nil
}

// CreatePayment 生成模拟收银台跳转。
func (p *Provider) CreatePayment(ctx context.Context, req provider.CreateRequest) (provider.CreateResponse, error) {
	if !req.Amount.IsPositive() {
		return provider.CreateResponse{}, errors.New("demo: 金额必须为正")
	}
	cents, err := money.ToCents(req.Amount)
	if err != nil {
		return provider.CreateResponse{}, fmt.Errorf("demo: 金额非法: %w", err)
	}

	base := p.cfg.CheckoutURL
	if base == "" {
		base = "about:blank"
	}
	q := url.Values{
		"out_trade_no": {req.OutTradeNo},
		"amount":       {strconv.FormatInt(cents, 10)},
		"currency":     {string(req.Currency)},
	}
	redirect := base + "?" + q.Encode()
	return provider.CreateResponse{
		HTTPMethod:  http.MethodGet,
		RedirectURL: redirect,
		FormFields: map[string]string{
			"out_trade_no": req.OutTradeNo,
			"amount":       strconv.FormatInt(cents, 10),
			"currency":     string(req.Currency),
		},
	}, nil
}

type notifyBody struct {
	EventID   string          `json:"event_id"`
	EventType string          `json:"event_type"`
	PaymentID string          `json:"payment_id"`
	OrderNo   string          `json:"order_no"`
	Amount    int64           `json:"amount"`
	Currency  string          `json:"currency"`
	Fee       int64           `json:"fee"`
	Method    string          `json:"method"`
	Raw       json.RawMessage `json:"raw"`
}

// ParseNotification 解析 JSON 回调并验签。
func (p *Provider) ParseNotification(ctx context.Context, in provider.Inbound) (provider.Notification, error) {
	body := in.Body
	if len(body) > 1<<20 {
		return provider.Notification{}, errors.New("demo: 报文过大")
	}

	var b notifyBody
	if err := json.Unmarshal(body, &b); err != nil {
		return provider.Notification{}, fmt.Errorf("demo: 报文不是合法 JSON: %w", err)
	}
	if b.EventID == "" {
		return provider.Notification{}, errors.New("demo: 缺少 event_id")
	}
	if b.OrderNo == "" {
		return provider.Notification{}, errors.New("demo: 缺少 order_no")
	}

	canonical := b.EventID + "|" + b.OrderNo + "|" +
		strconv.FormatInt(b.Amount, 10) + "|" + b.Currency
	sigHex := strings.TrimSpace(in.Header.Get("X-Aegis-Signature"))
	sig, _ := hex.DecodeString(sigHex)
	mac := hmac.New(sha256.New, p.cfg.Secret)
	mac.Write([]byte(canonical))
	expected := mac.Sum(nil)
	verified := len(sig) > 0 && hmac.Equal(expected, sig)

	cur, err := money.ParseCurrency(b.Currency)
	if err != nil {
		return provider.Notification{}, fmt.Errorf("demo: %w", err)
	}

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
		Currency:          cur,
		Status:            status,
		Method:            b.Method,
		Raw:               raw,
		SignatureVerified: verified,
	}, nil
}

// NotificationAck 回 JSON 并用 HTTP 状态码表达结果。
func (p *Provider) NotificationAck(in provider.AckInput) provider.AckOutput {
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

// QueryPayment 明确不支持主动查单。
func (p *Provider) QueryPayment(ctx context.Context, outTradeNo string) (provider.QueryResult, error) {
	return provider.QueryResult{}, fmt.Errorf("demo: %w", provider.ErrNotSupported)
}

// Sign 计算回调 HMAC-SHA256 签名（十六进制），供测试与本地联调造回调。
func Sign(secret []byte, eventID, orderNo string, amountCents int64, currency string) string {
	canonical := eventID + "|" + orderNo + "|" + strconv.FormatInt(amountCents, 10) + "|" + currency
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}
