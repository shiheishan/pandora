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
	pages, _, err := s.visible(ctx, userID, "", f, false)
	return pages, err
}

// GetVisible 门户读单篇可见文章。
func (s *Service) GetVisible(ctx context.Context, userID uuid.UUID, slug string, f VisibleFilter) (Page, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugPattern.MatchString(slug) {
		return Page{}, errs.New(errs.NotFound)
	}
	page, hasAccess, err := s.getVisible(ctx, userID, slug, f)
	if err != nil {
		return Page{}, err
	}
	// 端口没装时（只有单测会这样，生产由装配自检 MissingPorts 拦住）对应变量替换成空串
	var vars RenderVars
	if s.site != nil {
		site, err := s.site.Site(ctx)
		if err != nil {
			return Page{}, err
		}
		vars.SiteName = site.Name
		vars.SiteURL = strings.TrimRight(s.site.SiteURL(ctx), "/")
	}
	if s.links != nil && NeedsSubscribeURL(page.Body) {
		vars.SubscribeURL, err = s.links.CurrentLink(ctx)
		if err != nil {
			return Page{}, err
		}
	}
	page.Body = RenderBody(page.Body, vars, hasAccess)
	return page, nil
}

func (s *Service) getVisible(ctx context.Context, userID uuid.UUID, slug string, f VisibleFilter) (Page, bool, error) {
	pages, hasAccess, err := s.visible(ctx, userID, slug, f, true)
	if err != nil {
		return Page{}, false, err
	}
	if len(pages) == 0 {
		return Page{}, false, errs.New(errs.NotFound)
	}
	return pages[0], hasAccess, nil
}

func (s *Service) visible(ctx context.Context, userID uuid.UUID, slug string, filter VisibleFilter, includeBody bool) ([]Page, bool, error) {
	if filter.Kind != "" && !allowedKinds[filter.Kind] {
		return nil, false, errs.Invalid(map[string]string{"kind": "无效的内容类型"})
	}
	filter.Platform = strings.ToLower(strings.TrimSpace(filter.Platform))
	if filter.Platform != "" && filter.Platform != PlatformAny && !allowedPlatforms[filter.Platform] {
		return nil, false, errs.Invalid(map[string]string{"platform": "无效的平台"})
	}
	filter.Query = strings.TrimSpace(filter.Query)
	if utf8.RuneCountInString(filter.Query) > 100 {
		return nil, false, errs.Invalid(map[string]string{"q": "搜索词最多 100 个字"})
	}
	if filter.ClientVersion != "" && !versionPattern.MatchString(filter.ClientVersion) {
		return nil, false, errs.Invalid(map[string]string{"client_version": "客户端版本格式不正确"})
	}
	filter.Locale = strings.TrimSpace(filter.Locale)
	if filter.Locale == "" {
		filter.Locale = DefaultLocale
	}
	if !localePattern.MatchString(filter.Locale) {
		return nil, false, errs.Invalid(map[string]string{"locale": "语言格式不正确"})
	}

	planUUIDs, err := s.subs.LivePlanIDs(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	hasAccess := len(planUUIDs) > 0
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
	// 没有在用套餐时，搜索不能命中 access 区块里的文字：SQL 先按原文粗筛，再取正文在这里剔掉区块复核；
	// 列表照旧不返回正文。
	recheck := !hasAccess && q.Query != ""
	if recheck {
		q.IncludeBody = true
	}

	rows, err := s.repo.Visible(ctx, q)
	if err != nil {
		return nil, false, err
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
		if recheck {
			row.Matched = row.Matched && SearchHit(row.Title, row.Summary, row.Body, q.Query)
			if !includeBody {
				row.Body = ""
			}
		}
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
	return out, hasAccess, nil
}

// SubmitFeedback 记录用户对某版本是否有帮助。
func (s *Service) SubmitFeedback(ctx context.Context, userID uuid.UUID, slug string, version int, helpful bool, f VisibleFilter) error {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugPattern.MatchString(slug) {
		return errs.New(errs.NotFound)
	}
	if _, _, err := s.getVisible(ctx, userID, slug, f); err != nil {
		return err
	}
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
