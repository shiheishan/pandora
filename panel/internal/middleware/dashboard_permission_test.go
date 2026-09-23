package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestDashboardCombinedPermissionSemantics(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) })
	handler := RequireAuth(log)(RequirePermission("metering.read", log)(RequirePermission("node.read", log)(next)))
	for _, tc := range []struct {
		name      string
		principal *httpx.Principal
		want      int
		called    bool
	}{
		{name: "anonymous", want: http.StatusUnauthorized},
		{name: "no permissions", principal: &httpx.Principal{Kind: "admin"}, want: http.StatusNotFound},
		{name: "one of two", principal: &httpx.Principal{Kind: "admin", Permissions: []string{"metering.read"}}, want: http.StatusNotFound},
		{name: "both", principal: &httpx.Principal{Kind: "admin", Permissions: []string{"metering.read", "node.read"}}, want: http.StatusNoContent, called: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			r := httptest.NewRequest(http.MethodGet, "/v1/dashboard/traffic/nodes", nil)
			if tc.principal != nil {
				r = r.WithContext(httpx.WithPrincipal(r.Context(), tc.principal))
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || called != tc.called {
				t.Fatalf("status=%d called=%v, want %d/%v", w.Code, called, tc.want, tc.called)
			}
		})
	}
}
