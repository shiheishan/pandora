package app

import (
	"context"
	"strconv"
	"sync"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/webapp"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// captchaProviderReader 是门户入口页 CSP 要的设置读取面；*settingssvc.Service 直接满足（只读服务商，不读 secret、不解封）。
// SignupGeneration 是本进程保存注册设置的次数，保存成功提交后加一：缓存按它失效，后台改完新开的门户页立刻生效。
type captchaProviderReader interface {
	SignupCaptchaProvider(ctx context.Context) (string, error)
	SignupGeneration() uint64
}

// 入口页 CSP 读设置的进程内缓存：入口页是门户最热的地址，不能每次打开都多一次查库。
//   - 成功缓存 30 秒；本进程保存了注册设置就立刻失效（serve 是单进程，后台保存也走它），30 秒只是兜底；
//   - 读失败也缓存 5 秒（库慢或挂了时不让每个入口页请求都去等一次超时）；
//   - 同一时刻只有一趟读库（singleflight），读库不持锁、不跟随第一个调用者的取消，上限 2 秒。
const (
	portalCSPCacheTTL    = 30 * time.Second
	portalCSPErrorTTL    = 5 * time.Second
	portalCSPReadTimeout = 2 * time.Second
)

// providerCache 缓存人机验证服务商（含读失败），按 gen 失效。
type providerCache struct {
	read   func(ctx context.Context) (string, error)
	gen    func() uint64
	now    func() time.Time
	ttl    time.Duration
	errTTL time.Duration
	group  singleflight.Group

	mu       sync.Mutex
	value    string
	err      error
	cacheGen uint64
	expireAt time.Time
}

type providerResult struct {
	value string
	err   error
}

func (c *providerCache) get(ctx context.Context) (string, error) {
	g := c.gen()
	c.mu.Lock()
	if c.cacheGen == g && c.now().Before(c.expireAt) {
		v, err := c.value, c.err
		c.mu.Unlock()
		return v, err
	}
	c.mu.Unlock()

	// 键带上 gen：保存之后来的调用不会挂到保存前开始的那趟读库上拿旧值
	r, _, _ := c.group.Do(strconv.FormatUint(g, 10), func() (any, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), portalCSPReadTimeout)
		defer cancel()
		v, err := c.read(rctx)
		ttl := c.ttl
		if err != nil {
			ttl = c.errTTL
		}
		c.mu.Lock()
		if c.gen() == g {
			c.value, c.err, c.cacheGen, c.expireAt = v, err, g, c.now().Add(ttl)
		}
		c.mu.Unlock()
		return providerResult{value: v, err: err}, nil
	})
	res := r.(providerResult)
	return res.value, res.err
}

// portalCaptchaCSP 按注册设置给门户入口页追加人机验证的脚本与 iframe 来源：
// 没开人机验证时什么都不加（CSP 与原来逐字一致）；开了只放行所选那一家的固定来源。
// 读设置失败时不放行任何第三方来源（人机验证加载不了，注册会停在人机验证这一步，而不是放宽 CSP）。
func portalCaptchaCSP(settings captchaProviderReader) func(ctx context.Context) webapp.ExtraSources {
	return portalCaptchaCSPWith(&providerCache{
		read: settings.SignupCaptchaProvider, gen: settings.SignupGeneration, now: time.Now,
		ttl: portalCSPCacheTTL, errTTL: portalCSPErrorTTL,
	})
}

func portalCaptchaCSPWith(c *providerCache) func(ctx context.Context) webapp.ExtraSources {
	return func(ctx context.Context) webapp.ExtraSources {
		provider, err := c.get(ctx)
		if err != nil {
			plog.From(ctx).Warn("读取注册设置失败，门户入口页不放行人机验证来源", zap.Error(err))
			return webapp.ExtraSources{}
		}
		return captchaSources(provider)
	}
}

// captchaSources 是各家人机验证要的固定来源，取值照官方文档（2026-10-14 查）：
//   - reCAPTCHA：https://developers.google.com/recaptcha/docs/faq 的 CSP 一节——script-src 只放
//     www.google.com/recaptcha/ 与 www.gstatic.com/recaptcha/，frame-src 放 www.google.com/recaptcha/ 与
//     recaptcha.google.com/recaptcha/。收窄到路径：整个 www.google.com 上有能回调任意函数名的 JSONP 端点，
//     放行整个域名等于给 HTML 注入留了执行脚本的口子。文档没有把 recaptcha.net 列进 CSP（只在国内访问一节出现），
//     前端也只从 www.google.com 加载，所以不加。
//   - Turnstile：https://developers.cloudflare.com/turnstile/reference/content-security-policy/——script-src 与
//     frame-src 都是 https://challenges.cloudflare.com（文档只给到主机）。不收窄到 /turnstile/：部件的 iframe
//     从 /cdn-cgi/challenge-platform/ 下发，frame-src 限在 /turnstile/ 会把部件挡掉；这个主机只做人机验证，
//     没有 JSONP 一类的通用端点。
func captchaSources(provider string) webapp.ExtraSources {
	switch provider {
	case settingssvc.CaptchaRecaptchaV2, settingssvc.CaptchaRecaptchaV3:
		return webapp.ExtraSources{
			Script: []string{"https://www.google.com/recaptcha/", "https://www.gstatic.com/recaptcha/"},
			Frame:  []string{"https://www.google.com/recaptcha/", "https://recaptcha.google.com/recaptcha/"},
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
