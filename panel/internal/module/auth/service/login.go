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
	reasonNotAdmin           = "not_admin"
	reasonStaffPortal        = "staff_portal_blocked"
)

var errQuickLoginInvalid = errs.Newf(errs.Unauthorized, "快捷登录链接无效或已过期")

// failureKey 是登录失败计数键：按账号跨域共用；邮箱不存在时用邮箱哈希（对外行为一致，不暴露账号是否存在）。
func failureKey(uid uuid.UUID, emailHash string) string {
	if uid != uuid.Nil {
		return "lf:" + uid.String()
	}
	return "lf:" + emailHash
}

// Login 校验邮箱与口令并建立会话（门户或后台）。失败计数先占名额再验：
// 窗口内第 11 次起直接 429（口令对也拒）；成功清零；没有结论的结果退回名额。
func (s *Service) Login(ctx context.Context, audience, email, password string) (Tokens, error) {
	if s.issuer(audience) == nil {
		return Tokens{}, errs.Newf(errs.BadRequest, "未知的登录域")
	}

	eh := s.emailHash(email)
	u0, err := s.users.FindByEmail(ctx, normalizeEmail(email))
	if err != nil {
		if errs.As(err).Code != errs.NotFound {
			return Tokens{}, err
		}
		u0 = identitysvc.User{}
	}
	key := failureKey(u0.ID, eh)

	n, ttl, err := s.sessions.attempts.Incr(ctx, key, failureWindow, failureThreshold, failurePause)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Unavailable, err)
	}
	if n > failureThreshold {
		retry := int(math.Ceil(ttl.Seconds()))
		if retry < 1 {
			retry = int(failurePause / time.Second)
		}
		e := errs.Newf(errs.RateLimited, "登录失败次数过多，请稍后再试")
		e.RetryAfterSeconds = retry
		return Tokens{}, e
	}

	t, counted, err := s.login(ctx, audience, email, password, eh, u0.ID)
	switch {
	case err == nil:
		if cerr := s.sessions.attempts.Reset(ctx, key); cerr != nil {
			plog.From(ctx).Warn("清除登录失败计数失败", zap.Error(cerr))
		}
	case !counted:
		if rerr := s.sessions.attempts.Release(ctx, key); rerr != nil {
			plog.From(ctx).Warn("退回登录失败名额失败", zap.Error(rerr))
		}
	}
	return t, err
}

