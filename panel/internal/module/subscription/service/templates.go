package service

// 后台订阅模板的读、存、恢复缺省与预览；属主 subscription（subscription_templates）。
// 改动最多 30 秒后在拉取里生效（拉取侧快照，见 pull_extras.go）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 模板审计。
const (
	ActionTemplateSaved          = "subscription.template_saved"
	ActionTemplateReset          = "subscription.template_reset"
	ResourceSubscriptionTemplate = "subscription_template"
)

// TemplatePlaceholder 是模板可用的一个占位符。
type TemplatePlaceholder struct {
	Token    string
	Required bool
	Desc     string
}

// TemplateSummary 是后台模板列表的一行。
type TemplateSummary struct {
	Format     string
	Customized bool       // 库里有自定义模板
	Bytes      int        // 自定义模板的字节数；没有为 0
	UpdatedAt  *time.Time // 自定义模板的修改时刻；没有为 nil
}

// TemplateDetail 是一种格式的模板详情。
type TemplateDetail struct {
	Format       string
	Customized   bool
	Content      string // 当前生效的原文：有自定义的给自定义，没有给内置
	Default      string // 内置模板原文
	UpdatedAt    *time.Time
	Placeholders []TemplatePlaceholder
	MaxBytes     int
}

// TemplatePreview 是按示例节点渲染的预览。
type TemplatePreview struct {
	Body        string
	ContentType string
	NodeCount   int
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (s *Service) templateFormat(raw string) (render.Format, error) {
	f := render.Format(strings.TrimSpace(raw))
	if !render.Templatable(f) {
		return "", errs.New(errs.NotFound)
	}
	return f, nil
}

func (s *Service) renderOptions() render.Options {
	if s.delivery != nil {
		return s.delivery.Render
	}
	return render.Options{}
}

// AdminTemplates 列出各格式的模板概要（顺序与 render.TemplateFormats 一致）。
func (s *Service) AdminTemplates(ctx context.Context) ([]TemplateSummary, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return nil, err
	}
	rows, err := s.repo.TemplateSummaries(ctx)
	if err != nil {
		return nil, subErr(err)
	}
	byFmt := make(map[string]repo.TemplateSummary, len(rows))
	for _, r := range rows {
		byFmt[r.Format] = r
	}
	out := make([]TemplateSummary, 0, len(render.TemplateFormats))
	for _, f := range render.TemplateFormats {
		row, ok := byFmt[string(f)]
		sum := TemplateSummary{Format: string(f)}
		if ok {
			sum.Customized = true
			sum.Bytes = row.Bytes
			t := row.UpdatedAt.UTC()
			sum.UpdatedAt = &t
		}
		out = append(out, sum)
	}
	return out, nil
}

// AdminTemplate 返回一种格式的模板详情。
func (s *Service) AdminTemplate(ctx context.Context, format string) (TemplateDetail, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return TemplateDetail{}, err
	}
	f, err := s.templateFormat(format)
	if err != nil {
		return TemplateDetail{}, err
	}
	opts := s.renderOptions()
	tpl, found, err := s.repo.Template(ctx, string(f), repo.NoLock)
	if err != nil {
		return TemplateDetail{}, subErr(err)
	}
	detail := TemplateDetail{
		Format:       string(f),
		Default:      render.DefaultTemplate(f, opts),
		MaxBytes:     render.MaxTemplateBytes,
		Placeholders: placeholdersFor(f),
	}
	if found {
		detail.Customized = true
		detail.Content = tpl.Content
		t := tpl.UpdatedAt.UTC()
		detail.UpdatedAt = &t
	} else {
		detail.Content = detail.Default
	}
	return detail, nil
}

func placeholdersFor(f render.Format) []TemplatePlaceholder {
	ps := render.TemplatePlaceholders(f)
	out := make([]TemplatePlaceholder, len(ps))
	for i, p := range ps {
		out[i] = TemplatePlaceholder{Token: p.Token, Required: p.Required, Desc: p.Desc}
	}
	return out
}

// AdminSaveTemplate 保存一种格式的自定义模板。
func (s *Service) AdminSaveTemplate(ctx context.Context, format, content string) (TemplateDetail, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return TemplateDetail{}, err
	}
	f, err := s.templateFormat(format)
	if err != nil {
		return TemplateDetail{}, err
	}
	if err := render.ValidateTemplate(f, content); err != nil {
		var te *render.TemplateError
		if errors.As(err, &te) {
			return TemplateDetail{}, errs.Invalid(map[string]string{"content": te.Reason})
		}
		return TemplateDetail{}, err
	}
	opts := s.renderOptions()
	if err := s.tx.Do(ctx, func(ctx context.Context) error {
		oldTpl, hadRow, err := s.repo.Template(ctx, string(f), repo.ForNoKeyUpdate)
		if err != nil {
			return subErr(err)
		}
		oldContent := render.DefaultTemplate(f, opts)
		if hadRow {
			oldContent = oldTpl.Content
		}
		if hadRow && oldTpl.Content == content {
			return nil
		}
		if err := s.repo.UpsertTemplate(ctx, string(f), content, s.clock()); err != nil {
			return subErr(err)
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionTemplateSaved,
			ResourceType: ResourceSubscriptionTemplate,
			Data: map[string]any{
				"format":        string(f),
				"before_sha256": sha256Hex(oldContent),
				"after_sha256":  sha256Hex(content),
				"bytes":         len(content),
				"was_default":   !hadRow,
			},
		})
	}); err != nil {
		return TemplateDetail{}, err
	}
	return s.AdminTemplate(ctx, format)
}

// AdminResetTemplate 删除自定义模板，恢复内置缺省。
func (s *Service) AdminResetTemplate(ctx context.Context, format string) (TemplateDetail, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return TemplateDetail{}, err
	}
	f, err := s.templateFormat(format)
	if err != nil {
		return TemplateDetail{}, err
	}
	opts := s.renderOptions()
	if err := s.tx.Do(ctx, func(ctx context.Context) error {
		oldTpl, hadRow, err := s.repo.Template(ctx, string(f), repo.ForNoKeyUpdate)
		if err != nil {
			return subErr(err)
		}
		deleted, err := s.repo.DeleteTemplate(ctx, string(f))
		if err != nil {
			return subErr(err)
		}
		if !deleted {
			return nil
		}
		before := render.DefaultTemplate(f, opts)
		if hadRow {
			before = oldTpl.Content
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionTemplateReset,
			ResourceType: ResourceSubscriptionTemplate,
			Data: map[string]any{
				"format":        string(f),
				"before_sha256": sha256Hex(before),
			},
		})
	}); err != nil {
		return TemplateDetail{}, err
	}
	return s.AdminTemplate(ctx, format)
}

// AdminPreviewTemplate 用示例节点预览模板；content 空白时预览内置模板。
func (s *Service) AdminPreviewTemplate(ctx context.Context, format, content string) (TemplatePreview, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return TemplatePreview{}, err
	}
	f, err := s.templateFormat(format)
	if err != nil {
		return TemplatePreview{}, err
	}
	trimmed := strings.TrimSpace(content)
	if trimmed != "" {
		if err := render.ValidateTemplate(f, content); err != nil {
			var te *render.TemplateError
			if errors.As(err, &te) {
				return TemplatePreview{}, errs.Invalid(map[string]string{"content": te.Reason})
			}
			return TemplatePreview{}, err
		}
	} else {
		content = ""
	}
	body, ct, n := render.PreviewTemplate(f, content, s.renderOptions())
	return TemplatePreview{Body: string(body), ContentType: ct, NodeCount: n}, nil
}
