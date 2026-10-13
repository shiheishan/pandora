package service

import (
	"context"
	"errors"
	"sort"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ListPlans 后台套餐列表：全部套餐（含已归档），按 sort_order, created_at, id。
func (s *Service) ListPlans(ctx context.Context) ([]PlanSummary, error) {
	rows, err := s.repo.ListPlans(ctx)
	if err != nil {
		return nil, catalogErr(err)
	}
	plans, err := s.plansWithGroups(ctx, rows)
	if err != nil {
		return nil, catalogErr(err)
	}
	if len(plans) == 0 {
		return []PlanSummary{}, nil
	}

	planIDs := make([]uuid.UUID, len(plans))
	var versionIDs []uuid.UUID
	for i, p := range plans {
		planIDs[i] = p.ID
		if p.CurrentVersionID != nil {
			versionIDs = append(versionIDs, *p.CurrentVersionID)
		}
	}

	versionByID := map[uuid.UUID]Version{}
	if len(versionIDs) > 0 {
		vrows, err := s.repo.VersionsByIDs(ctx, versionIDs)
		if err != nil {
			return nil, catalogErr(err)
		}
		versions, err := s.versionsWithChildren(ctx, vrows)
		if err != nil {
			return nil, catalogErr(err)
		}
		for _, v := range versions {
			versionByID[v.ID] = v
		}
	}

	draftMap, err := s.repo.DraftVersionIDs(ctx, planIDs)
	if err != nil {
		return nil, catalogErr(err)
	}

	priceRows, err := s.repo.PricesByPlans(ctx, planIDs)
	if err != nil {
		return nil, catalogErr(err)
	}
	pricesByPlan := make(map[uuid.UUID][]Price)
	for _, row := range priceRows {
		pricesByPlan[row.PlanID] = append(pricesByPlan[row.PlanID], priceFromModel(row))
	}

	out := make([]PlanSummary, 0, len(plans))
	for _, p := range plans {
		sum := PlanSummary{Plan: p, Prices: []Price{}}
		if id, ok := draftMap[p.ID]; ok {
			sum.DraftVersionID = &id
		}
		if p.CurrentVersionID != nil {
			if v, ok := versionByID[*p.CurrentVersionID]; ok {
				ver := v.Version
				sum.Version = &ver
				sum.MaxDevices = v.MaxDevices
				sum.TrafficLimit = v.TrafficLimit()
			}
		}
		if ps := pricesByPlan[p.ID]; len(ps) > 0 {
			sortPricesForList(ps)
			sum.Prices = ps
		}
		out = append(out, sum)
	}
	return out, nil
}

func sortPricesForList(ps []Price) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Status != ps[j].Status {
			return ps[i].Status < ps[j].Status
		}
		if c := ps[i].UnitAmount.Cmp(ps[j].UnitAmount); c != 0 {
			return c < 0
		}
		return ps[i].ID.String() < ps[j].ID.String()
	})
}

// GetPlan 后台套餐详情。
func (s *Service) GetPlan(ctx context.Context, planID uuid.UUID) (PlanDetail, error) {
	detail, err := s.loadDetail(ctx, planID)
	return detail, catalogErr(err)
}

