package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// 注册与登录这一组的键。
const (
	keySignupCaptchaProvider      = "signup.captcha_provider"
	keySignupCaptchaSiteKey       = "signup.captcha_site_key"
	keySignupCaptchaSecret        = "signup.captcha_secret" //nolint:gosec // 设置键名；值经 Sealer 封存（AAD 为键名）后 base64 存
	keySignupRecaptchaV3Threshold = "signup.recaptcha_v3_threshold"
	keySignupEmailSuffixEnabled   = "signup.email_suffix_whitelist_enabled"
	keySignupEmailSuffixes        = "signup.email_suffix_whitelist"
	keySignupGmailAliasBlock      = "signup.gmail_alias_block"
	keySignupIPLimitEnabled       = "signup.ip_limit_enabled"
	keySignupIPLimitCount         = "signup.ip_limit_count"
	keySignupIPLimitWindow        = "signup.ip_limit_window_minutes"
	keySignupTrialPlanID          = "signup.trial_plan_id"
	keySignupTrialHours           = "signup.trial_hours"
	keySignupInviteMaxCodes       = "signup.invite_max_codes"
	keySignupInviteNeverExpire    = "signup.invite_never_expire"
	keySignupMailLinkLogin        = "signup.mail_link_login"
)

// 人机验证类型。
const (
	CaptchaRecaptchaV2 = "recaptcha_v2"
	CaptchaRecaptchaV3 = "recaptcha_v3"
	CaptchaTurnstile   = "turnstile"
)

// 取值上限。
const (
	SignupMaxTrialHours    = 8760
	SignupMaxInviteCodes   = 50
	SignupMaxIPLimitCount  = 1000
	SignupMaxIPLimitWindow = 1440
	SignupMaxEmailSuffixes = 100
)

const (
	defaultSignupRecaptchaV3Threshold = 0.5
	defaultSignupEmailSuffixes        = "gmail.com,qq.com,163.com,yahoo.com,sina.com,126.com,outlook.com,yeah.net,foxmail.com"
	defaultSignupIPLimitCount         = 10
	defaultSignupIPLimitWindow        = 1
	defaultSignupInviteMaxCodes       = 5
	auditSignupChanged                = "signup.settings_changed"
)

var signupKeys = []string{
	keySignupCaptchaProvider,
	keySignupCaptchaSecret,
	keySignupCaptchaSiteKey,
	keySignupEmailSuffixEnabled,
	keySignupEmailSuffixes,
	keySignupGmailAliasBlock,
	keySignupIPLimitCount,
	keySignupIPLimitEnabled,
	keySignupIPLimitWindow,
	keySignupInviteMaxCodes,
	keySignupInviteNeverExpire,
	keySignupMailLinkLogin,
	keySignupRecaptchaV3Threshold,
	keySignupTrialHours,
	keySignupTrialPlanID,
}

var emailSuffixPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Signup 是注册与登录相关设置。
type Signup struct {
	CaptchaProvider             string
	CaptchaSiteKey              string
	CaptchaSecret               string // 解封后的明文，只给 identity 校验用
	HasCaptchaSecret            bool
	RecaptchaV3Threshold        float64
	EmailSuffixWhitelistEnabled bool
	EmailSuffixes               []string // 永不为 nil
	GmailAliasBlock             bool
	IPLimitEnabled              bool
	IPLimitCount                int
	IPLimitWindowMinutes        int
	TrialPlanID                 *uuid.UUID
	TrialHours                  int
	InviteMaxCodes              int
	InviteNeverExpire           bool
	MailLinkLogin               bool
}

// CaptchaEnabled 报告是否开了人机验证（provider 非空）。
func (s Signup) CaptchaEnabled() bool {
	return s.CaptchaProvider != ""
}

// SignupUpdate 是部分更新：nil 字段保持原值。CaptchaSecret 指向 "" 表示清除，指向非空串是新密钥。
type SignupUpdate struct {
	CaptchaProvider             *string
	CaptchaSiteKey              *string
	CaptchaSecret               *string
	RecaptchaV3Threshold        *float64
	EmailSuffixWhitelistEnabled *bool
	EmailSuffixes               *[]string
	GmailAliasBlock             *bool
	IPLimitEnabled              *bool
	IPLimitCount                *int
	IPLimitWindowMinutes        *int
	TrialPlanID                 *string
	TrialHours                  *int
	InviteMaxCodes              *int
	InviteNeverExpire           *bool
	MailLinkLogin               *bool
}

