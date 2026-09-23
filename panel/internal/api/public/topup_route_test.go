package public

import (
	"os"
	"strings"
	"testing"
)

func TestTopupRouteHasIndependentIdempotencyMiddleware(t *testing.T) {
	b, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	idx := strings.Index(src, "/me/topups")
	if idx < 0 {
		t.Fatal("topup route missing")
	}
	start := idx - 220
	if start < 0 {
		start = 0
	}
	window := src[start:idx]
	if !strings.Contains(window, "middleware.Idempotency") ||
		!strings.Contains(window, "billing.TopupIdempotencyScope") {
		t.Fatal("topup route lacks its independent idempotency middleware")
	}
}

func TestTopupHandlerWritesTransactionPreparedResponse(t *testing.T) {
	b, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "func (h *handlers) createTopup")
	if start < 0 {
		t.Fatal("topup handler start missing")
	}
	end := strings.Index(src[start:], "func (h *handlers) createRenewal")
	if end < 0 {
		t.Fatal("topup handler boundaries missing")
	}
	body := src[start : start+end]
	for _, required := range []string{
		"middleware.IdempotencyClaimFrom",
		"middleware.ValidateIdempotencyClaim",
		"httpx.WritePrepared",
		"out.PreparedResponse()",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("topup handler missing %s", required)
		}
	}
	if strings.Contains(body, "httpx.OK") || strings.Contains(body, "map[string]any") {
		t.Fatal("topup handler re-encodes or rebuilds its committed response")
	}
}
