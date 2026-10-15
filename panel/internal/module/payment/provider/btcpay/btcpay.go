// Package btcpay 实现 BTCPay Server Greenfield API 支付适配器。
//
// Webhook 在 BTCPay 商店设置里配置（不随发票下发）。签名为请求头
// BTCPay-Sig: sha256=<hex(HMAC-SHA256(webhook 密钥, 原始请求体))>。
// 回调只用来通知有变化；金额、币种、状态一律以按发票号回查的结果为准。
// 手动标记为已结算（additionalStatus = Marked）的发票不自动入账；部分付款后过期算失败。
package btcpay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/module/payment/provider/safehttp"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

var (
	invoiceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	storeIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

// Config 是 BTCPay Server 接入配置。
type Config struct {
	BaseURL          string // BTCPay 服务地址，如 https://btcpay.example.test（可带路径前缀，末尾 / 去掉）
	StoreID          string
	APIKey           string // Greenfield API Key，不得落日志
	WebhookSecret    string // 商店 Webhook 的密钥，不得落日志
	AllowPrivateHost bool   // 只用于本地联调：允许 http 与内网地址
}

// Provider 是 BTCPay 渠道适配器。
type Provider struct {
	cfg    Config
	client *http.Client
	base   string
}

var _ provider.Provider = (*Provider)(nil)

// New 构造 BTCPay 适配器。
func New(cfg Config) (*Provider, error) {
	cfg.StoreID = strings.TrimSpace(cfg.StoreID)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.WebhookSecret = strings.TrimSpace(cfg.WebhookSecret)
	if cfg.StoreID == "" || cfg.APIKey == "" || cfg.WebhookSecret == "" {
		return nil, errors.New("btcpay: 缺少 Store ID、API Key 或 Webhook 密钥")
	}
	if !storeIDPattern.MatchString(cfg.StoreID) {
		return nil, errors.New("btcpay: Store ID 非法")
	}

	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, errors.New("btcpay: 服务地址非法")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("btcpay: 服务地址非法")
	}
	if u.Scheme != "https" && (!cfg.AllowPrivateHost || u.Scheme != "http") {
		return nil, errors.New("btcpay: 服务地址必须使用 https")
	}
	cfg.BaseURL = base

	return &Provider{
		cfg:    cfg,
		client: safehttp.Client(cfg.AllowPrivateHost),
		base:   base,
	}, nil
}

// flexAmount 兼容 BTCPay 发票金额字段为 JSON 字符串或数字。
type flexAmount string

func (f *flexAmount) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] == 'n' { // null
		*f = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = flexAmount(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*f = flexAmount(n.String())
	return nil
}

type invoice struct {
	ID               string     `json:"id"`
	StoreID          string     `json:"storeId"`
	Amount           flexAmount `json:"amount"`
	Currency         string     `json:"currency"`
	Status           string     `json:"status"`
	AdditionalStatus string     `json:"additionalStatus"`
	CheckoutLink     string     `json:"checkoutLink"`
	Metadata         struct {
		OrderID string `json:"orderId"`
	} `json:"metadata"`
}

type webhook struct {
	DeliveryID   string `json:"deliveryId"`
	WebhookID    string `json:"webhookId"`
	IsRedelivery bool   `json:"isRedelivery"`
	Type         string `json:"type"`
	StoreID      string `json:"storeId"`
	InvoiceID    string `json:"invoiceId"`
}

func (p *Provider) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("btcpay: 编码请求体失败: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	endpoint := p.base + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return fmt.Errorf("btcpay: 构造请求失败: %w", plog.StripURL(err))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+p.cfg.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("btcpay: 请求失败: %w", plog.StripURL(err))
	}
	defer resp.Body.Close()

	shortPath := shortPathLabel(path)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("btcpay: %s %s 返回 HTTP %d", method, shortPath, resp.StatusCode)
	}

	data, err := safehttp.ReadBody(resp.Body)
	if err != nil {
		return fmt.Errorf("btcpay: 读取响应失败: %w", err)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("btcpay: 响应不是合法 JSON: %w", err)
		}
	}
	return nil
}

func shortPathLabel(path string) string {
	// 错误信息里用不含具体 ID 的简短路径名。
	if strings.Contains(path, "/invoices/") && !strings.Contains(path, "?") {
		return "/invoices/{id}"
	}
	if strings.Contains(path, "/invoices?") {
		return "/invoices"
	}
	if strings.HasSuffix(path, "/invoices") {
		return "/invoices"
	}
	return path
}

func mapStatus(status, additional string) (st provider.Status, marked bool) {
	status = strings.TrimSpace(status)
	additional = strings.TrimSpace(additional)
	if strings.EqualFold(status, "Settled") {
		if strings.EqualFold(additional, "Marked") {
			return provider.StatusPending, true
		}
		// 商店开了付款容差时，少付但在容差内的发票也会进 Settled；带着 PaidPartial 的一律不自动入账（RUNBOOK 要求容差为 0）
		if strings.EqualFold(additional, "PaidPartial") {
			return provider.StatusPending, false
		}
		return provider.StatusSucceeded, false
	}
	if strings.EqualFold(status, "Expired") || strings.EqualFold(status, "Invalid") {
		return provider.StatusFailed, false
	}
	return provider.StatusPending, false
}