func validCaptchaProvider(p string) bool {
	return p == "" || p == CaptchaRecaptchaV2 || p == CaptchaRecaptchaV3 || p == CaptchaTurnstile
}

func splitEmailSuffixes(raw string) []string {
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinEmailSuffixes(suffixes []string) string {
	return strings.Join(suffixes, ",")
}

// NormalizeEmailSuffixes 规整邮箱后缀列表：去空白、去 @、小写、去重保序。
func NormalizeEmailSuffixes(in []string) ([]string, error) {
	fields := map[string]string{}
	seen := make(map[string]bool)
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		s = strings.TrimPrefix(s, "@")
		s = strings.ToLower(s)
		if s == "" {
			continue
		}
		if seen[s] {
			continue
		}
		if len(s) > 253 || !emailSuffixPattern.MatchString(s) {
			fields["email_suffixes"] = fmt.Sprintf("后缀格式不正确：%s", raw)
			break
		}
		seen[s] = true
		out = append(out, s)
	}
	if fields["email_suffixes"] != "" {
		return nil, errs.Invalid(fields)
	}
	if len(out) > SignupMaxEmailSuffixes {
		return nil, errs.Invalid(map[string]string{
			"email_suffixes": "最多 100 个后缀",
		})
	}
	return out, nil
}

func decodeSignup(vals map[string]json.RawMessage, openSecret func(enc string) string) (Signup, error) {
	provider, err := decodeOr(vals, keySignupCaptchaProvider, "")
	if err != nil {
		return Signup{}, err
	}
	if !validCaptchaProvider(provider) {
		return Signup{}, fmt.Errorf("设置 %s 的值 %q 不是合法的人机验证类型", keySignupCaptchaProvider, provider)
	}
	siteKey, err := decodeOr(vals, keySignupCaptchaSiteKey, "")
	if err != nil {
		return Signup{}, err
	}
	threshold, err := decodeOr(vals, keySignupRecaptchaV3Threshold, defaultSignupRecaptchaV3Threshold)
	if err != nil {
		return Signup{}, err
	}
	if threshold < 0 || threshold > 1 {
		return Signup{}, fmt.Errorf("设置 %s 的值 %v 超出范围", keySignupRecaptchaV3Threshold, threshold)
	}
	suffixEnabled, err := decodeOr(vals, keySignupEmailSuffixEnabled, false)
	if err != nil {
		return Signup{}, err
	}
	suffixRaw, err := decodeOr(vals, keySignupEmailSuffixes, defaultSignupEmailSuffixes)
	if err != nil {
		return Signup{}, err
	}
	gmailBlock, err := decodeOr(vals, keySignupGmailAliasBlock, false)
	if err != nil {
		return Signup{}, err
	}
	ipEnabled, err := decodeOr(vals, keySignupIPLimitEnabled, true)
	if err != nil {
		return Signup{}, err
	}
	ipCount, err := decodeOr(vals, keySignupIPLimitCount, defaultSignupIPLimitCount)
	if err != nil {
		return Signup{}, err
	}
	if ipCount < 1 || ipCount > SignupMaxIPLimitCount {
		return Signup{}, fmt.Errorf("设置 %s 的值 %d 超出范围", keySignupIPLimitCount, ipCount)
	}
	ipWindow, err := decodeOr(vals, keySignupIPLimitWindow, defaultSignupIPLimitWindow)
	if err != nil {
		return Signup{}, err
	}
	if ipWindow < 1 || ipWindow > SignupMaxIPLimitWindow {
		return Signup{}, fmt.Errorf("设置 %s 的值 %d 超出范围", keySignupIPLimitWindow, ipWindow)
	}
	trialPlanRaw, err := decodeOr(vals, keySignupTrialPlanID, "")
	if err != nil {
		return Signup{}, err
	}
	var trialPlanID *uuid.UUID
	if trialPlanRaw != "" {
		id, perr := uuid.Parse(trialPlanRaw)
		if perr != nil {
			return Signup{}, fmt.Errorf("设置 %s 的值 %q 不是合法的 UUID", keySignupTrialPlanID, trialPlanRaw)
		}
		trialPlanID = &id
	}
	trialHours, err := decodeOr(vals, keySignupTrialHours, 0)
	if err != nil {
		return Signup{}, err
	}
	if trialHours < 0 || trialHours > SignupMaxTrialHours {
		return Signup{}, fmt.Errorf("设置 %s 的值 %d 超出范围", keySignupTrialHours, trialHours)
	}
	inviteMax, err := decodeOr(vals, keySignupInviteMaxCodes, defaultSignupInviteMaxCodes)
	if err != nil {
		return Signup{}, err
	}
	if inviteMax < 1 || inviteMax > SignupMaxInviteCodes {
		return Signup{}, fmt.Errorf("设置 %s 的值 %d 超出范围", keySignupInviteMaxCodes, inviteMax)
	}
	inviteNever, err := decodeOr(vals, keySignupInviteNeverExpire, true)
	if err != nil {
		return Signup{}, err
	}
	mailLink, err := decodeOr(vals, keySignupMailLinkLogin, false)
	if err != nil {
		return Signup{}, err
	}

	suffixes := splitEmailSuffixes(suffixRaw)

	s := Signup{
		CaptchaProvider:             provider,
		CaptchaSiteKey:              siteKey,
		RecaptchaV3Threshold:        threshold,
		EmailSuffixWhitelistEnabled: suffixEnabled,
		EmailSuffixes:               suffixes,
		GmailAliasBlock:             gmailBlock,
		IPLimitEnabled:              ipEnabled,
		IPLimitCount:                ipCount,
		IPLimitWindowMinutes:        ipWindow,
		TrialPlanID:                 trialPlanID,
		TrialHours:                  trialHours,
		InviteMaxCodes:              inviteMax,
		InviteNeverExpire:           inviteNever,
		MailLinkLogin:               mailLink,
	}
	enc, err := decodeOr(vals, keySignupCaptchaSecret, "")
	if err != nil {
		return Signup{}, err
	}
	if enc != "" {
		s.HasCaptchaSecret = true
		if openSecret != nil {
			s.CaptchaSecret = openSecret(enc)
		}
	}
	return s, nil
}

