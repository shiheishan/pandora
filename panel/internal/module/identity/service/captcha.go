package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
)

// 人机验证服务端校验（Xboard CaptchaService 的对照）：标准库 http.Client，显式 10 秒超时，只发 HTTPS，响应限长读，
// 不发用户 IP（remoteip 可选，不发更少泄露），日志不打 secret 与 token。
const (
	recaptchaVerifyURL = "https://www.google.com/recaptcha/api/siteverify"
	turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	captchaTimeout     = 10 * time.Second
	captchaMaxBody     = 64 << 10
)

// 人机验证的 action：前端 getToken(action) 传同一个值（src/portal/AuthPage.tsx、PasswordReset.tsx、MailLinkLogin.tsx）；
// reCAPTCHA v3 的令牌带回 action，服务端要求与调用点一致，别的页面、别的动作拿到的令牌不能挪来用。
const (
	captchaActionRegister      = "register"
	captchaActionPasswordReset = "password_reset"
	captchaActionMailLogin     = "mail_login"
)

type captchaVerifier struct {
	client       *http.Client
	recaptchaURL string
	turnstileURL string
	// siteURLs 取本站认的全部根地址（settings 的 SiteURLAliases：当前主域名、改之前的那个、配置的 public_base_url）：
	// 令牌必须是在其中某个站点上解出来的。改主域名后旧域名上打开的页面拿到的令牌主机名是旧域名，也是本站
	siteURLs func(ctx context.Context) []string
}

func newCaptchaVerifier(siteURLs func(ctx context.Context) []string) *captchaVerifier {
	return &captchaVerifier{
		client:       &http.Client{Timeout: captchaTimeout},
		recaptchaURL: recaptchaVerifyURL,
		turnstileURL: turnstileVerifyURL,
		siteURLs:     siteURLs,
	}
}

// expectedHosts 是令牌可以来自的主机名（小写、去掉末尾的点、去重）；取不到时为空。
func (v *captchaVerifier) expectedHosts(ctx context.Context) []string {
	if v.siteURLs == nil {
		return nil
	}
	var out []string
	for _, u := range v.siteURLs(ctx) {
		if h := captchaHost(u); h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// captchaHost 取地址的主机名（小写、去掉末尾的点）；解析不了返回空串。
func captchaHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// verify 按设置校验人机验证令牌；没开直接通过。action 是调用点的动作（captchaAction*），v3 要求令牌里的 action 与它一致。
// 服务商回的 hostname 必须是本站某个根地址的主机名（Google 与 Cloudflare 的文档都要求服务端比对）；
// 对外地址没配好（取不到主机名）时一律按服务不可用处理，不放行。
func (v *captchaVerifier) verify(ctx context.Context, cfg settingssvc.Signup, token, action string) error {
	if cfg.CaptchaProvider == "" {
		return nil
	}
	hosts := v.expectedHosts(ctx)
	if len(hosts) == 0 {
		plog.From(ctx).Warn("人机验证：站点地址取不到主机名，无法核对令牌来源")
		return errCaptchaUnavailable()
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 4096 {
		return errCaptchaFailed()
	}
	if cfg.CaptchaSecret == "" {
		plog.From(ctx).Warn("人机验证密钥不可用")
		return errCaptchaUnavailable()
	}

	verifyURL := ""
	switch cfg.CaptchaProvider {
	case settingssvc.CaptchaRecaptchaV2, settingssvc.CaptchaRecaptchaV3:
		verifyURL = v.recaptchaURL
	case settingssvc.CaptchaTurnstile:
		verifyURL = v.turnstileURL
	default:
		return errCaptchaUnavailable()
	}

	form := url.Values{
		"secret":   {cfg.CaptchaSecret},
		"response": {token},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		plog.From(ctx).Warn("人机验证请求构造失败", zap.String("provider", cfg.CaptchaProvider), zap.Error(err))
		return errCaptchaUnavailable()
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.client.Do(req)
	if err != nil {
		plog.From(ctx).Warn("人机验证请求失败", zap.String("provider", cfg.CaptchaProvider), zap.Error(err))
		return errCaptchaUnavailable()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		plog.From(ctx).Warn("人机验证响应状态异常", zap.String("provider", cfg.CaptchaProvider), zap.Int("status", resp.StatusCode))
		return errCaptchaUnavailable()
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, captchaMaxBody))
	if err != nil {
		plog.From(ctx).Warn("人机验证响应读取失败", zap.String("provider", cfg.CaptchaProvider), zap.Error(err))
		return errCaptchaUnavailable()
	}

	var parsed struct {
		Success  bool    `json:"success"`
		Score    float64 `json:"score"`
		Hostname string  `json:"hostname"`
		Action   string  `json:"action"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		plog.From(ctx).Warn("人机验证响应解析失败", zap.String("provider", cfg.CaptchaProvider), zap.Error(err))
		return errCaptchaUnavailable()
	}
	if !parsed.Success {
		return errCaptchaFailed()
	}
	if !slices.Contains(hosts, strings.TrimSuffix(strings.ToLower(parsed.Hostname), ".")) {
		// 只记服务商回的主机名（不是密钥或令牌），方便排查站点地址配错
		plog.From(ctx).Warn("人机验证令牌来源不符", zap.String("provider", cfg.CaptchaProvider), zap.String("hostname", parsed.Hostname))
		return errCaptchaFailed()
	}
	if cfg.CaptchaProvider == settingssvc.CaptchaRecaptchaV3 {
		if parsed.Action != action {
			return errCaptchaFailed()
		}
		if parsed.Score < cfg.RecaptchaV3Threshold {
			return errCaptchaFailed()
		}
	}
	return nil
}

// VerifyCaptcha 按注册与登录设置校验人机验证令牌（没开直接通过）。给 auth 的邮件链接登录第 1 步用（action 为 mail_login），
// 与注册、找回同一套校验与文案。
func (s *Service) VerifyCaptcha(ctx context.Context, token, action string) error {
	cfg, err := s.settings.Signup(ctx)
	if err != nil {
		return err
	}
	return s.captcha.verify(ctx, cfg, token, action)
}

// CaptchaActionMailLogin 是邮件链接登录第 1 步的人机验证 action（auth 调 VerifyCaptcha 时传它）。
const CaptchaActionMailLogin = captchaActionMailLogin

func errCaptchaFailed() error {
	return errs.Invalid(map[string]string{"captcha": "人机验证未通过，请重试"})
}

func errCaptchaUnavailable() error {
	return errs.Invalid(map[string]string{"captcha": "人机验证服务暂时连不上，请稍后再试"})
}
