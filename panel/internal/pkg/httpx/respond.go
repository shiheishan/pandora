package httpx

import (
	"net/http"
	"strconv"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/gin-gonic/gin"
)

// errorBody 是对外错误信封。
type errorBody struct {
	Error errorEnvelope `json:"error"`
}

type errorEnvelope struct {
	Code      errs.Code         `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

func writeError(c *gin.Context, e *errs.Error) {
	if e == nil {
		e = errs.New(errs.Internal)
	}
	id := reqid.From(c.Request.Context())
	body := errorBody{
		Error: errorEnvelope{
			Code:    e.Code,
			Message: e.Message,
			Fields:  e.Fields,
		},
	}
	if id != "" {
		body.Error.RequestID = id
	}
	if e.RetryAfterSeconds > 0 {
		c.Header("Retry-After", strconv.Itoa(e.RetryAfterSeconds))
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.JSON(e.Status(), body)
}

// Fail 把错误记入上下文并中止后续处理器；响应由 Errors 中间件统一写出。
func Fail(c *gin.Context, err error) {
	_ = c.Error(err)
	c.Abort()
}

// OK 返回 200 JSON。
func OK(c *gin.Context, v any) {
	c.JSON(http.StatusOK, v)
}

// Created 返回 201 JSON。
func Created(c *gin.Context, v any) {
	c.JSON(http.StatusCreated, v)
}

// Accepted 返回 202 JSON：请求已登记、由后台任务完成（如批量建用户、签发证书）。
func Accepted(c *gin.Context, v any) {
	c.JSON(http.StatusAccepted, v)
}

// NoContent 返回 204。
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// NoRoute 处理未匹配路由。
func NoRoute(c *gin.Context) {
	Fail(c, errs.New(errs.NotFound))
}

// NoMethod 处理路由存在但方法不对的请求（引擎要开 HandleMethodNotAllowed）：回 405 信封。
// gin 调用前已写好 Allow 头，这里不动它。
func NoMethod(c *gin.Context) {
	Fail(c, errs.New(errs.MethodNotAllowed))
}
