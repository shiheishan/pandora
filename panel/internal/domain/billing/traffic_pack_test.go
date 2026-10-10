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
	if !strings.Contains(grant, `GrantTrafficPackTx(ctx, tx, tenantID, userID, sub, "gift_card", codeID, bytes)`) {
		t.Fatal("gift traffic must land in the user's traffic pack balance, one grant per code")
	}
	// 续费与流量重置的全部声明（原先按 renewal.go、traffic_reset.go 两个文件读）
	for name, body := range map[string]string{
		"renewal": pkg.Decls("ErrSubNotRenewable", "ErrRenewPriceGone", "RenewalIdempotencyScope", "CreateRenewalInput",
			"Service.CreateRenewal", "zeroPaySubscriptionCapture", "Service.captureZeroPaySubscriptionOrder",
			"subscriptionBoundOrderKind", "subscriptionAcceptsPaidChange", "lockOrderSubscriptionForSettlement",
			"Service.fulfillRenewal", "Service.fulfillRenewalLocked", "Service.RollQuotaPeriods",
			"renewSubscriptionTx", "restartQuotaPeriodsTx", "renewalBase"),
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
		"lockLiveSubscriptionForPack(", "balancePlan(", "checkExpectation(",
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
	for _, step := range []string{"snapshot_quotas->0->>'limit'", `GrantTrafficPackTx(ctx, tx, tenantID, userID, subID, "order", orderID, bytes)`,
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
	// 建单必须指定一份，履约不替用户挑一份、也不留作未分配：没有就报错（用户 2026-10-09 删掉
	// 升级前在途单的兼容）
	guard := strings.Index(fulfil, "if subID == nil {")
	grant := strings.Index(fulfil, "GrantTrafficPackTx(")
	if guard < 0 || guard > grant || !strings.HasPrefix(strings.TrimSpace(fulfil[guard+len("if subID == nil {"):]),
		`return "", errors.New(`) || strings.Contains(pkg.Source(), "soleLiveSubscription") {
		t.Fatal("an addon order without a target subscription must fail fulfilment, not be attached by guess")
	}
}

// 流量包转移：来源那份还在用（生效中或可救回）时一律拒绝，且在动余额之前；挑哪些余额只看挂在哪一份、
// 有没有余量，不看转移流水是谁写的（用户 2026-10-09 删掉了升级前旧包可从在用的那份挪一次）。
// 数据库那头由 00157 版改挂守卫兜底，见 PG18 traffic_pack 域的 traffic packs belong to a subscription。
func TestTrafficPackTransferRefusesLiveSource(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	body := strings.Join(strings.Fields(pkg.Decl("transferTrafficPacksTx")), " ")
	refuse := strings.Index(body, "if !ended { return 0, errTransferSource }")
	move := strings.Index(body, "UPDATE traffic_pack_grants g")
	if refuse < 0 || move < 0 || refuse > move {
		t.Fatal("a live source subscription must be refused before any balance is moved")
	}
	if strings.Contains(body, "FROM traffic_pack_transfers") || strings.Contains(pkg.Source(), "actor_kind = 'migration'") ||
		strings.Contains(pkg.Source(), "actor_kind <> 'migration'") {
		t.Fatal("which balances move must not depend on who wrote their transfer records")
	}
	if !strings.Contains(pkg.Decl("errTransferSource"), "httpx.CodeConflict") {
		t.Fatal("moving out of a subscription still in use is a state conflict (409), as for the target")
	}
}
