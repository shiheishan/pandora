// Package idempotencybind exposes the single database-owned resource binder.
// It deliberately contains no diagnostic read or fallback UPDATE: a lost
// full-tuple claim is indistinguishable from every other zero-row outcome.
package idempotencybind

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

var ErrIdempotencyClaimLost = errors.New("idempotency resource claim lost")

const bindResourceSQL = `
	SELECT bound_claim_id::text
	  FROM app.bind_idempotency_resource(
	    $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11
	  )`

const completeBoundSuccessSQL = `
	SELECT completed_claim_id::text
	  FROM app.complete_bound_idempotency_success(
	    $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18
	  )`

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// BindResource binds one business UUID to the exact database-derived claim
// tuple. The caller must use the same transaction that creates the reverse
// business link and reservation evidence.
func BindResource(
	ctx context.Context,
	tx pgx.Tx,
	claim middleware.IdempotencyClaim,
	resourceType string,
	resourceID string,
) error {
	return bindResource(ctx, tx, claim, resourceType, resourceID)
}

// bindResource keeps the query seam private so tests can verify the exact SQL
// tuple without weakening the exported transaction-only API.
func bindResource(
	ctx context.Context,
	queryer queryRower,
	claim middleware.IdempotencyClaim,
	resourceType string,
	resourceID string,
) error {
	var boundClaimID string
	err := queryer.QueryRow(ctx, bindResourceSQL,
		claim.ID,
		claim.TenantID,
		claim.ActorID,
		claim.Scope,
		claim.StorageScope,
		claim.Key,
		claim.RequestHash[:],
		claim.Generation,
		claim.LockedUntil,
		resourceType,
		resourceID,
	).Scan(&boundClaimID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIdempotencyClaimLost
	}
	if err != nil {
		return err
	}
	if boundClaimID != claim.ID {
		return errors.New("idempotency binder returned an unexpected claim id")
	}
	return nil
}

// CompleteSuccessJSON atomically completes an already bound claim with the
// exact prepared JSON response. The caller must use the same transaction that
// created and bound the business resource.
func CompleteSuccessJSON(
	ctx context.Context,
	tx pgx.Tx,
	claim middleware.IdempotencyClaim,
	resourceType string,
	resourceID string,
	response httpx.PreparedResponse,
) error {
	return completeSuccessJSON(ctx, tx, claim, resourceType, resourceID, response)
}

func completeSuccessJSON(
	ctx context.Context,
	queryer queryRower,
	claim middleware.IdempotencyClaim,
	resourceType string,
	resourceID string,
	response httpx.PreparedResponse,
) error {
	if response.StatusCode() < 200 || response.StatusCode() >= 300 {
		return errors.New("idempotency success response must have a 2xx status")
	}
	if response.ContentType() != "application/json; charset=utf-8" {
		return errors.New("idempotency success response must be prepared JSON")
	}

	var completedClaimID string
	err := queryer.QueryRow(ctx, completeBoundSuccessSQL,
		claim.ID,
		claim.TenantID,
		claim.ActorID,
		claim.Scope,
		claim.StorageScope,
		claim.Key,
		claim.RequestHash[:],
		claim.Generation,
		claim.LockedUntil,
		resourceType,
		resourceID,
		response.StatusCode(),
		response.BodyBytes(),
		response.ContentType(),
		nil,
		nil,
		nil,
		nil,
	).Scan(&completedClaimID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIdempotencyClaimLost
	}
	if err != nil {
		return err
	}
	if completedClaimID != claim.ID {
		return errors.New("idempotency completer returned an unexpected claim id")
	}
	return nil
}
