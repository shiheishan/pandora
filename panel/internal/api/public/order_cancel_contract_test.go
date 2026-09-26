// [INPUT]: 依赖 middleware.RequireAuth、platform/httpx、domain/billing 的 ReleaseOrderOutput，依赖 platform/sourcetest 按名取 NewRouter、handlers.cancelOrder 与 billing 的取消链路
// [OUTPUT]: 对外提供 TestCancelOrderHTTPAuthTenantAndResponseContract、TestCancelOrderAuthMiddlewareRejectsAnonymous、TestCancelOrderAlreadyTerminalHTTPResponse
// [POS]: api/public 用户取消订单的鉴权、租户与归属、响应形状契约
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestCancelOrderHTTPAuthTenantAndResponseContract(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	router := pkg.Decl("NewRouter")
	handler := pkg.Decl("handlers.cancelOrder")
	release := sourcetest.Load(t, "../../domain/billing").Decls("Service.CancelOrder", "lockReleaseOrder")

	authAt := strings.Index(router, "r.Use(middleware.RequireAuth(d.Log))")
	cancelAt := strings.Index(router, `r.Post("/orders/{id}/cancel", h.cancelOrder)`)
	webhookAt := strings.Index(router, `r.Get("/webhooks/payments/{provider}", h.paymentWebhook)`)
	if authAt < 0 || cancelAt < 0 || webhookAt < 0 || !(authAt < cancelAt && cancelAt < webhookAt) {
		t.Fatal("cancel route must remain in the authenticated public API group")
	}
	if strings.Count(router, `r.Post("/orders/{id}/cancel", h.cancelOrder)`) != 1 {
		t.Fatal("cancel route must have exactly one registration")
	}

	for _, required := range []string{
		"principal := httpx.PrincipalFrom(r.Context())",
		"httpx.TenantIDFrom(r.Context()), principal.UserID, chi.URLParam(r, \"id\")",
		"httpx.OK(w, out)",
	} {
		if !strings.Contains(handler, required) {
			t.Fatalf("cancel handler lost auth/tenant/response contract %q", required)
		}
	}
	for _, required := range []string{
		"s.pool.InTx(ctx, dbScope(tenantID, userID)",
		"OrderID: orderID, UserID: userID, Target: \"cancelled\"",
		"WHERE tenant_id=$1 AND id=$2::uuid`",
		"query += ` AND user_id=$3::uuid`",
	} {
		if !strings.Contains(release, required) {
			t.Fatalf("cancel service lost RLS/ownership contract %q", required)
		}
	}
}

func TestCancelOrderAuthMiddlewareRejectsAnonymous(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	guarded := middleware.RequireAuth(log)(next)

	anonymous := httptest.NewRecorder()
	guarded.ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost,
		"/v1/orders/order-id/cancel", nil))
	if anonymous.Code != http.StatusUnauthorized || called {
		t.Fatalf("anonymous status=%d downstream_called=%t", anonymous.Code, called)
	}

	called = false
	authenticatedRequest := httptest.NewRequest(http.MethodPost,
		"/v1/orders/order-id/cancel", nil)
	authenticatedRequest = authenticatedRequest.WithContext(httpx.WithPrincipal(
		authenticatedRequest.Context(), &httpx.Principal{Kind: "user", UserID: "user-id"}))
	authenticated := httptest.NewRecorder()
	guarded.ServeHTTP(authenticated, authenticatedRequest)
	if authenticated.Code != http.StatusNoContent || !called {
		t.Fatalf("authenticated status=%d downstream_called=%t", authenticated.Code, called)
	}
}

func TestCancelOrderAlreadyTerminalHTTPResponse(t *testing.T) {
	w := httptest.NewRecorder()
	httpx.OK(w, &billing.ReleaseOrderOutput{
		OrderID:         "00000000-0000-7000-8000-000000000001",
		Status:          "cancelled",
		AlreadyTerminal: true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type=%q", got)
	}
	var out billing.ReleaseOrderOutput
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Status != "cancelled" || !out.AlreadyTerminal {
		t.Fatalf("idempotent cancellation response=%#v", out)
	}
}
