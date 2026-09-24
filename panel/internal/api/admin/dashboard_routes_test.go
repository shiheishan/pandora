package admin

import (
	"go/ast"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

type dashboardRouteContract struct {
	handler      string
	permissions  []string
	recentReauth bool
}

func dashboardExprName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		prefix := dashboardExprName(value.X)
		if prefix == "" {
			return value.Sel.Name
		}
		return prefix + "." + value.Sel.Name
	default:
		return ""
	}
}

func dashboardLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func loadDashboardRoutes(t *testing.T) map[string]dashboardRouteContract {
	t.Helper()
	out := map[string]dashboardRouteContract{}
	inspectRouterFiles(t, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Get" {
			return true
		}
		path, ok := dashboardLiteral(call.Args[0])
		if !ok || len(path) < 11 || path[:11] != "/dashboard/" {
			return true
		}
		contract := dashboardRouteContract{handler: dashboardExprName(call.Args[1])}
		with, ok := selector.X.(*ast.CallExpr)
		if !ok || dashboardExprName(with.Fun) != "r.With" {
			out[path] = contract
			return true
		}
		for _, expr := range with.Args {
			middlewareCall, ok := expr.(*ast.CallExpr)
			if !ok {
				continue
			}
			switch dashboardExprName(middlewareCall.Fun) {
			case "middleware.RequirePermission":
				if len(middlewareCall.Args) > 0 {
					if permission, ok := dashboardLiteral(middlewareCall.Args[0]); ok {
						contract.permissions = append(contract.permissions, permission)
					}
				}
			case "middleware.RequireRecentReauth":
				contract.recentReauth = true
			}
		}
		sort.Strings(contract.permissions)
		out[path] = contract
		return true
	})
	return out
}

func TestDashboardRoutePermissionContracts(t *testing.T) {
	want := map[string]dashboardRouteContract{
		"/dashboard/traffic/nodes":         {handler: "h.dashboardNodeTraffic", permissions: []string{"metering.read", "node.read"}},
		"/dashboard/traffic/users":         {handler: "h.dashboardUserTraffic", permissions: []string{"iam.user.read", "metering.read"}},
		"/dashboard/backlog/notifications": {handler: "h.dashboardNotificationBacklog", permissions: []string{"ops.notification.read"}},
	}
	got := loadDashboardRoutes(t)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dashboard route contracts=%#v, want %#v", got, want)
	}
}
