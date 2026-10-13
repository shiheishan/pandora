package service

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/content/model"
	"github.com/aegispanel/aegis/internal/module/content/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ListVisible 门户列出当前用户可见的文章。
func (s *Service) ListVisible(ctx context.Context, userID uuid.UUID, f VisibleFilter) ([]Page, error) {
	return s.visible(ctx, userID, "", f, false)
}

// GetVisible 门户读单篇可见文章。
func (s *Service) GetVisible(ctx context.Context, userID uuid.UUID, slug string, f VisibleFilter) (Page, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugPattern.MatchString(slug) {
		return Page{}, errs.New(errs.NotFound)
	}
	pages, err := s.visible(ctx, userID, slug, f, true)
	if err != nil {
		return Page{}, err
	}
	if len(pages) == 0 {
		return Page{}, errs.New(errs.NotFound)
	}
	return pages[0], nil
}

func (s *Service) visible(ctx context.Context, userID uuid.UUID, slug string, filter VisibleFilter, includeBody bool) ([]Page, error) {
	if filter.Kind != "" && !allowedKinds[filter.Kind] {
		return nil, errs.Invalid(map[string]string{"kind": "无效的内容类型"})
	}
	filter.Platform = strings.ToLower(strings.TrimSpace(filter.Platform))
	if filter.Platform != "" && filter.Platform != PlatformAny && !allowedPlatforms[filter.Platform] {
		return nil, errs.Invalid(map[string]string{"platform": "无效的平台"})
	}
	filter.Query = strings.TrimSpace(filter.Query)
	if utf8.RuneCountInString(filter.Query) > 100 {
		return nil, errs.Invalid(map[string]string{"q": "搜索词最多 100 个字"})
	}
	if filter.ClientVersion != "" && !versionPattern.MatchString(filter.ClientVersion) {
		return nil, errs.Invalid(map[string]string{"client_version": "客户端版本格式不正确"})
	}
	filter.Locale = strings.TrimSpace(filter.Locale)
	if filter.Locale == "" {
		filter.Locale = DefaultLocale
	}
	if !localePattern.MatchString(filter.Locale) {
		return nil, errs.Invalid(map[string]string{"locale": "语言格式不正确"})
	}

	planUUIDs, err := s.subs.LivePlanIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	planStrs := make([]string, len(planUUIDs))
	for i, id := range planUUIDs {
		planStrs[i] = id.String()
	}

	q := repo.VisibleQuery{
		Kind:        filter.Kind,
		Slug:        slug,
		Locale:      filter.Locale,
		Query:       filter.Query,
		AnyPlatform: filter.Platform == PlatformAny,
		Platform:    filter.Platform,
		PlanIDs:     planStrs,
		IncludeBody: includeBody,
	}
	if slug != "" {
		q.Query = ""
	}

	rows, err := s.repo.Visible(ctx, q)
	if err != nil {
		return nil, err
	}

	out := make([]Page, 0)
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.Slug] {
			continue
		}
		if !versionVisible(row.MinClientVersion, row.MaxClientVersion, filter.ClientVersion) {
			continue
		}
		seen[row.Slug] = true
		if row.Matched {
			out = append(out, toPage(row.Page))
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i].PublishedAt, out[j].PublishedAt
		if left != nil && right != nil && !left.Equal(*right) {
			return left.After(*right)
		}
		if left != nil && right == nil {
			return true
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// SubmitFeedback 记录用户对某版本是否有帮助。
func (s *Service) SubmitFeedback(ctx context.Context, userID uuid.UUID, slug string, version int, helpful bool, f VisibleFilter) error {
	if _, err := s.GetVisible(ctx, userID, slug, f); err != nil {
		return err
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	versionInvalid := errs.Invalid(map[string]string{"version": "该版本不存在"})
	if version <= 0 {
		return versionInvalid
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		ok, err := s.repo.PublishedVersionExists(ctx, slug, version)
		if err != nil {
			return err
		}
		if !ok {
			return versionInvalid
		}
		fb := &model.Feedback{
			PageSlug:    slug,
			PageVersion: version,
			UserID:      userID,
			Helpful:     helpful,
		}
		return s.repo.UpsertFeedback(ctx, fb)
	})
}
