package billing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

const (
	checkoutPG18Tenant = "93000000-0000-7000-8000-000000000001"
	checkoutPG18User   = "93000000-0000-7000-8000-000000000011"
	checkoutPG18PlanA  = "93000000-0000-7000-8000-000000000031"
	checkoutPG18PriceA = "93000000-0000-7000-8000-000000000041"
	checkoutPG18PlanB  = "93000000-0000-7000-8000-000000000032"
	checkoutPG18PriceB = "93000000-0000-7000-8000-000000000042"
	checkoutPG18ClaimA = "93000000-0000-7000-8000-000000000101"
	checkoutPG18ClaimB = "93000000-0000-7000-8000-000000000102"
	checkoutPG18ClaimC = "93000000-0000-7000-8000-000000000103"
)

func TestCheckoutAtomicPG18(t *testing.T) {
	dsn := os.Getenv("AEGIS_CHECKOUT_PG18_DSN")
	if dsn == "" {
		t.Skip("AEGIS_CHECKOUT_PG18_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	pool, err := platformdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open checkout PG18 pool: %v", err)
	}
	defer pool.Close()
	checkoutPG18AssertRuntimeRole(t, ctx, pool)
	service := NewService(pool, nil)

	lease := checkoutPG18Time(t, "2099-07-30T20:00:00Z")
	t.Run("A unlimited pending graph and exact completion bytes", func(t *testing.T) {
		claim := checkoutPG18Claim(checkoutPG18ClaimA, "checkout-a", "checkout-a-request", lease)
		out, err := service.CreateOrder(ctx, checkoutPG18Tenant, CreateOrderInput{
			UserID: checkoutPG18User, PlanID: checkoutPG18PlanA,
			PriceID: checkoutPG18PriceA, Claim: claim,
		})
		if err != nil {
			t.Fatalf("CreateOrder A: %v", err)
		}
		if out.Status != "pending_payment" || out.PayableAmount != 1000 ||
			out.PreparedResponse().StatusCode() != http.StatusCreated {
			t.Fatalf("A output mismatch: %#v", out)
		}
		checkoutPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
			var status, keyStatus, resourceType, resourceID, contentType string
			var parent, events, items, stock, purchase, coupon, holds int
			var code int
			var payload []byte
			err := tx.QueryRow(ctx, `
				SELECT o.status,k.status,k.resource_type,k.resource_id::text,
				       k.response_code,k.response_payload,k.response_content_type,
				       (SELECT count(*) FROM order_reservations r WHERE r.order_id=o.id),
				       (SELECT count(*) FROM order_reservation_events e WHERE e.order_id=o.id),
				       (SELECT count(*) FROM order_items i WHERE i.order_id=o.id),
				       (SELECT count(*) FROM order_stock_reservations s WHERE s.order_id=o.id),
				       (SELECT count(*) FROM order_purchase_limit_reservations p WHERE p.order_id=o.id),
				       (SELECT count(*) FROM coupon_redemptions c WHERE c.order_id=o.id),
				       (SELECT count(*) FROM balance_holds h WHERE h.order_id=o.id)
				  FROM orders o JOIN idempotency_keys k ON k.id=o.idempotency_key_id
				 WHERE o.id=$1::uuid AND k.id=$2::uuid`, out.OrderID, claim.ID).
				Scan(&status, &keyStatus, &resourceType, &resourceID,
					&code, &payload, &contentType, &parent, &events, &items,
					&stock, &purchase, &coupon, &holds)
			if err != nil {
				t.Fatalf("query A graph: %v", err)
			}
			if status != "pending_payment" || keyStatus != "succeeded" ||
				resourceType != "order" || resourceID != out.OrderID ||
				code != http.StatusCreated || contentType != "application/json; charset=utf-8" ||
				!bytes.Equal(payload, out.PreparedResponse().BodyBytes()) ||
				parent != 1 || events != 1 || items != 1 || stock != 1 ||
				purchase != 0 || coupon != 0 || holds != 0 {
				t.Fatalf("A invariant mismatch status=%s key=%s resource=%s/%s code=%d type=%s graph=%d/%d/%d/%d/%d/%d/%d payload=%q",
					status, keyStatus, resourceType, resourceID, code, contentType,
					parent, events, items, stock, purchase, coupon, holds, payload)
			}
		})
		t.Log("marker=checkout_pg18_case_a_pending_exact_bytes_ok")

		// Middleware owns replay. Do not call CreateOrder again with a terminal
		// claim; prove instead that the completed key and graph remain singular.
		checkoutPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
			var orders, parents, stock int
			if err := tx.QueryRow(ctx, `
				SELECT (SELECT count(*) FROM orders o WHERE o.idempotency_key_id=$1::uuid),
				       (SELECT count(*) FROM order_reservations r JOIN orders o ON o.id=r.order_id
				         WHERE o.idempotency_key_id=$1::uuid),
				       (SELECT count(*) FROM order_stock_reservations s JOIN orders o ON o.id=s.order_id
				         WHERE o.idempotency_key_id=$1::uuid)`, claim.ID).
				Scan(&orders, &parents, &stock); err != nil {
				t.Fatalf("query terminal uniqueness: %v", err)
			}
			if orders != 1 || parents != 1 || stock != 1 {
				t.Fatalf("terminal graph duplicated: orders=%d parents=%d stock=%d", orders, parents, stock)
			}
		})
		t.Log("marker=checkout_pg18_case_d_terminal_key_graph_singular_ok")
	})

	t.Run("B limited coupon full balance captures and fulfils", func(t *testing.T) {
		claim := checkoutPG18Claim(checkoutPG18ClaimB, "checkout-b", "checkout-b-request", lease)
		out, err := service.CreateOrder(ctx, checkoutPG18Tenant, CreateOrderInput{
			UserID: checkoutPG18User, PlanID: checkoutPG18PlanB,
			PriceID: checkoutPG18PriceB, CouponCode: "FULL100",
			UseBalance: 900, Claim: claim,
		})
		if err != nil {
			t.Fatalf("CreateOrder B: %v", err)
		}
		if out.Status != "fulfilled" || out.TotalAmount != 900 ||
			out.BalanceApplied != 900 || out.PayableAmount != 0 {
			t.Fatalf("B output mismatch: %#v", out)
		}
		checkoutPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
			var orderStatus, reservationState, couponStatus, holdStatus, keyStatus string
			var stockReserved, stockSold, purchased, reserved, redeemedCoupons, reservedCoupons int
			var subscriptions, captureEvents, holdEntries, paidEntries int
			var code int
			var payload []byte
			err := tx.QueryRow(ctx, `
				SELECT o.status,r.state,cr.status,bh.status,k.status,k.response_code,k.response_payload,
				       p.stock_reserved,p.stock_sold,pc.purchased,pc.reserved,
				       c.redeemed_count,c.reserved_count,
				       (SELECT count(*) FROM subscriptions s WHERE s.id=o.subscription_id AND s.status='active'),
				       (SELECT count(*) FROM order_reservation_events e WHERE e.order_id=o.id AND e.event_kind='capture'),
				       (SELECT count(*) FROM ledger_entries e WHERE e.transaction_id=bh.hold_txn_id),
				       (SELECT count(*) FROM ledger_entries e WHERE e.transaction_id=bh.capture_txn_id)
				  FROM orders o
				  JOIN idempotency_keys k ON k.id=o.idempotency_key_id
				  JOIN order_reservations r ON r.order_id=o.id
				  JOIN order_stock_reservations sr ON sr.order_id=o.id
				  JOIN plans p ON p.id=sr.plan_id
				  JOIN order_purchase_limit_reservations pr ON pr.order_id=o.id
				  JOIN plan_purchase_counters pc ON pc.plan_id=pr.plan_id AND pc.user_id=pr.user_id
				  JOIN coupon_redemptions cr ON cr.order_id=o.id
				  JOIN coupons c ON c.id=cr.coupon_id
				  JOIN balance_holds bh ON bh.order_id=o.id
				 WHERE o.id=$1::uuid`, out.OrderID).
				Scan(&orderStatus, &reservationState, &couponStatus, &holdStatus,
					&keyStatus, &code, &payload, &stockReserved, &stockSold,
					&purchased, &reserved, &redeemedCoupons, &reservedCoupons,
					&subscriptions, &captureEvents, &holdEntries, &paidEntries)
			if err != nil {
				t.Fatalf("query B graph: %v", err)
			}
			if orderStatus != "fulfilled" || reservationState != "captured" ||
				couponStatus != "captured" || holdStatus != "captured" ||
				keyStatus != "succeeded" || code != http.StatusCreated ||
				!bytes.Equal(payload, out.PreparedResponse().BodyBytes()) ||
				stockReserved != 0 || stockSold != 1 || purchased != 1 || reserved != 0 ||
				redeemedCoupons != 1 || reservedCoupons != 0 || subscriptions != 1 ||
				captureEvents != 1 || holdEntries != 2 || paidEntries != 2 {
				t.Fatalf("B invariant mismatch order=%s reservation=%s coupon=%s hold=%s key=%s code=%d aggregates=%d/%d/%d/%d/%d/%d sub=%d events=%d entries=%d/%d",
					orderStatus, reservationState, couponStatus, holdStatus, keyStatus, code,
					stockReserved, stockSold, purchased, reserved, redeemedCoupons,
					reservedCoupons, subscriptions, captureEvents, holdEntries, paidEntries)
			}

			var holdBalanced, paidBalanced bool
			if err := tx.QueryRow(ctx, `
				SELECT
				 EXISTS(SELECT 1 FROM balance_holds h
				  JOIN ledger_transactions t ON t.id=h.hold_txn_id
				  JOIN ledger_entries d ON d.transaction_id=t.id AND d.account_id=h.available_account_id
				  JOIN ledger_entries c ON c.transaction_id=t.id AND c.account_id=h.hold_account_id
				 WHERE h.order_id=$1::uuid AND t.kind='balance_hold'
				   AND d.direction='debit' AND c.direction='credit'
				   AND d.amount=h.amount AND c.amount=h.amount),
				 EXISTS(SELECT 1 FROM balance_holds h
				  JOIN ledger_transactions t ON t.id=h.capture_txn_id
				  JOIN ledger_entries d ON d.transaction_id=t.id AND d.account_id=h.hold_account_id
				  JOIN ledger_entries c ON c.transaction_id=t.id
				  JOIN ledger_accounts a ON a.id=c.account_id AND a.account_type='platform_revenue'
				  JOIN orders o ON o.id=h.order_id
				 WHERE h.order_id=$1::uuid AND t.kind='order_paid'
				   AND d.direction='debit' AND d.amount=h.amount
				   AND c.direction='credit' AND c.amount=o.total_amount)`, out.OrderID).
				Scan(&holdBalanced, &paidBalanced); err != nil {
				t.Fatalf("query B ledger conservation: %v", err)
			}
			if !holdBalanced || !paidBalanced {
				t.Fatalf("B ledger conservation hold=%v paid=%v", holdBalanced, paidBalanced)
			}
		})
		t.Log("marker=checkout_pg18_case_b_captured_fulfilled_ledger_ok")
	})

	t.Run("C lost claim rolls back the complete graph", func(t *testing.T) {
		before := checkoutPG18Snapshot(t, ctx, pool)
		claim := checkoutPG18Claim(checkoutPG18ClaimC, "checkout-c", "checkout-c-request", lease.Add(time.Second))
		if _, err := service.CreateOrder(ctx, checkoutPG18Tenant, CreateOrderInput{
			UserID: checkoutPG18User, PlanID: checkoutPG18PlanA,
			PriceID: checkoutPG18PriceA, Claim: claim,
		}); err == nil {
			t.Fatal("CreateOrder C unexpectedly accepted a lost lease claim")
		}
		after := checkoutPG18Snapshot(t, ctx, pool)
		if before != after {
			t.Fatalf("C rollback mismatch before=%+v after=%+v", before, after)
		}
		checkoutPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
			var orders int
			var status string
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM orders WHERE idempotency_key_id=$1::uuid`, claim.ID).Scan(&orders); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRow(ctx, `SELECT status FROM idempotency_keys WHERE id=$1::uuid`, claim.ID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if orders != 0 || status != "in_flight" {
				t.Fatalf("C rollback residue orders=%d key_status=%s", orders, status)
			}
		})
		t.Log("marker=checkout_pg18_case_c_full_rollback_ok")
	})
}

func checkoutPG18Claim(id, key, hashSeed string, lease time.Time) middleware.IdempotencyClaim {
	actorHash := sha256.Sum256([]byte(checkoutPG18User))
	return middleware.IdempotencyClaim{
		ID: id, TenantID: checkoutPG18Tenant, ActorID: checkoutPG18User,
		Scope:        CheckoutIdempotencyScope,
		StorageScope: fmt.Sprintf("%s:actor:%x", CheckoutIdempotencyScope, actorHash[:12]),
		Key:          key, RequestHash: sha256.Sum256([]byte(hashSeed)),
		Generation: 1, LockedUntil: lease,
	}
}

func checkoutPG18Time(t *testing.T, value string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func checkoutPG18AssertRuntimeRole(t *testing.T, ctx context.Context, pool *platformdb.Pool) {
	t.Helper()
	var user string
	var superuser, bypass bool
	if err := pool.QueryRow(ctx, `SELECT rolname,rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).
		Scan(&user, &superuser, &bypass); err != nil {
		t.Fatal(err)
	}
	if user != "aegis_app" || superuser || bypass {
		t.Fatalf("unsafe checkout runtime role user=%s super=%v bypass=%v", user, superuser, bypass)
	}
}

