package app

import (
	"context"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/webapp"
	"go.uber.org/zap"
)

// signupReader 是门户入口页 CSP 要的设置读取面；*settingssvc.Service 直接满足。
type signupReader interface {
	Signup(ctx context.Context) (settingssvc.Signup, error)
}

// portalCaptchaCSP 按注册设置给门户入口页追加人机验证的脚本与 iframe 来源：
// 没开人机验证时什么都不加（CSP 与原来逐字一致）；开了只放行所选那一家的固定来源。
// 入口页每次回源校验，所以每次现取，后台一改设置，下次打开门户就生效。
// 读设置失败时不放行任何第三方来源（人机验证加载不了，注册会停在人机验证这一步，而不是放宽 CSP）。
func portalCaptchaCSP(settings signupReader) func(ctx context.Context) webapp.ExtraSources {
	return func(ctx context.Context) webapp.ExtraSources {
		s, err := settings.Signup(ctx)
		if err != nil {
			plog.From(ctx).Warn("读取注册设置失败，门户入口页不放行人机验证来源", zap.Error(err))
			return webapp.ExtraSources{}
		}
		return captchaSources(s.CaptchaProvider)
	}
}

// captchaSources 是各家人机验证要的固定来源。
func captchaSources(provider string) webapp.ExtraSources {
	switch provider {
	case settingssvc.CaptchaRecaptchaV2, settingssvc.CaptchaRecaptchaV3:
		// reCAPTCHA：脚本从 www.google.com 加载、再拉 www.gstatic.com 的资源；挑战框是 www.google.com 的 iframe
		return webapp.ExtraSources{
			Script: []string{"https://www.google.com", "https://www.gstatic.com"},
			Frame:  []string{"https://www.google.com"},
		}
	case settingssvc.CaptchaTurnstile:
		return webapp.ExtraSources{
			Script: []string{"https://challenges.cloudflare.com"},
			Frame:  []string{"https://challenges.cloudflare.com"},
		}
	default:
		return webapp.ExtraSources{}
	}
}
