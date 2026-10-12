package service

import (
	"context"
	"errors"
	"strings"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// smtpConfig 读当前发信配置；配置不齐时 ok 为 false；发件人名为空时用站点名。
func (s *Service) smtpConfig(ctx context.Context) (SMTPConfig, bool, error) {
	m, err := s.settings.Mail(ctx)
	if err != nil {
		return SMTPConfig{}, false, err
	}
	cfg := SMTPConfig{
		Host:        m.Host,
		Port:        m.Port,
		Encryption:  m.Encryption,
		Username:    m.Username,
		Password:    m.Password,
		FromAddress: m.FromAddress,
		FromName:    m.FromName,
	}
	if !m.Configured() {
		return cfg, false, nil
	}
	if cfg.FromName == "" {
		site, err := s.settings.Site(ctx)
		if err != nil {
			return SMTPConfig{}, false, err
		}
		cfg.FromName = site.Name
	}
	return cfg, true, nil
}

func errNotConfigured() error {
	return errs.Newf(errs.ValidationFailed, "SMTP 还没配置好：服务器地址、端口、发件人地址都要填")
}

func validateTestRecipient(to string) (string, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return "", errs.Invalid(map[string]string{"to": "请填写收件地址"})
	}
	if strings.ContainsRune(to, 0) || !identitysvc.ValidEmail(to) {
		return "", errs.Invalid(map[string]string{"to": "邮箱格式不正确"})
	}
	return to, nil
}

func (s *Service) recordTestSend(ctx context.Context, kind, to string, extra map[string]any) error {
	data := map[string]any{"kind": kind, "target": to}
	for k, v := range extra {
		data[k] = v
	}
	return s.audit.Write(ctx, auditsvc.Event{
		Action:       "notify.test_sent",
		ResourceType: "notification_test",
		Data:         data,
	})
}

func testSendError(err error) error {
	var se *SendError
	if errors.As(err, &se) {
		return errs.Newf(errs.ValidationFailed, "发送失败："+se.Error())
	}
	return errs.Wrap(errs.Internal, err)
}

// MailSettings 读邮件页的设置（口令只给是否已设置；发件人名为空时显示站点名）。
func (s *Service) MailSettings(ctx context.Context) (MailSettings, error) {
	m, err := s.settings.Mail(ctx)
	if err != nil {
		return MailSettings{}, err
	}
	reg, err := s.settings.Registration(ctx)
	if err != nil {
		return MailSettings{}, err
	}
	fromName := m.FromName
	if fromName == "" {
		site, err := s.settings.Site(ctx)
		if err != nil {
			return MailSettings{}, err
		}
		fromName = site.Name
	}
	return MailSettings{
		Host:              m.Host,
		Port:              m.Port,
		Encryption:        m.Encryption,
		Username:          m.Username,
		HasPassword:       m.HasPassword,
		FromAddress:       m.FromAddress,
		FromName:          fromName,
		EmailVerification: reg.EmailVerification,
		RegistrationMode:  reg.Mode,
	}, nil
}

// SaveMailSettings 在一个事务里先写 SMTP 设置再写注册设置（开启邮箱验证的校验要读到新的 SMTP 值）。
func (s *Service) SaveMailSettings(ctx context.Context, in MailSettingsUpdate) error {
	fields := map[string]string{}
	if strings.ContainsRune(in.Host, 0) {
		fields["smtp_host"] = "不能包含空字符"
	}
	if strings.ContainsRune(in.Username, 0) {
		fields["smtp_username"] = "不能包含空字符"
	}
	if in.Password != nil && strings.ContainsRune(*in.Password, 0) {
		fields["smtp_password"] = "不能包含空字符"
	}
	if strings.ContainsRune(in.FromAddress, 0) {
		fields["from_address"] = "不能包含空字符"
	}
	if strings.ContainsRune(in.FromName, 0) {
		fields["from_name"] = "不能包含空字符"
	}
	if in.RegistrationMode != nil && strings.ContainsRune(*in.RegistrationMode, 0) {
		fields["registration_mode"] = "不能包含空字符"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}

	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.settings.SetMail(ctx, settingssvc.MailUpdate{
			Host:        in.Host,
			Port:        in.Port,
			Encryption:  in.Encryption,
			Username:    in.Username,
			Password:    in.Password,
			FromAddress: in.FromAddress,
			FromName:    in.FromName,
		}); err != nil {
			return err
		}
		if in.EmailVerification == nil && in.RegistrationMode == nil {
			return nil
		}
		cur, err := s.settings.Registration(ctx)
		if err != nil {
			return err
		}
		if in.EmailVerification != nil {
			cur.EmailVerification = *in.EmailVerification
		}
		if in.RegistrationMode != nil {
			cur.Mode = *in.RegistrationMode
		}
		return s.settings.SetRegistration(ctx, cur)
	})
	return renameField(err, "password", "smtp_password")
}

func renameField(err error, from, to string) error {
	if err == nil {
		return nil
	}
	var e *errs.Error
	if !errors.As(err, &e) || e.Fields == nil {
		return err
	}
	msg, ok := e.Fields[from]
	if !ok {
		return err
	}
	fields := make(map[string]string, len(e.Fields))
	for k, v := range e.Fields {
		fields[k] = v
	}
	delete(fields, from)
	fields[to] = msg
	return errs.Invalid(fields)
}

// TestMailSettings 用当前已保存的配置实发一封测试邮件。
func (s *Service) TestMailSettings(ctx context.Context, to string) error {
	to, err := validateTestRecipient(to)
	if err != nil {
		return err
	}
	cfg, ok, err := s.smtpConfig(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errNotConfigured()
	}
	if err := s.recordTestSend(ctx, "mail_settings", to, nil); err != nil {
		return err
	}
	if err := s.sender.Send(ctx, cfg, Message{
		To:      to,
		Subject: headerSafe(cfg.FromName + " 邮件配置测试"),
		Body:    "这是一封测试邮件。收到它说明面板的发信配置是通的。",
	}); err != nil {
		return testSendError(err)
	}
	return nil
}
