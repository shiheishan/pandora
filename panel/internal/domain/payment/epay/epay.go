// Package epay 实现「易支付」（EPay / 彩虹易支付）协议适配器。
//
// 该协议是国内聚合支付站点的事实标准，Xboard 等面板普遍对接。
// 协议要点（也是最容易踩坑的地方）：
//
//  1. 金额是**元为单位的十进制字符串**（"9.90"），不是分。全程走
//     payment.ParseMinor / FormatMinor，绝不经过 float。
//  2. 签名是 MD5：非空参数按名 ASCII 升序拼成 a=1&b=2，末尾**直接**
//     追加商户密钥（不是 &key=xxx），再取 MD5 小写。拼接时**不做 URL 编码**。
//  3. 异步通知是 **GET**，参数在 query string 里。
//  4. 通知处理成功必须回**纯文本 success**；回 JSON 或别的内容渠道会持续重推。
//  5. 协议本身没有事件 ID。幂等键由 trade_no + trade_status 合成。
//  6. 绝大多数易支付站点**不提供退款 API**，Refund 返回 ErrNotSupported，
//     由人工走渠道后台并在平台登记。
package epay

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/domain/payment"
)

const (
	defaultSubmitPath = "/submit.php"
	defaultAPIPath    = "/api.php"
	defaultTimeout    = 10 * time.Second
)

// Config 是单个易支付商户的接入配置。
type Config struct {
	// Code 是平台内的渠道标识，对应 payment_providers.code
	Code string
	// BaseURL 形如 https://pay.example.com
	BaseURL string
	// MerchantID 即 pid
	MerchantID string
	// Key 是商户密钥，来自信封解密后的凭据（SEC-010），不得落日志
	Key string

	SubmitPath string
	APIPath    string

	// DefaultMethod 是未指定支付方式时使用的类型，如 alipay
	DefaultMethod string

	// AllowPrivateHost 仅用于本地联调。生产必须为 false，
	// 否则配置一个内网地址就能把服务端当成 SSRF 跳板（SEC-007）。
	AllowPrivateHost bool
}

type Provider struct {
	cfg    Config
	client *http.Client
	// 币种固定：易支付只结算人民币
	currency string
	exponent int32
}

func New(cfg Config) (*Provider, error) {
	if cfg.Code == "" {
		return nil, errors.New("epay: 缺少渠道 code")
	}
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
	if !cfg.AllowPrivateHost {
		if err := assertPublicHost(u.Hostname()); err != nil {
			return nil, fmt.Errorf("epay: %w", err)
		}
	}
	cfg.BaseURL = u.String()

	return &Provider{
		cfg: cfg,
		client: &http.Client{
			Timeout: defaultTimeout,
			// 不跟随跳转：查询接口的响应应当直出，跳转多半意味着被劫持或配错
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		currency: "CNY",
		exponent: payment.Exponent("CNY"),
	}, nil
}

func (p *Provider) Code() string { return p.cfg.Code }

//------------------------------------------------------------------------------
// 发起支付
//------------------------------------------------------------------------------

func (p *Provider) CreatePayment(ctx context.Context, req payment.CreateRequest) (*payment.CreateResponse, error) {
	if !strings.EqualFold(req.Currency, p.currency) {
		return nil, fmt.Errorf("epay: 仅支持 %s，收到 %s", p.currency, req.Currency)
	}
	if req.Amount <= 0 {
		return nil, errors.New("epay: 金额必须为正")
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
		"money":        payment.FormatMinor(req.Amount, p.exponent),
	}
	if req.ClientIP != "" {
		params["clientip"] = req.ClientIP
	}

	params["sign"] = Sign(params, p.cfg.Key)
	params["sign_type"] = "MD5"

	// 用 GET 跳转：易支付两种都支持，GET 让前端无需渲染自动提交表单。
	// 这里才做 URL 编码 —— 签名阶段用的是未编码的原始值。
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}

	return &payment.CreateResponse{
		HTTPMethod:  http.MethodGet,
		RedirectURL: p.cfg.BaseURL + p.cfg.SubmitPath + "?" + q.Encode(),
		FormFields:  params,
	}, nil
}