// loadDetail 读详情（可在事务里调用）。
func (s *Service) loadDetail(ctx context.Context, planID uuid.UUID) (PlanDetail, error) {
	row, found, err := s.repo.PlanByID(ctx, planID, repo.NoLock)
	if err != nil {
		return PlanDetail{}, err
	}
	if !found {
		return PlanDetail{}, ErrNotFound
	}
	plans, err := s.plansWithGroups(ctx, []model.Plan{row})
	if err != nil {
		return PlanDetail{}, err
	}
	plan := plans[0]

	vrows, err := s.repo.VersionsByPlan(ctx, planID)
	if err != nil {
		return PlanDetail{}, err
	}
	versions, err := s.versionsWithChildren(ctx, vrows)
	if err != nil {
		return PlanDetail{}, err
	}
	if versions == nil {
		versions = []Version{}
	}
	if len(versions) > 0 {
		versionIDs := make([]uuid.UUID, len(versions))
		for i, v := range versions {
			versionIDs[i] = v.ID
		}
		poolMap, err := s.repo.VersionPools(ctx, versionIDs)
		if err != nil {
			return PlanDetail{}, err
		}
		for i := range versions {
			pools := poolMap[versions[i].ID]
			if pools == nil {
				pools = []uuid.UUID{}
			}
			versions[i].PoolIDs = pools
		}
	}

	creatorIDs := make([]uuid.UUID, 0)
	seen := map[uuid.UUID]bool{}
	for _, v := range versions {
		if v.CreatedBy != nil && !seen[*v.CreatedBy] {
			seen[*v.CreatedBy] = true
			creatorIDs = append(creatorIDs, *v.CreatedBy)
		}
	}
	if len(creatorIDs) > 0 {
		emails, err := s.users.Emails(ctx, creatorIDs)
		if err != nil {
			return PlanDetail{}, err
		}
		for i := range versions {
			if versions[i].CreatedBy == nil {
				continue
			}
			if email, ok := emails[*versions[i].CreatedBy]; ok {
				e := email
				versions[i].CreatedByEmail = &e
			}
		}
	}

	prows, err := s.repo.PricesByPlans(ctx, []uuid.UUID{planID})
	if err != nil {
		return PlanDetail{}, err
	}
	prices := make([]Price, 0, len(prows))
	for _, pr := range prows {
		prices = append(prices, priceFromModel(pr))
	}
	if prices == nil {
		prices = []Price{}
	}

	return PlanDetail{
		Plan:     plan,
		Versions: versions,
		Prices:   prices,
	}, nil
}

// CreatePlan 新建草稿套餐。
func (s *Service) CreatePlan(ctx context.Context, in PlanInput) (PlanDetail, error) {
	if err := preparePlanInput(&in); err != nil {
		return PlanDetail{}, err
	}
	var planID uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		id, err := s.createPlanTx(ctx, in)
		if err != nil {
			return err
		}
		planID = id
		return nil
	})
	if err != nil {
		return PlanDetail{}, catalogErr(err)
	}
	return s.GetPlan(ctx, planID)
}

// createPlanTx 插入草稿套餐并写审计，返回新 id。
func (s *Service) createPlanTx(ctx context.Context, in PlanInput) (uuid.UUID, error) {
	hl, err := encodeHighlights(in.Highlights)
	if err != nil {
		return uuid.Nil, err
	}
	m := &model.Plan{
		Code:                 in.Code,
		Name:                 in.Name,
		Description:          in.Description,
		Status:               PlanDraft,
		Visibility:           in.Visibility,
		VisibleFrom:          in.VisibleFrom,
		VisibleUntil:         in.VisibleUntil,
		AllowNewPurchase:     in.AllowNewPurchase,
		AllowRenewal:         in.AllowRenewal,
		AllowUpgrade:         in.AllowUpgrade,
		PurchaseLimitPerUser: in.PurchaseLimitPerUser,
		StockTotal:           in.StockTotal,
		SortOrder:            in.SortOrder,
		Highlights:           hl,
		Recommended:          in.Recommended,
	}
	if err := s.repo.InsertPlan(ctx, m); err != nil {
		return uuid.Nil, err
	}
	if len(in.VisibleGroupIDs) > 0 {
		if err := s.repo.SetVisibleGroups(ctx, m.ID, in.VisibleGroupIDs); err != nil {
			if errors.Is(err, repo.ErrForeignKey) {
				return uuid.Nil, errs.Invalid(map[string]string{"visible_group_ids": "用户组不存在"})
			}
			return uuid.Nil, err
		}
	}
	if err := s.writeAudit(ctx, ActionPlanCreate, ResourcePlan, m.ID, map[string]any{
		"after": map[string]any{"code": in.Code, "visibility": in.Visibility},
	}); err != nil {
		return uuid.Nil, err
	}
	return m.ID, nil
}

