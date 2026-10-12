package service

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	netmail "net/mail"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
)

var mailKeys = []string{
	keyMailHost, keyMailPort, keyMailEncryption, keyMailUsername,
	keyMailPassword, keyMailFromAddress, keyMailFromName,
}

// Mail 读取 SMTP 发信设置（含解封后的口令，仅用于发信）。
func (s *Service) Mail(ctx context.Context) (Mail, error) {
	vals, err := s.store.Values(ctx, mailKeys)
	if err != nil {
		return Mail{}, err
	}
	host, err := decodeOr(vals, keyMailHost, "")
	if err != nil {
		return Mail{}, err
	}
	port, err := decodeOr(vals, keyMailPort, defaultMailPort)
	if err != nil {
		return Mail{}, err
	}
	encMode, err := decodeOr(vals, keyMailEncryption, defaultMailEncryption)
	if err != nil {
		return Mail{}, err
	}
	username, err := decodeOr(vals, keyMailUsername, "")
	if err != nil {
		return Mail{}, err
	}
	fromAddr, err := decodeOr(vals, keyMailFromAddress, "")
	if err != nil {
		return Mail{}, err
	}
	fromName, err := decodeOr(vals, keyMailFromName, "")
	if err != nil {
		return Mail{}, err
	}
	fromName = strings.TrimSpace(fromName)

	m := Mail{
		Host:        host,
		Port:        port,
		Encryption:  encMode,
		Username:    username,
		FromAddress: fromAddr,
		FromName:    fromName,
	}
	enc, err := decodeOr(vals, keyMailPassword, "")
	if err != nil {
		return Mail{}, err
	}
	if enc != "" {
		m.HasPassword = true
		m.Password = s.openPassword(ctx, enc)
	}
	return m, nil
}

// MailConfigured 判断发信配置是否齐全（不解封口令，供门户高频调用）。
func (s *Service) MailConfigured(ctx context.Context) (bool, error) {
	keys := []string{keyMailHost, keyMailPort, keyMailFromAddress}
	vals, err := s.store.Values(ctx, keys)
	if err != nil {
		return false, err
	}
	host, err := decodeOr(vals, keyMailHost, "")
	if err != nil {
		return false, err
	}
	port, err := decodeOr(vals, keyMailPort, defaultMailPort)
	if err != nil {
		return false, err
	}
	fromAddr, err := decodeOr(vals, keyMailFromAddress, "")
	if err != nil {
		return false, err
	}
	return Mail{Host: host, Port: port, FromAddress: fromAddr}.Configured(), nil
}

// SetMail 写入 SMTP 发信设置。
func (s *Service) SetMail(ctx context.Context, v MailUpdate) error {
	v.Host = strings.TrimSpace(v.Host)
	v.Username = strings.TrimSpace(v.Username)
	v.FromAddress = strings.TrimSpace(v.FromAddress)
	v.FromName = strings.TrimSpace(v.FromName)

	fields := map[string]string{}
	if len(v.Host) > 255 {
		fields["smtp_host"] = "超出上限"
	}
	if v.Port < 1 || v.Port > 65535 {
		fields["smtp_port"] = "端口需在 1 到 65535 之间"
	}
	switch v.Encryption {
	case MailEncryptionSSL, MailEncryptionTLS, MailEncryptionNone:
	default:
		fields["encryption"] = "加密方式只能是 ssl / tls / none"
	}
	if len(v.Username) > 255 {
		fields["smtp_username"] = "超出上限"
	}
	if v.Password != nil && len(*v.Password) > 512 {
		fields["smtp_password"] = "超出上限"
	}
	if v.FromAddress != "" {
		addr, err := netmail.ParseAddress(v.FromAddress)
		if err != nil || addr.Address != v.FromAddress || len(v.FromAddress) > 254 {
			fields["from_address"] = "邮箱格式不正确"
		}
	}
	if utf8.RuneCountInString(v.FromName) > 64 {
		fields["from_name"] = "最多 64 个字"
	} else if strings.ContainsFunc(v.FromName, unicode.IsControl) {
		// 发件人名进邮件头，带换行等控制字符会让所有邮件组装失败
		fields["from_name"] = "不能包含换行等控制字符"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, mailKeys)
		if err != nil {
			return err
		}
		oldHost, err := decodeOr(vals, keyMailHost, "")
		if err != nil {
			return err
		}
		oldPort, err := decodeOr(vals, keyMailPort, defaultMailPort)
		if err != nil {
			return err
		}
		oldEnc, err := decodeOr(vals, keyMailEncryption, defaultMailEncryption)
		if err != nil {
			return err
		}
		oldUser, err := decodeOr(vals, keyMailUsername, "")
		if err != nil {
			return err
		}
		oldFrom, err := decodeOr(vals, keyMailFromAddress, "")
		if err != nil {
			return err
		}
		oldFromName, err := decodeOr(vals, keyMailFromName, "")
		if err != nil {
			return err
		}

		before := map[string]any{
			keyMailHost:        oldHost,
			keyMailPort:        oldPort,
			keyMailEncryption:  oldEnc,
			keyMailUsername:    oldUser,
			keyMailFromAddress: oldFrom,
			keyMailFromName:    oldFromName,
		}
		after := map[string]any{
			keyMailHost:        v.Host,
			keyMailPort:        v.Port,
			keyMailEncryption:  v.Encryption,
			keyMailUsername:    v.Username,
			keyMailFromAddress: v.FromAddress,
			keyMailFromName:    v.FromName,
		}
		changes, b, a := diff(before, after)

		// 换了服务器、端口或用户名却沿用旧口令，等于把旧口令发给新服务器：要求重填或清除。
		if v.Password == nil && (v.Host != oldHost || v.Port != oldPort || v.Username != oldUser) {
			if stored, err := decodeOr(vals, keyMailPassword, ""); err != nil {
				return err
			} else if stored != "" {
				return errs.Invalid(map[string]string{
					"password": "改了服务器、端口或用户名，需要重新填写口令",
				})
			}
		}

		if v.Password != nil {
			if *v.Password == "" {
				if err := s.store.Delete(ctx, keyMailPassword); err != nil {
					return err
				}
			} else {
				if s.sealer == nil {
					return errs.Wrap(errs.Internal, errors.New("settings: sealer 未配置"))
				}
				blob, err := s.sealer.Seal([]byte(*v.Password), []byte(keyMailPassword))
				if err != nil {
					return errs.Wrap(errs.Internal, err)
				}
				changes = append(changes, change{
					key:   keyMailPassword,
					value: base64.StdEncoding.EncodeToString(blob),
				})
			}
		}

		if len(changes) == 0 && v.Password == nil {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditMailChanged, auditResourceSettings, map[string]any{
			"group":            "mail",
			"before":           b,
			"after":            a,
			"password_changed": v.Password != nil,
		})
	})
}

func (s *Service) openPassword(ctx context.Context, enc string) string {
	if s.sealer == nil {
		plog.From(ctx).Error("settings: SMTP 口令解封失败", zap.Error(errors.New("settings: sealer 未配置")))
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		plog.From(ctx).Error("settings: SMTP 口令解封失败", zap.Error(err))
		return ""
	}
	plain, err := s.sealer.Open(blob, []byte(keyMailPassword))
	if err != nil {
		plog.From(ctx).Error("settings: SMTP 口令解封失败", zap.Error(err))
		return ""
	}
	return string(plain)
}
