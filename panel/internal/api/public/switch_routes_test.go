package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestCheckoutAndRedeemRoutesAreSwitchGated(t *testing.T) {
	source := sourcetest.Load(t, ".").Decl("NewRouter")
	if !strings.Contains(source, `checkout := middleware.FeatureSwitch(d.Pool, "billing.checkout",`) {
		t.Fatal("billing.checkout gate is not defined")
	}
	for route, gate := range map[string]string{
		`Post("/orders", h.createOrder)`:                                 "r.With(checkout, middleware.Idempotency(",
		`Post("/me/traffic-pack-orders", h.createTrafficPackOrder)`:      "r.With(checkout, middleware.Idempotency(",
		`Post("/me/subscriptions/{id}/renew", h.createRenewal)`:          "r.With(checkout, middleware.Idempotency(",
		`Post("/me/subscriptions/{id}/change-plan", h.createPlanChange)`: "r.With(checkout, middleware.Idempotency(",
		`Post("/me/topups", h.createTopup)`:                              "r.With(checkout, middleware.Idempotency(",
		`Post("/orders/{id}/pay", h.payOrder)`:                           "r.With(checkout).",
		`Post("/me/checkout/quote", h.checkoutQuote)`:                    "r.With(checkout).",
		`Post("/gift-cards/redeem", h.redeemGiftCard)`:                   `middleware.FeatureSwitch(d.Pool, "marketing.giftcard.redeem",`,
	} {
		at := strings.Index(source, route)
		if at < 0 {
			t.Fatalf("route missing: %s", route)
		}
		if !strings.Contains(source[max(0, at-220):at], gate) {
			t.Errorf("%s must be gated by %q ahead of idempotency", route, gate)
		}
	}
	// 支付回调不受 billing.checkout 影响：已发起的支付必须照常入账
	at := strings.Index(source, `Post("/webhooks/payments/{provider}", h.paymentWebhook)`)
	if at < 0 || strings.Contains(source[max(0, at-120):at], "checkout") {
		t.Fatal("payment webhook must not be switch-gated")
	}
}
