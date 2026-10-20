package service

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/appearance/model"
	"github.com/aegispanel/aegis/internal/module/appearance/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// themeCodePattern 与 site_themes.code 的 CHECK 一致。
var themeCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,38}$`)

// toTheme 把库模型转成对外主题。
func toTheme(m model.Theme) Theme {
	return Theme{
		ID:        m.ID,
		Code:      m.Code,
		Name:      m.Name,
		IsBuiltin: m.IsBuiltin,
		IsActive:  m.IsActive,
		Tokens:    json.RawMessage(m.Tokens),
		Branding:  json.RawMessage(m.Branding),
	}
}

// ListThemes 列出全部主题。
func (s *Service) ListThemes(ctx context.Context) ([]Theme, error) {
	rows, err := s.repo.ListThemes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Theme, 0, len(rows))
	for _, m := range rows {
		out = append(out, toTheme(m))
	}
	return out, nil
}

// SaveTheme 新建或更新主题。
func (s *Service) SaveTheme(ctx context.Context, in SaveThemeInput) error {
	code := strings.ToLower(strings.TrimSpace(in.Code))
	name := strings.TrimSpace(in.Name)
	fields := map[string]string{}
	switch {
	case code == "":
		fields["code"] = "主题标识必填"
	case !themeCodePattern.MatchString(code):
		fields["code"] = "主题标识须以小写字母开头，只含小写字母、数字、- 与 _，共 2–39 个字符"
	}
	if n := utf8.RuneCountInString(name); n == 0 || n > 60 {
		fields["name"] = "主题名称必填，最多 60 个字"
	}
	if strings.Contains(name, "\x00") {
		fields["name"] = "主题名称不能包含空字符"
	}
	tokens, tokErr := normalizeTokens(in.Tokens)
	if err := mergeFields(fields, tokErr); err != nil {
		return err
	}
	branding, brandErr := normalizeBranding(in.Branding)
	if err := mergeFields(fields, brandErr); err != nil {
		return err
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}

	err := s.tx.Do(ctx, func(ctx context.Context) error {
		existing, ok, err := s.repo.ThemeByCode(ctx, code)
		if err != nil {
			return err
		}
		if ok && existing.IsBuiltin {
			return errs.Newf(errs.ValidationFailed, "内置主题不能直接改，请用另一个标识另存为自定义主题")
		}
		if ok && in.Create {
			return &errs.Error{
				Code:    errs.Conflict,
				Message: "主题标识已被占用",
				Fields:  map[string]string{"code": "主题标识「" + code + "」已被占用，换一个"},
			}
		}
		t := model.Theme{
			Code:     code,
			Name:     name,
			Tokens:   string(tokens),
			Branding: string(branding),
		}
		if ok {
			t.ID = existing.ID
			t.IsBuiltin = existing.IsBuiltin
			t.IsActive = existing.IsActive
		}
		if err := s.repo.UpsertTheme(ctx, &t); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return &errs.Error{
					Code:    errs.Conflict,
					Message: "主题标识已被占用",
					Fields:  map[string]string{"code": "主题标识「" + code + "」已被占用，换一个"},
				}
			}
			return err
		}
		if t.ID == uuid.Nil {
			loaded, ok, err := s.repo.ThemeByCode(ctx, code)
			if err != nil {
				return err
			}
			if !ok {
				return errs.Wrap(errs.Internal, errors.New("主题保存后读不回"))
			}
			t.ID = loaded.ID
		}
		tx.AfterCommit(ctx, "appearance:pagemeta", func(context.Context) { s.invalidatePageMeta() })
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionThemeSave,
			ResourceType: "site_theme",
			ResourceID:   &t.ID,
			Data:         map[string]any{"code": code, "create": in.Create},
		})
	})
	return err
}

// ActivateTheme 切换生效主题。
func (s *Service) ActivateTheme(ctx context.Context, code string) error {
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		_, ok, err := s.repo.ThemeByCode(ctx, code)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Newf(errs.NotFound, "主题不存在")
		}
		if err := s.repo.DeactivateThemes(ctx); err != nil {
			return err
		}
		id, ok, err := s.repo.ActivateTheme(ctx, code)
		if errors.Is(err, repo.ErrDuplicate) {
			return errs.Newf(errs.Conflict, "另一套主题正在切换，请刷新后重试")
		}
		if err != nil {
			return err
		}
		if !ok {
			return errs.Newf(errs.NotFound, "主题不存在")
		}
		tx.AfterCommit(ctx, "appearance:pagemeta", func(context.Context) { s.invalidatePageMeta() })
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionThemeActivate,
			ResourceType: "site_theme",
			ResourceID:   &id,
			Data:         map[string]any{"code": code},
		})
	})
	return err
}

// DeleteTheme 删除非内置、未生效的自定义主题。
func (s *Service) DeleteTheme(ctx context.Context, code string) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		id, ok, err := s.repo.DeleteCustomInactiveTheme(ctx, code)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Newf(errs.ValidationFailed, "删不掉：内置主题和正在生效的主题都不能删")
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionThemeDelete,
			ResourceType: "site_theme",
			ResourceID:   &id,
			Data:         map[string]any{"code": code},
		})
	})
}

// Public 返回门户渲染用的主题与插槽（读路径不开事务）。
func (s *Service) Public(ctx context.Context) (PublicAppearance, error) {
	out := PublicAppearance{Slots: map[string]string{}}
	m, ok, err := s.repo.ActiveTheme(ctx)
	if err != nil {
		return PublicAppearance{}, err
	}
	if ok {
		t := toTheme(m)
		t.Tokens = filterTokens(t.Tokens)
		t.Branding = filterBranding(t.Branding)
		out.Theme = &t
	}
	slots, err := s.repo.EnabledSlots(ctx)
	if err != nil {
		return PublicAppearance{}, err
	}
	for _, sl := range slots {
		out.Slots[sl.Key] = sl.Content
	}
	out.SiteName = s.PageMeta(ctx).SiteName
	return out, nil
}

// mergeFields 把校验错误的字段并入 fields；无法合并时返回原错误。
func mergeFields(fields map[string]string, err error) error {
	if err == nil {
		return nil
	}
	var he *errs.Error
	if errors.As(err, &he) && len(he.Fields) > 0 {
		maps.Copy(fields, he.Fields)
		return nil
	}
	return err
}
