// Package provider 定义支付渠道适配器的接口与归一化类型。
//
// 核心只认这个接口：订单、账本、结算的代码不知道对面是易支付还是别的渠道。
// 适配器只做协议（签名、报文、回执、查单），不碰数据库。
package provider

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

// Status 是跨渠道归一化后的支付状态。
type Status string

const (
	StatusPending   Status = "pending"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusRefunded  Status = "refunded"
)

// ErrNotSupported 表示渠道不支持这个操作（如 demo 不能查单）。
var ErrNotSupported = errors.New("payment provider: 不支持的操作")

// CreateRequest 是发起一次支付的请求。
type CreateRequest struct {
	OutTradeNo string // 商户单号，回调与查单时原样带回
	Amount     decimal.Decimal
	Currency   money.Currency
	Subject    string // 商品名称
	Method     string // 渠道内的支付方式，如 alipay / wxpay；空串交给渠道默认
	NotifyURL  string
	ReturnURL  string
	ClientIP   string
}

// CreateResponse 描述如何把用户引导到收银台。
type CreateResponse struct {
	// HTTPMethod 为 GET 时用 RedirectURL；为 POST 时前端用 FormFields 自动提交表单。
	HTTPMethod  string
	RedirectURL string
	FormFields  map[string]string
	// ProviderRef 是渠道侧的支付单标识，建单阶段可能还没有（易支付不给）。
	ProviderRef string
}

// Inbound 是一次渠道回调的原始请求，由 handler 从 HTTP 请求里取出。
type Inbound struct {
	Method string      // GET / POST
	Query  url.Values  // 查询串
	Header http.Header // 请求头
	Body   []byte      // 请求体（已限长）
}

// Notification 是归一化后的异步通知。
type Notification struct {
	// EventID 作为幂等键写入 payment_events.provider_event_id；渠道没有事件 ID 时由适配器合成一个对「同一事实」稳定的值。
	EventID string
	// PaymentRef 是渠道支付流水号，写入 payments.provider_payment_id。
	PaymentRef string
	// OutTradeNo 是我方商户单号，按它找支付意图。
	OutTradeNo string
	Amount     decimal.Decimal
	Fee        decimal.Decimal // 渠道手续费；没有就是 0
	Currency   money.Currency
	Status     Status
	Method     string
	// Raw 是报文原文（去掉签名值），留作证据。
	Raw map[string]string
	// SignatureVerified 为 false 时上游必须拒绝，不进业务。
	SignatureVerified bool
}

// QueryResult 是主动查单的结果。
type QueryResult struct {
	Found      bool
	PaymentRef string
	Amount     decimal.Decimal
	Currency   money.Currency
	Status     Status
	Method     string
}

// AckInput 是回执的依据：验签结果与业务处理结果。
type AckInput struct {
	SignatureValid bool
	// HandlerError 非空表示业务处理失败（找不到意图、金额不符、数据库错误等）。
	HandlerError   error
	Processed      bool
	AlreadyHandled bool
	PaymentID      string
}

// OK 报告是否可以告诉渠道「这条通知我收下了」。
func (a AckInput) OK() bool { return a.SignatureValid && a.HandlerError == nil }

// AckOutput 是回给渠道的 HTTP 应答。
type AckOutput struct {
	HTTPStatus  int
	ContentType string
	Body        []byte
}

// Provider 是支付渠道适配器。
type Provider interface {
	// CreatePayment 生成收银台跳转信息。
	CreatePayment(ctx context.Context, req CreateRequest) (CreateResponse, error)
	// ParseNotification 解析并验签回调。验签失败不返回 error，而是 SignatureVerified=false；
	// 报文本身无法解析才返回 error。
	ParseNotification(ctx context.Context, in Inbound) (Notification, error)
	// NotificationAck 决定回给渠道什么（各渠道要求不同：易支付要 200 + 纯文本 success）。
	NotificationAck(in AckInput) AckOutput
	// QueryPayment 按商户单号向渠道查单；不支持时返回 ErrNotSupported。
	QueryPayment(ctx context.Context, outTradeNo string) (QueryResult, error)
}
