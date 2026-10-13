package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

const (
	keyACMEContactEmail   = "acme.contact_email"   // string，空或邮箱
	keyACMEUseStaging     = "acme.use_staging"     // bool
	keyACMEZeroSSLEnabled = "acme.zerossl_enabled" // bool
	keyACMEZeroSSLEABKID  = "acme.zerossl_eab_kid" // string
	keyACMEZeroSSLEABHMAC = "acme.zerossl_eab_hmac" //nolint:gosec // 设置键名；值经 Sealer 封存（AAD 为键名）后 base64 存
	auditACMEChanged      = "acme_settings.updated"
)

var acmeKeys = []string{
	keyACMEContactEmail,
	keyACMEUseStaging,
	keyACMEZeroSSLEABHMAC,
	keyACMEZeroSSLEABKID,
	keyACMEZeroSSLEnabled,
}

// ACME 是 ACME 签发设置；EAB HMAC 只说配没配，明文经 ACMEEABHMAC 取。
type ACME struct {
	ContactEmail      string
	UseStaging        bool
	ZeroSSLEnabled    bool
	ZeroSSLEABKID     string
	ZeroSSLEABHMACSet bool
}

// ACMEUpdate 是整体保存。ZeroSSLEABHMAC：nil 保留原值，指向空串表示清除，其余为新值。
type ACMEUpdate struct {
	ContactEmail   string
	UseStaging     bool
	ZeroSSLEnabled bool
	ZeroSSLEABKID  string
	ZeroSSLEABHMAC *string
}

// ACME 读取 ACME 签发设置（不解封 EAB HMAC）。
func (s *Service) ACME(ctx context.Context) (ACME, error) {
	vals, err := s.store.Values(ctx, acmeKeys)
	if err != nil {
		return ACME{}, err
	}
	return decodeACME(vals)
}

// ACMEEABHMAC 返回已保存的 ZeroSSL EAB HMAC 明文；没配回 ""；解封失败或 sealer 未配置回内部错误。
func (s *Service) ACMEEABHMAC(ctx context.Context) (string, error) {
	vals, err := s.store.Values(ctx, []string{keyACMEZeroSSLEABHMAC})
	if err != nil {
		return "", err
	}
	enc, err := decodeOr(vals, keyACMEZeroSSLEABHMAC, "")
	if err != nil {
		return "", err
	}
	if enc == "" {
		return "", nil
	}
	if s.sealer == nil {
		return "", errs.Wrap(errs.Internal, errors.New("settings: sealer 未配置"))
	}
	blob, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", errs.Wrap(errs.Internal, err)
	}
	plain, err := s.sealer.Open(blob, []byte(keyACMEZeroSSLEABHMAC))
	if err != nil {
		return "", errs.Wrap(errs.Internal, err)
	}
	return string(plain), nil
}

