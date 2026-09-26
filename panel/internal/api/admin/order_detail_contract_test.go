// [INPUT]: 依赖 router_source_test.go 的 routerSource，依赖 platform/sourcetest 按名取订单详情、支付记录与取消处理器的源码
// [OUTPUT]: 对外提供 TestOrderDetailAndCancellationRoutesArePermissionGuarded
// [POS]: api/admin 订单详情与取消的权限、重认证、幂等契约，不许出现退款、改单、删单路由
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestOrderDetailAndCancellationRoutesArePermissionGuarded(t *testing.T) {
	router, err := routerSource()
	if err != nil {
		t.Fatal(err)
	}
	r := string(router)
	h := sourcetest.Load(t, ".").Decls("handlers.getOrder", "handlers.getOrderPayments", "handlers.cancelOrder")
	for _, want := range []string{
		`RequirePermission("billing.order.read", d.Log)`,
		`Get("/orders/{id}", h.getOrder)`,
		`RequirePermission("billing.payment.read", d.Log)`,
		`Get("/orders/{id}/payments", h.getOrderPayments)`,
		`func (h *handlers) getOrder(`,
		`h.d.Ops.GetOrder(`,
		`func (h *handlers) getOrderPayments(`,
		`h.d.Ops.GetOrderPaymentHistory(`,
		`RequirePermission("billing.order.write", d.Log)`,
		`RequireRecentReauth(d.Log)`,
		`Idempotency(d.Pool, "admin_order_cancel", d.Log)`,
		`Post("/orders/{id}/cancel", h.cancelOrder)`,
		`h.d.Billing.AdminCancelOrder(`,
		`ExpectedStateVersion: req.ExpectedStateVersion`,
		`chi.URLParam(r, "id")`,
		`map[string]any{"order": order}`,
	} {
		if !strings.Contains(r+h, want) {
			t.Fatalf("order detail HTTP contract is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`Post("/orders/{id}/refund"`,
		`Put("/orders/{id}"`,
		`Delete("/orders/{id}"`,
	} {
		if strings.Contains(r, forbidden) {
			t.Fatalf("unsafe order mutation route appeared: %q", forbidden)
		}
	}
}
