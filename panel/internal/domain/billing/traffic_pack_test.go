// [INPUT]: 依赖 platform/sourcetest 按名取 GiftGranter.GrantTraffic、续费与流量重置的全部声明、CreateTrafficPackOrder 与 fulfillTrafficPackOrder 的源码
// [OUTPUT]: 对外提供 TestGiftTrafficBecomesATrafficPackGrant、TestTrafficPackOrderShapeAndFulfilment
// [POS]: billing 流量包（D-E-1）的源码契约：礼品卡流量发成流量包余额、续费与流量重置不碰流量包，流量包订单的建单形状与按快照履约的次序
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 缺陷 16 / D-E-1：礼品卡流量不再加在订阅配额行上，而是发成一笔流量包余额；
// 续费与流量重置都不碰流量包余额。
func TestGiftTrafficBecomesATrafficPackGrant(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	grant := pkg.Decl("GiftGranter.GrantTraffic")
	if strings.Contains(grant, "granted_addon") || strings.Contains(grant, "quota_balances") {
		t.Fatal("gift traffic must not touch subscription quota rows any more")
	}
	if !strings.Contains(grant, `GrantTrafficPackTx(ctx, tx, tenantID, userID, "gift_card", codeID, bytes)`) {
		t.Fatal("gift traffic must land in the user's traffic pack balance, one grant per code")
	}
	// 续费与流量重置的全部声明（原先按 renewal.go、traffic_reset.go 两个文件读）
	for name, body := range map[string]string{
		"renewal": pkg.Decls("ErrSubNotRenewable", "ErrRenewPriceGone", "RenewalIdempotencyScope", "CreateRenewalInput",
			"Service.CreateRenewal", "zeroPaySubscriptionCapture", "Service.captureZeroPaySubscriptionOrder",
			"subscriptionBoundOrderKind", "subscriptionAcceptsPaidChange", "lockOrderSubscriptionForSettlement",
			"Service.fulfillRenewal", "Service.fulfillRenewalLocked", "Service.RollQuotaPeriods"),
		"traffic reset": pkg.Decls("LogTrafficReset", "ResetLog", "ListResetLogsInput", "Service.ListTrafficResets",
			"ResetStats", "Service.TrafficResetStats", "ManualResetInput", "Service.ManualResetTraffic"),
	} {
		for _, line := range strings.Split(body, "\n") {
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
	pkg := sourcetest.Load(t, ".")
	create := pkg.Decl("Service.CreateTrafficPackOrder")
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
	fulfil := pkg.Decl("fulfillTrafficPackOrder")
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
