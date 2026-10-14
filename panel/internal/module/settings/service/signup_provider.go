package service

import (
	"context"
	"fmt"
)

// SignupCaptchaProvider 只读当前的人机验证服务商（没开为空串）：不读 secret、不解封，给门户入口页 CSP 这种热路径用。
func (s *Service) SignupCaptchaProvider(ctx context.Context) (string, error) {
	vals, err := s.store.Values(ctx, []string{keySignupCaptchaProvider})
	if err != nil {
		return "", err
	}
	provider, err := decodeOr(vals, keySignupCaptchaProvider, "")
	if err != nil {
		return "", err
	}
	if !validCaptchaProvider(provider) {
		return "", fmt.Errorf("设置 %s 的值 %q 不是合法的人机验证类型", keySignupCaptchaProvider, provider)
	}
	return provider, nil
}

// SignupGeneration 是本进程保存注册设置的次数：SetSignup 提交后加一（没有改动不加）。
// 门户入口页 CSP 的缓存按它失效，后台改了人机验证，新开的门户页立刻拿到新来源。
func (s *Service) SignupGeneration() uint64 { return s.signupGen.Load() }

// SignupWithoutSecret 读注册与登录设置，但不解封人机验证 secret（CaptchaSecret 为空，HasCaptchaSecret 照常）：
// 后台设置页与公开的 signup-config 只要知道「有没有 secret」，不必每次都做一次 AEAD 解密。要真校验令牌的地方用 Signup。
func (s *Service) SignupWithoutSecret(ctx context.Context) (Signup, error) {
	vals, err := s.store.Values(ctx, signupKeys)
	if err != nil {
		return Signup{}, err
	}
	return decodeSignup(vals, nil)
}
