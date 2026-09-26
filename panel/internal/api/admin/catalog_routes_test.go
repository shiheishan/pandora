// [INPUT]: 依赖 router_source_test.go 的 inspectRouterFiles 读路由 AST，依赖 platform/sourcetest 按名取套餐版本处理器的源码
// [OUTPUT]: 对外提供 TestCatalogWriteRouteContracts、TestPublishContractUsesDualTokens、TestLegacyVersionUpdateCannotBypassPoolWriteRoute
// [POS]: api/admin 套餐目录写路由的权限码、重认证与幂等域契约，发布走双令牌、旧版本更新不绕道节点池写入
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

type catalogRouteContract struct {
	handler      string
	permission   string
	recentReauth bool
	idempotency  string
}

func catalogExprName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		prefix := catalogExprName(value.X)
		if prefix == "" {
			return value.Sel.Name
		}
		return prefix + "." + value.Sel.Name
	default:
		return ""
	}
}

func catalogStringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func loadCatalogRouteContracts(t *testing.T) map[string]catalogRouteContract {
	t.Helper()
	routes := map[string]catalogRouteContract{}
	inspectRouterFiles(t, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		methodSelector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (methodSelector.Sel.Name != "Get" && methodSelector.Sel.Name != "Post" && methodSelector.Sel.Name != "Put") {
			return true
		}
		path, ok := catalogStringLiteral(call.Args[0])
		if !ok || (path != "/plans" && !strings.HasPrefix(path, "/plans/")) {
			return true
		}
		contract := catalogRouteContract{handler: catalogExprName(call.Args[1])}
		withCall, ok := methodSelector.X.(*ast.CallExpr)
		if ok && catalogExprName(withCall.Fun) == "r.With" {
			for _, middlewareExpr := range withCall.Args {
				middlewareCall, ok := middlewareExpr.(*ast.CallExpr)
				if !ok {
					continue
				}
				switch catalogExprName(middlewareCall.Fun) {
				case "middleware.RequirePermission":
					if len(middlewareCall.Args) > 0 {
						contract.permission, _ = catalogStringLiteral(middlewareCall.Args[0])
					}
				case "middleware.RequireRecentReauth":
					contract.recentReauth = true
				case "middleware.Idempotency":
					if len(middlewareCall.Args) > 1 {
						contract.idempotency, _ = catalogStringLiteral(middlewareCall.Args[1])
					}
				}
			}
		}
		routes[strings.ToUpper(methodSelector.Sel.Name)+" "+path] = contract
		return true
	})
	return routes
}

func TestCatalogWriteRouteContracts(t *testing.T) {
	routes := loadCatalogRouteContracts(t)
	checks := []struct {
		key, handler, permission, idempotency string
		recentReauth                          bool
	}{
		{"POST /plans", "h.createPlan", "catalog.write", "catalog_plan_create", true},
		// D-C-2：向导能建价格、绑线路、发布，门槛与单独的发布接口相同。
		{"POST /plans/complete", "h.createPlanComplete", "catalog.publish", "catalog_plan_create_complete", true},
		{"PUT /plans/{id}/complete", "h.updatePlanComplete", "catalog.publish", "catalog_plan_update_complete", true},
		{"POST /plans/{id}/versions", "h.createPlanVersion", "catalog.write", "catalog_plan_version_create", false},
		{"PUT /plans/{id}/versions/{versionID}", "h.updatePlanVersion", "catalog.write", "", false},
		{"POST /plans/{id}/versions/{versionID}/publish", "h.publishPlanVersion", "catalog.publish", "catalog_plan_version_publish", true},
		{"POST /plans/{id}/prices", "h.createPlanPrice", "catalog.publish", "catalog_price_create", true},
		{"POST /plans/{id}/prices/{priceID}/archive", "h.archivePlanPrice", "catalog.publish", "catalog_price_archive", true},
		{"POST /plans/{id}/archive", "h.archivePlan", "catalog.publish", "catalog_plan_archive", true},
		{"POST /plans/{id}/pools", "h.setPlanPools", "catalog.publish", "catalog_plan_pools_update", true},
	}
	for _, check := range checks {
		contract, ok := routes[check.key]
		if !ok {
			t.Errorf("route missing: %s", check.key)
			continue
		}
		if contract.handler != check.handler || contract.permission != check.permission || contract.idempotency != check.idempotency || contract.recentReauth != check.recentReauth {
			t.Errorf("%s contract=%+v, want handler=%s permission=%s reauth=%v idempotency=%s", check.key, contract, check.handler, check.permission, check.recentReauth, check.idempotency)
		}
	}
	if _, exists := routes["POST /plans/{id}/status"]; exists {
		t.Fatal("removed unsafe plan status route returned")
	}
}

func TestPublishContractUsesDualTokens(t *testing.T) {
	src := sourcetest.Load(t, ".").Decl("handlers.publishPlanVersion")
	for _, field := range []string{"expected_plan_row_version", "expected_version_row_version", "plan_row_version", "version_row_version"} {
		if !strings.Contains(src, field) {
			t.Fatalf("publish contract missing %s", field)
		}
	}
}

func TestLegacyVersionUpdateCannotBypassPoolWriteRoute(t *testing.T) {
	update := sourcetest.Load(t, ".").Decl("handlers.updatePlanVersion")
	for _, required := range []string{"VersionSemanticsInput", "UpdatePlanVersion"} {
		if !strings.Contains(update, required) {
			t.Fatalf("legacy version update handler no longer delegates %s contract", required)
		}
	}
	if strings.Contains(update, "setPlanPools") {
		t.Fatal("legacy version update handler delegates to pool mutation path")
	}
}
