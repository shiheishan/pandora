package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// admin.writes 只读模式的豁免：读、切开关、登录与重认证、改自己密码
func TestAdminWriteExempt(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/users", true},
		{http.MethodHead, "/users", true},
		{http.MethodPost, "/switches/admin.writes", true},
		{http.MethodPost, "/auth/login", true},
		{http.MethodPost, "/auth/reauth", true},
		{http.MethodPost, "/me/password", true},
		{http.MethodPost, "/users/1/status", false},
		{http.MethodDelete, "/switches/admin.writes", false},
		{http.MethodPut, "/nodes/routing", false},
		{http.MethodPost, "/me/password/x", false},
	} {
		if got := adminWriteExempt(tc.method, tc.path); got != tc.want {
			t.Errorf("adminWriteExempt(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// 判断用的是 /v1 挂载点之内的相对路径，网关前面的前缀不影响
func TestRoutePathIsRelativeToMount(t *testing.T) {
	var seen string
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				seen = routePath(req)
				next.ServeHTTP(w, req)
			})
		})
		r.Post("/switches/{code}", func(http.ResponseWriter, *http.Request) {})
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/switches/admin.writes", nil))
	if seen != "/switches/admin.writes" {
		t.Fatalf("route path = %q", seen)
	}
	if got := routePath(httptest.NewRequest(http.MethodPost, "/secret-admin/v1/me/password", nil)); got != "/me/password" {
		t.Fatalf("fallback route path = %q", got)
	}
}
