package service

import (
	"context"
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

// CreateVersion 给套餐建一个草稿版本。
func (s *Service) CreateVersion(ctx context.Context, planID uuid.UUID) (Version, error) {
	var out Version
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		v, err := s.createVersionTx(ctx, planID)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, catalogErr(err)
}

// createVersionTx：repo.PlanByID(ForUpdate)，查不到 ErrNotFound；archived → 冲突；
// createdBy 取登录主体；repo.InsertNextVersion；审计后返回 versionFromModel。
func (s *Service) createVersionTx(ctx context.Context, planID uuid.UUID) (Version, error) {
	plan, found, err := s.repo.PlanByID(ctx, planID, repo.ForUpdate)
	if err != nil {
		return Version{}, err
	}
	if !found {
		return Version{}, ErrNotFound
	}
	if plan.Status == PlanArchived {
		return Version{}, errs.Newf(errs.Conflict, "已归档套餐不能创建新版本")
	}

	var createdBy *uuid.UUID
	if p, ok := authn.From(ctx); ok && p.UserID != uuid.Nil {
		id := p.UserID
		createdBy = &id
	}

	v, err := s.repo.InsertNextVersion(ctx, planID, createdBy)
	if err != nil {
		if errors.Is(err, repo.ErrDuplicate) {
			return Version{}, errs.Newf(errs.Conflict, "该套餐已有未发布的草稿版本，请先编辑或发布它")
		}
		return Version{}, err
	}

	ver := versionFromModel(v, nil, nil)
	if err := s.writeAudit(ctx, ActionVersionCreate, ResourcePlanVersion, ver.ID, map[string]any{
		"after": map[string]any{"plan_id": planID, "version": ver.Version},
	}); err != nil {
		return Version{}, err
	}
	return ver, nil
}

// UpdateVersion 全量覆盖草稿版本的语义、权益与配额，返回新的 row_version。
func (s *Service) UpdateVersion(ctx context.Context, planID, versionID uuid.UUID, expected int64, in VersionInput) (int64, error) {
	if expected <= 0 {
		return 0, errs.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	if err := validateVersionInput(&in); err != nil {
		return 0, err
	}
	var next int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		n, err := s.updateVersionTx(ctx, planID, versionID, expected, in)
		if err != nil {
			return err
		}
		next = n
		return nil
	})
	return next, catalogErr(err)
}

func (s *Service) updateVersionTx(ctx context.Context, planID, versionID uuid.UUID, expected int64, in VersionInput) (int64, error) {
	_, found, err := s.repo.PlanByID(ctx, planID, repo.ForUpdate)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrNotFound
	}

	cur, found, err := s.repo.VersionByID(ctx, planID, versionID, repo.ForUpdate)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrNotFound
	}
	if cur.RowVersion != expected {
		return 0, rowConflict("套餐版本", cur.RowVersion)
	}
	if cur.Status != VersionDraft {
		return 0, errs.Newf(errs.Conflict, "只有未发布草稿版本可以编辑")
	}

	mv := &model.PlanVersion{
		QuotaResetStrategy:   in.QuotaResetStrategy,
		QuotaResetDay:        in.QuotaResetDay,
		GracePeriodHours:     in.GracePeriodHours,
		GraceKeepsService:    in.GraceKeepsService,
		RenewalExtendsPeriod: in.RenewalExtendsPeriod,
		RenewalResetsQuota:   in.RenewalResetsQuota,
		RenewalKeepsAddons:   in.RenewalKeepsAddons,
		MaxDevices:           in.MaxDevices,
		MaxConcurrent:        in.MaxConcurrent,
		DeviceReleaseHours:   in.DeviceReleaseHours,
		ThrottleKbps:         in.ThrottleKbps,
		Notes:                in.Notes,
	}
	ok, err := s.repo.UpdateVersionSemantics(ctx, versionID, expected, mv)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, rowConflict("套餐版本", cur.RowVersion)
	}
	if err := s.repo.ReplaceVersionChildren(ctx, versionID, entitlementModels(in.Entitlements), quotaModels(in.Quotas)); err != nil {
		return 0, err
	}

	next := expected + 1
	if err := s.writeAudit(ctx, ActionVersionUpdate, ResourcePlanVersion, versionID, map[string]any{
		"before": map[string]any{"row_version": expected},
		"after": map[string]any{
			"row_version":  next,
			"entitlements": len(in.Entitlements),
			"quotas":       len(in.Quotas),
		},
	}); err != nil {
		return 0, err
	}
	return next, nil
}