// SetACME 整体保存 ACME 签发设置；有改动才写审计（审计里不含 HMAC 明文）。
func (s *Service) SetACME(ctx context.Context, u ACMEUpdate) (ACME, error) {
	u.ContactEmail = strings.TrimSpace(u.ContactEmail)
	u.ZeroSSLEABKID = strings.TrimSpace(u.ZeroSSLEABKID)

	fields := map[string]string{}
	if u.ContactEmail != "" {
		addr, err := mail.ParseAddress(u.ContactEmail)
		if err != nil || addr.Address != u.ContactEmail || len(u.ContactEmail) > 254 {
			fields["contact_email"] = "邮箱格式不对"
		}
	}
	if len(u.ZeroSSLEABKID) > 256 {
		fields["zerossl_eab_kid"] = "太长"
	}

	var newHMAC string
	if u.ZeroSSLEABHMAC != nil {
		newHMAC = strings.TrimSpace(*u.ZeroSSLEABHMAC)
		if newHMAC != "" {
			if _, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(newHMAC, "=")); err != nil || len(newHMAC) > 512 {
				fields["zerossl_eab_hmac"] = "EAB HMAC 应是 ZeroSSL 后台给的 base64url 字符串"
			}
		}
	}

	if strings.Contains(u.ContactEmail, "\x00") {
		fields["contact_email"] = "不能包含空字符"
	}
	if strings.Contains(u.ZeroSSLEABKID, "\x00") {
		fields["zerossl_eab_kid"] = "不能包含空字符"
	}
	if u.ZeroSSLEABHMAC != nil && strings.Contains(newHMAC, "\x00") {
		fields["zerossl_eab_hmac"] = "不能包含空字符"
	}

	if len(fields) > 0 {
		return ACME{}, errs.Invalid(fields)
	}

	var out ACME
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, acmeKeys)
		if err != nil {
			return err
		}
		old, err := decodeACME(vals)
		if err != nil {
			return err
		}

		hmacSet := old.ZeroSSLEABHMACSet
		if u.ZeroSSLEABHMAC != nil {
			hmacSet = newHMAC != ""
		}
		if u.ZeroSSLEnabled && (u.ZeroSSLEABKID == "" || !hmacSet) {
			bad := map[string]string{}
			if u.ZeroSSLEABKID == "" {
				bad["zerossl_eab_kid"] = "启用 ZeroSSL 要填 EAB KID"
			}
			if !hmacSet {
				bad["zerossl_eab_hmac"] = "启用 ZeroSSL 要填 EAB HMAC"
			}
			return errs.Invalid(bad)
		}

		after := ACME{
			ContactEmail:      u.ContactEmail,
			UseStaging:        u.UseStaging,
			ZeroSSLEnabled:    u.ZeroSSLEnabled,
			ZeroSSLEABKID:     u.ZeroSSLEABKID,
			ZeroSSLEABHMACSet: hmacSet,
		}
		changes, b, a := diff(acmeMap(old), acmeMap(after))

		// 清空一个本来就没配的 HMAC 不算改动
		hmacChanged := u.ZeroSSLEABHMAC != nil && (newHMAC != "" || old.ZeroSSLEABHMACSet)
		if hmacChanged {
			if newHMAC == "" {
				if err := s.store.Delete(ctx, keyACMEZeroSSLEABHMAC); err != nil {
					return err
				}
			} else {
				if s.sealer == nil {
					return errs.Wrap(errs.Internal, errors.New("settings: sealer 未配置"))
				}
				blob, err := s.sealer.Seal([]byte(newHMAC), []byte(keyACMEZeroSSLEABHMAC))
				if err != nil {
					return errs.Wrap(errs.Internal, err)
				}
				changes = append(changes, change{
					key:   keyACMEZeroSSLEABHMAC,
					value: base64.StdEncoding.EncodeToString(blob),
				})
			}
		}

		if len(changes) == 0 && !hmacChanged {
			out = old
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		if err := s.writeAudit(ctx, auditACMEChanged, auditResourceSettings, map[string]any{
			"group":                    "acme",
			"before":                   b,
			"after":                    a,
			"zerossl_eab_hmac_changed": hmacChanged,
		}); err != nil {
			return err
		}
		out = after
		return nil
	})
	if err != nil {
		return ACME{}, err
	}
	return out, nil
}

func decodeACME(vals map[string]json.RawMessage) (ACME, error) {
	contact, err := decodeOr(vals, keyACMEContactEmail, "")
	if err != nil {
		return ACME{}, err
	}
	staging, err := decodeOr(vals, keyACMEUseStaging, false)
	if err != nil {
		return ACME{}, err
	}
	enabled, err := decodeOr(vals, keyACMEZeroSSLEnabled, false)
	if err != nil {
		return ACME{}, err
	}
	kid, err := decodeOr(vals, keyACMEZeroSSLEABKID, "")
	if err != nil {
		return ACME{}, err
	}
	hmacEnc, err := decodeOr(vals, keyACMEZeroSSLEABHMAC, "")
	if err != nil {
		return ACME{}, err
	}
	return ACME{
		ContactEmail:      contact,
		UseStaging:        staging,
		ZeroSSLEnabled:    enabled,
		ZeroSSLEABKID:     kid,
		ZeroSSLEABHMACSet: hmacEnc != "",
	}, nil
}

func acmeMap(a ACME) map[string]any {
	return map[string]any{
		keyACMEContactEmail:   a.ContactEmail,
		keyACMEUseStaging:     a.UseStaging,
		keyACMEZeroSSLEnabled: a.ZeroSSLEnabled,
		keyACMEZeroSSLEABKID:  a.ZeroSSLEABKID,
	}
}
