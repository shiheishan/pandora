// [INPUT]: 依赖本包路由源码（router.go 与 router_<模块>.go，经 router_source_test.go 的 inspectRouterFiles）的 AST（r.With(...).Method(path, handler) 链）
// [OUTPUT]: 对外提供 TestFinanceRouteProtectionContracts 契约测试
// [POS]: admin 网关营销（礼品卡批次与导出、优惠券批量）与分销路由的保护契约：权限码、近期重认证与幂等域逐条钉死，旧的明文导出路由不得复活，与 coupon/catalog 两份路由契约测试互补
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"go/ast"
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

// loadRouteProtections 读出全部路由源文件里每条 r.With(...).Method(path, handler) 的保护链。
func loadRouteProtections(t *testing.T) map[string]routeProtection {
	t.Helper()
	out := map[string]routeProtection{}
	inspectRouterFiles(t, func(node ast.Node) bool {
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
		// 礼品卡批次：列表只读；明文卡码的唯一出口是一次性导出。
		"GET /gift-cards/batches": {handler: "h.listGiftBatches",
			permissions: []string{"marketing.giftcard.read"}},
		"POST /gift-cards/batches/{id}/export": {handler: "h.exportGiftBatch",
			permissions: []string{"marketing.giftcard.write"}, recentReauth: true,
			idempotency: "giftcard_batch_export"},
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
	// 旧的明文导出只要读权限、可无限次导出，必须下线。
	if _, exists := routes["GET /gift-cards/codes/export"]; exists {
		t.Error("plaintext re-export route GET /gift-cards/codes/export must stay removed")
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
