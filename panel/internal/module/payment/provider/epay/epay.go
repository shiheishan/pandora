// Package epay 实现「易支付」（EPay / 彩虹易支付）协议适配器。
//
// 该协议是国内聚合支付站点的事实标准，Xboard 等面板普遍对接。
// 协议要点（也是最容易踩坑的地方）：
//
//  1. 金额是**元为单位的十进制字符串**（"9.90"），不是分。全程走 money.FormatProvider / ParseProvider。
//  2. 签名是 MD5：非空参数按名 ASCII 升序拼成 a=1&b=2，末尾**直接**
//     追加商户密钥（不是 &key=xxx），再取 MD5 小写。拼接时**不做 URL 编码**。
//  3. 异步通知是 **GET**，参数在 query string 里。
//  4. 通知处理成功必须回**纯文本 success**；回 JSON 或别的内容渠道会持续重推。
//  5. 协议本身没有事件 ID。幂等键由 trade_no + trade_status 合成。
package epay

import (
	"context"
	"crypto/md5" //nolint:gosec // 易支付协议规定用 MD5 签名，不能换
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/module/payment/provider/safehttp"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

const (
	defaultSubmitPath = "/submit.php"
	defaultAPIPath    = "/api.php"
)

// Config 是单个易支付商户的接入配置。
type Config struct {
	// BaseURL 形如 https://pay.example.com
	BaseURL string
	// MerchantID 即 pid
	MerchantID string
	// Key 是商户密钥，来自信封解密后的凭据，不得落日志
	Key string

	SubmitPath string
	APIPath    string

	// DefaultMethod 是未指定支付方式时使用的类型，如 alipay
	DefaultMethod string

	// AllowPrivateHost 仅用于本地联调（只在开发环境打开）。生产必须为 false，
	// 否则配置一个内网地址就能把服务端当成 SSRF 跳板（SEC-007）。
	AllowPrivateHost bool
}

// Provider 是易支付渠道适配器。
type Provider struct {
	cfg    Config
	client *http.Client
}

var _ provider.Provider = (*Provider)(nil)

// New 构造易支付适配器。
func New(cfg Config) (*Provider, error) {
	if cfg.MerchantID == "" || cfg.Key == "" {
		return nil, errors.New("epay: 缺少商户号或密钥")
	}
	if cfg.SubmitPath == "" {
		cfg.SubmitPath = defaultSubmitPath
	}
	if cfg.APIPath == "" {
		cfg.APIPath = defaultAPIPath
	}
	if cfg.DefaultMethod == "" {
		cfg.DefaultMethod = "alipay"
	}

	u, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("epay: BaseURL 非法: %q", cfg.BaseURL)
	}
	if u.Scheme != "https" && !cfg.AllowPrivateHost {
		// 查询接口要在 URL 里带明文商户密钥，走 http 等于把密钥公开
		return nil, errors.New("epay: BaseURL 必须使用 https")
	}
	cfg.BaseURL = u.String()

	return &Provider{cfg: cfg, client: safehttp.Client(cfg.AllowPrivateHost)}, nil
}

// CreatePayment 生成收银台跳转信息。
func (p *Provider) CreatePayment(ctx context.Context, req provider.CreateRequest) (provider.CreateResponse, error) {
	if !req.Amount.IsPositive() {
		return provider.CreateResponse{}, errors.New("epay: 金额必须为正")
	}
	if err := money.Check(req.Amount); err != nil {
		return provider.CreateResponse{}, fmt.Errorf("epay: 金额非法: %w", err)
	}

	method := req.Method
	if method == "" {
		method = p.cfg.DefaultMethod
	}

	params := map[string]string{
		"pid":          p.cfg.MerchantID,
		"type":         method,
		"out_trade_no": req.OutTradeNo,
		"notify_url":   req.NotifyURL,
		"return_url":   req.ReturnURL,
		"name":         sanitizeSubject(req.Subject),
		"money":        money.FormatProvider(req.Amount),
	}
	if req.ClientIP != "" {
		params["clientip"] = req.ClientIP
	}

	params["sign"] = Sign(params, p.cfg.Key)
	params["sign_type"] = "MD5"

	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}

	return provider.CreateResponse{
		HTTPMethod:  http.MethodGet,
		RedirectURL: p.cfg.BaseURL + p.cfg.SubmitPath + "?" + q.Encode(),
		FormFields:  params,
	}, nil
}

