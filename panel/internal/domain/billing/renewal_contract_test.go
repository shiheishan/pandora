// [INPUT]: 依赖 renewal.go、plan_change.go 的源码文本与 subscriptionAcceptsPaidChange，依赖迁移 00095 的挂账守卫
// [OUTPUT]: 对外提供续费的源码契约测试（建单预留与幂等、零元单与履约锁序、配额周期独立）与订阅状态口径一致性测试
// [POS]: billing 续费的源码契约门禁；可续费状态组在 Go 与 00095 守卫之间只许有一份口径
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"os"
	"strings"
	"testing"
)

func TestRenewalCreateReservationAndIdempotencySourceContract(t *testing.T) {
	body, err := os.ReadFile("renewal.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (s *Service) CreateRenewal")
	end := strings.Index(src[start:], "type zeroPaySubscriptionCapture")
	if start < 0 || end < 0 {
		t.Fatal("renewal creation source boundary missing")
	}
	create := src[start : start+end]
	ordered := []string{
		"ValidateIdempotencyClaim(",
		"FROM subscriptions",
		"FOR UPDATE",
		"applyCoupon(",
		"prepareAndLockLedgerAccounts(",
		"INSERT INTO orders",
		"idempotency_key_id",
		"INSERT INTO order_reservations",
		"INSERT INTO order_reservation_events",
		"redeemCoupon(",
		"INSERT INTO balance_holds",
		"captureZeroPaySubscriptionOrder(",
		"httpx.PrepareJSON(http.StatusCreated, out)",
		"idempotencybind.BindResource(",
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
	body, err := os.ReadFile("renewal.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
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

func TestRenewalQuotaPeriodsRemainIndependent(t *testing.T) {
	body, err := os.ReadFile("renewal.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	for _, required := range []string{
		"AND period = 'cycle'",
		"qd.metric = qb.metric AND qd.period = qb.period",
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("renewal quota contract missing %q", required)
		}
	}
	if strings.Contains(src, "AND period <> 'total'") {
		t.Fatal("renewal must not reset independent day/month quota periods")
	}
	if strings.Contains(src, "qd.plan_version_id = $3::uuid AND qd.metric = qb.metric`") {
		t.Fatal("renewal quota definition join must include period")
	}
}

// R117：续费与变更套餐对订阅状态的口径只有一处（subscriptionAcceptsPaidChange），
// 建单、结算复核与迁移 00095 的挂账守卫必须是同一组状态——守卫若比 Go 宽，
// 隔离的钱会在提交时被拒、整笔回滚；若比 Go 窄，本该履约的钱会被当成挂账。
func TestSubscriptionPaidChangeStatusesMatchLatePaymentGuard(t *testing.T) {
	accepted := map[string]bool{"active": true, "trialing": true, "grace": true, "past_due": true}
	// 状态全集来自 00003 的 subscription_transitions
	for _, status := range []string{"pending", "trialing", "active", "past_due", "grace",
		"paused", "cancelled", "expired"} {
		if got := subscriptionAcceptsPaidChange(status); got != accepted[status] {
			t.Errorf("subscriptionAcceptsPaidChange(%q)=%v want %v", status, got, accepted[status])
		}
	}
	body, err := os.ReadFile("../../../migrations/00095_late_payment_ineligible_subscription.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(body)
	if at := strings.Index(up, "-- +goose Down"); at > 0 {
		up = up[:at]
	}
	if !strings.Contains(up, "AND v_sub_status IN ('active','trialing','grace','past_due')") {
		t.Fatal("00095 guard must reject exactly the statuses subscriptionAcceptsPaidChange accepts")
	}
	for _, src := range []string{"renewal.go", "plan_change.go"} {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `case "active", "trialing", "grace", "past_due":`) &&
			!strings.Contains(string(b), "func subscriptionAcceptsPaidChange(") {
			t.Errorf("%s restates the renewable status list instead of calling subscriptionAcceptsPaidChange", src)
		}
		if !strings.Contains(string(b), "subscriptionAcceptsPaidChange(status)") {
			t.Errorf("%s creation must check subscriptionAcceptsPaidChange", src)
		}
	}
}
