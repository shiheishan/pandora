package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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

type captchaVerifier struct {
	client       *http.Client
	recaptchaURL string
	turnstileURL string
}

func newCaptchaVerifier() *captchaVerifier {
	return &captchaVerifier{
		client:       &http.Client{Timeout: captchaTimeout},
		recaptchaURL: recaptchaVerifyURL,
		turnstileURL: turnstileVerifyURL,
	}
}

// verify 按设置校验人机验证令牌；没开直接通过。
func (v *captchaVerifier) verify(ctx context.Context, cfg settingssvc.Signup, token string) error {
	if cfg.CaptchaProvider == "" {
		return nil
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
		Success bool    `json:"success"`
		Score   float64 `json:"score"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		plog.From(ctx).Warn("人机验证响应解析失败", zap.String("provider", cfg.CaptchaProvider), zap.Error(err))
		return errCaptchaUnavailable()
	}
	if !parsed.Success {
		return errCaptchaFailed()
	}
	if cfg.CaptchaProvider == settingssvc.CaptchaRecaptchaV3 && parsed.Score < cfg.RecaptchaV3Threshold {
		return errCaptchaFailed()
	}
	return nil
}

func errCaptchaFailed() error {
	return errs.Invalid(map[string]string{"captcha": "人机验证未通过，请重试"})
}

func errCaptchaUnavailable() error {
	return errs.Invalid(map[string]string{"captcha": "人机验证服务暂时连不上，请稍后再试"})
}
