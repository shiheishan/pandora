package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/model"
	"github.com/aegispanel/aegis/internal/module/identity/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/google/uuid"
)

// StartRegistration 是注册第 1 步：校验注册策略与邀请码，签发注册令牌；需要验证邮箱时发验证码。
func (s *Service) StartRegistration(ctx context.Context, email, inviteCode string) (RegistrationStart, error) {
	start := time.Now()
	email = normalizeEmail(email)
	if !ValidEmail(email) {
		return RegistrationStart{}, errEmailInvalid()
	}
	inviteCode = strings.ToUpper(strings.TrimSpace(inviteCode))
	if len(inviteCode) > 32 {
		return RegistrationStart{}, ErrRegistrationUnavailable
	}
	if err := s.limit(ctx, ruleRegisterEmail, email); err != nil {
		return RegistrationStart{}, err
	}
	if inviteCode != "" {
		if err := s.limit(ctx, ruleRegisterInvite, inviteCode); err != nil {
			return RegistrationStart{}, err
		}
	}
	policy, err := s.settings.RegistrationPolicy(ctx)
	if err != nil {
		return RegistrationStart{}, err
	}
	if policy.Mode == settingssvc.RegistrationClosed {
		return RegistrationStart{}, ErrRegistrationUnavailable
	}
	token, err := newToken(registrationTokenBytes)
	if err != nil {
		return RegistrationStart{}, errs.Wrap(errs.Internal, err)
	}
	code, err := newNumericCode(verificationCodeDigits)
	if err != nil {
		return RegistrationStart{}, errs.Wrap(errs.Internal, err)
	}
	expiresAt := s.now().Add(registrationTTL)
	var taken bool
	if err = s.tx.Do(ctx, func(ctx context.Context) error {
		inviteCodeID, err := resolveInviteAtStart(ctx, s, policy, inviteCode)
		if err != nil {
			return err
		}
		taken, err = s.repo.EmailTaken(ctx, email)
		if err != nil {
			return err
		}
		meta := reqmeta.From(ctx)
		sess := model.RegistrationSession{
			TokenHash: hashToken(token), Email: email, InviteCodeID: inviteCodeID,
			IPHash: s.ipHash(meta.IP), UserAgent: truncateToPtr(meta.UserAgent, 512),
			Status: model.RegistrationPending, ExpiresAt: expiresAt,
		}
		if err := s.repo.InsertRegistration(ctx, &sess); err != nil {
			return err
		}
		if !taken && policy.EmailVerification {
			// 同一邮箱此前没用掉的注册验证码一律作废，只认最新一条（与找回密码一致）。
			if err := s.repo.ExpireCodes(ctx, model.PurposeEmailVerify, s.targetHash(email)); err != nil {
				return err
			}
			if err := s.repo.InsertCode(ctx, &model.VerificationCode{
				Purpose: model.PurposeEmailVerify, TargetHash: s.targetHash(email),
				CodeHash: s.codeHash(code), MaxAttempts: registrationMaxAttempts, ExpiresAt: expiresAt,
			}); err != nil {
				return err
			}
			mailer, err := s.needMailer()
			if err != nil {
				return err
			}
			return mailer.SendToAddress(ctx, TemplateEmailVerify, email, map[string]string{
				"code": code, "minutes": strconv.Itoa(int(registrationTTL / time.Minute)),
			}, nil)
		}
		return nil
	}); err != nil {
		return RegistrationStart{}, err
	}
	padSince(ctx, start, startFloor)
	out := RegistrationStart{Token: token, ExpiresAt: expiresAt, VerificationRequired: policy.EmailVerification}
	if s.devMode && policy.EmailVerification && !taken {
		out.DevCode = code
	}
	return out, nil
}

