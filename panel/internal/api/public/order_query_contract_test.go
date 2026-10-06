// [INPUT]: 依赖 platform/sourcetest 按名取 NewRouter 与 handlers.queryMyOrderPayment
// [OUTPUT]: 对外提供 TestOrderQueryRouteAuthRateLimitAndOwnership
// [POS]: api/public「我已支付，刷新状态」的路由契约：在登录分组里、按账号单独限流、不挂 checkout 开关、处理器把本人 ID 交给领域层做归属校验

package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestOrderQueryRouteAuthRateLimitAndOwnership(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	router := pkg.Decl("NewRouter")
	handler := pkg.Decl("handlers.queryMyOrderPayment")

	const route = `)).Post("/orders/{id}/query", h.queryMyOrderPayment)`
	authAt := strings.Index(router, "r.Use(middleware.RequireAuth(d.Log))")
	routeAt := strings.Index(router, route)
	webhookAt := strings.Index(router, `r.Get("/webhooks/payments/{provider}", h.paymentWebhook)`)
	if authAt < 0 || routeAt < 0 || webhookAt < 0 || !(authAt < routeAt && routeAt < webhookAt) {
		t.Fatal("order query route must stay in the authenticated public API group")
	}
	if strings.Count(router, route) != 1 {
		t.Fatal("order query route must have exactly one registration")
	}
	// 限流就挂在这条路由上：取路由注册前最近的一段 r.With(...)
	segment := router[strings.LastIndex(router[:routeAt], "r.With("):routeAt]
	if !strings.Contains(segment, `middleware.ByAccount("order_query", time.Minute, 6)`) {
		t.Fatalf("order query must carry its own per-account rate limit, got %q", segment)
	}
	if strings.Contains(segment, "checkout") {
		t.Fatal("order query must not sit behind the billing.checkout switch")
	}
	if !strings.Contains(handler, `httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), principal.UserID)`) {
		t.Fatal("order query handler must pass the caller as owner")
	}
}
