// Package logging 提供带脱敏的结构化日志。
//
// SEC-011 的验收标准是「自动扫描生产日志不发现认证材料」。
// 靠人人自觉不写敏感字段是靠不住的，所以脱敏做在 slog.Handler 层：
// 无论哪个调用点写了 password/token/cookie，落盘前都会被替换成掩码。
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// 命中这些子串的属性键一律掩码。用「包含」而非「相等」，
// 这样 refresh_token、authorization_header、stripe_secret_key 都能覆盖到。
var sensitiveKeyParts = []string{
	"password", "passwd", "secret", "token", "cookie", "authorization",
	"credential", "private_key", "api_key", "apikey", "session_id",
	"verification_code", "otp", "totp", "recovery_code", "seed",
	"card", "cvv", "signature", "phc",
}

const mask = "[REDACTED]"

type redactHandler struct {
	inner slog.Handler
}

func (h redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redact(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		safe[i] = redact(a)
	}
	return redactHandler{inner: h.inner.WithAttrs(safe)}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{inner: h.inner.WithGroup(name)}
}

func redact(a slog.Attr) slog.Attr {
	if isSensitive(a.Key) {
		return slog.String(a.Key, mask)
	}
	// 递归处理分组，避免 slog.Group("auth", "token", ...) 逃过脱敏
	if a.Value.Kind() == slog.KindGroup {
		src := a.Value.Group()
		dst := make([]slog.Attr, len(src))
		for i, s := range src {
			dst[i] = redact(s)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(dst...)}
	}
	return a
}

func isSensitive(key string) bool {
	k := strings.ToLower(key)
	for _, p := range sensitiveKeyParts {
		if strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// New 构造一个带脱敏的 logger。开发环境用文本格式便于阅读，
// 其他环境用 JSON 便于采集与自动扫描。
func New(env, service string) *slog.Logger {
	level := slog.LevelInfo
	if env == "development" {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{Level: level}
	var base slog.Handler
	if env == "development" {
		base = slog.NewTextHandler(os.Stdout, opts)
	} else {
		base = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(redactHandler{inner: base}).With(
		slog.String("service", service),
		slog.String("env", env),
	)
}
