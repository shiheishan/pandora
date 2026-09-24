// [INPUT]: 读 router.go 与 plan_change.go 的源码
// [OUTPUT]: 对外提供 TestPlanChangeRoutes
// [POS]: api/public 变更套餐路由的源码契约：下单挂独立幂等域并回放预制响应，试算不挂幂等
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"os"
	"strings"
	"testing"
)

// 变更套餐下单挂独立幂等域；试算不落库，不挂幂等（挂了会逼前端为一次试算造键）。
func TestPlanChangeRoutes(t *testing.T) {
	b, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	create := strings.Index(src, `Post("/me/subscriptions/{id}/change-plan", h.createPlanChange)`)
	preview := strings.Index(src, `r.Post("/me/subscriptions/{id}/change-plan/preview", h.previewPlanChange)`)
	if create < 0 || preview < 0 {
		t.Fatal("plan change routes missing")
	}
	if !strings.Contains(src[max(create-120, 0):create], "billing.PlanChangeIdempotencyScope") {
		t.Fatal("plan change create route lacks its idempotency scope")
	}

	h, err := os.ReadFile("plan_change.go")
	if err != nil {
		t.Fatal(err)
	}
	handler := string(h)
	start := strings.Index(handler, "func (h *handlers) createPlanChange")
	if start < 0 {
		t.Fatal("plan change handler missing")
	}
	for _, required := range []string{
		"middleware.IdempotencyClaimFrom(r.Context())",
		"in.Claim = claim",
		"httpx.WritePrepared(w, out.PreparedResponse())",
	} {
		if !strings.Contains(handler[start:], required) {
			t.Fatalf("plan change handler contract missing %q", required)
		}
	}
}
