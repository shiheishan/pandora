package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type couponRouteContract struct {
	handler      string
	permissions  []string
	recentReauth bool
}

func couponExprName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		prefix := couponExprName(value.X)
		if prefix == "" {
			return value.Sel.Name
		}
		return prefix + "." + value.Sel.Name
	default:
		return ""
	}
}

func couponString(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func loadCouponRouteContracts(t *testing.T) map[string]couponRouteContract {
	t.Helper()
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "router.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]couponRouteContract{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (selector.Sel.Name != "Get" && selector.Sel.Name != "Post") {
			return true
		}
		path, ok := couponString(call.Args[0])
		if !ok || !strings.HasPrefix(path, "/coupons") {
			return true
		}
		contract := couponRouteContract{handler: couponExprName(call.Args[1])}
		with, ok := selector.X.(*ast.CallExpr)
		if ok && couponExprName(with.Fun) == "r.With" {
			for _, expr := range with.Args {
				middlewareCall, ok := expr.(*ast.CallExpr)
				if !ok {
					continue
				}
				switch couponExprName(middlewareCall.Fun) {
				case "middleware.RequirePermission":
					if len(middlewareCall.Args) > 0 {
						if permission, ok := couponString(middlewareCall.Args[0]); ok {
							contract.permissions = append(contract.permissions, permission)
						}
					}
				case "middleware.RequireRecentReauth":
					contract.recentReauth = true
				}
			}
		}
		sort.Strings(contract.permissions)
		out[strings.ToUpper(selector.Sel.Name)+" "+path] = contract
		return true
	})
	return out
}

func TestCouponRoutePermissionContracts(t *testing.T) {
	want := map[string]couponRouteContract{
		// 两条只读路由挂只读权限（00068 授予所有持有写权限的角色）。
		"GET /coupons": {
			handler: "h.listCoupons", permissions: []string{"marketing.coupon.read"},
		},
		"GET /coupons/{id}/redemptions": {
			handler:     "h.couponRedemptions",
			permissions: []string{"billing.order.read", "marketing.coupon.read"},
		},
		"POST /coupons": {
			handler: "h.createCoupon", permissions: []string{"marketing.coupon.write"}, recentReauth: true,
		},
		// 批量生成一次能造出上千张能减钱的券，权限和重认证要求
		// 不得低于单张创建。
		"POST /coupons/batch": {
			handler: "h.generateCoupons", permissions: []string{"marketing.coupon.write"}, recentReauth: true,
		},
		"POST /coupons/{id}/status": {
			handler: "h.setCouponStatus", permissions: []string{"marketing.coupon.write"}, recentReauth: true,
		},
	}
	if got := loadCouponRouteContracts(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("coupon route contracts=%#v, want %#v", got, want)
	}
}
