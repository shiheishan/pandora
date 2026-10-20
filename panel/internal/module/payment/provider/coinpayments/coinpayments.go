// Package coinpayments 实现 CoinPayments 新版 REST API（v2）支付适配器。
package coinpayments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/safehttp"
	"github.com/shopspring/decimal"
)

const (
	// DefaultBaseURL 是 CoinPayments API 缺省基址。
	DefaultBaseURL = "https://a-api.coinpayments.net"

	signBOM           = "\ufeff" // 签名串固定以 UTF-8 BOM 开头（文档要求）
	invoiceIDPattern  = `^[A-Za-z0-9_-]{1,128}$`
	clientIDPattern   = `^[A-Za-z0-9_-]{1,128}$`
	currencyCacheTTL  = 24 * time.Hour
	maxWebhookTypeLen = 64
	maxPaymentRefLen  = 128
)

var (
	invoiceIDRe = regexp.MustCompile(invoiceIDPattern)
	clientIDRe  = regexp.MustCompile(clientIDPattern)
)

// Config 是 CoinPayments 商户接入配置。
type Config struct {
	BaseURL          string // 空 = DefaultBaseURL
	ClientID         string
	ClientSecret     string   // 不得落日志
	NotifyURLs       []string // 回调验签时可接受的完整回调 URL，按顺序都试
	AllowPrivateHost bool     // 只用于本地联调
}

// Provider 是 CoinPayments 渠道适配器。
type Provider struct {
	cfg    Config
	base   string
	client *http.Client
	now    func() time.Time
}

// 法币 ID 缓存放在包级：适配器每次调用都按渠道记录重新构造，挂在实例上等于不缓存。
var (
	curMu    sync.Mutex
	curCache = map[string]cachedID{}
)

type cachedID struct {
	id      string
	expires time.Time
}

var _ provider.Provider = (*Provider)(nil)

// New 构造 CoinPayments 适配器。
func New(cfg Config) (*Provider, error) {
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.ClientSecret = strings.TrimSpace(cfg.ClientSecret)
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("coinpayments: 缺少 Client ID 或 Client Secret")
	}
	if !clientIDRe.MatchString(cfg.ClientID) {
		return nil, errors.New("coinpayments: Client ID 格式非法")
	}

	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, errors.New("coinpayments: BaseURL 非法")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("coinpayments: BaseURL 不得包含用户、查询串或片段")
	}
	if u.Scheme != "https" && (!cfg.AllowPrivateHost || u.Scheme != "http") {
		return nil, errors.New("coinpayments: BaseURL 必须使用 https")
	}
	cfg.BaseURL = strings.TrimRight(u.String(), "/")

	return &Provider{
		cfg:    cfg,
		base:   cfg.BaseURL,
		client: safehttp.Client(cfg.AllowPrivateHost),
		now:    time.Now,
	}, nil
}

// sign 按 CoinPayments v2 规则计算请求签名（Base64 标准编码）。
func sign(secret, method, fullURL, clientID, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signBOM))
	mac.Write([]byte(strings.ToUpper(method)))
	mac.Write([]byte(fullURL))
	mac.Write([]byte(clientID))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (p *Provider) stamp() string {
	return p.now().UTC().Format("2006-01-02T15:04:05")
}

