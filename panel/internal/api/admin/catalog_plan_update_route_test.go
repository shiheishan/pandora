package admin

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const (
	catalogRouteTenant = "00000000-0000-7000-8000-000000000001"
	catalogRouteActor  = "71000000-0000-7000-8000-000000000011"
)

func catalogRouteLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func catalogRoutePrincipal(permission string, reauthed bool) *httpx.Principal {
	permissions := []string{}
	if permission != "" {
		permissions = append(permissions, permission)
	}
	return &httpx.Principal{
		Kind: "admin", Audience: "admin", UserID: catalogRouteActor,
		TenantID: catalogRouteTenant, Permissions: permissions,
		ReauthedRecently: reauthed,
	}
}

func catalogPlanUpdateTestRouter(
	t *testing.T,
	principal *httpx.Principal,
	idempotency catalogIdempotencyFactory,
	handler http.HandlerFunc,
) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := httpx.WithTenantID(r.Context(), catalogRouteTenant)
			ctx = httpx.WithPrincipal(ctx, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	r.Route("/v1", func(r chi.Router) {
		registerCatalogPlanUpdate(r, Deps{Log: catalogRouteLogger()}, handler, idempotency)
	})
	return r
}

func catalogRouteRequest(t *testing.T, router http.Handler, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/v1/plans/71000000-0000-7000-8000-000000000099", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestCatalogPlanUpdateRequestGuardsPrecedeIdempotencyAndHandler(t *testing.T) {
	tests := []struct {
		name       string
		principal  *httpx.Principal
		key        string
		wantStatus int
	}{
		{name: "permission denied", principal: catalogRoutePrincipal("", true), key: "catalog-route-key", wantStatus: http.StatusNotFound},
		{name: "recent reauth denied", principal: catalogRoutePrincipal("catalog.publish", false), key: "catalog-route-key", wantStatus: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idempotencyCalls := 0
			handlerCalls := 0
			factory := func(_ *db.Pool, scope string, _ *slog.Logger) func(http.Handler) http.Handler {
				if scope != "catalog_plan_update" {
					t.Fatalf("idempotency scope = %q", scope)
				}
				return func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						idempotencyCalls++
						next.ServeHTTP(w, r)
					})
				}
			}
			router := catalogPlanUpdateTestRouter(t, tc.principal, factory, func(w http.ResponseWriter, _ *http.Request) {
				handlerCalls++
				httpx.OK(w, map[string]bool{"ok": true})
			})
			if got := catalogRouteRequest(t, router, `{}`, tc.key); got.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", got.Code, tc.wantStatus)
			}
			if idempotencyCalls != 0 || handlerCalls != 0 {
				t.Fatalf("denied request reached idempotency=%d handler=%d", idempotencyCalls, handlerCalls)
			}
		})
	}
}

func TestCatalogPlanUpdateUsesProductionIdempotencyHeaderValidation(t *testing.T) {
	handlerCalls := 0
	router := catalogPlanUpdateTestRouter(
		t,
		catalogRoutePrincipal("catalog.publish", true),
		middleware.Idempotency,
		func(http.ResponseWriter, *http.Request) { handlerCalls++ },
	)
	for _, key := range []string{"", strings.Repeat("x", 256)} {
		if got := catalogRouteRequest(t, router, `{}`, key); got.Code != http.StatusBadRequest {
			t.Fatalf("key length %d status = %d, want 400", len(key), got.Code)
		}
	}
	if handlerCalls != 0 {
		t.Fatalf("invalid idempotency header reached handler %d times", handlerCalls)
	}
}

type catalogReplayRecord struct {
	body     string
	status   int
	response []byte
}

func catalogReplayFactory(t *testing.T, calls *int) catalogIdempotencyFactory {
	t.Helper()
	records := map[string]catalogReplayRecord{}
	return func(_ *db.Pool, scope string, _ *slog.Logger) func(http.Handler) http.Handler {
		if scope != "catalog_plan_update" {
			t.Fatalf("idempotency scope = %q", scope)
		}
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				*calls = *calls + 1
				key := r.Header.Get("Idempotency-Key")
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if existing, ok := records[key]; ok {
					if existing.body != string(body) {
						httpx.Fail(w, r, catalogRouteLogger(), httpx.New(httpx.CodeIdempotencyReuse, "请求已被使用"))
						return
					}
					w.Header().Set("Idempotency-Replayed", "true")
					w.WriteHeader(existing.status)
					_, _ = w.Write(existing.response)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				capture := httptest.NewRecorder()
				next.ServeHTTP(capture, r)
				for name, values := range capture.Header() {
					w.Header()[name] = append([]string(nil), values...)
				}
				records[key] = catalogReplayRecord{body: string(body), status: capture.Code, response: append([]byte(nil), capture.Body.Bytes()...)}
				w.WriteHeader(capture.Code)
				_, _ = w.Write(capture.Body.Bytes())
			})
		}
	}
}

