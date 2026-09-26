// [INPUT]: 依赖 router_billing.go 的 registerOrderRoutes、router_catalog.go 的 registerTrafficPackRoutes，依赖 finance_routes_contract_test.go 的 loadRouteProtections 与 catalog_plan_update_route_test.go 的主体构造助手
// [OUTPUT]: 对外提供 TestManualOrderAndTrafficPackRouteContracts、TestManualOrderAndTrafficPackWritesRequireRecentReauth
// [POS]: api/admin 人工开单（R64 改挂重认证，线下已收款因此开放）与流量包目录写接口的保护契约：源码层钉死权限、重认证与幂等域，运行层证明未重认证的请求到不了幂等与处理器
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestManualOrderAndTrafficPackRouteContracts(t *testing.T) {
	routes := loadRouteProtections(t)
	want := map[string]routeProtection{
		// 线下已收款直接记收入并触发佣金，与标记已支付同门槛（R64）
		"POST /orders/manual": {handler: "h.createManualOrder",
			permissions: []string{"billing.order.write"}, recentReauth: true,
			idempotency: "billing.CheckoutIdempotencyScope"},
		"POST /orders/{id}/mark-paid": {handler: "h.markOrderPaid",
			permissions: []string{"billing.order.write"}, recentReauth: true,
			idempotency: "admin_order_mark_paid"},
		// 流量包是在售商品：新建、改价、上下架与套餐改价同门槛（D-C-2）
		"GET /traffic-packs": {handler: "h.listTrafficPacks",
			permissions: []string{"catalog.read"}},
		"POST /traffic-packs": {handler: "h.createTrafficPack",
			permissions: []string{"catalog.publish"}, recentReauth: true,
			idempotency: "catalog_traffic_pack_create"},
		"PUT /traffic-packs/{id}": {handler: "h.updateTrafficPack",
			permissions: []string{"catalog.publish"}, recentReauth: true,
			idempotency: "catalog_traffic_pack_update"},
		"POST /traffic-packs/{id}/status": {handler: "h.setTrafficPackStatus",
			permissions: []string{"catalog.publish"}, recentReauth: true,
			idempotency: "catalog_traffic_pack_status"},
	}
	for key, expected := range want {
		got, ok := routes[key]
		if !ok {
			t.Errorf("route missing: %s", key)
			continue
		}
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("%s protection=%+v, want %+v", key, got, expected)
		}
	}
}

// 反向测试：用真实的注册函数装路由，只把鉴权换成注入的主体。
// 有权限但没近期重认证 → 403 reauth_required，且请求到不了幂等中间件
// （没带 Idempotency-Key 也不是 400）；补上重认证后才轮到幂等键校验。
func TestManualOrderAndTrafficPackWritesRequireRecentReauth(t *testing.T) {
	cases := []struct {
		method, path, permission string
	}{
		{http.MethodPost, "/v1/orders/manual", "billing.order.write"},
		{http.MethodPost, "/v1/traffic-packs", "catalog.publish"},
		{http.MethodPut, "/v1/traffic-packs/71000000-0000-7000-8000-000000000099", "catalog.publish"},
		{http.MethodPost, "/v1/traffic-packs/71000000-0000-7000-8000-000000000099/status", "catalog.publish"},
	}
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
		r.Route("/v1", func(r chi.Router) {
			registerOrderRoutes(r, d, h)
			registerTrafficPackRoutes(r, d, h)
		})
		return r
	}
	send := func(principal *httpx.Principal, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router(principal).ServeHTTP(w, req)
		return w
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if got := send(catalogRoutePrincipal("", true), tc.method, tc.path); got.Code != http.StatusNotFound {
				t.Fatalf("without permission status=%d, want 404", got.Code)
			}
			got := send(catalogRoutePrincipal(tc.permission, false), tc.method, tc.path)
			if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"code":"reauth_required"`) {
				t.Fatalf("without recent reauth status=%d body=%s, want 403 reauth_required", got.Code, got.Body.String())
			}
			got = send(catalogRoutePrincipal(tc.permission, true), tc.method, tc.path)
			if got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), "Idempotency-Key") {
				t.Fatalf("reauthed request without a key status=%d body=%s, want the idempotency 400", got.Code, got.Body.String())
			}
		})
	}
}
