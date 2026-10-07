package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 后端不可用（库、Valkey、超时）回 503，身份确实不对才回 401：pdnd 把 401 当永久失败，
// 回执就此作罢（审计 node-panel-link #6）。
func TestNodeAuthFailuresSeparateUnavailableFromUnauthorized(t *testing.T) {
	h := &handlers{d: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"database down", fmt.Errorf("%w: %w", nodefabric.ErrNodeAuthUnavailable, errors.New("conn refused")), http.StatusServiceUnavailable},
		{"timeout", context.DeadlineExceeded, http.StatusServiceUnavailable},
		{"revoked", fmt.Errorf("%w: %w", nodefabric.ErrNodeIdentityInvalid, httpx.New(httpx.CodeUnauthorized, "x")), http.StatusUnauthorized},
		{"bad signature", nodefabric.ErrNodeSignatureMismatch, http.StatusUnauthorized},
		{"replayed nonce", httpx.New(httpx.CodeUnauthorized, "节点身份校验失败"), http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.failNodeAuth(w, httptest.NewRequest(http.MethodGet, "/v1/nodes/heartbeat", nil), "node", tc.err)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// 心跳与拉生效配置把身份复核并进自己的查询；其余签名端点在中间件里复核。新增签名端点
// 默认走中间件复核，要放进 confirmInHandler 组就得像这两个 handler 一样自己复核。
func TestDeferredIdentityConfirmationIsLimitedToHandlersThatConfirm(t *testing.T) {
	src := string(mustReadFile(t, "router.go"))
	group := between(t, src, "r.Use(h.requireNodeSignature(confirmInHandler))", "})")
	for _, route := range []string{`r.Post("/nodes/heartbeat", h.heartbeat)`, `r.Get("/nodes/effective-config", h.fetchEffectiveConfig)`} {
		if !contains(group, route) {
			t.Fatalf("deferred group lost %s", route)
		}
	}
	if n := countOf(group, "r.Get(") + countOf(group, "r.Post("); n != 2 {
		t.Fatalf("deferred-confirmation group has %d routes; only heartbeat and effective-config confirm in the handler", n)
	}
	handlers := string(mustReadFile(t, "handlers.go"))
	if !contains(handlers, "HeartbeatSigned(") || !contains(handlers, "signed.confirm(") || !contains(handlers, "signed.confirmNow(") {
		t.Fatal("deferred handlers no longer confirm the identity themselves")
	}
}
