// [INPUT]: 依赖 platform/sourcetest 按名取 NewRouter 与 handlers.createPlanChange 的源码
// [OUTPUT]: 对外提供 TestPlanChangeRoutes
// [POS]: api/public 变更套餐路由的源码契约：下单挂独立幂等域并回放预制响应，试算不挂幂等
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 变更套餐下单挂独立幂等域；试算不落库，不挂幂等（挂了会逼前端为一次试算造键）。
func TestPlanChangeRoutes(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	src := pkg.Decl("NewRouter")
	create := strings.Index(src, `Post("/me/subscriptions/{id}/change-plan", h.createPlanChange)`)
	preview := strings.Index(src, `r.Post("/me/subscriptions/{id}/change-plan/preview", h.previewPlanChange)`)
	if create < 0 || preview < 0 {
		t.Fatal("plan change routes missing")
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
