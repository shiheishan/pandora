package service

import (
	"context"
	"crypto/subtle"
	"strconv"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/model"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// PasswordResetAvailable 报告找回密码现在开没开（没配好 SMTP 时关闭）。
func (s *Service) PasswordResetAvailable(ctx context.Context) (bool, error) {
	return s.settings.MailConfigured(ctx)
}

// StartPasswordReset 是找回密码第 1 步。账号存不存在，调用方拿到的结果一样（DevCode 只在非生产环境出现）。
func (s *Service) StartPasswordReset(ctx context.Context, email string) (PasswordResetStart, error) {
	start := time.Now()
	email = normalizeEmail(email)
	if !looksLikeEmail(email) {
		return PasswordResetStart{}, errEmailInvalid()
	}
	if err := s.limit(ctx, ruleResetStartEmail, email); err != nil {
		return PasswordResetStart{}, err
	}
	ok, err := s.PasswordResetAvailable(ctx)
	if err != nil {
		return PasswordResetStart{}, err
	}
	if !ok {
		return PasswordResetStart{}, ErrPasswordResetUnavailable
	}
	code, err := newNumericCode(verificationCodeDigits)
	if err != nil {
		return PasswordResetStart{}, errs.Wrap(errs.Internal, err)
	}
	expiresAt := s.now().Add(passwordResetTTL)
	target := s.targetHash(email)

	var sent bool
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		u, found, err := s.repo.UserByEmail(ctx, email)
		if err != nil {
			return err
		}
		if !found || u.Status != StatusActive {
			return nil
		}
		staff, err := s.isStaff(ctx, u.ID)
		if err != nil {
			return err
		}
		if staff {
			return nil
		}

		if err := s.repo.ExpireCodes(ctx, PurposePasswordReset, target); err != nil {
			return err
		}
		vc := model.VerificationCode{
			Purpose:     PurposePasswordReset,
			TargetHash:  target,
			CodeHash:    s.codeHash(code),
			UserID:      &u.ID,
			MaxAttempts: passwordResetMaxAttempts,
			ExpiresAt:   expiresAt,
		}
		if err := s.repo.InsertCode(ctx, &vc); err != nil {
			return err
		}
		mailer, err := s.needMailer()
		if err != nil {
			return err
		}
		if err := mailer.SendToAddress(ctx, TemplatePasswordReset, u.Email, map[string]string{
			"code":    code,
			"minutes": strconv.Itoa(int(passwordResetTTL / time.Minute)),
		}, nil); err != nil {
			return err
		}
		if err := s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionPasswordResetRequest,
			ResourceType: ResourceUser,
			ResourceID:   &u.ID,
		}); err != nil {
			return err
		}
		sent = true
		return nil
	})
	if err != nil {
		return PasswordResetStart{}, err
	}
	padSince(ctx, start, startFloor)
	out := PasswordResetStart{ExpiresAt: expiresAt}
	if sent && s.devMode {
		out.DevCode = code
	}
	return out, nil
}

// CompletePasswordReset 是找回密码第 2 步：核对验证码、改密、吊销该账号的全部会话。
func (s *Service) CompletePasswordReset(ctx context.Context, email, code, newPassword string) error {
	email = normalizeEmail(email)
	if !looksLikeEmail(email) {
		return errEmailInvalid()
	}
	// 弱口令先于验证码判断，口令不合格时不消耗验证码的尝试次数。
	if err := validatePassword(newPassword, false); err != nil {
		return err
	}
	if !validNumericCode(code, verificationCodeDigits) {
		return errCodeInvalid()
	}
	if err := s.limit(ctx, ruleResetCompleteEmail, email); err != nil {
		return err
	}
	ok, err := s.PasswordResetAvailable(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPasswordResetUnavailable
	}

	release, err := s.hasher.acquire(ctx, authn.AudiencePublic)
	if err != nil {
		return err
	}
	phc, err := hashPHC(newPassword)
	release()
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}

	target := s.targetHash(email)
	var rejected bool
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		c, found, err := s.repo.LockLatestCode(ctx, PurposePasswordReset, target)
		if err != nil {
			return err
		}
		if !found || c.Attempts >= c.MaxAttempts || c.UserID == nil {
			rejected = true
			return nil
		}
		if subtle.ConstantTimeCompare(c.CodeHash, s.codeHash(code)) != 1 {
			if err := s.repo.IncCodeAttempts(ctx, c.ID); err != nil {
				return err
			}
			rejected = true
			return nil
		}

		u, ok, err := s.repo.LockUser(ctx, *c.UserID)
		if err != nil {
			return err
		}
		if !ok || u.Status != StatusActive {
			rejected = true
			return nil
		}
		staff, err := s.isStaff(ctx, u.ID)
		if err != nil {
			return err
		}
		if staff {
			rejected = true
			return nil
		}

		if err := s.repo.UpsertPassword(ctx, u.ID, phc); err != nil {
			return err
		}
		if err := s.repo.ConsumeCode(ctx, c.ID); err != nil {
			return err
		}
		if err := s.repo.ExpireCodes(ctx, PurposePasswordReset, target); err != nil {
			return err
		}
		revoker, err := s.needRevoker()
		if err != nil {
			return err
		}
		if err := revoker.RevokeUser(ctx, u.ID, ""); err != nil {
			return err
		}
		return s.writeSelfAudit(ctx, u.ID, auditsvc.Event{
			Action: ActionPasswordReset,
			Data: map[string]any{
				"method":           "email_code",
				"sessions_revoked": true,
			},
		})
	})
	if err != nil {
		return err
	}
	if rejected {
		return errCodeInvalid()
	}
	return nil
}
