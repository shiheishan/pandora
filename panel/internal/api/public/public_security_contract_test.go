package public

import (
	"os"
	"strings"
	"testing"
)

func TestTelegramWebhookValidatesPersistedSecret(t *testing.T) {
	handlerRaw, err := os.ReadFile("telegram.go")
	if err != nil {
		t.Fatal(err)
	}
	handler := string(handlerRaw)
	for _, want := range []string{
		`chi.URLParam(r, "secret")`,
		"subtle.ConstantTimeCompare",
		"WebhookSecret",
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("telegram webhook handler lacks secret validation contract %q", want)
		}
	}
}

func TestCommissionTransferRequiresIdempotencyMiddleware(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	route := `Post("/me/commission/transfer", h.transferCommission)`
	at := strings.Index(source, route)
	if at < 0 {
		t.Fatal("commission transfer route missing")
	}
	start := at - 300
	if start < 0 {
		start = 0
	}
	window := source[start:at]
	if !strings.Contains(window, "middleware.Idempotency") ||
		!strings.Contains(window, "billing.CommissionTransferIdempotencyScope") {
		t.Fatal("commission transfer route lacks globally unique idempotency middleware scope")
	}
}

func TestPortalMoneyActionsUseStableIdempotencyAttempts(t *testing.T) {
	raw, err := os.ReadFile("../../../web/portal/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, route := range []string{
		"/v1/me/topups",
		"/renew',",
		"/v1/gift-cards/redeem",
	} {
		at := strings.Index(source, route)
		if at < 0 {
			t.Fatalf("portal route missing %q", route)
		}
		end := at + 350
		if end > len(source) {
			end = len(source)
		}
		if !strings.Contains(source[at:end], "keyFor(") {
			t.Fatalf("portal route %q lacks stable idempotency attempt", route)
		}
	}
}
