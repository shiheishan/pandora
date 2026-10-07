package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/iamguard"
)

// 自助找回密码（用户 2026-10-07 定：邮件验证码；重置后吊销该账号的全部登录；
// 没配邮件服务时隐藏入口）。
//
// 两步：
//
//  1. StartPasswordReset：填邮箱。账号存在、可用且不是后台人员时，往
//     verification_codes 写一枚 6 位验证码的哈希，并在同一事务里按地址入队一封信；
//     否则什么都不写。两种情况对外的响应完全一致（IAM-006：不能借它探测邮箱）。
//  2. CompletePasswordReset：邮箱 + 验证码 + 新密码。验证码一次性、10 分钟有效、
//     同一枚最多错 5 次；对上后改密、吊销全部登录凭据、写审计，全在一个事务里。
//
// 后台人员（有任何生效的角色绑定）不走这条路：门户不让他们登录，后台口令也不该
// 被「能收到这个邮箱的信」的人从公开接口改掉，忘了密码由别的管理员重置或用 adminctl。

const (
	passwordResetTemplateCode = "auth.password_reset"
	// passwordResetTTL 同时决定验证码的 expires_at 与邮件里写的分钟数（用户定：15 分钟以内）
	passwordResetTTL = 10 * time.Minute
	// passwordResetMaxAttempts 是同一枚验证码允许输错的次数，写进 verification_codes.max_attempts
	passwordResetMaxAttempts = 5
	passwordResetPurpose     = "password_reset"
)

// ErrPasswordResetUnavailable 是没配邮件服务时的拒绝：入口本来就该隐藏，走到这里的是直接调接口的。
var ErrPasswordResetUnavailable = httpx.New(httpx.CodeForbidden, "找回密码当前不可用，请联系客服")

// passwordResetCodeInvalid 是第 2 步一切「对不上」的统一回应：没有验证码、过期、错次数用完、
// 账号已不可用，都回同一句，不让人借错误的不同区分出账号是否存在。
func passwordResetCodeInvalid() error {
	return httpx.Invalid(map[string]string{"code": "验证码错误或已失效，请重新获取"})
}

// PasswordResetAvailable 报告找回密码现在开没开：门户 site-config 用它决定显不显示入口。
func (s *Service) PasswordResetAvailable(ctx context.Context, tenantID string) bool {
	return s.mailer != nil && s.mailer.EmailConfigured(ctx, tenantID)
}

type StartPasswordResetInput struct {
	Email     string
	IP        string
	UserAgent string
}

type StartPasswordResetOutput struct {
	ExpiresAt time.Time
	// DevCode 仅在开发模式且确实发了验证码时非空（与注册一致，生产恒为空）
	DevCode string
}

// passwordResetAccountSQL 按邮箱找要重置的账号，写法同 email_lookup.go（走 email_lower 索引）。
// 后台人员的判定与门户禁登同一口径（portalStaffSQL：有生效的角色绑定）。
const passwordResetAccountSQL = `
	SELECT u.id::text, u.email::text, u.status, ` + portalStaffSQL + `
	  FROM users u
	 WHERE u.tenant_id = $1
	   AND u.email_lower = lower($2::text)
	   AND u.email = $2::citext`