// login 是名额占好之后的登录本体。counted 为 true 表示这是一次算数的凭据失败（口令错、账号不存在、
// 停用、登后台的不是管理员；停用账号口令对时对外回 errAccountDisabled（后台只对管理员））；
// 其余错误（故障、后台人员登门户等口令其实是对的）由调用方退回名额。
func (s *Service) login(ctx context.Context, audience, email, password, eh string, uid0 uuid.UUID) (Tokens, bool, error) {
	now := s.now()
	// 纪元在验口令之前读：验口令到建会话之间改了密，建会话会被拒
	epoch0, err := s.sessions.epoch(ctx, uid0)
	if err != nil {
		return Tokens{}, false, errs.Wrap(errs.Unavailable, err)
	}

	var knownUID *uuid.UUID
	if uid0 != uuid.Nil {
		id := uid0
		knownUID = &id
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
			// identity 只在口令校验通过之后才回 ErrAccountInactive：到这里说明口令是对的。
			// 后台入口先看是不是管理员：不是的照旧与口令错相同（IAM-006），是的才明确提示停用。
			disabled, derr := s.disabledLoginError(ctx, audience, knownUID)
			if derr != nil {
				return Tokens{}, false, derr
			}
			s.loginFailed(ctx, audience, eh, knownUID, reasonAccountInactive)
			return Tokens{}, true, disabled
		case errs.As(err).Code == errs.Unauthorized, errs.As(err).Code == errs.NotFound:
			s.loginFailed(ctx, audience, eh, knownUID, reasonInvalidCredentials)
			return Tokens{}, true, errInvalidCredentials
		default:
			return Tokens{}, false, err
		}
	}

	if u.ID != uid0 {
		epoch0, err = s.sessions.epoch(ctx, u.ID)
		if err != nil {
			return Tokens{}, false, errs.Wrap(errs.Unavailable, err)
		}
		knownUID = &u.ID
	}

	if u.Status != UserStatusActive {
		disabled, derr := s.disabledLoginError(ctx, audience, knownUID)
		if derr != nil {
			return Tokens{}, false, derr
		}
		s.loginFailed(ctx, audience, eh, knownUID, reasonAccountInactive)
		return Tokens{}, true, disabled
	}

	admin, err := s.store.IsAdmin(ctx, u.ID)
	if err != nil {
		return Tokens{}, false, errs.Wrap(errs.Internal, err)
	}
	if audience == authn.AudienceAdmin {
		if !admin {
			// IAM-006：不是管理员与口令错完全相同，也计数
			s.loginFailed(ctx, audience, eh, knownUID, reasonNotAdmin)
			return Tokens{}, true, errInvalidCredentials
		}
	} else if admin {
		s.loginFailed(ctx, audience, eh, knownUID, reasonStaffPortal)
		return Tokens{}, false, ErrStaffPortalLogin
	}

	rec, refresh, err := s.newSession(ctx, u.ID, audience, []string{amrPassword}, epoch0, true, now)
	if err != nil {
		return Tokens{}, false, err
	}

	access, expiresIn, err := s.issueAccess(audience, u.ID, rec.ID, rec.AMR, now)
	if err != nil {
		return Tokens{}, false, err
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
		return Tokens{}, false, err
	}

	if err := s.users.TouchLastLogin(ctx, u.ID); err != nil {
		plog.From(ctx).Warn("更新最后登录时间失败", zap.Error(err))
	}

	return Tokens{
		AccessToken:    access,
		ExpiresIn:      expiresIn,
		UserID:         u.ID,
		Refresh:        refresh,
		RefreshExpires: rec.AbsExp,
	}, false, nil
}

// disabledLoginError 是停用账号口令正确时回给登录方的错误：门户一律 errAccountDisabled；
// 后台入口只有管理员名单里的人才回 errAccountDisabled，其余回 errInvalidCredentials（与口令错、不是管理员相同）。
func (s *Service) disabledLoginError(ctx context.Context, audience string, uid *uuid.UUID) (reply, err error) {
	if audience != authn.AudienceAdmin {
		return errAccountDisabled, nil
	}
	if uid == nil {
		return errInvalidCredentials, nil
	}
	admin, err := s.store.IsAdmin(ctx, *uid)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err)
	}
	if !admin {
		return errInvalidCredentials, nil
	}
	return errAccountDisabled, nil
}

// ClearLoginFailures 清掉该账号的登录失败计数与暂停（pandora admin reset-password 成功后调用：运维重置口令就是要恢复登录）。
func (s *Service) ClearLoginFailures(ctx context.Context, userID uuid.UUID) error {
	if err := s.sessions.attempts.Reset(ctx, failureKey(userID, "")); err != nil {
		return errs.Wrap(errs.Unavailable, err)
	}
	return nil
}

// loginFailed 写登录失败审计（anonymous：不传 ActorID 与 Audience，被撞账号放 ResourceID，audience 放 Data）。
// 同一邮箱或同一 IP 在窗口内只记一条；后台人员登门户每次都记。
func (s *Service) loginFailed(ctx context.Context, aud, emailHash string, uid *uuid.UUID, reason string) {
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
		Action:  loginFailedAction,
		Outcome: auditsvc.OutcomeFailure,
		Data:    map[string]any{"reason": reason, "email_hash": emailHash, "audience": aud},
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
		return Tokens{}, errAccountDisabled
	}

	admin, err := s.store.IsAdmin(ctx, uid)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Internal, err)
	}
	if admin {
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

	if err := s.users.TouchLastLogin(ctx, uid); err != nil {
		plog.From(ctx).Warn("更新最后登录时间失败", zap.Error(err))
	}

	return Tokens{
		AccessToken:    access,
		ExpiresIn:      expiresIn,
		UserID:         uid,
		Refresh:        refresh,
		RefreshExpires: rec.AbsExp,
	}, nil
}
