// [INPUT]: 依赖 ./router.go 的 NewRouter，依赖 chi.Walk / Routes.Match 解析注册表
// [OUTPUT]: 对外提供 public 路由契约测试与 assertRouteContract 断言助手
// [POS]: api/public 的路由存在性守卫：登出、通知偏好、/app 静态挂载，以及 /app/* 与订阅通配互不抢路由
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/config"
)

func TestPublicRouterExposesAuthenticatedLogoutContract(t *testing.T) {
	router := NewRouter(Deps{
		Cfg: &config.Config{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	assertRouteContract(t, router, http.MethodPost, "/v1/auth/logout")
}

func TestPublicRouterExposesNotificationPreferenceContracts(t *testing.T) {
	router := NewRouter(Deps{
		Cfg: &config.Config{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	assertRouteContract(t, router, http.MethodGet, "/v1/me/notification-preferences")
	assertRouteContract(t, router, http.MethodPut, "/v1/me/notification-preferences")
}

// React 候选门户挂在 /app，与订阅通配 /{prefix}/{token} 共存：
// 路由表里两者都在，而 /app/x 这种两段路径必须落到静态下发而不是订阅处理器。
func TestPublicRouterServesEmbeddedAppBesideSubscriptions(t *testing.T) {
	router := NewRouter(Deps{
		Cfg: &config.Config{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for _, path := range []string{"/app", "/app/*"} {
		assertRouteContract(t, router, http.MethodGet, path)
		assertRouteContract(t, router, http.MethodHead, path)
	}
	assertRouteContract(t, router, http.MethodGet, "/{prefix}/{token}")

	routes := router.(chi.Routes)
	for _, path := range []string{"/app/index.html", "/app/assets/missing.js", "/app/"} {
		rctx := chi.NewRouteContext()
		if !routes.Match(rctx, http.MethodGet, path) {
			t.Fatalf("GET %s matches no route", path)
		}
		if got := rctx.RoutePattern(); got != "/app/*" {
			t.Fatalf("GET %s routed to %q, want /app/*", path, got)
		}
	}
	rctx := chi.NewRouteContext()
	if !routes.Match(rctx, http.MethodGet, "/0123456789ab/sometoken") || rctx.RoutePattern() != "/{prefix}/{token}" {
		t.Fatalf("subscription link no longer reaches /{prefix}/{token}: %q", rctx.RoutePattern())
	}
}

func assertRouteContract(t *testing.T, handler http.Handler, method, path string) {
	t.Helper()
	routes, ok := handler.(chi.Routes)
	if !ok {
		t.Fatalf("router does not implement chi.Routes")
	}
	found := false
	if err := chi.Walk(routes, func(gotMethod, gotPath string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if gotMethod == method && gotPath == path {
			found = true
		}
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	if !found {
		t.Fatalf("route contract %s %s is not registered", method, path)
	}
}
