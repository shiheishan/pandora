// Package demo 是一个用 HMAC-SHA256 验签的参考适配器。
//
// 用途有两个：
//  1. 集成测试与本地联调不必依赖真实支付渠道；
//  2. 作为 Provider 接口的最小实现范例 —— 新接一个渠道时可以照着写。
//
// 与易支付的对比正好覆盖了协议差异的两个极端：
//
//	· 报文：JSON body（本适配器） vs GET query string（易支付）
//	· 验签：HMAC-SHA256 + 请求头 vs MD5 + 参数内联
//	· 回执：JSON vs 纯文本 success
//
// 上层业务代码对这些差异一无所知，这正是 PAY-002 的价值。
package demo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/platform/crypto"
)

type Config struct {
	Code string
	// Secret 是与调用方共享的验签密钥。
	Secret []byte
	// CheckoutURL 是模拟收银台地址，可为空。
	CheckoutURL string
}

type Provider struct{ cfg Config }

func New(cfg Config) (*Provider, error) {
	if cfg.Code == "" {
		return nil, errors.New("demo: 缺少渠道 code")
	}
	if len(cfg.Secret) == 0 {
		return nil, errors.New("demo: 缺少验签密钥")
	}
	return &Provider{cfg: cfg}, nil
}

func (p *Provider) Code() string { return p.cfg.Code }

func (p *Provider) CreatePayment(ctx context.Context, req payment.CreateRequest) (*payment.CreateResponse, error) {
	if req.Amount <= 0 {
		return nil, errors.New("demo: 金额必须为正")
	}
	base := p.cfg.CheckoutURL
	if base == "" {
		base = "about:blank"
	}
	return &payment.CreateResponse{
		HTTPMethod: http.MethodGet,
		RedirectURL: fmt.Sprintf("%s?out_trade_no=%s&amount=%d&currency=%s",
			base, req.OutTradeNo, req.Amount, req.Currency),
		FormFields: map[string]string{
			"out_trade_no": req.OutTradeNo,
			"amount":       strconv.FormatInt(req.Amount, 10),
			"currency":     req.Currency,
		},
	}, nil
}

type notifyBody struct {
	EventID   string          `json:"event_id"`
	EventType string          `json:"event_type"`
	PaymentID string          `json:"payment_id"`
	OrderID   string          `json:"order_id"`
	OrderNo   string          `json:"order_no"`
	Amount    int64           `json:"amount"`
	Currency  string          `json:"currency"`
	Fee       int64           `json:"fee"`
	Raw       json.RawMessage `json:"raw"`
}

func (p *Provider) ParseNotification(ctx context.Context, r *http.Request) (*payment.Notification, error) {
	// 限制报文体积，避免恶意超大 body 拖垮内存
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("demo: 读取报文失败: %w", err)
	}

	var b notifyBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, fmt.Errorf("demo: 报文不是合法 JSON: %w", err)
	}
	if b.EventID == "" || (b.OrderID == "" && b.OrderNo == "") {
		return nil, errors.New("demo: 缺少 event_id 或订单标识")
	}

	// 验签口径与旧实现保持一致，避免已接入方需要改代码
	canonical := b.EventID + "|" + b.OrderID + "|" +
		strconv.FormatInt(b.Amount, 10) + "|" + b.Currency
	sig, _ := hex.DecodeString(strings.TrimSpace(r.Header.Get("X-Aegis-Signature")))
	verified := len(sig) > 0 && crypto.HMACVerify(p.cfg.Secret, []byte(canonical), sig)

	status := payment.StatusPending
	switch b.EventType {
	case "payment.succeeded":
		status = payment.StatusSucceeded
	case "payment.failed":
		status = payment.StatusFailed
	case "payment.refunded":
		status = payment.StatusRefunded
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
	raw["__fee"] = strconv.FormatInt(b.Fee, 10)

	return &payment.Notification{
		EventID:    b.EventID,
		PaymentRef: b.PaymentID,
		// 本适配器同时支持两种定位方式：调用方给 UUID 就走 OrderID，
		// 给短单号就走 OutTradeNo。绝不把 UUID 冒充成 order_no。
		OrderID:           b.OrderID,
		OutTradeNo:        b.OrderNo,
		Amount:            b.Amount,
		FeeAmount:         b.Fee,
		Currency:          b.Currency,
		Status:            status,
		Raw:               raw,
		SignatureVerified: verified,
	}, nil
}

// NotificationAck 回 JSON 并用 HTTP 状态码表达结果 —— 与易支付的
// 「恒 200 + 纯文本」形成对照，说明回执策略确实必须由适配器决定。
func (p *Provider) NotificationAck(in payment.AckInput) payment.AckOutput {
	const ct = "application/json; charset=utf-8"

	if !in.SignatureValid {
		return payment.AckOutput{
			HTTPStatus:  http.StatusUnauthorized,
			ContentType: ct,
			Body: []byte(`{"error":{"code":"unauthorized",` +
				`"message":"回调签名校验失败"}}`),
		}
	}

	if in.HandlerError != nil {
		return payment.AckOutput{
			HTTPStatus:  http.StatusConflict,
			ContentType: ct,
			Body:        []byte(`{"error":{"code":"conflict","message":"回调处理失败"}}`),
		}
	}

	body, _ := json.Marshal(map[string]any{
		"processed":       in.Processed,
		"already_handled": in.AlreadyHandled,
		"payment_id":      in.PaymentID,
		"subscription_id": in.SubscriptionID,
		"ledger_txn_id":   in.LedgerTxnID,
	})
	return payment.AckOutput{HTTPStatus: http.StatusOK, ContentType: ct, Body: body}
}

func (p *Provider) QueryPayment(ctx context.Context, outTradeNo string) (*payment.QueryResult, error) {
	// 无真实渠道可查：明确返回未找到，而不是编造一个成功状态
	return &payment.QueryResult{Found: false}, nil
}

func (p *Provider) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResult, error) {
	return &payment.RefundResult{
		RefundRef: "demo-refund-" + req.PaymentRef,
		Status:    payment.StatusRefunded,
	}, nil
}