// Signup 读取注册与登录设置。
func (s *Service) Signup(ctx context.Context) (Signup, error) {
	vals, err := s.store.Values(ctx, signupKeys)
	if err != nil {
		return Signup{}, err
	}
	return decodeSignup(vals, func(enc string) string {
		return s.openSignupCaptchaSecret(ctx, enc)
	})
}

func (s *Service) openSignupCaptchaSecret(ctx context.Context, enc string) string {
	if s.sealer == nil {
		plog.From(ctx).Error("settings: 人机验证密钥解封失败", zap.Error(errors.New("settings: sealer 未配置")))
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		plog.From(ctx).Error("settings: 人机验证密钥解封失败", zap.Error(err))
		return ""
	}
	plain, err := s.sealer.Open(blob, []byte(keySignupCaptchaSecret))
	if err != nil {
		plog.From(ctx).Error("settings: 人机验证密钥解封失败", zap.Error(err))
		return ""
	}
	return string(plain)
}

func signupMap(v Signup) map[string]any {
	plan := ""
	if v.TrialPlanID != nil {
		plan = v.TrialPlanID.String()
	}
	return map[string]any{
		keySignupCaptchaProvider:      v.CaptchaProvider,
		keySignupCaptchaSiteKey:       v.CaptchaSiteKey,
		keySignupRecaptchaV3Threshold: v.RecaptchaV3Threshold,
		keySignupEmailSuffixEnabled:   v.EmailSuffixWhitelistEnabled,
		keySignupEmailSuffixes:        joinEmailSuffixes(v.EmailSuffixes),
		keySignupGmailAliasBlock:      v.GmailAliasBlock,
		keySignupIPLimitEnabled:       v.IPLimitEnabled,
		keySignupIPLimitCount:         v.IPLimitCount,
		keySignupIPLimitWindow:        v.IPLimitWindowMinutes,
		keySignupTrialPlanID:          plan,
		keySignupTrialHours:           v.TrialHours,
		keySignupInviteMaxCodes:       v.InviteMaxCodes,
		keySignupInviteNeverExpire:    v.InviteNeverExpire,
		keySignupMailLinkLogin:        v.MailLinkLogin,
	}
}

