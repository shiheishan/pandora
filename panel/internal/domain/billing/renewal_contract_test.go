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
	end := strings.Index(src[start:], "type zeroPayRenewalCapture")
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
		"captureZeroPayRenewal(",
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
		"func lockRenewalSubscriptionForSettlement(",
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
