// Package httpx 是新栈的 gin 中间件、统一错误响应与请求绑定。不碰数据库。
package httpx

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/reqstats"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// RequestID 规范化 X-Request-ID，回写响应头，把带 req_id 字段的 logger 与来源信息（IP、UA）放进请求 ctx，
// 并放一个每请求计数器（reqstats），供访问日志与指标读取。
func RequestID(base *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := reqid.Normalize(c.GetHeader(reqid.Header))
		c.Header(reqid.Header, id)
		ctx := plog.WithReqID(c.Request.Context(), base, id)
		ctx = reqmeta.With(ctx, reqmeta.Meta{IP: c.ClientIP(), UserAgent: c.Request.UserAgent()})
		ctx = reqstats.With(ctx, reqstats.New())
		c.Request = c.Request.WithContext(ctx)
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

// AccessLog 每个请求写一行 access 日志。SQL 条数与耗时、Redis 命令数（pipeline 按命令数）写入 sql_n/sql_ms/redis_n；没有计数器时为 0。
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		status := c.Writer.Status()
		st := reqstats.From(c.Request.Context())
		fields := []zap.Field{
			zap.String("method", c.Request.Method),
			zap.String("route", route(c)),
			zap.Int("status", status),
			zap.Float64("dur_ms", float64(time.Since(start).Microseconds())/1000),
			zap.Int("bytes", c.Writer.Size()),
			zap.Int64("sql_n", st.SQLCount()),
			zap.Float64("sql_ms", float64(st.SQLDuration().Microseconds())/1000),
			zap.Int64("redis_n", st.RedisCount()),
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

// hstsValue 是生产下的 HSTS：一年；不带 includeSubDomains（站长的其他子域是否全走 https 不归面板管），不带 preload。
const hstsValue = "max-age=31536000"

// permissionsPolicy 关掉面板用不到的浏览器能力（剪贴板不在内：门户要复制订阅链接）。
const permissionsPolicy = "camera=(), microphone=(), geolocation=(), usb=(), payment=()"

// robotsNoIndex 用于 /v1 API：接口响应不该被搜索引擎收录；入口页与静态资源不加，由入口页的 meta robots 与 robots.txt 管。
const robotsNoIndex = "noindex, nofollow"

// SecurityHeaders 写通用安全响应头。hsts 只在生产为 true：生产强制 https（public_base_url 校验），
// 本机开发走 http，带上 HSTS 会让浏览器把 localhost 记成只许 https。
// HSTS 放在这里而不是 nginx：nginx 里某个 location 一旦写了自己的 add_header，就不再继承 server 级的。
func SecurityHeaders(hsts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", permissionsPolicy)
		h.Set("Cache-Control", "no-store")
		if hsts {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		path := c.Request.URL.Path
		if path == "/v1" || strings.HasPrefix(path, "/v1/") {
			h.Set("X-Robots-Tag", robotsNoIndex)
		}
		c.Next()
	}
}

// Timeout 给请求 ctx 设期限；exempt 返回 true 的请求（如 SSE）不设。到期后 handler 报的 DeadlineExceeded 由 Errors 映射成 504 timeout。
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
// exempt 返回 true 的请求不占名额（如 SSE 长连接）。
func Concurrency(n int, exempt func(*gin.Context) bool) gin.HandlerFunc {
	sem := make(chan struct{}, n)
	return func(c *gin.Context) {
		if exempt != nil && exempt(c) {
			c.Next()
			return
		}
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
		// 请求 ctx 已到期（Timeout 设的期限）且错误链里有 DeadlineExceeded：回 504 timeout，而不是 500。
		// 只看请求 ctx 自己到期：handler 内部另设的短期限（如 readyz 的 2 秒 ping）到期不算请求超时。
		if e.Status() >= 500 && errors.Is(raw, context.DeadlineExceeded) &&
			errors.Is(c.Request.Context().Err(), context.DeadlineExceeded) {
			e = errs.Wrap(errs.Timeout, raw)
		}
		l := plog.From(c.Request.Context())
		if e.Status() >= 500 {
			l.Error("request failed", zap.String("route", route(c)), zap.String("code", string(e.Code)), zap.Error(raw))
		} else {
			l.Debug("request rejected", zap.String("route", route(c)), zap.String("code", string(e.Code)), zap.Error(raw))
		}
		writeError(c, e)
	}
}