func normalize(inv invoice) (amount decimal.Decimal, cur money.Currency, st provider.Status, marked bool, err error) {
	amount, err = money.ParseProvider(string(inv.Amount))
	if err != nil {
		return decimal.Zero, "", provider.StatusPending, false, fmt.Errorf("btcpay: 发票金额非法: %w", err)
	}
	cur, err = money.ParseCurrency(strings.ToUpper(strings.TrimSpace(inv.Currency)))
	if err != nil {
		return decimal.Zero, "", provider.StatusPending, false, fmt.Errorf("btcpay: 发票币种 %q 不受支持", inv.Currency)
	}
	st, marked = mapStatus(inv.Status, inv.AdditionalStatus)
	return amount, cur, st, marked, nil
}

func (p *Provider) validateCheckoutLink(link string) error {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Host == "" {
		return errors.New("btcpay: 发票付款链接非法")
	}
	if u.Scheme != "https" && (!p.cfg.AllowPrivateHost || u.Scheme != "http") {
		return errors.New("btcpay: 发票付款链接非法")
	}
	return nil
}

// CreatePayment 在 BTCPay 创建发票并返回收银台链接。
func (p *Provider) CreatePayment(ctx context.Context, req provider.CreateRequest) (provider.CreateResponse, error) {
	if !req.Currency.Valid() {
		return provider.CreateResponse{}, fmt.Errorf("btcpay: 不支持的币种 %s", req.Currency)
	}
	if !req.Amount.IsPositive() {
		return provider.CreateResponse{}, errors.New("btcpay: 金额必须为正")
	}
	if err := money.Check(req.Amount); err != nil {
		return provider.CreateResponse{}, fmt.Errorf("btcpay: 金额非法: %w", err)
	}

	payload := map[string]any{
		"amount":   money.FormatProvider(req.Amount),
		"currency": string(req.Currency),
		"metadata": map[string]string{
			"orderId":  req.OutTradeNo,
			"itemDesc": sanitizeSubject(req.Subject),
		},
		"checkout": map[string]any{
			"redirectAutomatically": true,
		},
	}
	if strings.TrimSpace(req.ReturnURL) != "" {
		checkout := payload["checkout"].(map[string]any)
		checkout["redirectURL"] = req.ReturnURL
	}

	path := fmt.Sprintf("/api/v1/stores/%s/invoices", url.PathEscape(p.cfg.StoreID))
	var inv invoice
	if err := p.do(ctx, http.MethodPost, path, payload, &inv); err != nil {
		return provider.CreateResponse{}, err
	}

	if !invoiceIDPattern.MatchString(inv.ID) {
		return provider.CreateResponse{}, errors.New("btcpay: 发票 ID 非法")
	}
	if err := p.validateCheckoutLink(inv.CheckoutLink); err != nil {
		return provider.CreateResponse{}, err
	}

	echoAmount, err := money.ParseProvider(string(inv.Amount))
	if err != nil {
		return provider.CreateResponse{}, fmt.Errorf("btcpay: 发票金额非法: %w", err)
	}
	echoCur, err := money.ParseCurrency(strings.ToUpper(strings.TrimSpace(inv.Currency)))
	if err != nil || !echoAmount.Equal(req.Amount) || echoCur != req.Currency {
		return provider.CreateResponse{}, errors.New("btcpay: 发票金额或币种与请求不符")
	}

	return provider.CreateResponse{
		HTTPMethod:  http.MethodGet,
		RedirectURL: inv.CheckoutLink,
		ProviderRef: inv.ID,
	}, nil
}

func verifyWebhookSignature(secret string, body []byte, header string) bool {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	hexPart := strings.ToLower(strings.TrimPrefix(header, "sha256="))
	got, err := hex.DecodeString(hexPart)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	return hmac.Equal(got, want)
}

func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func unsignedNotification(wh webhook, parseOK bool) provider.Notification {
	ref := ""
	typ := ""
	if parseOK {
		ref = wh.InvoiceID
		typ = wh.Type
	}
	return provider.Notification{
		SignatureVerified: false,
		PaymentRef:        truncateBytes(ref, 128),
		Raw: map[string]string{
			"type": truncateBytes(typ, 64),
		},
	}
}

