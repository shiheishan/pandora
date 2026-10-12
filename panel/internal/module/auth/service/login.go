package service

import (
	"context"
	"errors"
	"math"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	failureWindow      = 15 * time.Minute // 失败计数窗口
	failureThreshold   = 10               // 窗口内失败到这个次数就暂停
	failurePause       = 15 * time.Minute // 暂停时长（期间口令正确也拒）
	failureAuditWindow = 10 * time.Minute // 同一 IP 或同一邮箱在这个窗口内只记一条失败审计
	loginFailedAction  = "user.login_failed"
)

// 失败原因（写进审计 data.reason）
const (
	reasonInvalidCredentials = "invalid_credentials" //nolint:gosec // 审计原因码，不是凭据
	reasonAccountInactive    = "account_inactive"
	reasonNoAdminRole        = "no_admin_role"
	reasonStaffPortal        = "staff_portal_blocked"
)

var errQuickLoginInvalid = errs.Newf(errs.Unauthorized, "快捷登录链接无效或已过期")

func failureKey(aud, emailHash string) string { return "lf:" + aud + ":" + emailHash }

// Login 校验邮箱与口令并建立会话（门户或后台）。
func (s *Service) Login(ctx context.Context, audience, email, password string) (Tokens, error) {
	if s.issuer(audience) == nil {
		return Tokens{}, errs.Newf(errs.BadRequest, "未知的登录域")
	}

	now := s.now()
	eh := s.emailHash(email)
	key := failureKey(audience, eh)

	n, ttl, err := s.sessions.failures(ctx, key)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Unavailable, err)
	}
	if n >= failureThreshold {
		retry := int(math.Ceil(ttl.Seconds()))
		if ttl <= 0 {
			retry = int(failurePause / time.Second)
		}
		if retry < 1 {
			retry = 1
		}
		e := errs.Newf(errs.RateLimited, "登录失败次数过多，请稍后再试")
		e.RetryAfterSeconds = retry
		return Tokens{}, e
	}

	u0, err := s.users.FindByEmail(ctx, normalizeEmail(email))
	uid0 := uuid.Nil
	if err != nil {
		if errs.As(err).Code != errs.NotFound {
			return Tokens{}, err
		}
	} else {
		uid0 = u0.ID
	}

	epoch0, err := s.sessions.epoch(ctx, uid0)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Unavailable, err)
	}

	var knownUID *uuid.UUID
	if uid0 != uuid.Nil {
		knownUID = &u0.ID
	}

	u, err := s.users.Authenticate(ctx, audience, email, password)
	if err != nil {
		// identity 在账号存在时出错也带回 User，只用来记失败审计
		if knownUID == nil && u.ID != uuid.Nil {
			id := u.ID
			knownUID = &id
		}
		switch {
		case errors.Is(err, identitysvc.ErrAccountInactive):
			s.loginFailed(ctx, audience, eh, knownUID, reasonAccountInactive, true)
			return Tokens{}, errInvalidCredentials
		case errs.As(err).Code == errs.Unauthorized, errs.As(err).Code == errs.NotFound:
			s.loginFailed(ctx, audience, eh, knownUID, reasonInvalidCredentials, true)
			return Tokens{}, errInvalidCredentials
		default:
			return Tokens{}, err
		}
	}

	if u.ID != uid0 {
		epoch0, err = s.sessions.epoch(ctx, u.ID)
		if err != nil {
			return Tokens{}, errs.Wrap(errs.Unavailable, err)
		}
		knownUID = &u.ID
	}

	if u.Status != UserStatusActive {
		s.loginFailed(ctx, audience, eh, knownUID, reasonAccountInactive, true)
		return Tokens{}, errInvalidCredentials
	}

	var perms []string
	if audience == authn.AudienceAdmin {
		perms, err = s.store.Permissions(ctx, u.ID)
		if err != nil {
			return Tokens{}, errs.Wrap(errs.Internal, err)
		}
		if len(perms) == 0 {
			s.loginFailed(ctx, audience, eh, knownUID, reasonNoAdminRole, true)
			return Tokens{}, errInvalidCredentials
		}
	} else {
		staff, err := s.store.HasActiveRole(ctx, u.ID)
		if err != nil {
			return Tokens{}, errs.Wrap(errs.Internal, err)
		}
		if staff {
			s.loginFailed(ctx, audience, eh, knownUID, reasonStaffPortal, false)
			return Tokens{}, ErrStaffPortalLogin
		}
	}

	rec, refresh, err := s.newSession(ctx, u.ID, audience, []string{amrPassword}, epoch0, true, now)
	if err != nil {
		return Tokens{}, err
	}

	access, expiresIn, err := s.issueAccess(audience, u.ID, rec.ID, rec.AMR, now)
	if err != nil {
		return Tokens{}, err
	}

	p := authn.Principal{
		UserID:    u.ID,
		SessionID: rec.ID,
		Audience:  audience,
		AMR:       rec.AMR,
		ReauthAt:  rec.ReauthAt,
	}
	actx := authn.With(ctx, p)
	if err = s.tx.Do(actx, func(ctx context.Context) error {
		sid := uuid.MustParse(rec.ID)
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "user.login",
			ResourceType: "session",
			ResourceID:   &sid,
		}); err != nil {
			return err
		}
		ok, err := s.sessions.create(ctx, rec, now)
		if err != nil {
			return errs.Wrap(errs.Unavailable, err)
		}
		if !ok {
			return errInvalidCredentials
		}
		return nil
	}); err != nil {
		return Tokens{}, err
	}

	if err := s.sessions.clearFailures(ctx, key); err != nil {
		plog.From(ctx).Warn("清除登录失败计数失败", zap.Error(err))
	}

	return Tokens{
		AccessToken:    access,
		ExpiresIn:      expiresIn,
		UserID:         u.ID,
		Refresh:        refresh,
		RefreshExpires: rec.AbsExp,
		Permissions:    perms,
	}, nil
}

