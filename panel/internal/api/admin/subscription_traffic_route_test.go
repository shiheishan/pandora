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

// 后台加流量包（w5account）：凭空给用户加流量，与加时长同权限码；每执行一次多一笔，
// 不是天然幂等的，所以 权限 → 重认证 → 独立幂等 scope。
func TestSubscriptionTrafficGrantRouteProtection(t *testing.T) {
	routes := loadRouteProtections(t)
	const key = "POST /subscriptions/{id}/traffic-pack"
	want := routeProtection{handler: "h.grantSubscriptionTraffic",
		permissions: []string{"billing.adjustment.write"}, recentReauth: true,
		idempotency: "billing.SubscriptionTrafficGrantIdempotencyScope"}
	if got, ok := routes[key]; !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %+v (registered=%v), want %+v", key, got, ok, want)
	}
	users := 0
	for k, p := range routes {
		if p.idempotency == want.idempotency || strings.Trim(p.idempotency, `"`) == billing.SubscriptionTrafficGrantIdempotencyScope {
			users++
			if k != key {
				t.Errorf("%s shares the subscription traffic grant idempotency scope", k)
			}
		}
	}
	if users != 1 {
		t.Fatalf("traffic grant idempotency scope used by %d routes, want 1", users)
	}
}

// 反向：没有权限 404；有权限没有近期重认证 403 reauth_required（到不了幂等中间件）；
// 补上重认证后才轮到幂等键校验。
func TestSubscriptionTrafficGrantRequiresReauthBeforeIdempotency(t *testing.T) {
	const path = "/v1/subscriptions/71000000-0000-7000-8000-000000000021/traffic-pack"
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
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"bytes":1073741824,"reason":"补偿线路故障"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router(principal).ServeHTTP(w, req)
		return w
	}
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
