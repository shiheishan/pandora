package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 新建与编辑支付渠道写的是收款去向与商户密钥：写权限 + 近期重认证 + 各自的幂等 scope。
func TestPaymentProviderWriteRouteContracts(t *testing.T) {
	routes := loadRouteProtections(t)
	want := map[string]routeProtection{
		"GET /payment-providers": {handler: "h.listProviders",
			permissions: []string{"billing.payment.read"}},
		"POST /payment-providers": {handler: "h.createProvider",
			permissions: []string{"billing.provider.write"}, recentReauth: true,
			idempotency: "payment_provider_create"},
		"PUT /payment-providers/{code}": {handler: "h.updateProvider",
			permissions: []string{"billing.provider.write"}, recentReauth: true,
			idempotency: "payment_provider_update"},
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

func paymentProviderTestRouter(principal *httpx.Principal) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := httpx.WithTenantID(r.Context(), catalogRouteTenant)
			ctx = httpx.WithPrincipal(ctx, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	d := Deps{Log: catalogRouteLogger()}
	r.Route("/v1", func(r chi.Router) { registerPaymentProviderRoutes(r, d, &handlers{d: d}) })
	return r
}

// 反向测试：用真实的注册函数装路由，只把鉴权换成注入的主体。
// 门槛顺序 权限 → 重认证 → 幂等：缺权限 404；有权限没重认证 403 reauth_required，
// 且请求到不了幂等中间件（没带键也不是 400）；补上重认证后才轮到幂等键校验。
func TestPaymentProviderWritesGuardsPrecedeIdempotency(t *testing.T) {
	cases := []struct{ method, path string }{
		{http.MethodPost, "/v1/payment-providers"},
		{http.MethodPut, "/v1/payment-providers/epay"},
	}
	send := func(principal *httpx.Principal, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		paymentProviderTestRouter(principal).ServeHTTP(w, req)
		return w
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			// 只读财务能看渠道，不能改
			if got := send(catalogRoutePrincipal("billing.payment.read", true), tc.method, tc.path); got.Code != http.StatusNotFound {
				t.Fatalf("read-only operator status=%d, want 404", got.Code)
			}
			got := send(catalogRoutePrincipal("billing.provider.write", false), tc.method, tc.path)
			if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"code":"reauth_required"`) {
				t.Fatalf("without recent reauth status=%d body=%s, want 403 reauth_required", got.Code, got.Body.String())
			}
			got = send(catalogRoutePrincipal("billing.provider.write", true), tc.method, tc.path)
			if got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), "Idempotency-Key") {
				t.Fatalf("reauthed request without a key status=%d body=%s, want the idempotency 400", got.Code, got.Body.String())
			}
		})
	}
}

// 编辑的请求体不收 code / adapter：建后不可改由形状保证，带了就 400，到不了业务层。
func TestUpdateProviderRejectsIdentityFields(t *testing.T) {
	for _, body := range []string{`{"code":"other"}`, `{"adapter":"demo_hmac"}`, `{"credentials":"x"}`} {
		req := httptest.NewRequest(http.MethodPut, "/v1/payment-providers/epay", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := httpx.WithTenantID(req.Context(), catalogRouteTenant)
		ctx = httpx.WithPrincipal(ctx, catalogRoutePrincipal("billing.provider.write", true))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("code", "epay")
		req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		// Payments 为空：请求若越过解码走到业务层会直接 panic，测试即失败
		(&handlers{d: Deps{Log: catalogRouteLogger()}}).updateProvider(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s status=%d, want 400", body, w.Code)
		}
	}
}

// 渠道列表只给 has_credentials：任何带 key / merchant / secret / credential 字样的
// JSON 键都不许出现在列表行上（商户号与密钥只写不读）。
func TestProviderListRowCarriesNoSecrets(t *testing.T) {
	typ := reflect.TypeOf(adminops.ProviderRow{})
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "has_credentials" {
			continue
		}
		lower := strings.ToLower(tag)
		for _, bad := range []string{"key", "merchant", "secret", "credential", "sealed", "encrypted"} {
			if strings.Contains(lower, bad) {
				t.Errorf("ProviderRow.%s json=%q exposes a secret-like field", typ.Field(i).Name, tag)
			}
		}
	}
}
