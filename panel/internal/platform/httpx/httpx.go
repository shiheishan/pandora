// Package httpx 提供统一的响应与错误模型。
//
// SEC-006 要求「错误响应不暴露框架版本、内部路径和对象存在性」。
// 本包的做法是：错误分成「对外码」与「对内详情」两层，
// 对外只吐稳定的错误码与中性文案，详情只进日志。
// 由于 Error 结构体不导出 internal 字段的 JSON 标签，泄露在类型层面就不可能发生。
package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// Code 是对外错误码。列表是封闭的 —— 新增错误必须在这里登记，
// 避免有人随手 fmt.Errorf 就把内部信息漏到公网。
type Code string

const (
	CodeBadRequest       Code = "bad_request"
	CodeUnauthorized     Code = "unauthorized"
	CodeForbidden        Code = "forbidden"
	CodeNotFound         Code = "not_found"
	CodeConflict         Code = "conflict"
	CodeValidationFailed Code = "validation_failed"
	CodeRateLimited      Code = "rate_limited"
	CodeIdempotencyReuse Code = "idempotency_key_reuse"
	CodeUnavailable      Code = "service_unavailable"
	CodeInternal         Code = "internal_error"
)

var statusByCode = map[Code]int{
	CodeBadRequest:       http.StatusBadRequest,
	CodeUnauthorized:     http.StatusUnauthorized,
	CodeForbidden:        http.StatusForbidden,
	CodeNotFound:         http.StatusNotFound,
	CodeConflict:         http.StatusConflict,
	CodeValidationFailed: http.StatusUnprocessableEntity,
	CodeRateLimited:      http.StatusTooManyRequests,
	CodeIdempotencyReuse: http.StatusConflict,
	CodeUnavailable:      http.StatusServiceUnavailable,
	CodeInternal:         http.StatusInternalServerError,
}

// Error 同时承载对外与对内两份信息。
type Error struct {
	Code Code
	// Message 会返回给调用方，必须是中性的、不含内部结构的文案。
	Message string
	// Fields 用于表单校验错误，键是字段名。
	Fields map[string]string
	// internal 只进日志，永不出现在响应体里。
	internal error
}

func (e *Error) Error() string {
	if e.internal != nil {
		return string(e.Code) + ": " + e.internal.Error()
	}
	return string(e.Code) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.internal }

// WithInternal 附加只进日志的原因。
func (e *Error) WithInternal(err error) *Error {
	e.internal = err
	return e
}

func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func Invalid(fields map[string]string) *Error {
	return &Error{Code: CodeValidationFailed, Message: "请求参数校验未通过", Fields: fields}
}

// NotFoundOrForbidden 是 SEC-006「不暴露对象存在性」的统一出口。
//
// 「无权访问」与「对象不存在」必须返回同一个响应，否则调用方能靠状态码差异
// 探测出某个 ID 是否存在。全平台一律用 404 + 同一文案。
func NotFoundOrForbidden() *Error {
	return &Error{Code: CodeNotFound, Message: "资源不存在或无权访问"}
}

func Internal(err error) *Error {
	return &Error{Code: CodeInternal, Message: "服务暂时不可用，请稍后重试", internal: err}
}

//------------------------------------------------------------------------------
// 响应
//------------------------------------------------------------------------------

type errorBody struct {
	Error struct {
		Code      Code              `json:"code"`
		Message   string            `json:"message"`
		Fields    map[string]string `json:"fields,omitempty"`
		RequestID string            `json:"request_id,omitempty"`
	} `json:"error"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// 统一去掉可能泄露技术栈的响应头（SEC-006）
	w.Header().Del("X-Powered-By")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

const jsonContentType = "application/json; charset=utf-8"

// PreparedResponse is an immutable, already encoded HTTP response. Preparing
// the bytes once lets a business transaction persist the exact replay payload
// that the handler writes after commit.
type PreparedResponse struct {
	status      int
	body        []byte
	contentType string
}

// PrepareJSON applies the same Encoder.Encode contract as JSON, including its
// trailing newline. The returned body is kept private so callers cannot mutate
// durable idempotency evidence between commit and the HTTP write.
func PrepareJSON(status int, v any) (PreparedResponse, error) {
	if status < http.StatusOK || status > 999 {
		return PreparedResponse{}, fmt.Errorf("httpx: invalid prepared response status %d", status)
	}
	var body bytes.Buffer
	if v != nil {
		if err := json.NewEncoder(&body).Encode(v); err != nil {
			return PreparedResponse{}, fmt.Errorf("httpx: encode prepared JSON: %w", err)
		}
	}
	return PreparedResponse{
		status:      status,
		body:        append([]byte(nil), body.Bytes()...),
		contentType: jsonContentType,
	}, nil
}

func (p PreparedResponse) StatusCode() int { return p.status }

func (p PreparedResponse) ContentType() string { return p.contentType }

// BodyBytes returns a defensive copy suitable for durable storage.
func (p PreparedResponse) BodyBytes() []byte { return append([]byte(nil), p.body...) }

// WritePrepared writes the bytes produced by PrepareJSON without encoding the
// value a second time.
func WritePrepared(w http.ResponseWriter, p PreparedResponse) {
	w.Header().Set("Content-Type", p.contentType)
	w.Header().Del("X-Powered-By")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(p.status)
	if len(p.body) > 0 {
		_, _ = w.Write(p.body)
	}
}

// Fail 把错误写成响应，并把内部详情记入日志。
func Fail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var he *Error
	if !errors.As(err, &he) {
		he = Internal(err)
	}

	status, ok := statusByCode[he.Code]
	if !ok {
		status = http.StatusInternalServerError
	}

	reqID := RequestIDFrom(r.Context())

	// 5xx 记 error 级并带上内部原因；4xx 记 info 级，避免正常的客户端错误刷屏。
	attrs := []any{
		slog.String("code", string(he.Code)),
		slog.String("request_id", reqID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", status),
	}
	if he.internal != nil {
		attrs = append(attrs, slog.String("internal", he.internal.Error()))
	}
	if status >= 500 {
		log.Error("请求失败", attrs...)
	} else {
		log.Info("请求被拒绝", attrs...)
	}

	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "60")
	}

	var body errorBody
	body.Error.Code = he.Code
	body.Error.Message = he.Message
	body.Error.Fields = he.Fields
	body.Error.RequestID = reqID
	JSON(w, status, body)
}

func OK(w http.ResponseWriter, v any)      { JSON(w, http.StatusOK, v) }
func Created(w http.ResponseWriter, v any) { JSON(w, http.StatusCreated, v) }
func NoContent(w http.ResponseWriter)      { w.WriteHeader(http.StatusNoContent) }

// DecodeJSON 读取并校验请求体。限制体积，避免超大 body 打爆内存。
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	const maxBody = 1 << 20 // 1 MiB
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // 拼错字段名要立刻报错，而不是静默按默认值处理

	if err := dec.Decode(dst); err != nil {
		return New(CodeBadRequest, "请求体不是合法的 JSON").WithInternal(err)
	}
	if dec.More() {
		return New(CodeBadRequest, "请求体包含多余内容")
	}
	return nil
}