// ParseNotification 解析并验签异步通知。
func (p *Provider) ParseNotification(ctx context.Context, in provider.Inbound) (provider.Notification, error) {
	var src url.Values
	switch in.Method {
	// 易支付用 GET 推送；少数二开版本用 POST，两种都接住
	case http.MethodGet:
		src = in.Query
	case http.MethodPost:
		form, err := url.ParseQuery(string(in.Body))
		if err != nil {
			return provider.Notification{}, fmt.Errorf("epay: 解析表单失败: %w", err)
		}
		src = form
		if len(src) == 0 {
			src = in.Query
		}
	default:
		return provider.Notification{}, fmt.Errorf("epay: 不支持的请求方法 %s", in.Method)
	}

	raw := map[string]string{}
	for k := range src {
		raw[k] = src.Get(k)
	}

	if raw["out_trade_no"] == "" || raw["trade_no"] == "" {
		return provider.Notification{}, errors.New("epay: 通知缺少 out_trade_no 或 trade_no")
	}

	amount, err := money.ParseProvider(raw["money"])
	if err != nil {
		return provider.Notification{}, fmt.Errorf("epay: 通知金额非法: %w", err)
	}

	// 商户号必须匹配：否则别人拿自己的易支付账号也能给我们推通知
	merchantOK := raw["pid"] == p.cfg.MerchantID
	signOK := VerifySign(raw, p.cfg.Key)
	// 验完签再去掉 sign：签名值不入库
	delete(raw, "sign")

	status := provider.StatusPending
	switch strings.ToUpper(raw["trade_status"]) {
	case "TRADE_SUCCESS", "SUCCESS":
		status = provider.StatusSucceeded
	case "TRADE_CLOSED", "TRADE_FAILED", "FAILED":
		status = provider.StatusFailed
	case "REFUND", "TRADE_REFUND":
		status = provider.StatusRefunded
	}

	return provider.Notification{
		// 协议没有事件 ID：用 trade_no + 状态合成，同一笔支付的重复通知得到相同的键 → 撞唯一约束 → 幂等
		EventID:           raw["trade_no"] + ":" + strings.ToUpper(raw["trade_status"]),
		PaymentRef:        raw["trade_no"],
		OutTradeNo:        raw["out_trade_no"],
		Amount:            amount,
		Fee:               money.Zero,
		Status:            status,
		Method:            raw["type"],
		Raw:               raw,
		SignatureVerified: signOK && merchantOK,
	}, nil
}

// NotificationAck 返回易支付期望的回执。
func (p *Provider) NotificationAck(in provider.AckInput) provider.AckOutput {
	body := []byte("fail")
	if in.OK() {
		body = []byte("success")
	}
	return provider.AckOutput{
		HTTPStatus:  http.StatusOK,
		ContentType: "text/plain; charset=utf-8",
		Body:        body,
	}
}

type queryResponse struct {
	Code       int         `json:"code"`
	Msg        string      `json:"msg"`
	TradeNo    string      `json:"trade_no"`
	OutTradeNo string      `json:"out_trade_no"`
	Type       string      `json:"type"`
	Money      string      `json:"money"`
	Status     json.Number `json:"status"`
}

// QueryPayment 按商户单号向渠道查单。
func (p *Provider) QueryPayment(ctx context.Context, outTradeNo string) (provider.QueryResult, error) {
	q := url.Values{}
	q.Set("act", "order")
	q.Set("pid", p.cfg.MerchantID)
	q.Set("key", p.cfg.Key)
	q.Set("out_trade_no", outTradeNo)

	endpoint := p.cfg.BaseURL + p.cfg.APIPath + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		// 错误里可能带完整 URL（含商户密钥）
		return provider.QueryResult{}, fmt.Errorf("epay: 构造查询请求失败: %w", plog.StripURL(err))
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return provider.QueryResult{}, fmt.Errorf("epay: 查询请求失败: %w", plog.StripURL(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return provider.QueryResult{}, fmt.Errorf("epay: 查询返回 HTTP %d", resp.StatusCode)
	}

	body, err := safehttp.ReadBody(resp.Body)
	if err != nil {
		return provider.QueryResult{}, fmt.Errorf("epay: 读取查询响应失败: %w", err)
	}

	var qr queryResponse
	if err := json.Unmarshal(body, &qr); err != nil {
		return provider.QueryResult{}, fmt.Errorf("epay: 查询响应不是合法 JSON: %w", err)
	}
	if qr.Code != 1 {
		return provider.QueryResult{Found: false}, nil
	}
	// 对端回的单号必须是我们问的那一笔：渠道把别的单回过来时按「没找到」处理，不拿去入账
	if qr.OutTradeNo != outTradeNo {
		return provider.QueryResult{Found: false}, nil
	}

	amount, err := money.ParseProvider(qr.Money)
	if err != nil {
		return provider.QueryResult{}, fmt.Errorf("epay: 查询金额非法: %w", err)
	}

	status := provider.StatusPending
	if qr.Status.String() == "1" {
		status = provider.StatusSucceeded
	}

	return provider.QueryResult{
		Found:      true,
		PaymentRef: qr.TradeNo,
		Amount:     amount,
		Status:     status,
		Method:     qr.Type,
	}, nil
}

// Sign 按易支付规则计算 MD5 签名。
func Sign(params map[string]string, key string) string {
	sum := md5.Sum([]byte(signPayload(params, key))) //nolint:gosec // 协议规定的签名算法
	return hex.EncodeToString(sum[:])
}

func signPayload(params map[string]string, key string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(params[k])
	}
	sb.WriteString(key)
	return sb.String()
}

// VerifySign 校验通知签名。
func VerifySign(params map[string]string, key string) bool {
	got := params["sign"]
	if got == "" {
		return false
	}
	want := Sign(params, key)
	if len(got) != len(want) {
		return false
	}
	var diff byte
	for i := 0; i < len(want); i++ {
		diff |= lower(got[i]) ^ want[i]
	}
	return diff == 0
}

func lower(b byte) byte {
	if b >= 'A' && b <= 'F' {
		return b + ('a' - 'A')
	}
	return b
}

func sanitizeSubject(s string) string {
	s = strings.NewReplacer("&", "＆", "=", "＝", "\n", " ", "\r", " ").Replace(s)
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
