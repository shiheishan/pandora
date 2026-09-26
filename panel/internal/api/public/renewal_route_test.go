// [INPUT]: 依赖 platform/sourcetest 按名取 NewRouter、handlers.createRenewal 与 handlers.listPlans 的源码
// [OUTPUT]: 对外提供 TestRenewalRouteIsIdempotent、TestRenewalHandlerConsumesClaimAndWritesPreparedResponse、TestPublicCatalogRequiresApplicableAllowedCurrencyPrice
// [POS]: api/public 续费的独立幂等域与预制响应、公开套餐目录只给可用币种的适用价格
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestRenewalRouteIsIdempotent(t *testing.T) {
	src := sourcetest.Load(t, ".").Decl("NewRouter")
	idx := strings.Index(src, "/me/subscriptions/{id}/renew")
	if idx < 0 {
		t.Fatal("renewal route missing")
	}
	start := idx - 180
	if start < 0 {
		start = 0
	}
	if !strings.Contains(src[start:idx], "subscription_renewal_create") {
		t.Fatal("renewal route lacks independent idempotency scope")
	}
}

func TestRenewalHandlerConsumesClaimAndWritesPreparedResponse(t *testing.T) {
	handler := sourcetest.Load(t, ".").Decl("handlers.createRenewal")
	for _, required := range []string{
		"middleware.IdempotencyClaimFrom(r.Context())",
		"billing.RenewalIdempotencyScope",
		"Claim: claim",
		"httpx.WritePrepared(w, out.PreparedResponse())",
	} {
		if !strings.Contains(handler, required) {
			t.Fatalf("renewal handler contract missing %q", required)
		}
	}
	if strings.Contains(handler, "httpx.OK(") {
		t.Fatal("renewal handler must not reconstruct a response outside the transaction")
	}
}

func TestPublicCatalogRequiresApplicableAllowedCurrencyPrice(t *testing.T) {
	src := sourcetest.Load(t, ".").Decl("handlers.listPlans")
	for _, required := range []string{"offer.currency IN ('CNY','USD')", "pr.currency IN ('CNY','USD')", "offer.user_group_id IS NULL"} {
		if !strings.Contains(src, required) {
			t.Fatalf("public catalog guard missing %s", required)
		}
	}
}
