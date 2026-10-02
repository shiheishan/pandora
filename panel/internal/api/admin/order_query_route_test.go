// [INPUT]: 依赖 router_billing.go 的 registerOrderRoutes，依赖 finance_routes_contract_test.go 的 loadRouteProtections 与 catalog_plan_update_route_test.go 的主体构造助手
// [OUTPUT]: 对外提供 TestOrderQueryRouteContract、TestOrderQueryRouteNeedsWritePermissionAndKeyButNoReauth
// [POS]: api/admin「向渠道查单」的保护契约：源码层钉死订单写权限、幂等域 admin_order_query、不挂重认证；运行层证明缺权限 404、未重认证也能到幂等中间件（缺键 400）
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

func TestOrderQueryRouteContract(t *testing.T) {
	got, ok := loadRouteProtections(t)["POST /orders/{id}/query"]
	if !ok {
		t.Fatal("route missing: POST /orders/{id}/query")
	}
	want := routeProtection{handler: "h.queryOrderPayment",
		permissions: []string{"billing.order.write"}, idempotency: "admin_order_query"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order query protection=%+v, want %+v", got, want)
	}
}

func TestOrderQueryRouteNeedsWritePermissionAndKeyButNoReauth(t *testing.T) {
	send := func(principal *httpx.Principal) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := httpx.WithTenantID(r.Context(), catalogRouteTenant)
				ctx = httpx.WithPrincipal(ctx, principal)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})
		d := Deps{Log: catalogRouteLogger()}
		r.Route("/v1", func(r chi.Router) { registerOrderRoutes(r, d, &handlers{d: d}) })
		req := httptest.NewRequest(http.MethodPost,
			"/v1/orders/71000000-0000-7000-8000-000000000099/query", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if got := send(catalogRoutePrincipal("billing.order.read", true)); got.Code != http.StatusNotFound {
		t.Fatalf("read-only operator status=%d, want 404", got.Code)
	}
	got := send(catalogRoutePrincipal("billing.order.write", false))
	if got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), "Idempotency-Key") {
		t.Fatalf("writer without recent reauth status=%d body=%s, want the idempotency 400", got.Code, got.Body.String())
	}
}
