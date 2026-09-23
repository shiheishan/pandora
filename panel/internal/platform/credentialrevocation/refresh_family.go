// Package credentialrevocation centralizes fail-closed revocation of login
// credentials that are owned by security-definer database boundaries.
package credentialrevocation

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrRefreshFamilyRevocationUnavailable = errors.New(
	"client refresh-family credentials exist but their secure revocation boundary is unavailable",
)

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const refreshFamilyCapabilitySQL = `SELECT
  to_regclass('public.refresh_families') IS NOT NULL,
  to_regprocedure('app.revoke_user_refresh_families(uuid,uuid)') IS NOT NULL`

const revokeRefreshFamiliesSQL = `SELECT app.revoke_user_refresh_families($1::uuid, $2::uuid)`

// RevokeRefreshFamilies preserves compatibility with schema 40, where the
// client-auth relation does not exist. Once the relation exists, password
// rotation is allowed only through the fixed SECURITY DEFINER function. The
// runtime role must never receive UPDATE privileges on the protected table.
// A partially installed capability fails closed so the surrounding password
// transaction rolls back instead of leaving a client credential usable.
func RevokeRefreshFamilies(
	ctx context.Context, q queryRower, tenantID, userID string,
) (int64, error) {
	var relationExists, functionExists bool
	if err := q.QueryRow(ctx, refreshFamilyCapabilitySQL).Scan(
		&relationExists, &functionExists,
	); err != nil {
		return 0, fmt.Errorf("inspect refresh-family revocation capability: %w", err)
	}

	if !relationExists && !functionExists {
		return 0, nil
	}
	if !relationExists || !functionExists {
		return 0, ErrRefreshFamilyRevocationUnavailable
	}

	var revoked int64
	if err := q.QueryRow(ctx, revokeRefreshFamiliesSQL, tenantID, userID).Scan(&revoked); err != nil {
		return 0, fmt.Errorf("revoke client refresh-family credentials: %w", err)
	}
	if revoked < 0 {
		return 0, errors.New("client refresh-family revocation returned a negative count")
	}
	return revoked, nil
}
