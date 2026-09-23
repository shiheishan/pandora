package admin

import (
	"os"
	"strings"
	"testing"
)

func TestOrderDetailAndCancellationRoutesArePermissionGuarded(t *testing.T) {
	router, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	r := string(router)
	h := string(handlers)
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
