package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func templateView(t model.Template) TemplateView {
	v := TemplateView{
		Code:      t.Code,
		Channel:   t.Channel,
		Locale:    templateLocale,
		Status:    templateStatus,
		Version:   t.Version,
		Subject:   t.Subject,
		Body:      t.Body,
		UpdatedAt: t.UpdatedAt,
	}
	def, ok := lookupDef(t.Code, t.Channel)
	if ok {
		v.Category = def.Category
		v.Description = def.Description
		v.AllowedVariables = append([]string(nil), def.Allowed...)
		v.HasDefault = true
		v.IsDefault = t.Subject == def.Subject && t.Body == def.Body
	} else {
		v.Category = ""
		v.AllowedVariables = []string{}
	}
	v.PreviewSubject, v.PreviewBody = renderPreview(t.Subject, t.Body, v.AllowedVariables)
	return v
}

// ListTemplates 返回全部模板（按 code、channel），附说明与示例渲染。
func (s *Service) ListTemplates(ctx context.Context) ([]TemplateView, error) {
	rows, err := s.store.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TemplateView, 0, len(rows))
	for _, t := range rows {
		out = append(out, templateView(t))
	}
	return out, nil
}

// SaveTemplate 改一个内置模板的主题与正文（version 加 1），同事务写审计。
func (s *Service) SaveTemplate(ctx context.Context, in TemplateEdit) (TemplateView, error) {
	def, ok := lookupDef(in.Code, in.Channel)
	if !ok {
		return TemplateView{}, errs.New(errs.NotFound)
	}
	subject, body, err := validateTemplateContent(in.Subject, in.Body, def.Allowed)
	if err != nil {
		return TemplateView{}, err
	}

	var result model.Template
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		cur, found, err := s.store.LockTemplate(ctx, in.Code, in.Channel)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}

		out := cur
		if cur.Subject != subject || cur.Body != body {
			out, err = s.store.UpdateTemplateContent(ctx, cur.ID, subject, body)
			if err != nil {
				return err
			}
			id := cur.ID
			if err := s.audit.Write(ctx, auditsvc.Event{
				Action:       "notification_template.updated",
				ResourceType: "notification_template",
				ResourceID:   &id,
				Data: map[string]any{
					"code":               in.Code,
					"channel":            in.Channel,
					"version_before":     cur.Version,
					"version_after":      out.Version,
					"subject_before":     cur.Subject,
					"subject_after":      out.Subject,
					"body_sha256_before": bodySHA256Hex(cur.Body),
					"body_sha256_after":  bodySHA256Hex(out.Body),
				},
			}); err != nil {
				return err
			}
		}
		result = out
		return nil
	})
	if err != nil {
		return TemplateView{}, err
	}
	return templateView(result), nil
}

// ResetTemplate 把内置模板恢复成默认内容。
func (s *Service) ResetTemplate(ctx context.Context, code, channel string) (TemplateView, error) {
	def, ok := lookupDef(code, channel)
	if !ok {
		return TemplateView{}, errs.New(errs.NotFound)
	}
	return s.SaveTemplate(ctx, TemplateEdit{
		Code:    code,
		Channel: channel,
		Subject: def.Subject,
		Body:    def.Body,
	})
}

// PreviewDraft 用示例值渲染未保存的草稿，并列出白名单外的变量（不写库）。
func (s *Service) PreviewDraft(ctx context.Context, in TemplateEdit) (DraftPreview, error) {
	def, ok := lookupDef(in.Code, in.Channel)
	if !ok {
		return DraftPreview{}, errs.New(errs.NotFound)
	}

	fields := map[string]string{}
	if strings.ContainsRune(in.Subject, 0) {
		fields["subject"] = "不能包含空字符"
	}
	if strings.ContainsRune(in.Body, 0) {
		fields["body"] = "不能包含空字符"
	}
	if len(fields) > 0 {
		return DraftPreview{}, errs.Invalid(fields)
	}

	unknown, err := mergeUnknownVariables(in.Subject, in.Body, def.Allowed)
	if err != nil {
		return DraftPreview{}, err
	}
	if len(unknown) == 0 {
		unknown = []string{}
	}

	sub, body := renderPreview(in.Subject, in.Body, def.Allowed)
	return DraftPreview{
		Subject:          sub,
		Body:             body,
		UnknownVariables: unknown,
	}, nil
}

func bodySHA256Hex(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func mergeUnknownVariables(subject, body string, allowed []string) ([]string, error) {
	badSub, err := unknownVariables(subject, allowed)
	if err != nil {
		return nil, errs.Invalid(map[string]string{"subject": errTemplateSyntax.Error()})
	}
	badBody, err := unknownVariables(body, allowed)
	if err != nil {
		return nil, errs.Invalid(map[string]string{"body": errTemplateSyntax.Error()})
	}
	seen := make(map[string]struct{}, len(badSub)+len(badBody))
	var merged []string
	for _, name := range append(badSub, badBody...) {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	sort.Strings(merged)
	return merged, nil
}

// TestTemplate 用示例值渲染模板并实发一封测试信，返回渲染后的主题。
func (s *Service) TestTemplate(ctx context.Context, in TemplateTest) (string, error) {
	if in.Channel != ChannelEmail {
		return "", errs.Newf(errs.ValidationFailed, "只有邮件模板可以测试发送，站内信请到用户端查看")
	}
	to, err := validateTestRecipient(in.To)
	if err != nil {
		return "", err
	}
	def, ok := lookupDef(in.Code, in.Channel)
	if !ok {
		return "", errs.New(errs.NotFound)
	}

	var subject, body string
	draft := in.Subject != nil || in.Body != nil
	if draft {
		if in.Subject != nil {
			subject = *in.Subject
		}
		if in.Body != nil {
			body = *in.Body
		}
		subject, body, err = validateTemplateContent(subject, body, def.Allowed)
		if err != nil {
			return "", err
		}
	} else {
		t, found, err := s.store.GetTemplate(ctx, in.Code, in.Channel)
		if err != nil {
			return "", err
		}
		if !found {
			return "", errs.New(errs.NotFound)
		}
		subject, body = t.Subject, t.Body
	}

	ps, pb := renderPreview(subject, body, def.Allowed)
	cfg, ready, err := s.smtpConfig(ctx)
	if err != nil {
		return "", err
	}
	if !ready {
		return "", errNotConfigured()
	}
	if err := s.recordTestSend(ctx, "mail_template", to, map[string]any{
		"template": in.Code,
		"channel":  in.Channel,
		"draft":    draft,
	}); err != nil {
		return "", err
	}
	if err := s.sender.Send(ctx, cfg, Message{
		To:      to,
		Subject: headerSafe("[测试] " + ps),
		Body:    pb,
	}); err != nil {
		return "", testSendError(err)
	}
	return ps, nil
}
