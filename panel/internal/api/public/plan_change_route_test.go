package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 变更套餐下单挂独立幂等域；试算 change-plan/preview 已退役（统一报价取代，设计稿 2.2）。
// 报价不落库，不挂幂等（挂了会逼前端为一次报价造键）。
func TestPlanChangeRoutes(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	src := pkg.Decl("NewRouter")
	create := strings.Index(src, `Post("/me/subscriptions/{id}/change-plan", h.createPlanChange)`)
	quote := strings.Index(src, `r.With(checkout).Post("/me/checkout/quote", h.checkoutQuote)`)
	if create < 0 || quote < 0 {
		t.Fatal("plan change or quote routes missing")
	}
	if strings.Contains(src, "change-plan/preview") {
		t.Fatal("the retired change-plan/preview route is still registered")
	}
	if !strings.Contains(src[max(create-120, 0):create], "billing.PlanChangeIdempotencyScope") {
		t.Fatal("plan change create route lacks its idempotency scope")
	}

	handler := pkg.Decl("handlers.createPlanChange")
	for _, required := range []string{
		"middleware.IdempotencyClaimFrom(r.Context())",
		"in.Claim = claim",
		"httpx.WritePrepared(w, out.PreparedResponse())",
	} {
		if !strings.Contains(handler, required) {
			t.Fatalf("plan change handler contract missing %q", required)
		}
	}
}
