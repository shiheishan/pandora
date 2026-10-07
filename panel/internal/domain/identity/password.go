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
	"github.com/aegispanel/aegis/internal/platform/iamguard"
)

type ChangePasswordInput struct {
	UserID      string
	OldPassword string
	NewPassword string
	APIDomain   string
	IPHash      []byte
	IP          string
	UserAgent   string
	// KeepSessionID 是改密时保留的当前会话（门户按设计保留）；admin 域一律忽略，
	// 全部会话都吊销（保留规则 4）
	KeepSessionID string
}

// 改密的旧口令试错上限：同一账号 15 分钟内旧口令错满 5 次，之后（含旧口令正确）
// 一律 429，直到窗口滑过。拿到会话的攻击者不能借改密接口在线猜旧口令，再顺手
// 改掉密码把真主人锁在外面。
//
// 计数复用审计：每次旧口令错误本来就提交一条 user.password_changed / failure /
// invalid_password（见下），按操作者与时间数它，走 idx_audit_events_actor
// (tenant_id, actor_id, occurred_at)，不另建计数表、也不依赖 Valkey。
const (
	passwordChangeFailureLimit  = 5
	passwordChangeFailureWindow = "15 minutes"
)

const passwordChangeFailuresSQL = `
	SELECT count(*)
	  FROM audit_events
	 WHERE tenant_id = $1 AND actor_id = $2::uuid
	   AND action = 'user.password_changed'
	   AND outcome = 'failure' AND error_code = 'invalid_password'
	   AND occurred_at > now() - interval '` + passwordChangeFailureWindow + `'`

// ChangePassword 更新当前用户的口令，并让既有登录凭据立即失效。
//
// 旧密码错误的审计不能随事务错误一起回滚，因此失败分支先提交审计，
// 再在事务外返回面向用户的认证错误。
//
// 口令策略按账号算：后台人员（有角色绑定）不论从门户还是后台改，都至少 12 位。
func (s *Service) ChangePassword(ctx context.Context, tenantID string, in ChangePasswordInput) error {
	var resultErr error
	userID := in.UserID
	apiDomain := in.APIDomain
	if apiDomain != "admin" {
		apiDomain = "public"
	}
	keepSession := in.KeepSessionID
	if apiDomain == "admin" {
		keepSession = ""
	}

	// 旧口令校验与新口令哈希都在事务里做；名额在开事务之前拿，不拿着连接排队
	slot, err := acquirePasswordSlot(ctx)
	if err != nil {
		return err
	}
	defer slot.Release()

	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
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

		// 上面的 FOR UPDATE 让同一账号的改密排成一队，这里数到的失败次数不会被并发绕过
		var failures int
		if err := tx.QueryRow(ctx, passwordChangeFailuresSQL, tenantID, userID).Scan(&failures); err != nil {
			return err
		}
		if failures >= passwordChangeFailureLimit {
			resultErr = httpx.New(httpx.CodeRateLimited, "当前密码错误次数过多，请 15 分钟后再试")
			return nil
		}

		ok, _, err := slot.Verify(in.OldPassword, phc)
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
		staff, err := iamguard.IsStaff(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if err := validatePasswordFor(staff, in.NewPassword); err != nil {
			return err
		}

		newPHC, err := slot.Hash(in.NewPassword, crypto.DefaultArgon2Params())
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
		// 唯一的例外是门户的当前会话：改密的人就是它，踢掉只会逼他立刻再登一次。
		if _, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET revoked_at = now(), revoked_reason = 'password_changed'
			 WHERE tenant_id = $1 AND user_id = $2::uuid AND revoked_at IS NULL
			   AND ($3 = '' OR id <> nullif($3, '')::uuid)`,
			tenantID, userID, keepSession,
		); err != nil {
			return err
		}

		// 旧刷新令牌必须同步失效，否则仍能绕过会话吊销换取新的访问令牌。
		if _, err := tx.Exec(ctx, `
			UPDATE refresh_tokens
			   SET status = 'revoked'
			 WHERE tenant_id = $1 AND user_id = $2::uuid AND status = 'active'
			   AND ($3 = '' OR session_id IS NULL OR session_id <> nullif($3, '')::uuid)`,
			tenantID, userID, keepSession,
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