//------------------------------------------------------------------------------
// 异步通知
//------------------------------------------------------------------------------

func (p *Provider) ParseNotification(ctx context.Context, r *http.Request) (*payment.Notification, error) {
	// 易支付用 GET 推送；少数二开版本用 POST，两种都接住
	var src url.Values
	switch r.Method {
	case http.MethodGet:
		src = r.URL.Query()
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			return nil, fmt.Errorf("epay: 解析表单失败: %w", err)
		}
		src = r.PostForm
		if len(src) == 0 {
			src = r.URL.Query()
		}
	default:
		return nil, fmt.Errorf("epay: 不支持的请求方法 %s", r.Method)
	}

	raw := map[string]string{}
	for k := range src {
		raw[k] = src.Get(k)
	}

	if raw["out_trade_no"] == "" || raw["trade_no"] == "" {
		return nil, errors.New("epay: 通知缺少 out_trade_no 或 trade_no")
	}

	amount, err := payment.ParseMinor(raw["money"], p.exponent)
	if err != nil {
		return nil, fmt.Errorf("epay: 通知金额非法: %w", err)
	}

	// 商户号必须匹配：否则别人拿自己的易支付账号也能给我们推通知
	merchantOK := raw["pid"] == p.cfg.MerchantID
	signOK := VerifySign(raw, p.cfg.Key)

	status := payment.StatusPending
	switch strings.ToUpper(raw["trade_status"]) {
	case "TRADE_SUCCESS", "SUCCESS":
		status = payment.StatusSucceeded
	case "TRADE_CLOSED", "TRADE_FAILED", "FAILED":
		status = payment.StatusFailed
	case "REFUND", "TRADE_REFUND":
		status = payment.StatusRefunded
	}

	return &payment.Notification{
		// 协议没有事件 ID。用 trade_no + 状态合成：
		// 同一笔支付的重复通知得到相同的键 → 撞唯一约束 → 幂等；
		// 后续若有退款通知则是另一个键，不会被误判为重复。
		EventID:           raw["trade_no"] + ":" + strings.ToUpper(raw["trade_status"]),
		PaymentRef:        raw["trade_no"],
		OutTradeNo:        raw["out_trade_no"],
		Amount:            amount,
		Currency:          p.currency,
		Status:            status,
		Method:            raw["type"],
		Raw:               raw,
		SignatureVerified: signOK && merchantOK,
	}, nil
}

// NotificationAck 返回易支付期望的回执。
//
// 三条协议约束：
//   - 成功必须是纯文本 success，多一个字节都不行；
//   - HTTP 状态恒为 200：易支付对非 2xx 会按固定间隔无限重推，
//     即使是「签名错误」这种重推一万次也不会变好的情况；
//   - 失败回 fail，渠道按其策略有限重试。
func (p *Provider) NotificationAck(in payment.AckInput) payment.AckOutput {
	body := []byte("fail")
	if in.OK() {
		body = []byte("success")
	}
	return payment.AckOutput{
		HTTPStatus:  http.StatusOK,
		ContentType: "text/plain; charset=utf-8",
		Body:        body,
	}
}

//------------------------------------------------------------------------------
// 主动查询（PAY-009 降级补偿）
//------------------------------------------------------------------------------

type queryResponse struct {
	Code       int    `json:"code"`
	Msg        string `json:"msg"`
	TradeNo    string `json:"trade_no"`
	OutTradeNo string `json:"out_trade_no"`
	Type       string `json:"type"`
	Money      string `json:"money"`
	// 部分站点返回数字 1/0，部分返回字符串，故用 json.Number 兼容
	Status json.Number `json:"status"`
}