func (p *Provider) do(ctx context.Context, method, pathAndQuery string, body any, signed bool, out any, opName string) error {
	fullURL := p.base + pathAndQuery
	// 签名用的请求体必须与实际发出的字节完全一致；GET 没有请求体，签名里是空串
	var bodyBytes []byte
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("coinpayments: 编码请求体失败: %w", err)
		}
		bodyBytes = b
		reqBody = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return fmt.Errorf("coinpayments: 构造请求失败: %w", plog.StripURL(err))
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if signed {
		ts := p.stamp()
		req.Header.Set("X-CoinPayments-Client", p.cfg.ClientID)
		req.Header.Set("X-CoinPayments-Timestamp", ts)
		req.Header.Set("X-CoinPayments-Signature", sign(p.cfg.ClientSecret, method, fullURL, p.cfg.ClientID, ts, bodyBytes))
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("coinpayments: %s 请求失败: %w", opName, plog.StripURL(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("coinpayments: %s 返回 HTTP %d", opName, resp.StatusCode)
	}

	raw, err := safehttp.ReadBody(resp.Body)
	if err != nil {
		return fmt.Errorf("coinpayments: 读取 %s 响应失败: %w", opName, err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("coinpayments: %s 响应不是合法 JSON: %w", opName, err)
	}
	return nil
}

// flexAmount 接受 JSON 字符串、数字或 null。
type flexAmount string

func (f *flexAmount) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return errors.New("coinpayments: 空 JSON")
	}
	if b[0] == 'n' { // null
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexAmount(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexAmount(n.String())
	return nil
}

func (f flexAmount) string() string { return string(f) }

type invoice struct {
	ID        string `json:"id"`
	InvoiceID string `json:"invoiceId"`
	Status    string `json:"status"`
	Amount    struct {
		Total     flexAmount `json:"total"`
		Breakdown struct {
			Total flexAmount `json:"total"`
		} `json:"breakdown"`
	} `json:"amount"`
	Currency struct {
		ID     flexAmount `json:"id"`
		Symbol string     `json:"symbol"`
	} `json:"currency"`
	Payments []struct {
		ExpectedAmount flexAmount `json:"expectedAmount"`
		ActualAmount   flexAmount `json:"actualAmount"`
	} `json:"payments"`
}

func normalize(inv invoice) (amount decimal.Decimal, st provider.Status, partial bool, err error) {
	amtStr := inv.Amount.Total.string()
	if amtStr == "" {
		amtStr = inv.Amount.Breakdown.Total.string()
	}
	amount, err = money.ParseProvider(amtStr)
	if err != nil {
		return decimal.Zero, provider.StatusPending, false, fmt.Errorf("coinpayments: 发票金额非法: %w", err)
	}
	sym := strings.TrimSpace(inv.Currency.Symbol)
	if !strings.EqualFold(sym, provider.FiatCurrency) {
		return decimal.Zero, provider.StatusPending, false, fmt.Errorf("coinpayments: 发票币种 %q 不受支持", sym)
	}
	partial = invoicePartial(inv)
	st = mapInvoiceStatus(inv.Status, partial)
	return amount, st, partial, nil
}

func invoicePartial(inv invoice) bool {
	for _, pay := range inv.Payments {
		exp, err1 := decimal.NewFromString(strings.TrimSpace(pay.ExpectedAmount.string()))
		act, err2 := decimal.NewFromString(strings.TrimSpace(pay.ActualAmount.string()))
		if err1 == nil && err2 == nil && act.LessThan(exp) {
			return true
		}
	}
	return false
}

func mapInvoiceStatus(status string, partial bool) provider.Status {
	s := strings.ToLower(strings.TrimSpace(status))
	switch s {
	case "completed":
		if partial {
			return provider.StatusPending
		}
		return provider.StatusSucceeded
	case "cancelled", "timedout", "deleted":
		return provider.StatusFailed
	default:
		return provider.StatusPending
	}
}

func (p *Provider) currencyID(ctx context.Context, code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	key := p.base + "|" + code
	now := p.now()
	curMu.Lock()
	if ent, ok := curCache[key]; ok && now.Before(ent.expires) {
		id := ent.id
		curMu.Unlock()
		return id, nil
	}
	curMu.Unlock()

	path := "/api/v2/currencies?q=" + url.QueryEscape(code) + "&types=fiat"
	var raw json.RawMessage
	if err := p.do(ctx, http.MethodGet, path, nil, false, &raw, "查询币种"); err != nil {
		return "", err
	}

	items, err := parseCurrencyItems(raw)
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if strings.EqualFold(it.Type, "fiat") && strings.EqualFold(it.Symbol, code) {
			id := flexIDString(it.ID)
			if id == "" {
				continue
			}
			curMu.Lock()
			curCache[key] = cachedID{id: id, expires: now.Add(currencyCacheTTL)}
			curMu.Unlock()
			return id, nil
		}
	}
	return "", fmt.Errorf("coinpayments: 找不到币种 %s 的 ID", code)
}

type currencyItem struct {
	ID     json.RawMessage `json:"id"`
	Type   string          `json:"type"`
	Symbol string          `json:"symbol"`
}

func parseCurrencyItems(raw json.RawMessage) ([]currencyItem, error) {
	var arr []currencyItem
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	var wrap struct {
		Items []currencyItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, fmt.Errorf("coinpayments: 币种列表响应无法解析: %w", err)
	}
	return wrap.Items, nil
}

func flexIDString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func validateHTTPSURL(s string, allowHTTP bool, msg string) error {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" {
		return errors.New(msg)
	}
	if u.Scheme == "https" {
		return nil
	}
	if allowHTTP && u.Scheme == "http" {
		return nil
	}
	return errors.New(msg)
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

// CreatePayment 在 CoinPayments 创建发票并返回收银台链接。
func (p *Provider) CreatePayment(ctx context.Context, req provider.CreateRequest) (provider.CreateResponse, error) {
	if !req.Amount.IsPositive() {
		return provider.CreateResponse{}, errors.New("coinpayments: 金额必须为正")
	}
	if err := money.Check(req.Amount); err != nil {
		return provider.CreateResponse{}, fmt.Errorf("coinpayments: 金额非法: %w", err)
	}
	if err := validateHTTPSURL(req.NotifyURL, p.cfg.AllowPrivateHost, "coinpayments: 回调地址必须是 https"); err != nil {
		return provider.CreateResponse{}, err
	}

	curID, err := p.currencyID(ctx, provider.FiatCurrency)
	if err != nil {
		return provider.CreateResponse{}, err
	}

	amt := money.FormatProvider(req.Amount)
	payload := map[string]any{
		"currency":  curID,
		"invoiceId": req.OutTradeNo,
		"items": []map[string]any{
			{
				"name":     sanitizeSubject(req.Subject),
				"quantity": map[string]any{"value": 1, "type": "quantity"},
				"amount":   amt,
			},
		},
		"amount": map[string]any{
			"breakdown": map[string]any{"subtotal": amt},
			"total":     amt,
		},
		"webhooks": []map[string]any{
			{
				"notificationsUrl": req.NotifyURL,
				"notifications": []string{
					"invoiceCompleted", "invoicePaid", "invoicePending",
					"invoiceCancelled", "invoiceTimedOut",
				},
			},
		},
		"hideShoppingCart": true,
	}
	if strings.TrimSpace(req.ReturnURL) != "" {
		payload["successUrl"] = req.ReturnURL
		payload["cancelUrl"] = req.ReturnURL
	}

	var resp struct {
		Invoices []struct {
			ID           string `json:"id"`
			Link         string `json:"link"`
			CheckoutLink string `json:"checkoutLink"`
		} `json:"invoices"`
	}
	if err := p.do(ctx, http.MethodPost, "/api/v2/merchant/invoices", payload, true, &resp, "创建发票"); err != nil {
		return provider.CreateResponse{}, err
	}
	if len(resp.Invoices) == 0 {
		return provider.CreateResponse{}, errors.New("coinpayments: 创建发票响应为空")
	}
	inv := resp.Invoices[0]
	if !invoiceIDRe.MatchString(inv.ID) {
		return provider.CreateResponse{}, fmt.Errorf("coinpayments: 发票 ID 非法: %q", inv.ID)
	}
	if err := validateHTTPSURL(inv.CheckoutLink, p.cfg.AllowPrivateHost, "coinpayments: checkoutLink 必须是 https"); err != nil {
		return provider.CreateResponse{}, err
	}

	return provider.CreateResponse{
		HTTPMethod:  http.MethodGet,
		RedirectURL: inv.CheckoutLink,
		ProviderRef: inv.ID,
	}, nil
}

type webhookPayload struct {
	ID        json.RawMessage `json:"id"`
	Type      string          `json:"type"`
	Timestamp json.RawMessage `json:"timestamp"`
	Invoice   invoice         `json:"invoice"`
}

func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func (p *Provider) verifyWebhook(in provider.Inbound) (clientOK, tsOK, sigOK bool) {
	clientHdr := in.Header.Get("X-CoinPayments-Client")
	clientOK = hmac.Equal([]byte(clientHdr), []byte(p.cfg.ClientID))

	tsHdr := in.Header.Get("X-CoinPayments-Timestamp")
	ts, err := time.ParseInLocation("2006-01-02T15:04:05", tsHdr, time.UTC)
	if err == nil {
		diff := p.now().Sub(ts)
		if diff < 0 {
			diff = -diff
		}
		tsOK = diff <= 5*time.Minute
	}

	sigHdr := in.Header.Get("X-CoinPayments-Signature")
	gotSig, err := base64.StdEncoding.DecodeString(sigHdr)
	if err != nil {
		return clientOK, tsOK, false
	}
	for _, notifyURL := range p.cfg.NotifyURLs {
		want := sign(p.cfg.ClientSecret, http.MethodPost, notifyURL, clientHdr, tsHdr, in.Body)
		wantBytes, err := base64.StdEncoding.DecodeString(want)
		if err == nil && hmac.Equal(gotSig, wantBytes) {
			return clientOK, tsOK, true
		}
	}
	return clientOK, tsOK, false
}

func (p *Provider) fetchInvoice(ctx context.Context, id string) (invoice, error) {
	path := "/api/v2/merchant/invoices/" + url.PathEscape(id)
	var raw json.RawMessage
	if err := p.do(ctx, http.MethodGet, path, nil, true, &raw, "查询发票"); err != nil {
		return invoice{}, err
	}
	var inv invoice
	if err := json.Unmarshal(raw, &inv); err != nil {
		return invoice{}, fmt.Errorf("coinpayments: 查询发票响应无法解析: %w", err)
	}
	if inv.ID == "" {
		var wrap struct {
			Invoice invoice `json:"invoice"`
		}
		if err := json.Unmarshal(raw, &wrap); err != nil {
			return invoice{}, fmt.Errorf("coinpayments: 查询发票响应无法解析: %w", err)
		}
		inv = wrap.Invoice
	}
	return inv, nil
}

// ParseNotification 解析并验签 CoinPayments Webhook。
func (p *Provider) ParseNotification(ctx context.Context, in provider.Inbound) (provider.Notification, error) {
	if in.Method != http.MethodPost {
		return provider.Notification{}, fmt.Errorf("coinpayments: 不支持的请求方法 %s", in.Method)
	}

	clientOK, tsOK, sigOK := p.verifyWebhook(in)
	if !clientOK || !tsOK || !sigOK {
		var loose webhookPayload
		_ = json.Unmarshal(in.Body, &loose)
		ref := truncateBytes(strings.TrimSpace(loose.Invoice.ID), maxPaymentRefLen)
		raw := map[string]string{
			"type": truncateBytes(loose.Type, maxWebhookTypeLen),
		}
		return provider.Notification{
			PaymentRef:        ref,
			Raw:               raw,
			SignatureVerified: false,
		}, nil
	}

	var wh webhookPayload
	if err := json.Unmarshal(in.Body, &wh); err != nil {
		return provider.Notification{}, fmt.Errorf("coinpayments: 回调体不是合法 JSON: %w", err)
	}
	invID := strings.TrimSpace(wh.Invoice.ID)
	if !invoiceIDRe.MatchString(invID) {
		return provider.Notification{}, errors.New("coinpayments: 回调缺少合法 invoice.id")
	}

	inv, err := p.fetchInvoice(ctx, invID)
	if err != nil {
		return provider.Notification{}, err
	}
	if inv.ID != invID {
		return provider.Notification{}, errors.New("coinpayments: 回查发票 ID 与回调不一致")
	}
	if strings.TrimSpace(inv.InvoiceID) == "" {
		return provider.Notification{}, errors.New("coinpayments: 回查发票缺少 invoiceId")
	}

	amount, st, partial, err := normalize(inv)
	if err != nil {
		return provider.Notification{}, err
	}

	eventID := inv.ID + ":" + strings.ToUpper(strings.TrimSpace(inv.Status))
	if partial {
		eventID += ":PARTIAL"
	}

	amtRaw := inv.Amount.Total.string()
	if amtRaw == "" {
		amtRaw = inv.Amount.Breakdown.Total.string()
	}
	raw := map[string]string{
		"source":       "webhook",
		"webhook_type": truncateBytes(wh.Type, maxWebhookTypeLen),
		"webhook_id":   flexIDString(wh.ID),
		"invoice_id":   inv.ID,
		"out_trade_no": inv.InvoiceID,
		"status":       inv.Status,
		"amount":       amtRaw,
		"currency":     strings.ToUpper(strings.TrimSpace(inv.Currency.Symbol)),
		"partial":      partialString(partial),
	}

	return provider.Notification{
		EventID:           eventID,
		PaymentRef:        inv.ID,
		OutTradeNo:        inv.InvoiceID,
		Amount:            amount,
		Fee:               money.Zero,
		Status:            st,
		Method:            "",
		SignatureVerified: true,
		Raw:               raw,
	}, nil
}

func partialString(partial bool) string {
	if partial {
		return "true"
	}
	return "false"
}

// NotificationAck 返回 CoinPayments Webhook 回执。
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

// QueryPayment 按商户单号查询发票列表并归一化状态。
func (p *Provider) QueryPayment(ctx context.Context, outTradeNo string) (provider.QueryResult, error) {
	path := "/api/v2/merchant/invoices?q=" + url.QueryEscape(outTradeNo) + "&limit=20"
	var resp struct {
		Items []invoice `json:"items"`
	}
	if err := p.do(ctx, http.MethodGet, path, nil, true, &resp, "查询发票列表"); err != nil {
		return provider.QueryResult{}, err
	}

	var matched []invoice
	for _, it := range resp.Items {
		if it.InvoiceID == outTradeNo {
			matched = append(matched, it)
		}
	}
	if len(matched) == 0 {
		return provider.QueryResult{Found: false}, nil
	}

	chosen := matched[0]
	for _, it := range matched {
		_, st, partial, err := normalize(it)
		if err != nil {
			continue
		}
		if st == provider.StatusSucceeded && !partial {
			chosen = it
			break
		}
	}

	amount, st, _, err := normalize(chosen)
	if err != nil {
		return provider.QueryResult{}, err
	}

	return provider.QueryResult{
		Found:      true,
		PaymentRef: chosen.ID,
		Amount:     amount,
		Status:     st,
		Method:     "",
	}, nil
}
