// [INPUT]: 依赖 platform/sourcetest 按名取 AdminCancelOrder、releaseOrderReservation 及释放链路各锁函数、ExpireDueReservations 的源码
// [OUTPUT]: 对外提供 TestAdminCancellationAddsCASWithoutWeakeningTerminalIdempotency、TestReleaseTransactionSourceContract、TestReleasePaymentEvidenceSourceContract、TestReleaseResourceAndEvidenceSourceContract、TestReservationExpiryWorkerSourceContract
// [POS]: billing 取消与过期释放的源码契约：后台取消加 state_version CAS 而不削弱同终态幂等、释放事务的锁序、已结算收款证据的有序锁、资源回退形状、过期扫描的 SKIP LOCKED 与同步钩子次序
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestAdminCancellationAddsCASWithoutWeakeningTerminalIdempotency(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	s := pkg.Decls("Service.AdminCancelOrder", "releaseOrderReservation")
	for _, want := range []string{
		`func (s *Service) AdminCancelOrder(`,
		`ExpectedStateVersion: in.ExpectedStateVersion`,
		`ActorKind: "admin"`,
		`APIDomain: "admin"`,
		`if req.ExpectedStateVersion > 0 && shape.StateVersion != req.ExpectedStateVersion`,
		`AND ($4::bigint=0 OR state_version=$4)`,
		`RETURNING state_version,cancelled_at,cancel_reason`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("admin cancellation contract is missing %q", want)
		}
	}
	release := pkg.Decl("releaseOrderReservation")
	terminal := strings.Index(release, `case req.Target:`)
	cas := strings.Index(release, `if req.ExpectedStateVersion > 0 && shape.StateVersion != req.ExpectedStateVersion`)
	if terminal < 0 || cas < 0 || terminal >= cas {
		t.Fatal("terminal idempotency must be evaluated before active-order CAS")
	}
}

func TestReleaseTransactionSourceContract(t *testing.T) {
	tx := sourcetest.Load(t, ".").Decl("releaseOrderReservation")
	ordered := []string{
		"lockReleaseOrder(",
		"lockActiveReleaseIntents(",
		"lockAndRejectSettledPaymentEvidence(",
		"lockReleaseReservationGraph(",
		"prepareAndLockLedgerAccounts(",
		"Post(ctx, tx",
		"releaseLockedReservation(",
		"UPDATE orders",
		"audit.Write(",
		"SET CONSTRAINTS ALL IMMEDIATE",
	}
	last := -1
	for _, needle := range ordered {
		at := strings.Index(tx, needle)
		if at < 0 {
			t.Fatalf("release transaction missing %q", needle)
		}
		if at <= last {
			t.Fatalf("release transaction order is not monotonic at %q", needle)
		}
		last = at
	}
	for _, needle := range []string{
		`case "paid", "fulfilled", "partially_refunded", "refunded"`,
		`case req.Target:`,
		`AlreadyTerminal = true`,
		`AND status=$5`,
		`Kind: "balance_release"`,
		`AccountID: locked.Balance.HoldAccountID, Direction: Debit`,
		`AccountID: locked.Balance.AvailableAccountID, Direction: Credit`,
	} {
		if !strings.Contains(tx, needle) {
			t.Errorf("release transaction contract missing %q", needle)
		}
	}
	idempotentStart := strings.Index(tx, "case req.Target:")
	if idempotentStart < 0 {
		t.Fatal("idempotent release branch is missing")
	}
	idempotentEnd := strings.Index(tx[idempotentStart:], `case "cancelled", "expired":`)
	if idempotentEnd < 0 {
		t.Fatal("idempotent release branch is missing")
	}
	idempotent := tx[idempotentStart : idempotentStart+idempotentEnd]
	if strings.Contains(idempotent, "lockAndRejectSettledPaymentEvidence(") {
		t.Fatal("same-terminal idempotency must not be rejected by later payment evidence")
	}
	if !strings.Contains(idempotent, "AlreadyTerminal = true") {
		t.Fatal("same-terminal release must remain idempotent")
	}
}