// StartPasswordReset 是找回密码第 1 步。账号存不存在，调用方拿到的都是同一个结果。
func (s *Service) StartPasswordReset(ctx context.Context, tenantID string,
	in StartPasswordResetInput) (*StartPasswordResetOutput, error) {

	email := normalizeEmail(in.Email)
	if !looksLikeEmail(email) {
		return nil, httpx.Invalid(map[string]string{"email": "邮箱格式不正确"})
	}
	if !s.PasswordResetAvailable(ctx, tenantID) {
		return nil, ErrPasswordResetUnavailable
	}
	code, err := crypto.NewNumericCode(6)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	expiresAt := time.Now().Add(passwordResetTTL)
	targetHash := crypto.HashIdentifier(s.hashSalt, email)

	var sent bool
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var userID, storedEmail, status string
		var staff bool
		err := tx.QueryRow(ctx, passwordResetAccountSQL, tenantID, email).
			Scan(&userID, &storedEmail, &status, &staff)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if status != "active" || staff {
			return nil
		}

		// 只认最新一枚：再点一次「发送验证码」，之前没用掉的立刻作废，
		// 散在邮箱里的有效验证码不会越积越多
		if _, err := tx.Exec(ctx, `
			UPDATE verification_codes SET expires_at = now()
			 WHERE tenant_id = $1 AND purpose = '`+passwordResetPurpose+`'
			   AND target_hash = $2 AND consumed_at IS NULL AND expires_at > now()`,
			tenantID, targetHash); err != nil {
			return err
		}
		var codeID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO verification_codes
				(tenant_id, purpose, target_hash, code_hash, user_id, max_attempts, expires_at)
			VALUES ($1, '`+passwordResetPurpose+`', $2, $3, $4::uuid, $5, $6)
			RETURNING id::text`,
			tenantID, targetHash, crypto.HashToken(code), userID,
			passwordResetMaxAttempts, expiresAt).Scan(&codeID); err != nil {
			return err
		}
		// 与验证码同一事务入队：要么两者都在，要么都不在。收件地址用库里的邮箱
		if err := s.mailer.EnqueueToAddress(ctx, tx, tenantID, passwordResetTemplateCode, storedEmail,
			map[string]string{
				"code":    code,
				"minutes": strconv.Itoa(int(passwordResetTTL / time.Minute)),
			}, passwordResetTemplateCode+":"+codeID); err != nil {
			return err
		}
		sent = true
		// 谁在什么时候、从哪里替这个账号要过验证码：用户事后说「我没申请过」时的线索
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "anonymous", Action: "user.password_reset_requested",
			ResourceType: "user", ResourceID: &userID,
			APIDomain: "public", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx), SourceIP: in.IP, UserAgent: in.UserAgent,
		})
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	if sent {
		// 事务已提交，催派发循环立刻发，不等它的周期
		s.mailer.Kick()
	}
	out := &StartPasswordResetOutput{ExpiresAt: expiresAt}
	if sent && s.devMode {
		out.DevCode = code
	}
	return out, nil
}

type CompletePasswordResetInput struct {
	Email       string
	Code        string
	NewPassword string
	IP          string
	UserAgent   string
}

// CompletePasswordReset 是找回密码第 2 步：核对验证码，改密，吊销该账号的全部登录。
//
// 验证码错误时先把错误次数提交，再在事务外返回错误（同注册第 2 步）：
// 否则计数随事务回滚，「最多错 5 次」形同虚设。
func (s *Service) CompletePasswordReset(ctx context.Context, tenantID string,
	in CompletePasswordResetInput) error {

	email := normalizeEmail(in.Email)
	if !looksLikeEmail(email) {
		return httpx.Invalid(map[string]string{"email": "邮箱格式不正确"})
	}
	if !validNumericCode(in.Code, 6) {
		return passwordResetCodeInvalid()
	}
	if err := validatePassword(in.NewPassword); err != nil {
		return err
	}
	if !s.PasswordResetAvailable(ctx, tenantID) {
		return ErrPasswordResetUnavailable
	}

	slot, err := acquirePasswordSlot(ctx)
	if err != nil {
		return err
	}
	defer slot.Release()

	targetHash := crypto.HashIdentifier(s.hashSalt, email)
	var rejected bool
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var codeID, userID string
		var codeHash []byte
		var attempts, maxAttempts int16
		err := tx.QueryRow(ctx, `
			SELECT id::text, code_hash, attempts, max_attempts, coalesce(user_id::text, '')
			  FROM verification_codes
			 WHERE tenant_id = $1 AND purpose = '`+passwordResetPurpose+`'
			   AND target_hash = $2 AND consumed_at IS NULL AND expires_at > now()
			 ORDER BY created_at DESC LIMIT 1
			 FOR UPDATE`,
			tenantID, targetHash).Scan(&codeID, &codeHash, &attempts, &maxAttempts, &userID)
		if errors.Is(err, pgx.ErrNoRows) {
			rejected = true
			return nil
		}
		if err != nil {
			return err
		}
		if attempts >= maxAttempts || userID == "" {
			rejected = true
			return nil
		}
		if subtle.ConstantTimeCompare(codeHash, crypto.HashToken(in.Code)) != 1 {
			if _, err := tx.Exec(ctx,
				`UPDATE verification_codes SET attempts = attempts + 1 WHERE id = $1::uuid`,
				codeID); err != nil {
				return err
			}
			rejected = true
			return nil
		}

		// 验证码对上了。账号此刻仍须可用、不是后台人员（发码之后可能被停用或授了角色）；
		// 锁住账号行，与并发的改状态、后台重置互斥
		var status string
		var staff bool
		err = tx.QueryRow(ctx, `
			SELECT u.status, `+portalStaffSQL+`
			  FROM users u
			 WHERE u.tenant_id = $1 AND u.id = $2::uuid
			 FOR NO KEY UPDATE OF u`, tenantID, userID).Scan(&status, &staff)
		if errors.Is(err, pgx.ErrNoRows) {
			rejected = true
			return nil
		}
		if err != nil {
			return err
		}
		if status != "active" || staff {
			rejected = true
			return nil
		}
		// 口令策略照常按账号算（后台人员到不了这里，这一步只为与其它改密入口同口径）
		isStaff, err := iamguard.IsStaff(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if err := validatePasswordFor(isStaff, in.NewPassword); err != nil {
			return err
		}
		newPHC, err := slot.Hash(in.NewPassword, crypto.DefaultArgon2Params())
		if err != nil {
			return err
		}
		// 账号可能从没设过密码（只用过快捷登录、批量生成后没改过），所以是 upsert
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_passwords (tenant_id, user_id, phc, rotated_at, must_rotate)
			VALUES ($1, $2::uuid, $3, now(), false)
			ON CONFLICT (user_id) DO UPDATE
			   SET phc = EXCLUDED.phc, rotated_at = now(), must_rotate = false`,
			tenantID, userID, newPHC); err != nil {
			return err
		}
		// 这一枚用掉；同一邮箱别的未用验证码一并作废
		if _, err := tx.Exec(ctx, `
			UPDATE verification_codes
			   SET consumed_at = CASE WHEN id = $3::uuid THEN now() ELSE consumed_at END,
			       expires_at = least(expires_at, now())
			 WHERE tenant_id = $1 AND purpose = '`+passwordResetPurpose+`'
			   AND target_hash = $2 AND consumed_at IS NULL`,
			tenantID, targetHash, codeID); err != nil {
			return err
		}
		// 用户定：重置后吊销该账号的全部登录（与后台替人重置同一份实现）
		if err := revokeAllLogins(ctx, tx, tenantID, userID, "password_reset"); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "user.password_reset", ResourceType: "user", ResourceID: &userID,
			AfterDigest: map[string]any{"method": "email_code", "sessions_revoked": true},
			APIDomain:   "public", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx), SourceIP: in.IP, UserAgent: in.UserAgent,
		})
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return he
		}
		return httpx.Internal(err)
	}
	if rejected {
		return passwordResetCodeInvalid()
	}
	return nil
}

// validNumericCode 只收恰好 n 位 ASCII 数字。
func validNumericCode(code string, n int) bool {
	if len(code) != n {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}
