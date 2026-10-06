package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestCreateOrderHandlerOwnsClaimAndPreparedResponse(t *testing.T) {
	window := sourcetest.Load(t, ".").Decl("handlers.createOrder")
	for _, needle := range []string{
		"middleware.IdempotencyClaimFrom(r.Context())",
		"middleware.ValidateIdempotencyClaim(",
		"billing.CheckoutIdempotencyScope",
		"Claim:      claim",
		"httpx.WritePrepared(w, out.PreparedResponse())",
	} {
		if !strings.Contains(window, needle) {
			t.Errorf("createOrder handler missing %q", needle)
		}
	}
	if strings.Contains(window, "httpx.Created(") {
		t.Fatal("createOrder must not re-encode the completed idempotent response")
	}
}
