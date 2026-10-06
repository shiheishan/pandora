// [INPUT]: 依赖 admin.go 的 adminClient、retryAfter、str / num，httptest 扮演后台网关
// [OUTPUT]: 单测：429 按 Retry-After 退避后用同一个幂等键重试、reauth_required 走口令重认证后换新令牌重试、错误信封的 code 被解析、节流间隔生效、取值助手对缺字段报错
// [POS]: tools/loadtest/seed 的后台客户端测试，不连真网关
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package seed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type fakeAdmin struct {
	mu        sync.Mutex
	idemKeys  []string
	throttled bool
	reauths   int
	tokens    []string
}

func (f *fakeAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/v1/auth/login":
		writeJSON(http.StatusOK, map[string]any{"access_token": "t-login"})
	case "/v1/auth/reauth":
		f.reauths++
		writeJSON(http.StatusOK, map[string]any{"access_token": "t-reauth"})
	case "/v1/nodes/bootstrap-token":
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		f.idemKeys = append(f.idemKeys, r.Header.Get("Idempotency-Key"))
		if !f.throttled {
			f.throttled = true
			w.Header().Set("Retry-After", "1")
			writeJSON(http.StatusTooManyRequests, map[string]any{"error": map[string]any{"code": "rate_limited"}})
			return
		}
		if r.Header.Get("Authorization") != "Bearer t-reauth" {
			writeJSON(http.StatusForbidden, map[string]any{"error": map[string]any{"code": "reauth_required", "message": "x"}})
			return
		}
		writeJSON(http.StatusCreated, map[string]any{"token": "boot"})
	case "/v1/servers":
		writeJSON(http.StatusConflict, map[string]any{"error": map[string]any{"code": "conflict"}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestAdminClientRetriesThrottleAndReauth(t *testing.T) {
	fake := &fakeAdmin{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := newAdminClient(srv.URL, "admin@example.test", "not-a-secret", 0)
	ctx := context.Background()
	if err := c.login(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := c.call(ctx, http.MethodPost, "/v1/nodes/bootstrap-token", jsonObject{"node_name": "n"}, http.StatusCreated)
	if err != nil {
		t.Fatal(err)
	}
	if tok, _ := str(out, "token"); tok != "boot" {
		t.Fatalf("unexpected response %v", out)
	}
	// 429 → 403 reauth_required → 201：三次请求同一个幂等键，最后一次带重认证后的令牌
	if len(fake.idemKeys) != 3 || fake.idemKeys[0] == "" || fake.idemKeys[0] != fake.idemKeys[1] || fake.idemKeys[1] != fake.idemKeys[2] {
		t.Fatalf("retries must reuse one idempotency key: %v", fake.idemKeys)
	}
	if fake.reauths != 1 || fake.tokens[2] != "Bearer t-reauth" {
		t.Fatalf("reauth not performed once before the final retry: reauths=%d tokens=%v", fake.reauths, fake.tokens)
	}
	if c.Calls != 5 { // login + 429 + 403 + reauth + 201
		t.Fatalf("Calls = %d, want 5", c.Calls)
	}

	_, err = c.call(ctx, http.MethodPost, "/v1/servers", jsonObject{})
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != http.StatusConflict || ae.Code != "conflict" {
		t.Fatalf("expected parsed conflict error, got %v", err)
	}
}

func TestAdminClientPacesRequests(t *testing.T) {
	srv := httptest.NewServer(&fakeAdmin{})
	defer srv.Close()
	c := newAdminClient(srv.URL, "admin@example.test", "not-a-secret", 40*time.Millisecond)
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := c.login(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("3 paced requests took %s, want at least 2 intervals", elapsed)
	}
}

func TestRetryAfterAndLookups(t *testing.T) {
	if retryAfter("3") != 3*time.Second || retryAfter("") != 5*time.Second || retryAfter("9999") != 5*time.Second {
		t.Fatal("retryAfter must honour small integers and fall back to 5s")
	}
	o := jsonObject{"plan": map[string]any{"id": "p", "row_version": 7.0}}
	if s, err := str(o, "plan.id"); err != nil || s != "p" {
		t.Fatalf("str = %q %v", s, err)
	}
	if n, err := num(o, "plan.row_version"); err != nil || n != 7 {
		t.Fatalf("num = %d %v", n, err)
	}
	if _, err := str(o, "plan.missing"); err == nil {
		t.Fatal("missing field must be an error")
	}
	if _, err := num(o, "plan.id"); err == nil {
		t.Fatal("non-numeric field must be an error")
	}
}
