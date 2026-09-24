// [INPUT]: 依赖本包 router.go 源码
// [OUTPUT]: 对外提供 TestCheckoutAndRedeemRoutesAreSwitchGated
// [POS]: api/public 的降级开关路由契约：新建订单、发起支付、充值与礼品卡兑换都挂开关门，且排在幂等之前
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"os"
	"strings"
	"testing"
)

func TestCheckoutAndRedeemRoutesAreSwitchGated(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
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
