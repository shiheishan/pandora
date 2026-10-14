package service

import (
	"context"
	"strings"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// MailLinkSettings 是邮件链接登录要读的设置；*settingssvc.Service 直接满足。
type MailLinkSettings interface {
	Signup(ctx context.Context) (settingssvc.Signup, error) // 用 MailLinkLogin 字段
	MailConfigured(ctx context.Context) (bool, error)
}

// MailLinkCaptcha 由 identity 实现（*identitysvc.Service 的 VerifyCaptcha）：与注册、找回同一套人机验证校验与文案；没开人机验证时直接通过。
type MailLinkCaptcha interface {
	VerifyCaptcha(ctx context.Context, token, action string) error
}

// mailLinkCaptchaAction 与 identity 的 CaptchaActionMailLogin、前端 MailLinkLogin.tsx 的 getToken('mail_login') 一致。
const mailLinkCaptchaAction = "mail_login"

// MailLinkMailer 由 notify 实现：在调用方事务里写一条投递记录，提交后入队发送。
type MailLinkMailer interface {
	SendToAddress(ctx context.Context, templateCode, address string, vars map[string]string, userID *uuid.UUID) error
}

// TemplateLoginLink 是邮件链接登录的模板代码。
const TemplateLoginLink = "auth.login_link"

var (
	// ErrMailLinkUnavailable 是没开邮件链接登录或没配发信（门户本该隐藏入口）。
	ErrMailLinkUnavailable = errs.Newf(errs.Forbidden, "邮件链接登录未开启")
	errMailLinkInvalid     = errs.Newf(errs.Unauthorized, "登录链接无效或已过期，请重新获取")
)

// UseMailLink 只在启动装配时调用。publicBaseURL 是面板对外地址（config.PublicBaseURL），链接拼成 <publicBaseURL 去掉末尾 />/#/mail-login/<令牌>。
func (s *Service) UseMailLink(settings MailLinkSettings, mailer MailLinkMailer, captcha MailLinkCaptcha, limiter *ratelimit.Limiter, publicBaseURL string) {
	s.mailSettings = settings
	s.mailCaptcha = captcha
	s.mailer = mailer
	s.mailLimiter = limiter
	s.publicBaseURL = publicBaseURL
}

// StartMailLogin 发送免密登录邮件链接。开了人机验证时与找回密码第 1 步同口径：先过人机验证（在按邮箱限频之前），
// 否则开了人机验证以后，脚本可以改走这个接口给全站已注册邮箱刷信。
func (s *Service) StartMailLogin(ctx context.Context, email, captchaToken string) error {
	start := time.Now()
	if s.mailSettings == nil || s.mailer == nil || s.mailLimiter == nil || s.mailCaptcha == nil {
		return ErrMailLinkUnavailable
	}

	signup, err := s.mailSettings.Signup(ctx)
	if err != nil {
		return err
	}
	if !signup.MailLinkLogin {
		return ErrMailLinkUnavailable
	}
	configured, err := s.mailSettings.MailConfigured(ctx)
	if err != nil {
		return err
	}
	if !configured {
		return ErrMailLinkUnavailable
	}

	email = normalizeEmail(email)
	if len(email) < 3 || len(email) > 254 || !strings.Contains(email, "@") {
		return errs.Invalid(map[string]string{"email": "邮箱格式不正确"})
	}
	if err := s.mailCaptcha.VerifyCaptcha(ctx, captchaToken, mailLinkCaptchaAction); err != nil {
		return err
	}

	h := s.emailHash(email)
	if len(h) > 32 {
		h = h[:32]
	}
	if err := s.mailLimiter.Allow(ctx, ratelimit.PerWindow("mail_link_email", 3, 15*time.Minute).StrictMode(), h); err != nil {
		return err
	}

	u, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		if errs.As(err).Code == errs.NotFound {
			padMailLink(ctx, start, 300*time.Millisecond)
			return nil
		}
		return err
	}

	if u.Status != UserStatusActive {
		padMailLink(ctx, start, 300*time.Millisecond)
		return nil
	}

	admin, err := s.store.IsAdmin(ctx, u.ID)
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	if admin {
		padMailLink(ctx, start, 300*time.Millisecond)
		return nil
	}

	epoch, err := s.sessions.epoch(ctx, u.ID)
	if err != nil {
		return errs.Wrap(errs.Unavailable, err)
	}

	token, err := newSecret()
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}

	link := strings.TrimRight(s.publicBaseURL, "/") + "/#/mail-login/" + token
	if err := s.sessions.issueMailLink(ctx, u.ID, hashSecret(token), epoch, mailLinkTTL); err != nil {
		return errs.Wrap(errs.Unavailable, err)
	}

	if err := s.tx.Do(ctx, func(ctx context.Context) error {
		return s.mailer.SendToAddress(ctx, TemplateLoginLink, email, map[string]string{
			"link":    link,
			"minutes": "15",
		}, &u.ID)
	}); err != nil {
		return err
	}

	padMailLink(ctx, start, 300*time.Millisecond)
	return nil
}

// ConsumeMailLogin 消费邮件登录令牌并签发新会话。
func (s *Service) ConsumeMailLogin(ctx context.Context, token string) (Tokens, error) {
	if token == "" || len(token) > 128 {
		return Tokens{}, errMailLinkInvalid
	}

	uid, epoch, ok, err := s.sessions.consumeMailLink(ctx, hashSecret(token))
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Unavailable, err)
	}
	if !ok {
		return Tokens{}, errMailLinkInvalid
	}

	curEpoch, err := s.sessions.epoch(ctx, uid)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Unavailable, err)
	}
	if curEpoch != epoch {
		return Tokens{}, errMailLinkInvalid
	}

	u, err := s.users.Get(ctx, uid)
	if err != nil {
		if errs.As(err).Code == errs.NotFound {
			return Tokens{}, errMailLinkInvalid
		}
		return Tokens{}, err
	}
	if u.Status != UserStatusActive {
		return Tokens{}, errs.Newf(errs.Forbidden, "账号当前不可登录")
	}

	admin, err := s.store.IsAdmin(ctx, uid)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Internal, err)
	}
	if admin {
		return Tokens{}, ErrStaffPortalLogin
	}

	now := s.now()
	rec, refresh, err := s.newSession(ctx, uid, authn.AudiencePublic, []string{amrMailLink}, epoch, false, now)
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
			Action:       "auth.mail_link_login",
			ResourceType: "session",
			ResourceID:   &sid,
			Data: map[string]any{
				"auth_method": amrMailLink,
			},
		}); err != nil {
			return err
		}
		ok, err := s.sessions.create(ctx, rec, now)
		if err != nil {
			return errs.Wrap(errs.Unavailable, err)
		}
		if !ok {
			return errMailLinkInvalid
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

// padMailLink 睡到 start 之后至少 d；ctx 取消时提前返回。
func padMailLink(ctx context.Context, start time.Time, d time.Duration) {
	left := d - time.Since(start)
	if left <= 0 {
		return
	}
	t := time.NewTimer(left)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
