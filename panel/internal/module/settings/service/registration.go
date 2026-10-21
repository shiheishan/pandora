package service

import (
	"context"
	"fmt"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// Registration 读取注册相关设置。
func (s *Service) Registration(ctx context.Context) (Registration, error) {
	keys := []string{keyRegistrationMode, keyEmailVerification}
	vals, err := s.store.Values(ctx, keys)
	if err != nil {
		return Registration{}, err
	}
	mode, err := decodeOr(vals, keyRegistrationMode, defaultRegistrationMode)
	if err != nil {
		return Registration{}, err
	}
	if !validRegistrationMode(mode) {
		// 库里被手改成不认识的值：报内部错误，不让注册流程按未知模式往下走
		return Registration{}, fmt.Errorf("设置 %s 的值 %q 不是合法的注册模式", keyRegistrationMode, mode)
	}
	verify, err := decodeOr(vals, keyEmailVerification, defaultEmailVerification)
	if err != nil {
		return Registration{}, err
	}
	return Registration{Mode: mode, EmailVerification: verify}, nil
}

// SetRegistration 写入注册相关设置。
func (s *Service) SetRegistration(ctx context.Context, v Registration) error {
	if !validRegistrationMode(v.Mode) {
		return errs.Invalid(map[string]string{
			"registration_mode": "注册模式只能是 closed / invite_only / open",
		})
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		keys := []string{keyRegistrationMode, keyEmailVerification}
		vals, err := s.store.LockValues(ctx, keys)
		if err != nil {
			return err
		}
		oldMode, err := decodeOr(vals, keyRegistrationMode, defaultRegistrationMode)
		if err != nil {
			return err
		}
		oldVerify, err := decodeOr(vals, keyEmailVerification, defaultEmailVerification)
		if err != nil {
			return err
		}
		if v.EmailVerification {
			ok, err := s.MailConfigured(ctx)
			if err != nil {
				return err
			}
			if !ok {
				return errs.Invalid(map[string]string{
					"email_verification": "开启邮箱验证前请先填好 SMTP 服务器与发件人地址",
				})
			}
		}
		before := map[string]any{
			keyRegistrationMode:  oldMode,
			keyEmailVerification: oldVerify,
		}
		after := map[string]any{
			keyRegistrationMode:  v.Mode,
			keyEmailVerification: v.EmailVerification,
		}
		changes, b, a := diff(before, after)
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		if err := s.writeAudit(ctx, auditRegistrationChanged, auditResourceSettings, map[string]any{
			"group":  "registration",
			"before": b,
			"after":  a,
		}); err != nil {
			return err
		}
		// 门户首页按注册方式出「注册」「购买」入口
		s.homeChanged(ctx)
		return nil
	})
}

// RegistrationPolicy 返回当前实际生效的注册策略（不走开关缓存，后台改完立刻生效）。
// auth.registration 开关缺行视为关闭，与一般开关相反，是注册的紧急总闸。
func (s *Service) RegistrationPolicy(ctx context.Context) (Registration, error) {
	sw, ok, err := s.store.GetSwitch(ctx, SwitchAuthRegistration)
	if err != nil {
		return Registration{}, err
	}
	reg, err := s.Registration(ctx)
	if err != nil {
		return Registration{}, err
	}
	if !ok || !sw.Enabled {
		reg.Mode = RegistrationClosed
	}
	return reg, nil
}

func validRegistrationMode(m string) bool {
	switch m {
	case RegistrationClosed, RegistrationInviteOnly, RegistrationOpen:
		return true
	default:
		return false
	}
}
