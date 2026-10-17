package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"unicode"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
)

// 支付这一组的键。
const (
	keyPaymentNotifyBaseURL = "payment.notify_base_url" // string：回调地址前缀，空 = 用站点地址
	auditPaymentChanged     = "settings.payment_updated"
)

// PaymentCallbackMaxLen 是回调地址前缀的长度上限。
const PaymentCallbackMaxLen = 200

var paymentKeys = []string{keyPaymentNotifyBaseURL}

// PaymentCallback 是支付回调的设置。
type PaymentCallback struct {
	// NotifyBaseURL 是回调地址前缀（scheme + host[:port]，可带路径前缀，不带末尾 /）；空 = 用站点地址。
	NotifyBaseURL string
}

// PaymentCallback 读取支付回调设置。
func (s *Service) PaymentCallback(ctx context.Context) (PaymentCallback, error) {
	vals, err := s.store.Values(ctx, paymentKeys)
	if err != nil {
		return PaymentCallback{}, err
	}
	return decodePaymentCallback(ctx, vals), nil
}

// SetPaymentCallback 写入回调地址前缀；空串表示清除（回退站点地址）。
func (s *Service) SetPaymentCallback(ctx context.Context, notifyBaseURL string) error {
	norm, ok := NormalizeNotifyBaseURL(notifyBaseURL)
	if !ok {
		return errs.Invalid(map[string]string{
			"notify_base_url": "请填写完整的地址，如 https://pay-cb.example.com，不带查询串",
		})
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, paymentKeys)
		if err != nil {
			return err
		}
		old := decodePaymentCallback(ctx, vals)
		newVal := PaymentCallback{NotifyBaseURL: norm}
		changes, b, a := diff(paymentMap(old), paymentMap(newVal))
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditPaymentChanged, auditResourceSettings, map[string]any{
			"group":  "payment",
			"before": b,
			"after":  a,
		})
	})
}

// NormalizeNotifyBaseURL 校验并规整回调地址前缀：去首尾空白与末尾的 /；空串原样返回空串。
// 合法：url.Parse 成功、scheme 为 http 或 https、Host 非空、没有 User、RawQuery、Fragment（也不以 ? 或 # 结尾）、
// 不含控制字符与空白、长度不超过 PaymentCallbackMaxLen。不合法返回 ok=false。
func NormalizeNotifyBaseURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimRight(s, "/")
	if s == "" {
		return "", true
	}
	if len(s) > PaymentCallbackMaxLen {
		return "", false
	}
	if strings.Contains(s, "?") || strings.Contains(s, "#") {
		return "", false
	}
	for _, r := range s {
		if r < 32 || r == 127 || unicode.IsSpace(r) {
			return "", false
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", false
	}
	if u.User != nil {
		return "", false
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if u.Host == "" {
		return "", false
	}
	// url.Parse 已把 scheme 转成小写；存规整后的写法，payment 判断 https 时不用再管大小写
	return u.Scheme + s[len(u.Scheme):], true
}

func decodePaymentCallback(ctx context.Context, vals map[string]json.RawMessage) PaymentCallback {
	raw, err := decodeOr(vals, keyPaymentNotifyBaseURL, "")
	if err != nil {
		plog.From(ctx).Warn("支付回调设置的值非法，按空串", zap.String("key", keyPaymentNotifyBaseURL))
		return PaymentCallback{}
	}
	norm, ok := NormalizeNotifyBaseURL(raw)
	if !ok {
		plog.From(ctx).Warn("支付回调设置的值非法，按空串", zap.String("key", keyPaymentNotifyBaseURL))
		return PaymentCallback{}
	}
	return PaymentCallback{NotifyBaseURL: norm}
}

func paymentMap(p PaymentCallback) map[string]any {
	return map[string]any{
		keyPaymentNotifyBaseURL: p.NotifyBaseURL,
	}
}
