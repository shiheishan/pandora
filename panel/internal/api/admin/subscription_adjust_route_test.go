package admin

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 后台加时长（第 2 波规划第 5 条）：直接改用户的到期时间，与人工调账同权限码；
// 每执行一次多 N 天，不是天然幂等的，所以 权限 → 重认证 → 独立幂等 scope。
func TestSubscriptionExtendRouteProtection(t *testing.T) {
	routes := loadRouteProtections(t)
	want := routeProtection{handler: "h.extendSubscription",
		permissions: []string{"billing.adjustment.write"}, recentReauth: true,
		idempotency: "billing.SubscriptionExtendIdempotencyScope"}
	if got, ok := routes["POST /subscriptions/{id}/extend"]; !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("POST /subscriptions/{id}/extend = %+v (registered=%v), want %+v", got, ok, want)
	}
	// 幂等 scope 不与任何别的路由共用：同一个键在两边会被当成重放
	users := 0
	for key, p := range routes {
		if p.idempotency == want.idempotency || strings.Trim(p.idempotency, `"`) == billing.SubscriptionExtendIdempotencyScope {
			users++
			if key != "POST /subscriptions/{id}/extend" {
				t.Errorf("%s shares the subscription extend idempotency scope", key)
			}
		}
	}
	if users != 1 {
		t.Fatalf("subscription extend idempotency scope used by %d routes, want 1", users)
	}
}

// 反向测试：用真实的注册函数装路由，只把鉴权换成注入的主体。没有权限 → 404；
// 有权限没有近期重认证 → 403 reauth_required，且请求到不了幂等中间件（没带
// Idempotency-Key 也不是 400）；补上重认证后才轮到幂等键校验。
func TestSubscriptionExtendRequiresReauthBeforeIdempotency(t *testing.T) {
	const path = "/v1/subscriptions/71000000-0000-7000-8000-000000000021/extend"
	router := func(principal *httpx.Principal) http.Handler {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := httpx.WithTenantID(r.Context(), catalogRouteTenant)
				ctx = httpx.WithPrincipal(ctx, principal)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})
		d := Deps{Log: catalogRouteLogger()}
		h := &handlers{d: d}
		r.Route("/v1", func(r chi.Router) { registerUserRoutes(r, d, h) })
		return r
	}
	send := func(principal *httpx.Principal) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"days":7,"reason":"补偿线路故障"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router(principal).ServeHTTP(w, req)
		return w
	}
	// iam.user.write 管得了用户，却不该能改到期时间
	for _, perm := range []string{"", "iam.user.write"} {
		if got := send(catalogRoutePrincipal(perm, true)); got.Code != http.StatusNotFound {
			t.Fatalf("permission %q status=%d, want 404", perm, got.Code)
		}
	}
	got := send(catalogRoutePrincipal("billing.adjustment.write", false))
	if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"code":"reauth_required"`) {
		t.Fatalf("without recent reauth status=%d body=%s, want 403 reauth_required", got.Code, got.Body.String())
	}
	got = send(catalogRoutePrincipal("billing.adjustment.write", true))
	if got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), "Idempotency-Key") {
		t.Fatalf("reauthed request without a key status=%d body=%s, want the idempotency 400", got.Code, got.Body.String())
	}
}
