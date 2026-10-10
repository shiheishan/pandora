package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// adminRouteChain 从真实路由表里取出一条路由的处理器链。
//
// 不用 chi.Walk：它把链拆成端点加中间件列表，全局中间件（鉴权、限流）也
// 混在里面。这里直接取路由树里登记的处理器 —— chi 在注册时已把组内与
// r.With(...) 的中间件（登录、权限、重认证、幂等）串在它上面，顺序就是
// 生产上的顺序；全局的令牌校验不在其中，由测试往 context 放 Principal 代替。
func adminRouteChain(t *testing.T, method, pattern string) http.Handler {
	t.Helper()
	router := NewRouter(Deps{
		Cfg: &config.Config{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if h := findRouteHandler(router.(chi.Routes), "", method, pattern); h != nil {
		return h
	}
	t.Fatalf("route %s %s is not registered", method, pattern)
	return nil
}

func findRouteHandler(r chi.Routes, prefix, method, pattern string) http.Handler {
	for _, route := range r.Routes() {
		full := strings.TrimSuffix(prefix, "/*") + route.Pattern
		if route.SubRoutes != nil {
			if h := findRouteHandler(route.SubRoutes, full, method, pattern); h != nil {
				return h
			}
			continue
		}
		if full == pattern {
			if h, ok := route.Handlers[method]; ok {
				return h
			}
		}
	}
	return nil
}

func serveGuarded(h http.Handler, method string, p *httpx.Principal) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/guarded", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "guard-test-key")
	ctx := httpx.WithTenantID(req.Context(), catalogRouteTenant)
	ctx = httpx.WithPrincipal(ctx, p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func guardPrincipal(reauthed bool, permissions ...string) *httpx.Principal {
	return &httpx.Principal{
		Kind: "admin", Audience: "admin", UserID: catalogRouteActor,
		TenantID: catalogRouteTenant, Permissions: permissions,
		ReauthedRecently: reauthed,
	}
}

func TestStepOneAdminRouteGuards(t *testing.T) {
	for _, tc := range []struct {
		method, pattern string
		permission      string
		reauth          bool
		// stale 是修复前挂的权限码：只有它、没有 permission 的管理员必须被拒
		stale string
	}{
		{http.MethodGet, "/v1/users/bulk/export", "iam.user.write", true, "iam.user.read"},
		{http.MethodPost, "/v1/users/bulk/generate", "iam.user.write", true, ""},
		{http.MethodPost, "/v1/users/bulk/mail", "ops.notification.write", true, ""},
		{http.MethodPost, "/v1/users/{id}/status", "iam.user.write", true, ""},
		{http.MethodPost, "/v1/settings/device-limit", "iam.user.write", true, ""},
		{http.MethodPost, "/v1/users/{id}/reset-password", "iam.user.write", true, ""},
		{http.MethodPost, "/v1/subscriptions/{id}/rotate", "iam.user.write", true, ""},
		{http.MethodPost, "/v1/settings/telegram/test", "ops.notification.write", false, "billing.provider.write"},
		{http.MethodPost, "/v1/settings/mail/test", "ops.notification.write", false, "billing.provider.write"},
		{http.MethodPost, "/v1/mail/templates/test", "ops.notification.write", true, "billing.provider.write"},
	} {
		t.Run(tc.method+" "+tc.pattern, func(t *testing.T) {
			h := adminRouteChain(t, tc.method, tc.pattern)

			// 既无权限又未重认证：必须先按权限拒成 404。反过来（先重认证）的话
			// 这里会是 403，没有权限的人也会被要求先输一遍密码
			if w := serveGuarded(h, tc.method, guardPrincipal(false)); w.Code != http.StatusNotFound {
				t.Fatalf("no permission, no reauth: status=%d want 404 (permission before reauth)", w.Code)
			}
			if tc.stale != "" {
				if w := serveGuarded(h, tc.method, guardPrincipal(true, tc.stale)); w.Code != http.StatusNotFound {
					t.Fatalf("stale permission %s still admitted: status=%d", tc.stale, w.Code)
				}
			}
			if tc.reauth {
				w := serveGuarded(h, tc.method, guardPrincipal(false, tc.permission))
				if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"reauth_required"`) {
					t.Fatalf("permission without recent reauth: status=%d body=%s", w.Code, w.Body.String())
				}
			}
		})
	}
}

// 批量改节点状态只剩 status:batch。旧别名 batch/status 已删：再加回来，
// 同一个 Idempotency-Key 换一条路径就会再执行一次，除非它共用同一个 scope。
func TestNodeBatchStatusHasNoLegacyAlias(t *testing.T) {
	raw, err := routerSource()
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Contains(source, `Post("/nodes/batch/status"`) {
		t.Fatal("legacy alias POST /nodes/batch/status is still registered")
	}
	route := `Post("/nodes/status:batch", h.batchAdminNodeStatus)`
	at := strings.Index(source, route)
	if at < 0 {
		t.Fatal("status:batch route missing")
	}
	window := source[max(0, at-200):at]
	if !strings.Contains(window, "middleware.Idempotency(d.Pool, nodeBatchStatusIdempotencyScope, d.Log)") {
		t.Fatal("status:batch must use nodeBatchStatusIdempotencyScope")
	}
	if strings.Contains(source, `"node_batch_status"`) {
		t.Fatal("the retired node_batch_status scope literal is back")
	}
}

// POST /nodes/{id}/protocol 是 PATCH /nodes/{id} 的重复入口，已经删除。
func TestNodeProtocolCompatibilityRouteIsGone(t *testing.T) {
	raw, err := routerSource()
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Contains(source, `Post("/nodes/{id}/protocol"`) || strings.Contains(source, "h.nodeSetProtocol") {
		t.Fatal("compatibility endpoint POST /nodes/{id}/protocol is still registered")
	}
}

// D-B-1：后台换发订阅链接的响应只能有这两个键，新令牌明文不回给管理员。
func TestAdminRotateResponseCarriesNoToken(t *testing.T) {
	raw, err := json.Marshal(adminRotateResponse(&subscription.AdminRotateOutput{UserEmail: "owner@example.test"}))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"old_revoked":true,"user_email":"owner@example.test"}` {
		t.Fatalf("rotate response = %s", raw)
	}
}