func checkoutPG18InTx(t *testing.T, ctx context.Context, pool *platformdb.Pool, fn func(pgx.Tx)) {
	t.Helper()
	if err := pool.InTx(ctx, platformdb.Scope{TenantID: checkoutPG18Tenant, ActorID: checkoutPG18User}, func(tx pgx.Tx) error {
		fn(tx)
		return nil
	}); err != nil {
		t.Fatalf("checkout PG18 query transaction: %v", err)
	}
}

type checkoutPG18Counts struct {
	Orders, Reservations, Events, Items, Stock, Purchase, Coupons, Holds int
	LedgerTransactions, LedgerEntries, Audits, Subscriptions             int
	PlanAReserved, PlanASold                                             int
}

func checkoutPG18Snapshot(t *testing.T, ctx context.Context, pool *platformdb.Pool) checkoutPG18Counts {
	t.Helper()
	var out checkoutPG18Counts
	checkoutPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM orders),
			       (SELECT count(*) FROM order_reservations),
			       (SELECT count(*) FROM order_reservation_events),
			       (SELECT count(*) FROM order_items),
			       (SELECT count(*) FROM order_stock_reservations),
			       (SELECT count(*) FROM order_purchase_limit_reservations),
			       (SELECT count(*) FROM coupon_redemptions),
			       (SELECT count(*) FROM balance_holds),
			       (SELECT count(*) FROM ledger_transactions),
			       (SELECT count(*) FROM ledger_entries),
			       (SELECT count(*) FROM audit_events),
			       (SELECT count(*) FROM subscriptions),
			       (SELECT stock_reserved FROM plans WHERE id=$1::uuid),
			       (SELECT stock_sold FROM plans WHERE id=$1::uuid)`, checkoutPG18PlanA).
			Scan(&out.Orders, &out.Reservations, &out.Events, &out.Items,
				&out.Stock, &out.Purchase, &out.Coupons, &out.Holds,
				&out.LedgerTransactions, &out.LedgerEntries, &out.Audits,
				&out.Subscriptions, &out.PlanAReserved, &out.PlanASold)
		if err != nil {
			t.Fatalf("checkout PG18 snapshot: %v", err)
		}
	})
	return out
}