// UpdatePlan 整体改写套餐资料，返回新的 row_version。
func (s *Service) UpdatePlan(ctx context.Context, planID uuid.UUID, expected int64, in PlanInput) (int64, error) {
	if expected <= 0 {
		return 0, errs.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	if err := preparePlanInput(&in); err != nil {
		return 0, err
	}
	var next int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		n, err := s.updatePlanTx(ctx, planID, expected, in)
		if err != nil {
			return err
		}
		next = n
		return nil
	})
	return next, catalogErr(err)
}

func (s *Service) updatePlanTx(ctx context.Context, planID uuid.UUID, expected int64, in PlanInput) (int64, error) {
	cur, found, err := s.repo.PlanByID(ctx, planID, repo.ForUpdate)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrNotFound
	}
	if cur.Status == PlanArchived {
		return 0, errs.Newf(errs.Conflict, "已归档套餐不能恢复或编辑")
	}
	if cur.RowVersion != expected {
		return 0, rowConflict("套餐", cur.RowVersion)
	}
	minStock := cur.StockSold + cur.StockReserved
	if in.StockTotal != nil && *in.StockTotal < minStock {
		return 0, errs.Invalid(map[string]string{"stock_total": "不能低于已售与已预留库存之和"})
	}

	hl, err := encodeHighlights(in.Highlights)
	if err != nil {
		return 0, err
	}
	mv := &model.Plan{
		Code:                 in.Code,
		Name:                 in.Name,
		Description:          in.Description,
		Visibility:           in.Visibility,
		VisibleFrom:          in.VisibleFrom,
		VisibleUntil:         in.VisibleUntil,
		AllowNewPurchase:     in.AllowNewPurchase,
		AllowRenewal:         in.AllowRenewal,
		AllowUpgrade:         in.AllowUpgrade,
		PurchaseLimitPerUser: in.PurchaseLimitPerUser,
		StockTotal:           in.StockTotal,
		SortOrder:            in.SortOrder,
		Highlights:           hl,
		Recommended:          in.Recommended,
	}
	ok, err := s.repo.UpdatePlanProfile(ctx, planID, expected, mv)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, rowConflict("套餐", cur.RowVersion)
	}

	groups := in.VisibleGroupIDs
	if in.Visibility != VisibilityGroup {
		groups = []uuid.UUID{}
	}
	if err := s.repo.SetVisibleGroups(ctx, planID, groups); err != nil {
		if errors.Is(err, repo.ErrForeignKey) {
			return 0, errs.Invalid(map[string]string{"visible_group_ids": "用户组不存在"})
		}
		return 0, err
	}

	next := expected + 1
	if err := s.writeAudit(ctx, ActionPlanUpdate, ResourcePlan, planID, map[string]any{
		"before": map[string]any{"row_version": expected},
		"after":  map[string]any{"row_version": next, "code": in.Code},
	}); err != nil {
		return 0, err
	}
	return next, nil
}

// ArchivePlan 归档套餐（不可恢复），返回新的 row_version。
func (s *Service) ArchivePlan(ctx context.Context, planID uuid.UUID, expected int64) (int64, error) {
	if expected <= 0 {
		return 0, errs.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	var next int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, found, err := s.repo.PlanByID(ctx, planID, repo.ForUpdate)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		if cur.RowVersion != expected {
			return rowConflict("套餐", cur.RowVersion)
		}
		if !CanTransitionPlan(cur.Status, PlanArchived) {
			return errs.Newf(errs.Conflict, "套餐已经归档")
		}
		ok, err := s.repo.ArchivePlan(ctx, planID, expected)
		if err != nil {
			return err
		}
		if !ok {
			return rowConflict("套餐", cur.RowVersion)
		}
		next = expected + 1
		return s.writeAudit(ctx, ActionPlanArchive, ResourcePlan, planID, map[string]any{
			"before": map[string]any{"status": cur.Status, "row_version": expected},
			"after":  map[string]any{"status": PlanArchived, "row_version": next},
		})
	})
	return next, catalogErr(err)
}
