// [INPUT]: 依赖 coupon.go 的 setCouponStatus / couponRedemptions，依赖 chi 路由上下文与 platform/httpx
// [OUTPUT]: 对外提供 TestCouponHandlersRejectMalformedIDsAsNotFound
// [POS]: api/admin 优惠券处理器的单元测试：路径 id 不是 UUID 时在碰数据库之前回中性 404（以前是 500）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestCouponHandlersRejectMalformedIDsAsNotFound(t *testing.T) {
	// Pool 为空：若处理器没有先拦住非法 id，就会在访问数据库时 panic。
	h := &handlers{d: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	for name, call := range map[string]struct {
		handler http.HandlerFunc
		method  string
		body    string
	}{
		"status":      {h.setCouponStatus, http.MethodPost, `{"status":"paused"}`},
		"redemptions": {h.couponRedemptions, http.MethodGet, ``},
	} {
		req := httptest.NewRequest(call.method, "/v1/coupons/not-a-uuid", strings.NewReader(call.body))
		req.Header.Set("Content-Type", "application/json")
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", "not-a-uuid")
		w := httptest.NewRecorder()
		call.handler(w, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc)))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s with a malformed id: status=%d body=%s, want 404", name, w.Code, w.Body.String())
		}
	}
}
