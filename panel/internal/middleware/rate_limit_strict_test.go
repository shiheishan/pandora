package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRateLimitStrictRejectsRedisOutageBeforeHandler(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  20 * time.Millisecond,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
		MaxRetries:   0,
	})
	t.Cleanup(func() { _ = rdb.Close() })

	called := false
	h := RateLimitStrict(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		ByRoute("registration_test", time.Minute, 5),
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/start", strings.NewReader(`{"email":"a@example.com"}`))
	req.RemoteAddr = "203.0.113.10:1234"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", rr.Code, rr.Body.String())
	}
	if called {
		t.Fatal("strict limiter must not call registration handler during Redis outage")
	}
}

func TestByJSONFieldHashDoesNotLeakSubjectAndRestoresBody(t *testing.T) {
	body := []byte(`{"email":"  User@Example.COM  "}`)
	req := httptest.NewRequest(http.MethodPost, "/register", bytes.NewReader(body))
	limit := ByJSONFieldHash("email", "email", time.Minute, 5,
		func(v string) string { return strings.ToLower(strings.TrimSpace(v)) })
	key := limit.KeyFn(req)
	if key == "" || strings.Contains(key, "user") || strings.Contains(key, "example") {
		t.Fatalf("subject key must be non-empty and non-PII: %q", key)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, body) {
		t.Fatalf("restored body=%q, want %q", restored, body)
	}
}
