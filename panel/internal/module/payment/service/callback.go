package service

import (
	"context"
	"strings"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
)

const webhookPathPrefix = "/v1/webhooks/payments/"

// PaymentSettings 是后台「支付回调」设置。
type PaymentSettings struct {
	NotifyBaseURL string // 存下的回调地址前缀；空 = 用站点地址
	EffectiveBase string // 实际用来拼回调地址的前缀
	PublicBaseURL string // 站点地址（缺省前缀）
}

// usableNotifyBase 报告存下的前缀此刻能不能用：非空、NormalizeNotifyBaseURL 通过、https（cfg.AllowPrivateHosts 时也允许 http）。
func (s *Service) usableNotifyBase(v string) bool {
	norm, ok := settingssvc.NormalizeNotifyBaseURL(v)
	if !ok || norm == "" {
		return false
	}
	if !s.cfg.AllowPrivateHosts && !strings.HasPrefix(strings.ToLower(norm), "https://") {
		return false
	}
	return true
}

// notifyBase 返回拼回调地址用的前缀：存下的前缀能用就用它，否则 PublicBaseURL（存的值不能用时 plog Warn 一条，不打印值）。
func (s *Service) notifyBase(ctx context.Context) (string, error) {
	cb, err := s.settings.PaymentCallback(ctx)
	if err != nil {
		return "", err
	}
	if cb.NotifyBaseURL != "" {
		if s.usableNotifyBase(cb.NotifyBaseURL) {
			norm, _ := settingssvc.NormalizeNotifyBaseURL(cb.NotifyBaseURL)
			return norm, nil
		}
		plog.From(ctx).Warn("支付回调前缀设置无效，回退至站点地址", zap.String("key", "payment.notify_base_url"))
	}
	return s.cfg.PublicBaseURL, nil
}

// notifyURL 拼渠道回调地址：base + webhookPathPrefix + code。
func notifyURL(base, code string) string {
	return strings.TrimRight(base, "/") + webhookPathPrefix + code
}

// notifyURLCandidates 是验签时可接受的回调地址：当前前缀拼的在前，站点地址拼的在后（改过前缀后，改之前建的发票仍按旧地址签名）；去重。
func (s *Service) notifyURLCandidates(ctx context.Context, code string) ([]string, error) {
	curBase, err := s.notifyBase(ctx)
	if err != nil {
		return nil, err
	}
	curURL := notifyURL(curBase, code)
	out := []string{curURL}
	if s.cfg.PublicBaseURL != "" {
		siteURL := notifyURL(s.cfg.PublicBaseURL, code)
		if siteURL != curURL {
			out = append(out, siteURL)
		}
	}
	return out, nil
}

// PaymentSettings 读后台「支付回调」设置。
func (s *Service) PaymentSettings(ctx context.Context) (PaymentSettings, error) {
	cb, err := s.settings.PaymentCallback(ctx)
	if err != nil {
		return PaymentSettings{}, err
	}
	effective, err := s.notifyBase(ctx)
	if err != nil {
		return PaymentSettings{}, err
	}
	return PaymentSettings{
		NotifyBaseURL: cb.NotifyBaseURL,
		EffectiveBase: effective,
		PublicBaseURL: s.cfg.PublicBaseURL,
	}, nil
}

// SetPaymentSettings 写回调地址前缀（空串 = 清除，改回用站点地址）。生产只收 https；开发环境（cfg.AllowPrivateHosts）也收 http。审计由 settings 写。
func (s *Service) SetPaymentSettings(ctx context.Context, notifyBaseURL string) error {
	v, ok := settingssvc.NormalizeNotifyBaseURL(notifyBaseURL)
	if !ok {
		return errs.Invalid(map[string]string{
			"notify_base_url": "请填写完整的地址，如 https://pay-cb.example.com，不带查询串",
		})
	}
	if v != "" && !strings.HasPrefix(strings.ToLower(v), "https://") && !s.cfg.AllowPrivateHosts {
		return errs.Invalid(map[string]string{
			"notify_base_url": "回调地址必须使用 https",
		})
	}
	return s.settings.SetPaymentCallback(ctx, v)
}
