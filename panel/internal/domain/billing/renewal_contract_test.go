package billing

import (
	"os"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestRenewalCreateReservationAndIdempotencySourceContract(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	// 订阅、套餐、价格的读取与校验在 loadRenewalTargetTx（报价不锁、建单锁，renewal_price.go）
	loader := pkg.Decls("loadRenewalTargetTx", "loadRenewalSourceTx")
	for _, needle := range []string{"FROM subscriptions", "FOR UPDATE", "ensureNoOpenSubscriptionOrder(",
		"loadRenewalPriceTx("} {
		if !strings.Contains(loader, needle) {
			t.Fatalf("renewal target loader missing %q", needle)
		}
	}
	create := pkg.Decl("Service.CreateRenewal")
	ordered := []string{
		"ValidateIdempotencyClaim(",
		"loadRenewalTargetTx(ctx, tx, tenantID, in.UserID, in.SubscriptionID,\n\t\t\tin.PriceID, true, now)",
		"applyCoupon(",
		"prepareAndLockLedgerAccounts(",
		"INSERT INTO orders",
		"idempotency_key_id",
		"INSERT INTO order_reservations",
		"INSERT INTO order_reservation_events",
		"redeemCoupon(",
		"INSERT INTO balance_holds",
		"captureZeroPaySubscriptionOrder(",
		"idempotencybind.BindResource(",
		"s.settlePaymentTx(",
		"httpx.PrepareJSON(http.StatusCreated, out)",
		"idempotencybind.CompleteSuccessJSON(",
		"audit.Write(",
		"SET CONSTRAINTS ALL IMMEDIATE",
	}
	last := -1
	for _, needle := range ordered {
		at := strings.Index(create, needle)
		if at < 0 {
			t.Fatalf("renewal create contract missing %q", needle)
		}
		if at <= last {
			t.Fatalf("renewal create contract order is not monotonic at %q", needle)
		}
		last = at
	}
}

func TestRenewalZeroPayAndFulfilmentLockSourceContract(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	// 零元单捕获、续费结算锁（锁序写在它的文档注释里）与续费履约
	src := pkg.Decls("Service.CreateRenewal", "Service.captureZeroPaySubscriptionOrder", "Service.fulfillRenewalLocked") +
		"\n" + pkg.DeclWithDoc("lockOrderSubscriptionForSettlement")
	for _, required := range []string{
		"lockOrderReservationGraph(ctx, tx, reservationLockRequest{",
		`Kind: "renewal"`,
		`Kind: "order_paid"`,
		"captureLockedReservation(",
		`SET status='paid', paid_amount=total_amount`,
		"s.fulfillRenewalLocked(",
		"func lockOrderSubscriptionForSettlement(",
		"order -> subscription -> payment intents -> reservation graph -> ledger",
		"FOR UPDATE",
		"AND user_id=$3::uuid",
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("renewal settlement contract missing %q", required)
		}
	}
}

// 续费的配额口径（2026-10-07 规则 5 与过期恢复）：提前续费一行配额都不动，只有过期恢复
// 才把全部配额对齐清零；更新上限的连接键必须带 period。
func TestRenewalQuotaPeriodsRemainIndependent(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	renew := pkg.Decl("renewSubscriptionTx")
	for _, required := range []string{
		"renewalBase(out.PreviousEnd, paidAt, g.OrderCreatedAt)",
		"if restart {",
		"restartQuotaPeriodsTx(",
		"qd.metric = qb.metric AND qd.period = qb.period",
	} {
		if !strings.Contains(renew, required) {
			t.Fatalf("renewal quota contract missing %q", required)
		}
	}
	if strings.Count(pkg.Source(), "restartQuotaPeriodsTx(") != 2 {
		t.Fatal("restartQuotaPeriodsTx must only be called from renewSubscriptionTx")
	}
	restart := pkg.Decl("restartQuotaPeriodsTx")
	if !strings.Contains(restart, `quotaPeriodEndSQL("qb.period", "$3::timestamptz", "$4::timestamptz")`) {
		t.Fatal("restart must align every quota period through quotaPeriodEndSQL")
	}
	if !strings.Contains(pkg.Decl("Service.fulfillRenewalLocked"), "SELECT paid_at, created_at FROM orders") {
		t.Fatal("renewal fulfilment must use the payment moment as its base")
	}
	if strings.Contains(pkg.Source(), "qd.plan_version_id = $3::uuid AND qd.metric = qb.metric`") {
		t.Fatal("renewal quota definition join must include period")
	}
}

// R117：续费与变更套餐对订阅状态的口径只有一处（subscriptionAcceptsPaidChange），
// 建单、结算复核与迁移 00095 的挂账守卫必须是同一组状态——守卫若比 Go 宽，
// 隔离的钱会在提交时被拒、整笔回滚；若比 Go 窄，本该履约的钱会被当成挂账。
func TestSubscriptionPaidChangeStatusesMatchLatePaymentGuard(t *testing.T) {
	accepted := map[string]bool{"active": true, "trialing": true, "grace": true, "past_due": true}
	// 状态全集来自 00003 的 subscription_transitions；已过期只在原地续费窗口没关时收（00124）
	for _, status := range []string{"pending", "trialing", "active", "past_due", "grace",
		"paused", "cancelled", "expired"} {
		for _, closed := range []bool{false, true} {
			want := accepted[status] || (status == "expired" && !closed)
			if got := subscriptionAcceptsPaidChange(status, closed); got != want {
				t.Errorf("subscriptionAcceptsPaidChange(%q, closed=%v)=%v want %v", status, closed, got, want)
			}
		}
	}
	body, err := os.ReadFile("../../../migrations/00124_subscription_expired_renewal.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(body)
	if at := strings.Index(up, "-- +goose Down"); at > 0 {
		up = up[:at]
	}
	if !strings.Contains(up, `AND (v_sub_status IN ('active','trialing','grace','past_due')
            OR (v_sub_status='expired' AND v_sub_closed IS NULL)) THEN`) {
		t.Fatal("00124 guard must reject exactly the statuses subscriptionAcceptsPaidChange accepts")
	}
	pkg := sourcetest.Load(t, ".")
	// 状态列表只许写在 subscriptionAcceptsPaidChange 里，包内别处重写一份即失败
	statusList := `case "active", "trialing", "grace", "past_due":`
	if strings.Count(pkg.Source(), statusList) != strings.Count(pkg.Decl("subscriptionAcceptsPaidChange"), statusList) {
		t.Error("the renewable status list is restated outside subscriptionAcceptsPaidChange")
	}
	for creator, call := range map[string]string{
		"loadRenewalSourceTx": "subscriptionAcceptsPaidChange(t.Status, renewalClosed)",
		"loadChangeSourceTx":  "subscriptionAcceptsPaidChange(src.Status, renewalClosed)",
	} {
		if !strings.Contains(pkg.Decl(creator), call) {
			t.Errorf("%s creation must check subscriptionAcceptsPaidChange", creator)
		}
	}
}
