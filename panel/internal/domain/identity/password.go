// [INPUT]: 依赖 platform 的 crypto/db/httpx/audit，依赖 service.go 的 validatePasswordFor
// [OUTPUT]: 对外提供 ChangePasswordInput、Service.ChangePassword
// [POS]: domain/identity 的改自己密码：校验旧密码、按网关域套长度规则（admin 至少 12 位）、同事务吊销全部会话与 refresh 令牌并写审计
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/credentialrevocation"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type ChangePasswordInput struct {
	UserID      string
	OldPassword string
	NewPassword string
	APIDomain   string
	IPHash      []byte
	IP          string
	UserAgent   string
}

// ChangePassword 更新当前用户的口令，并让既有登录凭据立即失效。
//
// 旧密码错误的审计不能随事务错误一起回滚，因此失败分支先提交审计，
// 再在事务外返回面向用户的认证错误。
func (s *Service) ChangePassword(ctx context.Context, tenantID string, in ChangePasswordInput) error {
	var resultErr error
	userID := in.UserID
	apiDomain := in.APIDomain
	if apiDomain != "admin" {
		apiDomain = "public"
	}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		writeAudit := func(outcome, errorCode string) error {
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind:    "user",
				ActorID:      &userID,
				Action:       "user.password_changed",
				ResourceType: "user",
				ResourceID:   &userID,
				APIDomain:    apiDomain,
				Outcome:      outcome,
				ErrorCode:    errorCode,
				RequestID:    httpx.RequestIDFrom(ctx),
				SourceIP:     in.IP,
				UserAgent:    in.UserAgent,
			})
		}

		var phc string
		err := tx.QueryRow(ctx, `
			SELECT phc
			  FROM user_passwords
			 WHERE tenant_id = $1 AND user_id = $2::uuid
			 FOR UPDATE`,
			tenantID, userID,
		).Scan(&phc)
		if errors.Is(err, pgx.ErrNoRows) {
			resultErr = httpx.New(httpx.CodeUnauthorized, "当前密码不正确")
			return writeAudit("failure", "invalid_password")
		}
		if err != nil {
			return err
		}

		ok, _, err := crypto.VerifyPassword(in.OldPassword, phc)
		if err != nil {
			return err
		}
		if !ok {
			resultErr = httpx.New(httpx.CodeUnauthorized, "当前密码不正确")
			return writeAudit("failure", "invalid_password")
		}

		if in.NewPassword == in.OldPassword {
			return httpx.New(httpx.CodeBadRequest, "新密码不能与当前密码相同")
		}
		if err := validatePasswordFor(apiDomain, in.NewPassword); err != nil {
			return err
		}

		newPHC, err := crypto.HashPassword(in.NewPassword, crypto.DefaultArgon2Params())
		if err != nil {
			return err
		}

		passwordTag, err := tx.Exec(ctx, `
			UPDATE user_passwords
			   SET phc = $2, rotated_at = now(), must_rotate = false
			 WHERE user_id = $1::uuid AND tenant_id = $3`,
			userID, newPHC, tenantID)
		if err != nil {
			return err
		}
		if passwordTag.RowsAffected() != 1 {
			return errors.New("password rotation did not update exactly one credential")
		}

		// 改密通常意味着用户怀疑凭据已经泄露，保留旧会话会让攻击者继续驻留。
		if _, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET revoked_at = now(), revoked_reason = 'password_changed'
			 WHERE tenant_id = $1 AND user_id = $2::uuid AND revoked_at IS NULL`,
			tenantID, userID,
		); err != nil {
			return err
		}

		// 旧刷新令牌必须同步失效，否则仍能绕过会话吊销换取新的访问令牌。
		if _, err := tx.Exec(ctx, `
			UPDATE refresh_tokens
			   SET status = 'revoked'
			 WHERE tenant_id = $1 AND user_id = $2::uuid AND status = 'active'`,
			tenantID, userID,
		); err != nil {
			return err
		}
		if _, err := credentialrevocation.RevokeRefreshFamilies(
			ctx, tx, tenantID, userID,
		); err != nil {
			return err
		}

		return writeAudit("success", "")
	})
	if err != nil {
		var httpErr *httpx.Error
		if errors.As(err, &httpErr) {
			return httpErr
		}
		return httpx.Internal(err)
	}

	return resultErr
}
