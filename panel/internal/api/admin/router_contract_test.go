// [INPUT]: 依赖 ./router.go 的 NewRouter，依赖 chi.Walk 遍历注册表
// [OUTPUT]: 对外提供 admin 路由契约测试与 assertAdminRouteContract 断言助手
// [POS]: api/admin 的路由存在性守卫：账户操作与 /app 静态挂载必须注册在预期方法上
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/config"
)

func TestAdminRouterExposesAuthenticatedAccountContracts(t *testing.T) {
	router := NewRouter(Deps{
		Cfg: &config.Config{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	assertAdminRouteContract(t, router, http.MethodPost, "/v1/auth/logout")
	assertAdminRouteContract(t, router, http.MethodPost, "/v1/me/password")
}

// React 候选控制台与旧单页并存：/app 与 /app/* 只接 GET/HEAD
func TestAdminRouterServesEmbeddedApp(t *testing.T) {
	router := NewRouter(Deps{
		Cfg: &config.Config{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for _, path := range []string{"/app", "/app/*"} {
		assertAdminRouteContract(t, router, http.MethodGet, path)
		assertAdminRouteContract(t, router, http.MethodHead, path)
	}
	assertAdminRouteContract(t, router, http.MethodGet, "/")
}

func assertAdminRouteContract(t *testing.T, handler http.Handler, method, path string) {
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
