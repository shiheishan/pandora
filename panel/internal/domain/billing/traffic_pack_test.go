package billing

import (
	"os"
	"strings"
	"testing"
)

// 缺陷 16 / D-E-1：礼品卡流量不再加在订阅配额行上，而是发成一笔流量包余额；
// 续费与流量重置都不碰流量包余额。
func TestGiftTrafficBecomesATrafficPackGrant(t *testing.T) {
	body, err := os.ReadFile("giftgrant.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	grant := src[strings.Index(src, "func (g *GiftGranter) GrantTraffic("):]
	grant = grant[:strings.Index(grant, "\n}\n")]
	if strings.Contains(grant, "granted_addon") || strings.Contains(grant, "quota_balances") {
		t.Fatal("gift traffic must not touch subscription quota rows any more")
	}
	if !strings.Contains(grant, `GrantTrafficPackTx(ctx, tx, tenantID, userID, "gift_card", codeID, bytes)`) {
		t.Fatal("gift traffic must land in the user's traffic pack balance, one grant per code")
	}
	for _, name := range []string{"renewal.go", "traffic_reset.go"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") {
				continue
			}
			if strings.Contains(code, "traffic_pack_grants") || strings.Contains(code, "granted_addon") {
				t.Fatalf("%s must leave traffic pack balances and the retired addon column alone: %s", name, code)
			}
		}
	}
}

// 流量包订单：一行订单项指向流量包、容量快照；履约只认快照，且与订单收尾同事务。
func TestTrafficPackOrderShapeAndFulfilment(t *testing.T) {
	body, err := os.ReadFile("traffic_pack.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	create := src[strings.Index(src, "func (s *Service) CreateTrafficPackOrder("):strings.Index(src, "// fulfillTrafficPackOrder")]
	for _, needle := range []string{
		"middleware.ValidateIdempotencyClaim(", "CheckoutIdempotencyScope",
		"s.pool.InTxSerializableRetry(", "status = 'active'", "applyCoupon(",
		"'addon', 'pending_payment'", "insertHeldReservation(", "redeemCoupon(",
		"traffic_pack_id", `"metric": "traffic.bytes"`, "postBalanceHold(",
		`Kind: "addon"`, "idempotencybind.BindResource(", "SET CONSTRAINTS ALL IMMEDIATE",
	} {
		if !strings.Contains(create, needle) {
			t.Errorf("CreateTrafficPackOrder missing %q", needle)
		}
	}
	fulfil := src[strings.Index(src, "func fulfillTrafficPackOrder("):strings.Index(src, "// GrantTrafficPackTx")]
	last := -1
	for _, step := range []string{"snapshot_quotas->0->>'limit'", `GrantTrafficPackTx(ctx, tx, tenantID, userID, "order", orderID, bytes)`,
		"SET status = 'fulfilled'", "AND status = 'paid'"} {
		at := strings.Index(fulfil, step)
		if at <= last {
			t.Fatalf("fulfillTrafficPackOrder step %q missing or out of order", step)
		}
		last = at
	}
	if strings.Contains(fulfil, "FROM traffic_packs") {
		t.Fatal("fulfilment must use the purchase-time snapshot, not the pack's current size")
	}
}
