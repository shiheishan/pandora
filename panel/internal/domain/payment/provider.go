package payment

import (
	"context"
	"net/http"
)

// Status 是跨渠道归一化后的支付状态。
type Status string

const (
	StatusPending   Status = "pending"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusRefunded  Status = "refunded"
)

// CreateRequest 是发起一次支付的请求。
type CreateRequest struct {
	OutTradeNo string // 平台订单号，回调时原样带回
	Amount     int64  // 币种最小单位
	Currency   string
	Subject    string // 商品名称
	Method     string // 渠道内的支付方式，如 alipay / wxpay
	NotifyURL  string
	ReturnURL  string
	ClientIP   string
}

// CreateResponse 描述如何把用户引导到收银台。
type CreateResponse struct {
	// HTTPMethod 为 GET 时用 RedirectURL；为 POST 时前端需用 FormFields 自动提交表单。
	HTTPMethod  string
	RedirectURL string
	FormFields  map[string]string
	// ProviderRef 是渠道侧的支付单标识，创建阶段可能还没有。
	ProviderRef string
	// ExpiresInSeconds 为 0 表示渠道未声明有效期。
	ExpiresInSeconds int
}

// Notification 是归一化后的异步通知。
type Notification struct {
	// EventID 作为幂等键写入 payment_events.provider_event_id。
	// 渠道若不提供事件 ID，适配器负责合成一个对「同一事实」稳定的值。
	EventID    string
	PaymentRef string // 渠道支付单号
	// OutTradeNo 是我方对外的短单号（orders.order_no），绝大多数渠道只回传这个。
	OutTradeNo string
	// OrderID 是内部 UUID，仅当渠道能原样回传我方自定义字段时才有值。
	// 与 OutTradeNo 分开是必要的：把 UUID 塞进 OutTradeNo 会让上游拿它去
	// 匹配 order_no，结果是永远查不到订单。
	OrderID   string
	Amount    int64
	FeeAmount int64 // 渠道实际收取的手续费（最小货币单位）
	Currency  string
	Status    Status
	Method    string
	Raw       map[string]string
	// SignatureVerified 为 false 时，上游必须拒绝并记录，绝不可继续处理。
	SignatureVerified bool
}

// QueryResult 是主动查询的结果，用于 PAY-009 的降级补偿：
// 渠道故障期间不猜测支付结果，恢复后靠查询与对账把状态补齐。
type QueryResult struct {
	Found      bool
	PaymentRef string
	Amount     int64
	Currency   string
	Status     Status
	Method     string
}

// AckInput 是回执决策的依据：既有验签结果，也有业务处理结果。
type AckInput struct {
	SignatureValid bool
	// HandlerError 非空表示业务处理失败（订单不存在、金额不符、数据库错误等）
	HandlerError   error
	Processed      bool
	AlreadyHandled bool
	PaymentID      string
	SubscriptionID string
	LedgerTxnID    string
}

// OK 报告是否可以告诉渠道「这条通知我收下了」。
func (a AckInput) OK() bool {
	return a.SignatureValid && a.HandlerError == nil
}

type AckOutput struct {
	HTTPStatus  int
	ContentType string
	Body        []byte
}

type RefundRequest struct {
	PaymentRef string
	OutTradeNo string
	Amount     int64
	Currency   string
	Reason     string
}

type RefundResult struct {
	RefundRef string
	Status    Status
}

// Provider 是支付渠道适配器（PAY-002）。
//
// 「更换渠道不修改核心订单逻辑」的落点：核心只认这个接口，
// 订单、账本、订阅激活的代码不知道对面是易支付还是别的什么。
type Provider interface {
	Code() string

	CreatePayment(ctx context.Context, req CreateRequest) (*CreateResponse, error)

	// ParseNotification 解析并验签异步通知。
	// 验签失败不返回 error，而是把 SignatureVerified 置 false ——
	// 这样上游能把「签名不对」和「解析崩了」区分开，前者要记安全事件。
	ParseNotification(ctx context.Context, r *http.Request) (*Notification, error)

	// NotificationAck 决定回给渠道什么。
	//
	// 交给适配器而不是网关统一处理，是因为各渠道的期望差异极大：
	// 易支付要 200 + 纯文本 success，回 JSON 会被判投递失败并持续重推；
	// 而带重试退避的渠道更希望用 4xx/5xx 表达「别再推了」或「稍后再推」。
	NotificationAck(in AckInput) AckOutput

	QueryPayment(ctx context.Context, outTradeNo string) (*QueryResult, error)

	// Refund 不支持时返回 ErrNotSupported。
	// 明确报错而不是假装成功 —— 退款是资金动作，静默失败会造成账实不符。
	Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)
}

// Registry 按渠道 code 查找适配器实例。
type Registry struct {
	providers map[string]Provider
}

func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

func (r *Registry) Register(p Provider) { r.providers[p.Code()] = p }

func (r *Registry) Get(code string) (Provider, bool) {
	p, ok := r.providers[code]
	return p, ok
}

func (r *Registry) Codes() []string {
	out := make([]string, 0, len(r.providers))
	for c := range r.providers {
		out = append(out, c)
	}
	return out
}
