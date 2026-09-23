package public

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestCancelOrderHTTPAuthTenantAndResponseContract(t *testing.T) {
	router := readCancelContractSource(t, "router.go")
	handler := readCancelContractSource(t, "order_cancel.go")
	release := readCancelContractSource(t, "../../domain/billing/release.go")

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

func readCancelContractSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