func (s *Service) loginFailed(ctx context.Context, aud, emailHash string, uid *uuid.UUID, reason string, count bool) {
	if count {
		if _, err := s.sessions.recordFailure(ctx, failureKey(aud, emailHash), failureWindow, failureThreshold, failurePause); err != nil {
			plog.From(ctx).Warn("记录登录失败计数失败", zap.Error(err))
		}
	}

	if reason != reasonStaffPortal {
		keys := []string{"lfa:em:" + aud + ":" + emailHash}
		if ih := s.ipHash(ctx); ih != "" {
			keys = append(keys, "lfa:ip:"+aud+":"+ih)
		}
		first, err := s.sessions.markOnce(ctx, failureAuditWindow, keys...)
		if err != nil {
			first = true
		}
		if !first {
			return
		}
	}

	ev := auditsvc.Event{
		Action:   loginFailedAction,
		ActorID:  uid,
		Audience: aud,
		Outcome:  auditsvc.OutcomeFailure,
		Data:     map[string]any{"reason": reason, "email_hash": emailHash},
	}
	if uid != nil {
		ev.ResourceType = "user"
		ev.ResourceID = uid
	}

	if err := s.tx.Do(ctx, func(ctx context.Context) error {
		return s.audit.Write(ctx, ev)
	}); err != nil {
		plog.From(ctx).Warn("写入登录失败审计失败", zap.Error(err))
	}
}

// ConsumeQuickLogin 消费门户快捷登录令牌并签发新会话。
func (s *Service) ConsumeQuickLogin(ctx context.Context, token string) (Tokens, error) {
	if token == "" || len(token) > 128 {
		return Tokens{}, errQuickLoginInvalid
	}

	now := s.now()
	srcSID, uid, ok, err := s.sessions.consumeQuickLogin(ctx, hashSecret(token), authn.AudiencePublic, now)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Unavailable, err)
	}
	if !ok {
		return Tokens{}, errQuickLoginInvalid
	}

	epoch, err := s.sessions.epoch(ctx, uid)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Unavailable, err)
	}

	u, err := s.users.Get(ctx, uid)
	if err != nil {
		if errs.As(err).Code == errs.NotFound {
			return Tokens{}, errQuickLoginInvalid
		}
		return Tokens{}, err
	}
	if u.Status != UserStatusActive {
		return Tokens{}, errs.Newf(errs.Forbidden, "账号当前不可登录")
	}

	staff, err := s.store.HasActiveRole(ctx, uid)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Internal, err)
	}
	if staff {
		return Tokens{}, ErrStaffPortalLogin
	}

	rec, refresh, err := s.newSession(ctx, uid, authn.AudiencePublic, []string{amrQuickLogin}, epoch, false, now)
	if err != nil {
		return Tokens{}, err
	}

	access, expiresIn, err := s.issueAccess(authn.AudiencePublic, uid, rec.ID, rec.AMR, now)
	if err != nil {
		return Tokens{}, err
	}

	p := authn.Principal{
		UserID:    uid,
		SessionID: rec.ID,
		Audience:  authn.AudiencePublic,
		AMR:       rec.AMR,
		ReauthAt:  rec.ReauthAt,
	}
	actx := authn.With(ctx, p)
	if err = s.tx.Do(actx, func(ctx context.Context) error {
		sid := uuid.MustParse(rec.ID)
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "auth.quick_login",
			ResourceType: "session",
			ResourceID:   &sid,
			Data: map[string]any{
				"source_session": srcSID,
				"auth_method":    amrQuickLogin,
			},
		}); err != nil {
			return err
		}
		ok, err := s.sessions.create(ctx, rec, now)
		if err != nil {
			return errs.Wrap(errs.Unavailable, err)
		}
		if !ok {
			return errQuickLoginInvalid
		}
		return nil
	}); err != nil {
		return Tokens{}, err
	}

	return Tokens{
		AccessToken:    access,
		ExpiresIn:      expiresIn,
		UserID:         uid,
		Refresh:        refresh,
		RefreshExpires: rec.AbsExp,
	}, nil
}
