package middleware

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// scriptedRedis 截住全部命令、不连网络：记下每次限流脚本调用的参数，并按 reply 回结果。
type scriptedRedis struct {
	calls [][]any
	reply []any
}

func (s *scriptedRedis) DialHook(redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) { return nil, net.ErrClosed }
}

func (s *scriptedRedis) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		s.calls = append(s.calls, cmd.Args())
		if v, ok := cmd.(*redis.Cmd); ok {
			v.SetVal(s.reply)
		}
		return nil
	}
}

func (s *scriptedRedis) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func newScriptedRedis(t *testing.T, reply ...any) (*redis.Client, *scriptedRedis) {
	t.Helper()
	hook := &scriptedRedis{reply: reply}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	rdb.AddHook(hook)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, hook
}

func limitTestLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func fixedKey(v string) func(*http.Request) string { return func(*http.Request) string { return v } }

// 全部适用维度一次 EVALSHA：键按声明顺序、跳过不适用的维度，每个维度带 (上限, 过期毫秒)
func TestRateLimitSendsAllDimensionsInOneScriptCall(t *testing.T) {
	rdb, hook := newScriptedRedis(t, int64(0), int64(0))
	called := false
	h := RateLimit(rdb, limitTestLog(),
		Limit{Name: "a", Window: time.Minute, Max: 10, KeyFn: fixedKey("x")},
		Limit{Name: "skip", Window: time.Minute, Max: 1, KeyFn: fixedKey("")},
		Limit{Name: "b", Window: 10 * time.Minute, Max: 3, KeyFn: fixedKey("y")},
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
	if !called || rr.Code != http.StatusOK {
		t.Fatalf("status=%d called=%v, want pass-through", rr.Code, called)
	}
	if len(hook.calls) != 1 {
		t.Fatalf("Valkey calls=%d, want 1 (one script for every dimension)", len(hook.calls))
	}
	args := hook.calls[0]
	if args[0] != "evalsha" || args[2] != 2 {
		t.Fatalf("script call args=%v, want evalsha with 2 keys", args)
	}
	if k := args[3].(string); !strings.HasPrefix(k, "rl:a:x:") {
		t.Fatalf("first key=%q, want rl:a:x:<window>", k)
	}
	if k := args[4].(string); !strings.HasPrefix(k, "rl:b:y:") {
		t.Fatalf("second key=%q, want rl:b:y:<window>", k)
	}
	if args[5] != 10 || args[6] != int64(61000) || args[7] != 3 || args[8] != int64(601000) {
		t.Fatalf("limit args=%v, want (10, 61000ms) (3, 601000ms)", args[5:])
	}

	// 没有适用维度时不碰 Valkey
	hook.calls = nil
	RateLimit(rdb, limitTestLog(), Limit{Name: "anon", Window: time.Minute, Max: 1, KeyFn: fixedKey("")})(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if len(hook.calls) != 0 {
		t.Fatalf("Valkey calls=%d with no applicable dimension, want 0", len(hook.calls))
	}
}

// 脚本回报第 n 个适用维度超限：回 429，不进处理器
func TestRateLimitRejectsWhenScriptReportsAnExceededDimension(t *testing.T) {
	rdb, _ := newScriptedRedis(t, int64(2), int64(4))
	for name, mw := range map[string]func(*redis.Client, *slog.Logger, ...Limit) func(http.Handler) http.Handler{
		"RateLimit": RateLimit, "RateLimitStrict": RateLimitStrict,
	} {
		called := false
		h := mw(rdb, limitTestLog(),
			Limit{Name: "a", Window: time.Minute, Max: 10, KeyFn: fixedKey("x")},
			Limit{Name: "b", Window: time.Minute, Max: 3, KeyFn: fixedKey("y")},
		)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusTooManyRequests || called {
			t.Fatalf("%s: status=%d called=%v, want 429 before handler", name, rr.Code, called)
		}
	}
}

// 后端故障：普通限流放行（舱壁），严格限流回 503
func TestRateLimitBackendFailureKeepsFailOpenAndFailClosed(t *testing.T) {
	for _, rdb := range []*redis.Client{nil, redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, MaxRetries: -1,
	})} {
		lim := Limit{Name: "a", Window: time.Minute, Max: 1, KeyFn: fixedKey("x")}
		called := false
		rr := httptest.NewRecorder()
		RateLimit(rdb, limitTestLog(), lim)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).
			ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if !called {
			t.Fatalf("RateLimit must fail open on backend failure (rdb nil=%v), status=%d", rdb == nil, rr.Code)
		}
		called = false
		rr = httptest.NewRecorder()
		RateLimitStrict(rdb, limitTestLog(), lim)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).
			ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if called || rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("RateLimitStrict must fail closed (rdb nil=%v): status=%d called=%v", rdb == nil, rr.Code, called)
		}
		if rdb != nil {
			_ = rdb.Close()
		}
	}
}

// 脚本本身的语义：逐个 INCR、首次建键即设过期、遇到第一个超限维度就返回（后面的维度不计数）
func TestRateLimitScriptShortCircuitsInDeclarationOrder(t *testing.T) {
	body := rateLimitScriptSource
	incr := strings.Index(body, "redis.call('INCR', key)")
	expire := strings.Index(body, "redis.call('PEXPIRE', key, ARGV[2 * i])")
	reject := strings.Index(body, "return {i, n}")
	loopEnd := strings.Index(body, "return {0, 0}")
	if incr < 0 || expire < incr || reject < expire || loopEnd < reject ||
		!strings.Contains(body, "if n == 1 then") {
		t.Fatalf("rate-limit script lost INCR → PEXPIRE-on-create → early reject ordering:\n%s", body)
	}
}
