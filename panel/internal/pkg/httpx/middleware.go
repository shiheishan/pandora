// Package httpx 是新栈的 gin 中间件、统一错误响应与请求绑定。不碰数据库。
package httpx

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// RequestID 规范化 X-Request-ID，回写响应头，并把带 req_id 字段的 logger 放进请求 ctx。
func RequestID(base *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := reqid.Normalize(c.GetHeader(reqid.Header))
		c.Header(reqid.Header, id)
		c.Request = c.Request.WithContext(plog.WithReqID(c.Request.Context(), base, id))
		c.Set("req_id", id)
		c.Next()
	}
}

// route 返回路由模板；未匹配时为 "-"。日志里只写模板，绝不写原始路径（路径段里可能有令牌）。
func route(c *gin.Context) string {
	if p := c.FullPath(); p != "" {
		return p
	}
	return "-"
}

// AccessLog 每个请求写一行 access 日志。
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		status := c.Writer.Status()
		fields := []zap.Field{
			zap.String("method", c.Request.Method),
			zap.String("route", route(c)),
			zap.Int("status", status),
			zap.Float64("dur_ms", float64(time.Since(start).Microseconds())/1000),
			zap.Int("bytes", c.Writer.Size()),
		}
		l := plog.From(c.Request.Context())
		if status >= 500 {
			l.Error("access", fields...)
			return
		}
		l.Info("access", fields...)
	}
}

// Recovery 捕获 panic，记日志并回 500 信封；连接已断时只记日志。
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			l := plog.From(c.Request.Context())
			if err, ok := rec.(error); ok && brokenPipe(err) {
				l.Warn("client gone", zap.String("route", route(c)), zap.Error(err))
				c.Abort()
				return
			}
			l.Error("panic", zap.String("route", route(c)), zap.Any("panic", rec), zap.Stack("stack"))
			if !c.Writer.Written() {
				writeError(c, errs.New(errs.Internal))
			}
			c.Abort()
		}()
		c.Next()
	}
}

func brokenPipe(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) {
		var se *os.SyscallError
		if errors.As(op.Err, &se) {
			return errors.Is(se.Err, syscall.EPIPE) || errors.Is(se.Err, syscall.ECONNRESET)
		}
	}
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}

// SecurityHeaders 写通用安全响应头。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		c.Next()
	}
}

// Timeout 给请求 ctx 设期限；exempt 返回 true 的请求（如 SSE）不设。
func Timeout(d time.Duration, exempt func(*gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if exempt != nil && exempt(c) {
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// Concurrency 限制同时处理的请求数，满了立即回 503，不排队。
func Concurrency(n int) gin.HandlerFunc {
	sem := make(chan struct{}, n)
	return func(c *gin.Context) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			c.Next()
		default:
			writeError(c, errs.New(errs.Unavailable))
			c.Abort()
		}
	}
}

// Errors 把处理器经 Fail 记下的错误统一写成信封；5xx 记 Error 日志，原因只进日志。
func Errors() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if len(c.Errors) == 0 || c.Writer.Written() {
			return
		}
		raw := c.Errors.Last().Err
		e := errs.As(raw)
		l := plog.From(c.Request.Context())
		if e.Status() >= 500 {
			l.Error("request failed", zap.String("route", route(c)), zap.String("code", string(e.Code)), zap.Error(raw))
		} else {
			l.Debug("request rejected", zap.String("route", route(c)), zap.String("code", string(e.Code)), zap.Error(raw))
		}
		writeError(c, e)
	}
}