// ParseNotification 验签 Webhook 并按发票回查归一化通知。
func (p *Provider) ParseNotification(ctx context.Context, in provider.Inbound) (provider.Notification, error) {
	if in.Method != http.MethodPost {
		return provider.Notification{}, fmt.Errorf("btcpay: 不支持的请求方法 %s", in.Method)
	}

	sigOK := verifyWebhookSignature(p.cfg.WebhookSecret, in.Body, in.Header.Get("BTCPay-Sig"))
	var wh webhook
	parseOK := json.Unmarshal(in.Body, &wh) == nil

	if !sigOK {
		return unsignedNotification(wh, parseOK), nil
	}
	if !parseOK {
		return provider.Notification{}, errors.New("btcpay: Webhook 请求体不是合法 JSON")
	}
	if wh.StoreID != p.cfg.StoreID {
		return unsignedNotification(wh, true), nil
	}

	if !invoiceIDPattern.MatchString(wh.InvoiceID) {
		return provider.Notification{}, errors.New("btcpay: 发票 ID 非法")
	}

	path := fmt.Sprintf("/api/v1/stores/%s/invoices/%s", url.PathEscape(p.cfg.StoreID), url.PathEscape(wh.InvoiceID))
	var inv invoice
	if err := p.do(ctx, http.MethodGet, path, nil, &inv); err != nil {
		return provider.Notification{}, err
	}

	if inv.ID != wh.InvoiceID {
		return provider.Notification{}, errors.New("btcpay: 回查发票 ID 不一致")
	}
	if inv.StoreID != "" && inv.StoreID != p.cfg.StoreID {
		return provider.Notification{}, errors.New("btcpay: 回查商店 ID 不一致")
	}
	if strings.TrimSpace(inv.Metadata.OrderID) == "" {
		return provider.Notification{}, errors.New("btcpay: 发票缺少 orderId")
	}

	amount, cur, st, marked, err := normalize(inv)
	if err != nil {
		return provider.Notification{}, err
	}

	eventID := inv.ID + ":" + strings.ToUpper(strings.TrimSpace(inv.Status))
	if marked {
		eventID += ":MARKED"
	} else if strings.EqualFold(strings.TrimSpace(inv.AdditionalStatus), "PaidPartial") {
		eventID += ":PARTIAL"
	}

	raw := map[string]string{
		"source":            "webhook",
		"webhook_type":      truncateBytes(wh.Type, 64),
		"delivery_id":       wh.DeliveryID,
		"is_redelivery":     boolString(wh.IsRedelivery),
		"invoice_id":        inv.ID,
		"order_id":          inv.Metadata.OrderID,
		"status":            inv.Status,
		"additional_status": inv.AdditionalStatus,
		"amount":            string(inv.Amount),
		"currency":          inv.Currency,
		"manually_marked":   boolString(marked),
	}

	return provider.Notification{
		EventID:           eventID,
		PaymentRef:        inv.ID,
		OutTradeNo:        inv.Metadata.OrderID,
		Amount:            amount,
		Fee:               money.Zero,
		Currency:          cur,
		Status:            st,
		Method:            "",
		SignatureVerified: true,
		Raw:               raw,
	}, nil
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// NotificationAck 返回 BTCPay Webhook 期望的 JSON 回执。
func (p *Provider) NotificationAck(in provider.AckInput) provider.AckOutput {
	if in.OK() {
		return provider.AckOutput{
			HTTPStatus:  http.StatusOK,
			ContentType: "application/json",
			Body:        []byte(`{"ok":true}`),
		}
	}
	if !in.SignatureValid {
		return provider.AckOutput{
			HTTPStatus:  http.StatusUnauthorized,
			ContentType: "application/json",
			Body:        []byte(`{"ok":false}`),
		}
	}
	return provider.AckOutput{
		HTTPStatus:  http.StatusInternalServerError,
		ContentType: "application/json",
		Body:        []byte(`{"ok":false}`),
	}
}

// QueryPayment 按商户单号查询 BTCPay 发票。
func (p *Provider) QueryPayment(ctx context.Context, outTradeNo string) (provider.QueryResult, error) {
	path := fmt.Sprintf("/api/v1/stores/%s/invoices?orderId=%s&take=20",
		url.PathEscape(p.cfg.StoreID), url.QueryEscape(outTradeNo))

	var list []invoice
	if err := p.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return provider.QueryResult{}, err
	}

	var matched []invoice
	for _, inv := range list {
		if inv.Metadata.OrderID == outTradeNo {
			matched = append(matched, inv)
		}
	}
	if len(matched) == 0 {
		return provider.QueryResult{Found: false}, nil
	}

	chosen := matched[0]
	for _, inv := range matched {
		_, _, st, _, err := normalize(inv)
		if err != nil {
			return provider.QueryResult{}, err
		}
		if st == provider.StatusSucceeded {
			chosen = inv
			break
		}
	}

	amount, cur, st, _, err := normalize(chosen)
	if err != nil {
		return provider.QueryResult{}, err
	}

	return provider.QueryResult{
		Found:      true,
		PaymentRef: chosen.ID,
		Amount:     amount,
		Currency:   cur,
		Status:     st,
		Method:     "",
	}, nil
}

func sanitizeSubject(s string) string {
	s = strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
	s = strings.TrimSpace(s)
	if s == "" {
		s = "订阅服务"
	}
	r := []rune(s)
	if len(r) > 64 {
		s = string(r[:64])
	}
	return s
}
