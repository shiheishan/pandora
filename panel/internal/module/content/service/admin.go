package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/content/model"
	"github.com/aegispanel/aegis/internal/module/content/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ListAdmin 后台列出内容版本。
func (s *Service) ListAdmin(ctx context.Context, f ListFilter) ([]Page, error) {
	// handler 已经 pagination.Parse 过（缺省 200、上限 500）；这里只兜底，口径与它一致
	limit := min(f.Limit, 500)
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.repo.ListAdmin(ctx, repo.AdminFilter{
		Kind: f.Kind, Status: f.Status, Query: f.Query, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	creatorIDs := make([]uuid.UUID, 0)
	seen := map[uuid.UUID]bool{}
	for _, row := range rows {
		if row.CreatedBy != nil && !seen[*row.CreatedBy] {
			seen[*row.CreatedBy] = true
			creatorIDs = append(creatorIDs, *row.CreatedBy)
		}
	}
	names := map[uuid.UUID]string{}
	if len(creatorIDs) > 0 {
		names, err = s.users.DisplayNames(ctx, creatorIDs)
		if err != nil {
			return nil, err
		}
	}
	out := make([]Page, 0, len(rows))
	for _, row := range rows {
		p := toPage(row.Page)
		p.LatestVersion = row.LatestVersion
		p.IsLatestInAudience = row.IsLatestInAudience
		if row.CreatedBy != nil {
			p.CreatedBy = row.CreatedBy
			if n := names[*row.CreatedBy]; n != "" {
				p.CreatedByName = &n
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// GetAdmin 后台读单个版本详情。
func (s *Service) GetAdmin(ctx context.Context, id uuid.UUID) (Page, error) {
	row, ok, err := s.repo.GetAdmin(ctx, id)
	if err != nil {
		return Page{}, err
	}
	if !ok {
		return Page{}, errs.New(errs.NotFound)
	}
	p := toPage(row.Page)
	p.LatestVersion = row.LatestVersion
	p.IsLatestInAudience = row.IsLatestInAudience
	p.CreatedBy = nil
	p.CreatedByName = nil
	return p, nil
}

// PublishVersion 发一个新版本。
func (s *Service) PublishVersion(ctx context.Context, in PublishInput) (PublishResult, error) {
	norm, err := normalizePublishInput(in)
	if err != nil {
		return PublishResult{}, err
	}
	if len(norm.TargetPlanIDs) > 0 {
		ids := make([]uuid.UUID, len(norm.TargetPlanIDs))
		for i, raw := range norm.TargetPlanIDs {
			ids[i] = uuid.MustParse(raw) // normalizePublishInput 已校验并规范化
		}
		plans, err := s.plans.PlansByIDs(ctx, ids)
		if err != nil {
			return PublishResult{}, err
		}
		if len(plans) != len(norm.TargetPlanIDs) {
			return PublishResult{}, errs.Invalid(map[string]string{"target_plan_ids": "包含不存在的套餐"})
		}
	}

	var result PublishResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		latest, err := s.repo.LatestVersionForUpdate(ctx, norm.Slug)
		if err != nil {
			return err
		}
		if latest != norm.ExpectedLatestVersion {
			return errs.Newf(errs.Conflict, "内容已产生新版本，请刷新后重试")
		}
		version := latest + 1
		var publishedAt *time.Time
		var superseded []repo.Superseded
		if norm.Status == StatusPublished {
			aud := repo.Audience{
				Slug:             norm.Slug,
				Locale:           norm.Locale,
				TargetPlatforms:  encodeList(norm.TargetPlatforms),
				MinClientVersion: norm.MinClientVersion,
				MaxClientVersion: norm.MaxClientVersion,
				TargetPlanIDs:    encodeList(norm.TargetPlanIDs),
				Visibility:       norm.Visibility,
			}
			superseded, err = s.repo.ArchivePublishedInAudience(ctx, aud)
			if err != nil {
				return err
			}
			now := s.now().UTC()
			publishedAt = &now
		}
		page := &model.Page{
			Slug:             norm.Slug,
			Kind:             norm.Kind,
			Category:         norm.Category,
			Version:          version,
			Title:            norm.Title,
			Summary:          norm.Summary,
			Body:             norm.Body,
			Locale:           norm.Locale,
			SanitizerVersion: "plain-v1",
			TargetPlatforms:  encodeList(norm.TargetPlatforms),
			MinClientVersion: norm.MinClientVersion,
			MaxClientVersion: norm.MaxClientVersion,
			TargetPlanIDs:    encodeList(norm.TargetPlanIDs),
			Visibility:       norm.Visibility,
			Status:           norm.Status,
			ReviewDueAt:      norm.ReviewDueAt,
			PublishedAt:      publishedAt,
			CreatedBy:        actorID(ctx),
		}
		if err := s.repo.InsertPage(ctx, page); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Newf(errs.Conflict, "内容版本已变化，请刷新后重试")
			}
			return err
		}
		supData := make([]map[string]any, len(superseded))
		for i, sp := range superseded {
			supData[i] = map[string]any{"id": sp.ID.String(), "version": sp.Version}
		}
		id := page.ID
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionVersionCreated,
			ResourceType: ResourcePage,
			ResourceID:   &id,
			Data: map[string]any{
				"slug": norm.Slug, "kind": norm.Kind, "version": version,
				"status": norm.Status, "visibility": norm.Visibility,
				"superseded": supData,
			},
		}); err != nil {
			return err
		}
		result = PublishResult{ID: page.ID, Slug: norm.Slug, Version: version, Status: norm.Status}
		return nil
	})
	if err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

// Archive 把指定版本归档。
func (s *Service) Archive(ctx context.Context, id uuid.UUID, expectedVersion int) (ArchiveResult, error) {
	if expectedVersion <= 0 {
		return ArchiveResult{}, errs.New(errs.NotFound)
	}
	var result ArchiveResult
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		locked, ok, err := s.repo.LockPage(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		if locked.Version != expectedVersion {
			return errs.Newf(errs.Conflict, "内容版本已变化，请刷新后重试")
		}
		result = ArchiveResult{ID: id, Version: expectedVersion, Status: StatusArchived}
		if locked.Status == StatusArchived {
			result.AlreadyArchived = true
			return nil
		}
		beforeStatus := locked.Status
		changed, err := s.repo.ArchivePage(ctx, id, expectedVersion)
		if err != nil {
			return err
		}
		if !changed {
			result.AlreadyArchived = true
			return nil
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionArchived,
			ResourceType: ResourcePage,
			ResourceID:   &id,
			Data:         map[string]any{"before_status": beforeStatus, "version": expectedVersion},
		})
	})
	if err != nil {
		return ArchiveResult{}, err
	}
	return result, nil
}
