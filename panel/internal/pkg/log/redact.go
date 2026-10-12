package log

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.uber.org/zap/zapcore"
)

const redacted = "[REDACTED]"

var sensitiveSubstrings = []string{
	"password", "passwd", "secret", "token", "cookie", "authorization",
	"credential", "private_key", "privatekey", "api_key", "apikey",
	"session_id", "verification_code", "otp", "totp", "recovery_code",
	"seed", "card", "cvv", "signature", "phc",
}

// NewRedactingCore 在写出前把敏感字段的值替换为 [REDACTED]。
func NewRedactingCore(c zapcore.Core) zapcore.Core {
	return &redactingCore{inner: c}
}

type redactingCore struct {
	inner zapcore.Core
}

func (r *redactingCore) Enabled(lvl zapcore.Level) bool {
	return r.inner.Enabled(lvl)
}

func (r *redactingCore) With(fields []zapcore.Field) zapcore.Core {
	return &redactingCore{inner: r.inner.With(redactFields(fields))}
}

func (r *redactingCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if r.Enabled(ent.Level) {
		return ce.AddCore(ent, r)
	}
	return ce
}

func (r *redactingCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	return r.inner.Write(ent, redactFields(fields))
}

func (r *redactingCore) Sync() error {
	return r.inner.Sync()
}

func redactFields(fields []zapcore.Field) []zapcore.Field {
	out := make([]zapcore.Field, len(fields))
	for i, f := range fields {
		out[i] = redactField(f)
	}
	return out
}

func redactField(f zapcore.Field) zapcore.Field {
	if !fieldSensitive(f.Key) {
		return f
	}
	return zapcore.Field{Key: f.Key, Type: zapcore.StringType, String: redacted}
}

func fieldSensitive(key string) bool {
	k := strings.ToLower(key)
	for _, sub := range sensitiveSubstrings {
		if strings.Contains(k, sub) {
			return true
		}
	}
	return false
}

// StripURL 若错误链含 *url.Error，则把 URL 缩成 scheme://host，去掉路径、查询与用户凭据。
func StripURL(err error) error {
	if err == nil {
		return nil
	}
	var uerr *url.Error
	if !errors.As(err, &uerr) || uerr.URL == "" {
		return err
	}
	stripped, stripErr := stripURLString(uerr.URL)
	if stripErr != nil {
		return err
	}
	msg := fmt.Sprintf("%s %q: %v", uerr.Op, stripped, errors.Unwrap(uerr))
	if errors.Unwrap(uerr) == nil {
		msg = fmt.Sprintf("%s %q", uerr.Op, stripped)
	}
	return errors.New(msg)
}

func stripURLString(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.User = nil
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	u.RawFragment = ""
	return u.Scheme + "://" + u.Host, nil
}