func validateSignupUpdateFields(u SignupUpdate) map[string]string {
	fields := map[string]string{}
	if u.CaptchaProvider != nil && !validCaptchaProvider(*u.CaptchaProvider) {
		fields["captcha_provider"] = "人机验证类型只能是 recaptcha_v2、recaptcha_v3、turnstile 或留空"
	}
	if u.CaptchaSiteKey != nil {
		if len(*u.CaptchaSiteKey) > 200 {
			fields["captcha_site_key"] = "超出上限"
		} else if strings.ContainsFunc(*u.CaptchaSiteKey, unicode.IsControl) {
			fields["captcha_site_key"] = "不能包含控制字符"
		}
	}
	if u.CaptchaSecret != nil {
		if len(*u.CaptchaSecret) > 512 {
			fields["captcha_secret"] = "超出上限"
		} else if strings.ContainsFunc(*u.CaptchaSecret, unicode.IsControl) {
			fields["captcha_secret"] = "不能包含控制字符"
		}
	}
	if u.RecaptchaV3Threshold != nil {
		t := *u.RecaptchaV3Threshold
		if t < 0 || t > 1 {
			fields["recaptcha_v3_threshold"] = "阈值需在 0 到 1 之间"
		}
	}
	if u.EmailSuffixes != nil {
		if _, err := NormalizeEmailSuffixes(*u.EmailSuffixes); err != nil {
			e := errs.As(err)
			if e.Fields != nil {
				for k, v := range e.Fields {
					fields[k] = v
				}
			}
		}
	}
	if u.IPLimitCount != nil {
		if *u.IPLimitCount < 1 || *u.IPLimitCount > SignupMaxIPLimitCount {
			fields["ip_limit_count"] = "注册 IP 限额需在 1 到 1000 之间"
		}
	}
	if u.IPLimitWindowMinutes != nil {
		if *u.IPLimitWindowMinutes < 1 || *u.IPLimitWindowMinutes > SignupMaxIPLimitWindow {
			fields["ip_limit_window_minutes"] = "时间窗口需在 1 到 1440 分钟之间"
		}
	}
	if u.TrialPlanID != nil && *u.TrialPlanID != "" {
		if _, err := uuid.Parse(*u.TrialPlanID); err != nil {
			fields["trial_plan_id"] = "不是合法的套餐 ID"
		}
	}
	if u.TrialHours != nil {
		if *u.TrialHours < 0 || *u.TrialHours > SignupMaxTrialHours {
			fields["trial_hours"] = "试用时长需在 0 到 8760 小时之间"
		}
	}
	if u.InviteMaxCodes != nil {
		if *u.InviteMaxCodes < 1 || *u.InviteMaxCodes > SignupMaxInviteCodes {
			fields["invite_max_codes"] = "邀请码上限需在 1 到 50 之间"
		}
	}
	return fields
}

