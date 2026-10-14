package log

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestRedactingCore_masksSensitiveFields(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(NewRedactingCore(core))

	logger.Info("evt",
		zap.String("password", "p@ss"),
		zap.String("X-Auth-Token", "tok"),
		zap.String("session_id", "sid"),
		zap.Int64("user_id", 42),
		zap.String("route", "/api/x"),
	)

	if logs.Len() != 1 {
		t.Fatalf("日志条数 %d", logs.Len())
	}
	ctx := logs.All()[0].ContextMap()
	assertRedacted(t, ctx, "password")
	assertRedacted(t, ctx, "X-Auth-Token")
	assertRedacted(t, ctx, "session_id")
	if ctx["user_id"] != int64(42) {
		t.Fatalf("user_id 应保持: %v", ctx["user_id"])
	}
	if ctx["route"] != "/api/x" {
		t.Fatalf("route 应保持: %v", ctx["route"])
	}

	child := logger.With(zap.String("api_key", "k1"))
	child.Info("child")
	if logs.Len() != 2 {
		t.Fatalf("第二条日志缺失")
	}
	childCtx := logs.All()[1].ContextMap()
	assertRedacted(t, childCtx, "api_key")
}

func assertRedacted(t *testing.T, m map[string]interface{}, key string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("缺少字段 %q", key)
	}
	if v != redacted {
		t.Fatalf("字段 %q 应为 %q，得到 %v", key, redacted, v)
	}
}

func TestStripURL_stripsCredentialsPathAndQuery(t *testing.T) {
	raw := "https://u:p@api.example.com/x?key=abc"
	inner := errors.New("connection reset")
	uerr := &url.Error{Op: "Get", URL: raw, Err: inner}

	out := StripURL(uerr)
	if out == uerr {
		t.Fatal("应返回新错误")
	}
	text := out.Error()
	if !strings.Contains(text, "https://api.example.com") {
		t.Fatalf("应保留 scheme://host: %q", text)
	}
	if strings.Contains(text, "key=abc") || strings.Contains(text, "/x") || strings.Contains(text, "u:p") {
		t.Fatalf("不应含路径/查询/用户: %q", text)
	}
	if !strings.Contains(text, "connection reset") {
		t.Fatalf("应保留底层错误: %q", text)
	}

	if StripURL(errors.New("plain")) == nil {
		t.Fatal("非 url.Error 不应变 nil")
	}
}

func TestFrom_and_WithReqID(t *testing.T) {
	if From(context.Background()) == nil {
		t.Fatal("From 在无 logger 的 ctx 上不应为 nil")
	}

	base := zap.NewNop()
	id := "01234567-89ab-7def-8123-456789abcdef"
	ctx := WithReqID(context.Background(), base, id)
	if reqid.From(ctx) != id {
		t.Fatalf("reqid.From 得到 %q", reqid.From(ctx))
	}
	if From(ctx) == nil {
		t.Fatal("WithReqID 后 From 不应为 nil")
	}
}
