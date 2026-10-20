// Package errs 是对外错误的唯一定义：封闭的错误码表、HTTP 状态和中性文案。
//
// 对外只给码和中文文案，内部原因只进日志。前端 core/api.ts 的 SERVER_ERROR_CODES
// 与 Codes() 由 TestServerErrorCodesMatchFrontend 对齐，新增码两处同改。
package errs

import (
	"errors"
	"net/http"
)

// Code 是对外错误码。
type Code string

const (
	BadRequest       Code = "bad_request"
	Unauthorized     Code = "unauthorized"
	Forbidden        Code = "forbidden"
	ReauthRequired   Code = "reauth_required"
	NotFound         Code = "not_found"
	Conflict         Code = "conflict"
	IdempotencyReuse Code = "idempotency_key_reuse"
	QuoteChanged     Code = "quote_changed"
	OrderPending     Code = "order_pending"
	OrderLapsed      Code = "order_lapsed"
	// PaymentInProgress 是同一张单上一次发起支付还在去渠道建单（订单仍可付，稍后重发）。
	PaymentInProgress Code = "payment_in_progress"
	ValidationFailed  Code = "validation_failed"
	RateLimited       Code = "rate_limited"
	Internal          Code = "internal_error"
	Unavailable       Code = "service_unavailable"
	// MethodNotAllowed 是路由存在但方法不对（405，引擎的 NoMethod）。
	MethodNotAllowed Code = "method_not_allowed"
	// PayloadTooLarge 是 JSON 请求体超过 httpx.MaxBodyBytes 或幂等中间件的上限（413）。
	PayloadTooLarge Code = "payload_too_large"
	// Timeout 是请求 ctx 到期（httpx.Timeout）后 handler 报错（504，由 httpx.Errors 改写）。
	Timeout Code = "timeout"
)

type spec struct {
	status  int
	message string
}

var specs = map[Code]spec{
	BadRequest:        {http.StatusBadRequest, "请求格式不正确"},
	Unauthorized:      {http.StatusUnauthorized, "凭据无效或已过期"},
	Forbidden:         {http.StatusForbidden, "没有权限执行此操作"},
	ReauthRequired:    {http.StatusForbidden, "此操作需要重新验证身份"},
	NotFound:          {http.StatusNotFound, "资源不存在或无权访问"},
	Conflict:          {http.StatusConflict, "数据已被修改，请刷新后重试"},
	IdempotencyReuse:  {http.StatusConflict, "同一个幂等键不能用于不同的请求"},
	QuoteChanged:      {http.StatusConflict, "价格已更新，请重新确认"},
	OrderPending:      {http.StatusConflict, "已有一张待支付的订单"},
	OrderLapsed:       {http.StatusConflict, "订单已过付款期限"},
	PaymentInProgress: {http.StatusConflict, "支付正在发起，请稍候"},
	ValidationFailed:  {http.StatusUnprocessableEntity, "请求参数校验未通过"},
	RateLimited:       {http.StatusTooManyRequests, "操作太频繁，请稍后再试"},
	Internal:          {http.StatusInternalServerError, "服务暂时不可用，请稍后重试"},
	Unavailable:       {http.StatusServiceUnavailable, "服务暂时不可用，请稍后重试"},
	MethodNotAllowed:  {http.StatusMethodNotAllowed, "请求方法不被允许"},
	PayloadTooLarge:   {http.StatusRequestEntityTooLarge, "请求内容过大"},
	Timeout:           {http.StatusGatewayTimeout, "请求超时，请稍后再试"},
}

// Codes 返回全部对外错误码，供对齐测试使用。
func Codes() []Code {
	out := make([]Code, 0, len(specs))
	for c := range specs {
		out = append(out, c)
	}
	return out
}

// Error 同时承载对外信息与只进日志的内部原因。
type Error struct {
	Code    Code
	Message string
	// Fields 是字段级校验错误，键为 JSON 字段名。
	Fields map[string]string
	// RetryAfterSeconds 大于 0 时，响应带 Retry-After 头。
	RetryAfterSeconds int
	cause             error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return string(e.Code) + ": " + e.cause.Error()
	}
	return string(e.Code) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// Status 返回该错误对应的 HTTP 状态码；未登记的码按 500 处理。
func (e *Error) Status() int {
	if s, ok := specs[e.Code]; ok {
		return s.status
	}
	return http.StatusInternalServerError
}

// New 用缺省文案构造错误。
func New(code Code) *Error {
	return &Error{Code: code, Message: specs[code].message}
}

// Newf 用自定义文案构造错误；文案会返回给调用方，必须中性、不含内部结构。
func Newf(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Wrap 构造带内部原因的错误；原因只进日志。
func Wrap(code Code, cause error) *Error {
	return &Error{Code: code, Message: specs[code].message, cause: cause}
}

// Invalid 构造字段校验错误（422）。
func Invalid(fields map[string]string) *Error {
	return &Error{Code: ValidationFailed, Message: specs[ValidationFailed].message, Fields: fields}
}

// As 把任意错误转成对外错误；不是 *Error 的一律当作 internal_error，原因保留在 cause 里。
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Wrap(Internal, err)
}
