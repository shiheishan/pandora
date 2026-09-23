package identity

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type LogoutInput struct {
	UserID    string
	SessionID string
	Audience  string
}

// LogoutCurrentSession revokes exactly the session represented by the current
// access token. The session and all of its refresh-token chain are revoked in
// one transaction so a successful response never leaves a usable credential.
func (s *Service) LogoutCurrentSession(ctx context.Context, tenantID string, in LogoutInput) error {
	if !validLogoutPrincipal(tenantID, in) {
		return invalidSession()
	}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.UserID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET revoked_at = COALESCE(revoked_at, now()),
			       revoked_reason = COALESCE(revoked_reason, 'user_logout')
			 WHERE tenant_id = $1
			   AND id = $2::uuid
			   AND user_id = $3::uuid
			   AND audience = $4
			   AND revoked_at IS NULL`,
			tenantID, in.SessionID, in.UserID, in.Audience,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exactSessionExists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM sessions
					 WHERE tenant_id = $1
					   AND id = $2::uuid
					   AND user_id = $3::uuid
					   AND audience = $4
				)`, tenantID, in.SessionID, in.UserID, in.Audience).Scan(&exactSessionExists); err != nil {
				return err
			}
			if !exactSessionExists {
				return invalidSession()
			}
			// A concurrent request already completed the same terminal state.
			// Return success without revoking twice or writing a duplicate audit.
			return nil
		}
		if tag.RowsAffected() != 1 {
			return errors.New("unexpected session revocation row count")
		}

		// Rotated tokens stay part of the same credential chain. Revoking both
		// active and rotated entries makes the whole current-session chain inert
		// while preserving compromised entries for forensic state.
		if _, err := tx.Exec(ctx, `
			UPDATE refresh_tokens
			   SET status = 'revoked'
			 WHERE tenant_id = $1
			   AND session_id = $2::uuid
			   AND user_id = $3::uuid
			   AND status IN ('active', 'rotated')`,
			tenantID, in.SessionID, in.UserID,
		); err != nil {
			return err
		}

		actorKind := "user"
		if in.Audience == "admin" {
			actorKind = "admin"
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind:    actorKind,
			ActorID:      &in.UserID,
			Action:       "user.logout",
			ResourceType: "session",
			ResourceID:   &in.SessionID,
			APIDomain:    in.Audience,
			Outcome:      "success",
			RequestID:    httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		var httpErr *httpx.Error
		if errors.As(err, &httpErr) {
			return httpErr
		}
		return httpx.Internal(err)
	}
	return nil
}

func validLogoutPrincipal(tenantID string, in LogoutInput) bool {
	if uuid.Validate(tenantID) != nil || uuid.Validate(in.UserID) != nil || uuid.Validate(in.SessionID) != nil {
		return false
	}
	switch in.Audience {
	case "public", "admin", "client":
		return true
	default:
		return false
	}
}

func invalidSession() *httpx.Error {
	return httpx.New(httpx.CodeUnauthorized, "凭据无效或已过期")
}