// CompleteRegistration 是注册第 2 步：凭注册令牌与验证码建号。返回新用户。
func (s *Service) CompleteRegistration(ctx context.Context, token, code, password string) (User, error) {
	if err := validatePassword(password, false); err != nil {
		return User{}, err
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 128 {
		return User{}, ErrRegistrationUnavailable
	}
	if err := s.limit(ctx, ruleRegisterToken, token); err != nil {
		return User{}, err
	}
	// 先不加锁看一眼令牌：无效、已用或错满次数直接拒，不白算 Argon2；事务里照旧加锁复核。
	pre, ok, err := s.repo.RegistrationByToken(ctx, hashToken(token))
	if err != nil {
		return User{}, err
	}
	if !ok || pre.Status != model.RegistrationPending || pre.Attempts >= registrationMaxAttempts {
		return User{}, ErrRegistrationUnavailable
	}
	release, err := s.hasher.acquire(ctx, authn.AudiencePublic)
	if err != nil {
		return User{}, err
	}
	phc, err := hashPHC(password)
	release()
	if err != nil {
		return User{}, errs.Wrap(errs.Internal, err)
	}
	var rejected bool
	var out User
	var codeRow *model.VerificationCode
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		policy, err := s.settings.RegistrationPolicy(ctx)
		if err != nil {
			return err
		}
		if policy.Mode == settingssvc.RegistrationClosed {
			return ErrRegistrationUnavailable
		}
		sess, ok, err := s.repo.LockRegistrationByToken(ctx, hashToken(token))
		if err != nil {
			return err
		}
		if !ok || sess.Status != model.RegistrationPending || sess.Attempts >= registrationMaxAttempts {
			return ErrRegistrationUnavailable
		}
		if policy.Mode == settingssvc.RegistrationInviteOnly && sess.InviteCodeID == nil {
			return ErrRegistrationUnavailable
		}
		var inv model.InviteCode
		if sess.InviteCodeID != nil {
			inv, ok, err = s.repo.LockUsableInvite(ctx, *sess.InviteCodeID)
			if err != nil {
				return err
			}
			if !ok {
				return ErrRegistrationUnavailable
			}
		}
		if policy.EmailVerification {
			if !validNumericCode(code, verificationCodeDigits) {
				return incRegReject(ctx, s, sess.ID, &rejected)
			}
			target := s.targetHash(sess.Email)
			vc, hit, err := s.repo.LockLatestCode(ctx, model.PurposeEmailVerify, target)
			if err != nil {
				return err
			}
			if !hit || vc.Attempts >= vc.MaxAttempts {
				return incRegReject(ctx, s, sess.ID, &rejected)
			}
			if subtle.ConstantTimeCompare(vc.CodeHash, s.codeHash(code)) != 1 {
				// 验错累加这条码的次数，满 max_attempts 作废
				if err := s.repo.IncCodeAttempts(ctx, vc.ID); err != nil {
					return err
				}
				if vc.Attempts+1 >= vc.MaxAttempts {
					if err := s.repo.ExpireCodes(ctx, model.PurposeEmailVerify, target); err != nil {
						return err
					}
				}
				return incRegReject(ctx, s, sess.ID, &rejected)
			}
			codeRow = &vc
		}
		now := s.now()
		u := model.User{Email: sess.Email, Status: model.UserActive}
		if policy.EmailVerification {
			u.EmailVerifiedAt = &now
		}
		if err := s.repo.InsertUser(ctx, &u); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return ErrRegistrationUnavailable
			}
			return err
		}
		if err := s.repo.UpsertPassword(ctx, u.ID, phc); err != nil {
			return err
		}
		if codeRow != nil {
			if err := s.repo.ConsumeCode(ctx, codeRow.ID); err != nil {
				return err
			}
		}
		if err := s.repo.ConsumeRegistration(ctx, sess.ID); err != nil {
			return err
		}
		if sess.InviteCodeID != nil {
			if inv.OwnerUserID != nil && *inv.OwnerUserID == u.ID {
				return ErrRegistrationUnavailable
			}
			if inv.OwnerUserID != nil {
				if err := s.repo.InsertReferral(ctx, &model.Referral{
					RefereeUserID: u.ID, ReferrerUserID: *inv.OwnerUserID, InviteCodeID: &inv.ID,
					Channel: inv.Channel, Campaign: inv.Campaign,
				}); err != nil {
					return err
				}
			}
			ok, err := s.repo.ConsumeInvite(ctx, inv.ID)
			if err != nil {
				return err
			}
			if !ok {
				return ErrRegistrationUnavailable
			}
		}
		if err := s.writeSelfAudit(ctx, u.ID, auditsvc.Event{Action: ActionRegistered}); err != nil {
			return err
		}
		if err := s.emitPlugin(ctx, "user.registered", "user.registered:"+u.ID.String(), map[string]any{"event": "user.registered", "user_id": u.ID.String(), "email": u.Email}); err != nil {
			return err
		}
		out = User{ID: u.ID, Email: u.Email, Status: u.Status, CreatedAt: u.CreatedAt}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	if rejected {
		return User{}, ErrRegistrationUnavailable
	}
	return out, nil
}

func resolveInviteAtStart(ctx context.Context, s *Service, policy settingssvc.Registration, inviteCode string) (*uuid.UUID, error) {
	need := policy.Mode == settingssvc.RegistrationInviteOnly
	if need && inviteCode == "" {
		return nil, ErrRegistrationUnavailable
	}
	if inviteCode == "" {
		return nil, nil
	}
	inv, ok, err := s.repo.FindUsableInvite(ctx, inviteCode)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrRegistrationUnavailable
	}
	return &inv.ID, nil
}

func incRegReject(ctx context.Context, s *Service, sessID uuid.UUID, rejected *bool) error {
	if err := s.repo.IncRegistrationAttempts(ctx, sessID); err != nil {
		return err
	}
	*rejected = true
	return nil
}

// truncateToPtr 把请求头里的文本清成合法 UTF-8、去掉 NUL，再按字节上限在字符边界截断；空串回 nil。
func truncateToPtr(s string, maxBytes int) *string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
	if len(s) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	if s == "" {
		return nil
	}
	return &s
}