// PublishVersion 发布草稿版本并设为套餐当前版本，返回 (套餐新 row_version, 版本新 row_version)。
func (s *Service) PublishVersion(ctx context.Context, planID, versionID uuid.UUID, expectedPlan, expectedVersion int64) (int64, int64, error) {
	if expectedPlan <= 0 || expectedVersion <= 0 {
		return 0, 0, errs.Invalid(map[string]string{
			"expected_plan_row_version":    "必须为正整数",
			"expected_version_row_version": "必须为正整数",
		})
	}
	var planNext, verNext int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		p, v, err := s.publishVersionTx(ctx, planID, versionID, expectedPlan, expectedVersion)
		if err != nil {
			return err
		}
		planNext, verNext = p, v
		return nil
	})
	return planNext, verNext, catalogErr(err)
}

func (s *Service) publishVersionTx(ctx context.Context, planID, versionID uuid.UUID, expectedPlan, expectedVersion int64) (int64, int64, error) {
	plan, found, err := s.repo.PlanByID(ctx, planID, repo.ForUpdate)
	if err != nil {
		return 0, 0, err
	}
	if !found {
		return 0, 0, ErrNotFound
	}
	if plan.Status == PlanArchived {
		return 0, 0, errs.Newf(errs.Conflict, "已归档套餐不能重新发布")
	}

	ver, found, err := s.repo.VersionByID(ctx, planID, versionID, repo.ForUpdate)
	if err != nil {
		return 0, 0, err
	}
	if !found {
		return 0, 0, ErrNotFound
	}
	if plan.RowVersion != expectedPlan {
		return 0, 0, rowConflict("套餐", plan.RowVersion)
	}
	if ver.RowVersion != expectedVersion {
		return 0, 0, rowConflict("套餐版本", ver.RowVersion)
	}
	if ver.Status != VersionDraft {
		return 0, 0, errs.Newf(errs.Conflict, "只有未发布草稿版本可以发布")
	}
	if plan.Visibility == VisibilityInviteOnly {
		return 0, 0, errs.Invalid(map[string]string{"visibility": "邀请购买授权尚未实现，禁止发布"})
	}

	now := s.now()
	groupMap, err := s.repo.VisibleGroups(ctx, []uuid.UUID{planID})
	if err != nil {
		return 0, 0, err
	}
	groups := groupMap[planID]
	if groups == nil {
		groups = []uuid.UUID{}
	}

	priceRows, err := s.repo.ActivePrices(ctx, []uuid.UUID{planID}, repo.ForShare)
	if err != nil {
		return 0, 0, err
	}
	prices := make([]Price, 0, len(priceRows))
	for _, row := range priceRows {
		prices = append(prices, priceFromModel(row))
	}
	if !publishPriceCoverage(plan.Visibility, groups, prices, now) {
		return 0, 0, errs.Invalid(map[string]string{"prices": "至少需要一个当前有效且适用的 CNY 或 USD 活动价格"})
	}

	if s.gate != nil {
		if err := s.gate.CheckPublishable(ctx, versionID); err != nil {
			return 0, 0, err
		}
	}

	ok, err := s.repo.PublishVersion(ctx, versionID, expectedVersion)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, rowConflict("套餐版本", ver.RowVersion)
	}
	ok, err = s.repo.ActivateVersion(ctx, planID, versionID, expectedPlan)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, rowConflict("套餐", plan.RowVersion)
	}

	priceIDs := qualifyingPublishPriceIDs(plan.Visibility, groups, prices, now)
	planNext := expectedPlan + 1
	verNext := expectedVersion + 1
	if err := s.writeAudit(ctx, ActionVersionPublish, ResourcePlanVersion, versionID, map[string]any{
		"before": map[string]any{
			"plan_row_version":    expectedPlan,
			"version_row_version": expectedVersion,
		},
		"after": map[string]any{
			"plan_id":             planID,
			"plan_row_version":    planNext,
			"version_row_version": verNext,
			"price_ids":           priceIDs,
		},
	}); err != nil {
		return 0, 0, err
	}
	return planNext, verNext, nil
}

// qualifyingPublishPriceIDs 收集参与发布价格覆盖判断的在售 id（CNY/USD、在窗、公开或组内适用）。
func qualifyingPublishPriceIDs(visibility string, groups []uuid.UUID, prices []Price, now time.Time) []uuid.UUID {
	groupSet := make(map[uuid.UUID]bool, len(groups))
	for _, g := range groups {
		groupSet[g] = true
	}
	var ids []uuid.UUID
	for _, p := range prices {
		if p.Status != PriceActive {
			continue
		}
		if p.Currency != money.CNY && p.Currency != money.USD {
			continue
		}
		if !priceInWindow(p, now) {
			continue
		}
		if p.UserGroupID == nil {
			ids = append(ids, p.ID)
			continue
		}
		if visibility == VisibilityGroup && groupSet[*p.UserGroupID] {
			ids = append(ids, p.ID)
		}
	}
	return ids
}