func (p *Provider) QueryPayment(ctx context.Context, outTradeNo string) (*payment.QueryResult, error) {
	q := url.Values{}
	q.Set("act", "order")
	q.Set("pid", p.cfg.MerchantID)
	q.Set("key", p.cfg.Key) // 查询接口用明文密钥，故强制 https
	q.Set("out_trade_no", outTradeNo)

	endpoint := p.cfg.BaseURL + p.cfg.APIPath + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("epay: 查询请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("epay: 查询返回 HTTP %d", resp.StatusCode)
	}

	// 限制响应体积，避免恶意/故障端点拖垮内存
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("epay: 读取查询响应失败: %w", err)
	}

	var qr queryResponse
	if err := json.Unmarshal(body, &qr); err != nil {
		return nil, fmt.Errorf("epay: 查询响应不是合法 JSON: %w", err)
	}
	if qr.Code != 1 {
		// code != 1 通常表示订单不存在，不是错误
		return &payment.QueryResult{Found: false}, nil
	}

	amount, err := payment.ParseMinor(qr.Money, p.exponent)
	if err != nil {
		return nil, fmt.Errorf("epay: 查询金额非法: %w", err)
	}

	status := payment.StatusPending
	if qr.Status.String() == "1" {
		status = payment.StatusSucceeded
	}

	return &payment.QueryResult{
		Found:      true,
		PaymentRef: qr.TradeNo,
		Amount:     amount,
		Currency:   p.currency,
		Status:     status,
		Method:     qr.Type,
	}, nil
}

// Refund 明确声明不支持。
//
// 易支付协议本身没有标准退款接口。返回 ErrNotSupported 让上层把退款单
// 转为「需人工在渠道后台操作后回平台登记」，而不是假装退成功 ——
// 后者会让账本记了一笔实际没发生的资金流出。
func (p *Provider) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResult, error) {
	return nil, fmt.Errorf("epay(%s): %w，请在渠道后台操作后回平台登记",
		p.cfg.Code, payment.ErrNotSupported)
}

//------------------------------------------------------------------------------
// 签名
//------------------------------------------------------------------------------

// Sign 按易支付规则计算 MD5 签名。
//
// 规则（顺序不能错）：
//  1. 剔除 sign、sign_type 以及**值为空**的参数
//  2. 参数名按 ASCII 升序
//  3. 拼成 k1=v1&k2=v2，值保持原样，**不做 URL 编码**
//  4. 末尾直接追加商户密钥（不是 &key=xxx）
//  5. MD5 取小写十六进制
//
// 第 3 步是最常见的错误来源：很多实现顺手用了 url.Values.Encode()，
// 那会把中文商品名编码成 %E4%B8%AD，签名必然对不上。
func Sign(params map[string]string, key string) string {
	sum := md5.Sum([]byte(signPayload(params, key)))
	return hex.EncodeToString(sum[:])
}

// signPayload 构造待哈希的原文。
// 单独抽出来是为了让测试能精确断言拼接结果 —— 签名对不上时，
// 99% 的问题出在这个串上（少剔了空值、编码了中文、排序不对），而不是 MD5。
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
//
// 用 subtle 风格的恒定时间比较意义有限（MD5 本身已不抗碰撞，
// 且签名值会随响应时间泄露），但比较本身不该引入额外侧信道，
// 故仍逐字节等长比较。
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

//------------------------------------------------------------------------------
// 辅助
//------------------------------------------------------------------------------

// sanitizeSubject 清理商品名。
// & 和 = 会破坏签名串的结构，中文本身没问题但控制字符要去掉。
func sanitizeSubject(s string) string {
	s = strings.NewReplacer("&", "＆", "=", "＝", "\n", " ", "\r", " ").Replace(s)
	s = strings.TrimSpace(s)
	if s == "" {
		s = "订阅服务"
	}
	// 易支付多数站点对商品名长度有限制，按字符数截断
	r := []rune(s)
	if len(r) > 64 {
		s = string(r[:64])
	}
	return s
}

// assertPublicHost 拒绝内网地址（SEC-007）。
//
// 支付渠道地址来自管理员配置，看似可信，但配置面被攻破或管理员被钓鱼时，
// 一个指向 169.254.169.254 的「支付渠道」就能让服务端去读云元数据。
func assertPublicHost(host string) error {
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("无法解析主机 %q: %w", host, err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("主机 %q 解析到非公网地址 %s", host, ip)
		}
	}
	return nil
}
