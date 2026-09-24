// [INPUT]: 依赖本包 router.go 源码的 AST（r.With(...).Method(path, handler) 链）
// [OUTPUT]: 对外提供 TestFinanceRouteProtectionContracts 契约测试
// [POS]: admin 网关营销与分销路由的保护契约：权限码、近期重认证与幂等域逐条钉死，与 coupon/catalog 两份路由契约测试互补
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
)

type routeProtection struct {
	handler      string
	permissions  []string
	recentReauth bool
	idempotency  string
}

// loadRouteProtections 读出 router.go 里每条 r.With(...).Method(path, handler) 的保护链。
func loadRouteProtections(t *testing.T) map[string]routeProtection {
	t.Helper()
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "router.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]routeProtection{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		method := strings.ToUpper(selector.Sel.Name)
		if method != "GET" && method != "POST" && method != "PUT" && method != "DELETE" {
			return true
		}
		path, ok := couponString(call.Args[0])
		if !ok {
			return true
		}
		contract := routeProtection{handler: couponExprName(call.Args[1])}
		if with, ok := selector.X.(*ast.CallExpr); ok && couponExprName(with.Fun) == "r.With" {
			for _, expr := range with.Args {
				mw, ok := expr.(*ast.CallExpr)
				if !ok {
					continue
				}
				switch couponExprName(mw.Fun) {
				case "middleware.RequirePermission":
					if permission, ok := couponString(mw.Args[0]); ok {
						contract.permissions = append(contract.permissions, permission)
					}
				case "middleware.RequireRecentReauth":
					contract.recentReauth = true
				case "middleware.Idempotency":
					if scope, ok := couponString(mw.Args[1]); ok {
						contract.idempotency = scope
					} else {
						contract.idempotency = couponExprName(mw.Args[1])
					}
				}
			}
		}
		out[method+" "+path] = contract
		return true
	})
	return out
}

func TestFinanceRouteProtectionContracts(t *testing.T) {
	routes := loadRouteProtections(t)
	want := map[string]routeProtection{
		// 批量生券一次上千张：重试不能生成第二批。
		"POST /coupons/batch": {handler: "h.generateCoupons",
			permissions: []string{"marketing.coupon.write"}, recentReauth: true,
			idempotency: "coupon_batch_generate"},
		// 改模板奖励会改变全部未兑换码的价值。
		"POST /gift-cards": {handler: "h.saveGiftTemplate",
			permissions: []string{"marketing.giftcard.write"}, recentReauth: true},
		// 分销对齐营销域权限码，不再借用 billing.order.read / billing.provider.write。
		"GET /commission/overview": {handler: "h.commissionOverview",
			permissions: []string{"marketing.commission.read"}},
		"GET /withdrawals": {handler: "h.listWithdrawals",
			permissions: []string{"marketing.commission.read"}},
		"POST /withdrawals/{id}/review": {handler: "h.reviewWithdrawal",
			permissions: []string{"marketing.withdrawal.approve"}, recentReauth: true},
		"POST /withdrawals/{id}/paid": {handler: "h.markWithdrawalPaid",
			permissions: []string{"marketing.withdrawal.approve"}, recentReauth: true,
			idempotency: "commission_withdrawal_mark_paid"},
		"POST /commission/config": {handler: "h.setCommissionConfig",
			permissions: []string{"marketing.commission.write"}, recentReauth: true},
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
