package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	defaultReservationExpiryBatch = 50
	maxReservationExpiryBatch     = 500
)

type reservationExpiryCandidatesLockedHook func([]string) error

type reservationExpiryCandidatesLockedHookKey struct{}

func reservationExpiryCandidatesHook(ctx context.Context) reservationExpiryCandidatesLockedHook {
	hook, _ := ctx.Value(reservationExpiryCandidatesLockedHookKey{}).(reservationExpiryCandidatesLockedHook)
	return hook
}

// ExpireDueReservations releases up to limit due reservations. Candidate
// discovery locks candidate order rows until its bounded transaction ends;
// every order is then handled in its own transaction. The order is the first
// locked row and SKIP LOCKED makes multiple workers and a concurrent
// payment/cancellation cooperate without waiting on each other.
// A malformed order rolls back independently and does not starve the rest of
// the candidate batch.
func (s *Service) ExpireDueReservations(ctx context.Context, tenantID string,
	limit int) (int, error) {

	if tenantID == "" {
		return 0, errors.New("reservation expiry requires a tenant")
	}
	if limit <= 0 {
		limit = defaultReservationExpiryBatch
	}
	if limit > maxReservationExpiryBatch {
		limit = maxReservationExpiryBatch
	}

	var candidates []string
	err := s.pool.InTx(ctx, dbScope(tenantID, ""), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT o.id::text
			  FROM order_reservations r
			  JOIN orders o ON o.tenant_id=r.tenant_id AND o.id=r.order_id
			 WHERE r.tenant_id=$1 AND r.state='held' AND r.expires_at<=now()
			   AND o.status IN ('draft','pending_payment','processing')
			 ORDER BY r.expires_at,r.id
			 LIMIT $2 FOR UPDATE OF o SKIP LOCKED`, tenantID, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var orderID string
			if err := rows.Scan(&orderID); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, orderID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if hook := reservationExpiryCandidatesHook(ctx); hook != nil {
			return hook(append([]string(nil), candidates...))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	released := 0
	var failures []error
	for _, orderID := range candidates {
		var result releaseOrderResult
		err := s.pool.InTx(ctx, dbScope(tenantID, ""), func(tx pgx.Tx) error {
			var err error
			result, err = releaseOrderReservation(ctx, tx, tenantID, releaseOrderRequest{
				OrderID: orderID, Target: "expired", EventKind: "expire",
				Reason: "reservation_expired", ActorKind: "system",
				RequireDue: true, SkipLocked: true,
			})
			return err
		})
		if err != nil {
			// A payment or user cancellation may have won after candidate
			// discovery. That is a normal no-op, not a poisoned worker item.
			if errors.Is(err, errOrderReleaseConflict) ||
				errors.Is(err, errOrderReleaseNotFound) {
				continue
			}
			failures = append(failures, fmt.Errorf("expire order %s: %w", orderID, err))
			continue
		}
		if result.Skipped || result.Output.AlreadyTerminal {
			continue
		}
		released++
	}
	return released, errors.Join(failures...)
}