func mergeSignupUpdate(old Signup, u SignupUpdate) Signup {
	v := old
	if u.CaptchaProvider != nil {
		v.CaptchaProvider = *u.CaptchaProvider
	}
	if u.CaptchaSiteKey != nil {
		v.CaptchaSiteKey = *u.CaptchaSiteKey
	}
	if u.RecaptchaV3Threshold != nil {
		v.RecaptchaV3Threshold = *u.RecaptchaV3Threshold
	}
	if u.EmailSuffixWhitelistEnabled != nil {
		v.EmailSuffixWhitelistEnabled = *u.EmailSuffixWhitelistEnabled
	}
	if u.EmailSuffixes != nil {
		normalized, err := NormalizeEmailSuffixes(*u.EmailSuffixes)
		if err == nil {
			v.EmailSuffixes = normalized
		}
	}
	if u.GmailAliasBlock != nil {
		v.GmailAliasBlock = *u.GmailAliasBlock
	}
	if u.IPLimitEnabled != nil {
		v.IPLimitEnabled = *u.IPLimitEnabled
	}
	if u.IPLimitCount != nil {
		v.IPLimitCount = *u.IPLimitCount
	}
	if u.IPLimitWindowMinutes != nil {
		v.IPLimitWindowMinutes = *u.IPLimitWindowMinutes
	}
	if u.TrialPlanID != nil {
		if *u.TrialPlanID == "" {
			v.TrialPlanID = nil
		} else {
			id, _ := uuid.Parse(*u.TrialPlanID)
			v.TrialPlanID = &id
		}
	}
	if u.TrialHours != nil {
		v.TrialHours = *u.TrialHours
	}
	if u.InviteMaxCodes != nil {
		v.InviteMaxCodes = *u.InviteMaxCodes
	}
	if u.InviteNeverExpire != nil {
		v.InviteNeverExpire = *u.InviteNeverExpire
	}
	if u.MailLinkLogin != nil {
		v.MailLinkLogin = *u.MailLinkLogin
	}
	return v
}

// SetSignup 合并写入注册与登录设置。
func (s *Service) SetSignup(ctx context.Context, u SignupUpdate) error {
	fields := validateSignupUpdateFields(u)
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, signupKeys)
		if err != nil {
			return err
		}
		old, err := decodeSignup(vals, nil)
		if err != nil {
			return err
		}
		v := mergeSignupUpdate(old, u)

		combo := map[string]string{}
		if v.CaptchaProvider != "" {
			if strings.TrimSpace(v.CaptchaSiteKey) == "" {
				combo["captcha_site_key"] = "开启人机验证前请填写站点密钥（site key）"
			}
			hasSecret := old.HasCaptchaSecret
			if u.CaptchaSecret != nil {
				hasSecret = *u.CaptchaSecret != ""
			}
			if !hasSecret {
				combo["captcha_secret"] = "开启人机验证前请填写服务端密钥（secret）"
			}
		}
		if v.EmailSuffixWhitelistEnabled && len(v.EmailSuffixes) == 0 {
			combo["email_suffixes"] = "开启后缀白名单时至少填一个后缀"
		}
		if v.TrialHours > 0 && v.TrialPlanID == nil {
			combo["trial_plan_id"] = "开启注册试用前请选择试用套餐"
		}
		if len(combo) > 0 {
			return errs.Invalid(combo)
		}

		turningOnMailLink := u.MailLinkLogin != nil && *u.MailLinkLogin && !old.MailLinkLogin
		if turningOnMailLink {
			ok, err := s.MailConfigured(ctx)
			if err != nil {
				return err
			}
			if !ok {
				return errs.Invalid(map[string]string{
					"mail_link_login": "开启邮件链接登录前请先在通知渠道里配好发信",
				})
			}
		}

		changes, b, a := diff(signupMap(old), signupMap(v))

		if u.CaptchaSecret != nil {
			if *u.CaptchaSecret == "" {
				if err := s.store.Delete(ctx, keySignupCaptchaSecret); err != nil {
					return err
				}
			} else {
				if s.sealer == nil {
					return errs.Wrap(errs.Internal, errors.New("settings: sealer 未配置"))
				}
				blob, err := s.sealer.Seal([]byte(*u.CaptchaSecret), []byte(keySignupCaptchaSecret))
				if err != nil {
					return errs.Wrap(errs.Internal, err)
				}
				changes = append(changes, change{
					key:   keySignupCaptchaSecret,
					value: base64.StdEncoding.EncodeToString(blob),
				})
			}
		}

		if len(changes) == 0 && u.CaptchaSecret == nil {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		tx.AfterCommit(ctx, "settings:signup_saved", func(context.Context) { s.signupGen.Add(1) })
		return s.writeAudit(ctx, auditSignupChanged, auditResourceSettings, map[string]any{
			"group":          "signup",
			"before":         b,
			"after":          a,
			"secret_changed": u.CaptchaSecret != nil,
		})
	})
}
