package handler

import (
	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

type signupResponse struct {
	CaptchaProvider             string   `json:"captcha_provider"`
	CaptchaSiteKey              string   `json:"captcha_site_key"`
	HasCaptchaSecret            bool     `json:"has_captcha_secret"`
	RecaptchaV3Threshold        float64  `json:"recaptcha_v3_threshold"`
	EmailSuffixWhitelistEnabled bool     `json:"email_suffix_whitelist_enabled"`
	EmailSuffixes               []string `json:"email_suffixes"`
	GmailAliasBlock             bool     `json:"gmail_alias_block"`
	IPLimitEnabled              bool     `json:"ip_limit_enabled"`
	IPLimitCount                int      `json:"ip_limit_count"`
	IPLimitWindowMinutes        int      `json:"ip_limit_window_minutes"`
	TrialPlanID                 *string  `json:"trial_plan_id"`
	TrialHours                  int      `json:"trial_hours"`
	InviteMaxCodes              int      `json:"invite_max_codes"`
	InviteNeverExpire           bool     `json:"invite_never_expire"`
	MailLinkLogin               bool     `json:"mail_link_login"`
	MailConfigured              bool     `json:"mail_configured"`
}

type setSignupRequest struct {
	CaptchaProvider             *string   `json:"captcha_provider"`
	CaptchaSiteKey              *string   `json:"captcha_site_key"`
	CaptchaSecret               *string   `json:"captcha_secret"`
	RecaptchaV3Threshold        *float64  `json:"recaptcha_v3_threshold"`
	EmailSuffixWhitelistEnabled *bool     `json:"email_suffix_whitelist_enabled"`
	EmailSuffixes               *[]string `json:"email_suffixes"`
	GmailAliasBlock             *bool     `json:"gmail_alias_block"`
	IPLimitEnabled              *bool     `json:"ip_limit_enabled"`
	IPLimitCount                *int      `json:"ip_limit_count"`
	IPLimitWindowMinutes        *int      `json:"ip_limit_window_minutes"`
	TrialPlanID                 *string   `json:"trial_plan_id"`
	TrialHours                  *int      `json:"trial_hours"`
	InviteMaxCodes              *int      `json:"invite_max_codes"`
	InviteNeverExpire           *bool     `json:"invite_never_expire"`
	MailLinkLogin               *bool     `json:"mail_link_login"`
}

type signupConfigCaptcha struct {
	Provider string `json:"provider"`
	SiteKey  string `json:"site_key"`
}

type signupConfigResponse struct {
	Captcha       *signupConfigCaptcha `json:"captcha"`
	EmailSuffixes []string             `json:"email_suffixes"`
	MailLinkLogin bool                 `json:"mail_link_login"`
}

func signupToResponse(s service.Signup, mailConfigured bool) signupResponse {
	var trialPlanID *string
	if s.TrialPlanID != nil {
		id := s.TrialPlanID.String()
		trialPlanID = &id
	}
	suffixes := s.EmailSuffixes
	if suffixes == nil {
		suffixes = []string{}
	}
	return signupResponse{
		CaptchaProvider:             s.CaptchaProvider,
		CaptchaSiteKey:              s.CaptchaSiteKey,
		HasCaptchaSecret:            s.HasCaptchaSecret,
		RecaptchaV3Threshold:        s.RecaptchaV3Threshold,
		EmailSuffixWhitelistEnabled: s.EmailSuffixWhitelistEnabled,
		EmailSuffixes:               suffixes,
		GmailAliasBlock:             s.GmailAliasBlock,
		IPLimitEnabled:              s.IPLimitEnabled,
		IPLimitCount:                s.IPLimitCount,
		IPLimitWindowMinutes:        s.IPLimitWindowMinutes,
		TrialPlanID:                 trialPlanID,
		TrialHours:                  s.TrialHours,
		InviteMaxCodes:              s.InviteMaxCodes,
		InviteNeverExpire:           s.InviteNeverExpire,
		MailLinkLogin:               s.MailLinkLogin,
		MailConfigured:              mailConfigured,
	}
}

func (h *Handler) getSignup(c *gin.Context) {
	ctx := c.Request.Context()
	signup, err := h.svc.SignupWithoutSecret(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	mailOK, err := h.svc.MailConfigured(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, signupToResponse(signup, mailOK))
}

func (h *Handler) setSignup(c *gin.Context) {
	ctx := c.Request.Context()
	var req setSignupRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SetSignup(ctx, service.SignupUpdate{
		CaptchaProvider:             req.CaptchaProvider,
		CaptchaSiteKey:              req.CaptchaSiteKey,
		CaptchaSecret:               req.CaptchaSecret,
		RecaptchaV3Threshold:        req.RecaptchaV3Threshold,
		EmailSuffixWhitelistEnabled: req.EmailSuffixWhitelistEnabled,
		EmailSuffixes:               req.EmailSuffixes,
		GmailAliasBlock:             req.GmailAliasBlock,
		IPLimitEnabled:              req.IPLimitEnabled,
		IPLimitCount:                req.IPLimitCount,
		IPLimitWindowMinutes:        req.IPLimitWindowMinutes,
		TrialPlanID:                 req.TrialPlanID,
		TrialHours:                  req.TrialHours,
		InviteMaxCodes:              req.InviteMaxCodes,
		InviteNeverExpire:           req.InviteNeverExpire,
		MailLinkLogin:               req.MailLinkLogin,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	signup, err := h.svc.SignupWithoutSecret(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	mailOK, err := h.svc.MailConfigured(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, signupToResponse(signup, mailOK))
}

func (h *Handler) signupConfig(c *gin.Context) {
	ctx := c.Request.Context()
	signup, err := h.svc.SignupWithoutSecret(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	mailOK, err := h.svc.MailConfigured(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	emailOn, err := h.svc.SwitchEnabled(ctx, service.SwitchNotifyEmail)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	var captcha *signupConfigCaptcha
	if signup.CaptchaEnabled() {
		captcha = &signupConfigCaptcha{
			Provider: signup.CaptchaProvider,
			SiteKey:  signup.CaptchaSiteKey,
		}
	}
	suffixes := []string{}
	if signup.EmailSuffixWhitelistEnabled {
		suffixes = signup.EmailSuffixes
		if suffixes == nil {
			suffixes = []string{}
		}
	}
	httpx.OK(c, signupConfigResponse{
		Captcha:       captcha,
		EmailSuffixes: suffixes,
		MailLinkLogin: signup.MailLinkLogin && mailOK && emailOn,
	})
}