func TestReleasePaymentEvidenceSourceContract(t *testing.T) {
	lock := sourcetest.Load(t, ".").Decl("lockAndRejectSettledPaymentEvidence")
	for _, needle := range []string{
		`SELECT id::text FROM payment_intents`,
		`status='succeeded'`,
		`SELECT id::text FROM payments`,
		`errOrderReleasePaymentEvidence`,
	} {
		if !strings.Contains(lock, needle) {
			t.Errorf("settled-payment evidence lock missing %q", needle)
		}
	}
	if count := strings.Count(lock, "ORDER BY id FOR UPDATE"); count != 1 {
		t.Fatalf("active intent evidence needs one ordered mutable-row lock, got %d", count)
	}
	if !strings.Contains(lock, "SELECT id::text FROM payments") ||
		!strings.Contains(lock, "ORDER BY id`") {
		t.Fatal("immutable payment evidence must be read after the serialized order lock")
	}
}

func TestReleaseResourceAndEvidenceSourceContract(t *testing.T) {
	// 释放链路上加锁、校验与回退资源的各个函数
	s := sourcetest.Load(t, ".").Decls("releaseOrderReservation", "lockActiveReleaseIntents",
		"lockAndRejectSettledPaymentEvidence", "lockReleaseBalance", "releaseLockedReservation", "validateIdempotentRelease")
	for _, needle := range []string{
		`status='succeeded'`,
		`SELECT id::text FROM payments`,
		`errOrderReleasePaymentEvidence`,
		`stock_reserved=stock_reserved-$3`,
		`reserved=reserved-$4`,
		`reserved_count=reserved_count-1`,
		`status='released',released_at=now(),reverted_at=now()`,
		`status='released',release_txn_id=$4::uuid,released_at=now()`,
		`state='released',released_at=now(),release_reason=$4`,
		`'held','released',$4,$5::uuid`,
		`business_request_id=$5::uuid`,
		`tag.RowsAffected() != 1`,
		`FOR UPDATE OF h`,
		`ORDER BY id FOR UPDATE`,
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("release resource contract missing %q", needle)
		}
	}
}

func TestReservationExpiryWorkerSourceContract(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	s := pkg.Decl("Service.ExpireDueReservations")
	for _, needle := range []string{
		`r.state='held' AND r.expires_at<=now()`,
		`o.status IN ('draft','pending_payment','processing')`,
		`LIMIT $2 FOR UPDATE OF o SKIP LOCKED`,
		`rows.Close()`,
		`reservationExpiryCandidatesHook(ctx)`,
		`append([]string(nil), candidates...)`,
		`for _, orderID := range candidates`,
		`s.pool.InTx`,
		`RequireDue: true, SkipLocked: true`,
		`errors.Join(failures...)`,
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("reservation expiry contract missing %q", needle)
		}
	}
	queryAt := strings.Index(s, `LIMIT $2 FOR UPDATE OF o SKIP LOCKED`)
	closeAt, hookAt, releaseAt := -1, -1, -1
	if queryAt >= 0 {
		if relative := strings.Index(s[queryAt:], `rows.Close()`); relative >= 0 {
			closeAt = queryAt + relative
		}
		if relative := strings.Index(s[queryAt:], `reservationExpiryCandidatesHook(ctx)`); relative >= 0 {
			hookAt = queryAt + relative
		}
		if relative := strings.Index(s[queryAt:], `for _, orderID := range candidates`); relative >= 0 {
			releaseAt = queryAt + relative
		}
	}
	if queryAt < 0 || closeAt <= queryAt || hookAt <= closeAt || releaseAt <= hookAt {
		t.Fatalf("expiry synchronization hook order query=%d close=%d hook=%d release=%d",
			queryAt, closeAt, hookAt, releaseAt)
	}
	if !strings.Contains(pkg.Decl("lockReleaseOrder"), "query += ` SKIP LOCKED`") {
		t.Fatal("expiry must acquire the order with SKIP LOCKED")
	}
}
