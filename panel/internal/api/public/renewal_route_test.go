package public

import (
	"os"
	"strings"
	"testing"
)

func TestRenewalRouteIsIdempotent(t *testing.T) {
	b, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
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
	b, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "func (h *handlers) createRenewal")
	end := strings.Index(src[start:], "func (h *handlers) myAnnouncements")
	if start < 0 || end < 0 {
		t.Fatal("renewal handler source boundary missing")
	}
	handler := src[start : start+end]
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
	b, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, required := range []string{"offer.currency IN ('CNY','USD')", "pr.currency IN ('CNY','USD')", "offer.user_group_id IS NULL"} {
		if !strings.Contains(src, required) {
			t.Fatalf("public catalog guard missing %s", required)
		}
	}
}