func TestCatalogPlanUpdateValidGuardsReachOnceAndReplay(t *testing.T) {
	idempotencyCalls := 0
	handlerCalls := 0
	router := catalogPlanUpdateTestRouter(
		t,
		catalogRoutePrincipal("catalog.publish", true),
		catalogReplayFactory(t, &idempotencyCalls),
		func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			httpx.OK(w, map[string]bool{"ok": true})
		},
	)
	first := catalogRouteRequest(t, router, `{"expected_row_version":1}`, "catalog-route-key")
	second := catalogRouteRequest(t, router, `{"expected_row_version":1}`, "catalog-route-key")
	conflict := catalogRouteRequest(t, router, `{"expected_row_version":2}`, "catalog-route-key")
	if first.Code != http.StatusOK || second.Code != http.StatusOK || conflict.Code != http.StatusConflict {
		t.Fatalf("statuses first=%d replay=%d conflict=%d", first.Code, second.Code, conflict.Code)
	}
	if second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("same request was not marked replayed")
	}
	if handlerCalls != 1 || idempotencyCalls != 3 {
		t.Fatalf("handler=%d idempotency=%d, want 1/3", handlerCalls, idempotencyCalls)
	}
}

func TestCatalogPlanUpdateDefaultDenyReturnsNeutral503(t *testing.T) {
	passThrough := func(_ *db.Pool, _ string, _ *slog.Logger) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler { return next }
	}
	router := catalogPlanUpdateTestRouter(
		t,
		catalogRoutePrincipal("catalog.publish", true),
		passThrough,
		func(w http.ResponseWriter, r *http.Request) {
			_, err := adminops.NewService(nil).CreatePlanPrice(r.Context(), catalogRouteTenant, catalogRouteActor, adminops.CreatePriceInput{})
			httpx.Fail(w, r, catalogRouteLogger(), err)
		},
	)
	got := catalogRouteRequest(t, router, `{}`, "catalog-route-key")
	if got.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", got.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != string(httpx.CodeUnavailable) || body.Error.Message != "服务暂时不可用，请稍后重试" {
		t.Fatalf("non-neutral response code=%q message=%q", body.Error.Code, body.Error.Message)
	}
	lower := strings.ToLower(got.Body.String())
	for _, forbidden := range []string{"p0b", "sales", "capability", "release"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("response disclosed internal gate %q: %s", forbidden, got.Body.String())
		}
	}
}

func TestProductionRouterBindsUpdateHandlerThroughGuardedRegistrar(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "router.go", source, 0)
	if err != nil {
		t.Fatalf("parse router.go: %v", err)
	}
	var newRouter *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "NewRouter" {
			newRouter = function
			break
		}
	}
	if newRouter == nil {
		t.Fatal("NewRouter function is missing")
	}
	var v1Body *ast.BlockStmt
	ast.Inspect(newRouter.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || catalogExprName(call.Fun) != "r.Route" || len(call.Args) != 2 {
			return true
		}
		path, literal := catalogStringLiteral(call.Args[0])
		closure, closureOK := call.Args[1].(*ast.FuncLit)
		if literal && closureOK && path == "/v1" {
			if v1Body != nil {
				t.Fatal("NewRouter registers /v1 more than once")
			}
			v1Body = closure.Body
			return false
		}
		return true
	})
	if v1Body == nil {
		t.Fatal("NewRouter /v1 route closure is missing")
	}

	authGroups := 0
	registrations := 0
	for _, statement := range v1Body.List {
		expression, ok := statement.(*ast.ExprStmt)
		if !ok {
			continue
		}
		group, ok := expression.X.(*ast.CallExpr)
		if !ok || catalogExprName(group.Fun) != "r.Group" || len(group.Args) != 1 {
			continue
		}
		closure, ok := group.Args[0].(*ast.FuncLit)
		if !ok {
			continue
		}
		hasRequireAuth := false
		groupRegistrations := 0
		ast.Inspect(closure.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if catalogExprName(call.Fun) == "r.Use" && len(call.Args) == 1 {
				middlewareCall, ok := call.Args[0].(*ast.CallExpr)
				hasRequireAuth = hasRequireAuth || ok && catalogExprName(middlewareCall.Fun) == "middleware.RequireAuth"
			}
			if catalogExprName(call.Fun) == "registerCatalogPlanUpdate" {
				groupRegistrations++
				if len(call.Args) != 4 || catalogExprName(call.Args[0]) != "r" ||
					catalogExprName(call.Args[1]) != "d" || catalogExprName(call.Args[2]) != "h.updatePlan" ||
					catalogExprName(call.Args[3]) != "middleware.Idempotency" {
					t.Fatalf("unexpected production registrar arguments")
				}
			}
			return true
		})
		if hasRequireAuth {
			authGroups++
			registrations += groupRegistrations
		} else if groupRegistrations != 0 {
			t.Fatal("catalog plan update was registered outside the authenticated /v1 group")
		}
	}
	if authGroups != 1 || registrations != 1 {
		t.Fatalf("authenticated /v1 groups=%d catalog update registrations=%d, want 1/1", authGroups, registrations)
	}

	totalRegistrations := 0
	ast.Inspect(newRouter.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && catalogExprName(call.Fun) == "registerCatalogPlanUpdate" {
			totalRegistrations++
		}
		return true
	})
	if totalRegistrations != 1 {
		t.Fatalf("NewRouter catalog update registrations=%d, want exactly 1", totalRegistrations)
	}
}
