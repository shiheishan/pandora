package public

import (
	"os"
	"strings"
	"testing"
)

func TestCreateOrderHandlerOwnsClaimAndPreparedResponse(t *testing.T) {
	body, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	start := strings.Index(s, "func (h *handlers) createOrder(")
	end := strings.Index(s[start:], "type payOrderReq struct")
	if start < 0 || end < 0 {
		t.Fatal("createOrder handler window not found")
	}
	window := s[start : start+end]
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
