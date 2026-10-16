package service

// 后台套餐的读与分块写：列表、详情、新建（一个事务建成套餐 + 草稿版本 + 线路 + 价格 + 可选发布）、
// 基本信息、销售、归档。流量重置在 reset.go，价格在 prices.go，用量与发布在 versions.go，线路在 plan_pools.go。

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// maxQRunes 是列表搜索词的最大长度。
const maxQRunes = 100

// ListPlans 后台套餐列表（含已归档）：每个套餐一行摘要 + 在售价格（按周期从短到长）。
func (s *Service) ListPlans(ctx context.Context, f PlanFilter) ([]PlanSummary, error) {
	fields := map[string]string{}
	switch f.Kind {
	case "", KindSubscription, KindOneTime:
	default:
		fields["kind"] = "不支持的套餐类型"
	}
	switch f.Status {
	case "", PlanDraft, PlanActive, PlanArchived:
	default:
		fields["status"] = "不支持的套餐状态"
	}
	f.Q = strings.TrimSpace(f.Q)
	if utf8.RuneCountInString(f.Q) > maxQRunes {
		fields["q"] = "最多 100 个字符"
	}
	if err := invalid(fields); err != nil {
		return nil, err
	}
	rows, err := s.repo.ListPlans(ctx, repo.PlanFilter{Kind: f.Kind, Status: f.Status, Q: f.Q})
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
	usage, err := s.planUsage(ctx, plans)
	if err != nil {
		return nil, catalogErr(err)
	}
	planIDs := make([]uuid.UUID, len(plans))
	for i, p := range plans {
		planIDs[i] = p.ID
	}
	prows, err := s.repo.ActivePrices(ctx, planIDs, repo.NoLock)
	if err != nil {
		return nil, catalogErr(err)
	}
	pricesByPlan := make(map[uuid.UUID][]Price, len(plans))
	for _, row := range prows {
		pricesByPlan[row.PlanID] = append(pricesByPlan[row.PlanID], priceFromModel(row))
	}
	out := make([]PlanSummary, 0, len(plans))
	for _, p := range plans {
		ps := pricesByPlan[p.ID]
		if ps == nil {
			ps = []Price{}
		}
		sortPricesByTier(ps)
		out = append(out, PlanSummary{Plan: p, PlanUsage: usage[p.ID], Prices: ps})
	}
	return out, nil
}

// planUsage 批量算套餐的用量摘要（版本号、草稿、流量、设备、限速、线路）。
// 查询次数与套餐个数无关：版本、草稿、线路各一条 SQL。
func (s *Service) planUsage(ctx context.Context, plans []Plan) (map[uuid.UUID]PlanUsage, error) {
	out := make(map[uuid.UUID]PlanUsage, len(plans))
	if len(plans) == 0 {
		return out, nil
	}
	planIDs := make([]uuid.UUID, len(plans))
	for i, p := range plans {
		planIDs[i] = p.ID
	}
	draftMap, err := s.repo.DraftVersionIDs(ctx, planIDs)
	if err != nil {
		return nil, err
	}
	versionIDs := make([]uuid.UUID, 0, 2*len(plans))
	for _, p := range plans {
		if p.CurrentVersionID != nil {
			versionIDs = append(versionIDs, *p.CurrentVersionID)
		}
		if id, ok := draftMap[p.ID]; ok {
			versionIDs = append(versionIDs, id)
		}
	}
	versionByID := make(map[uuid.UUID]Version, len(versionIDs))
	if len(versionIDs) > 0 {
		vrows, err := s.repo.VersionsByIDs(ctx, versionIDs)
		if err != nil {
			return nil, err
		}
		versions, err := s.versionsWithChildren(ctx, vrows)
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			versionByID[v.ID] = v
		}
	}
	poolMap, err := s.repo.PlanPools(ctx, planIDs)
	if err != nil {
		return nil, err
	}
	for _, p := range plans {
		u := PlanUsage{PoolIDs: []uuid.UUID{}}
		if pools := poolMap[p.ID]; len(pools) > 0 {
			u.PoolIDs = pools
		}
		var draft Version
		if id, ok := draftMap[p.ID]; ok {
			if v, ok := versionByID[id]; ok {
				draft = v
				did, dv := id, v.Version
				u.DraftVersionID = &did
				u.DraftVersion = &dv
			}
		}
		var cur Version
		if p.CurrentVersionID != nil {
			if v, ok := versionByID[*p.CurrentVersionID]; ok {
				cur = v
				ver := v.Version
				u.Version = &ver
			}
		}
		// 用量来源：当前发布版本优先，还没发布过取草稿版本。
		v := cur
		if v.ID == uuid.Nil {
			v = draft
		}
		if v.ID != uuid.Nil {
			u.TrafficBytes = v.TrafficLimit()
			u.MaxDevices = v.MaxDevices
			u.ThrottleKbps = v.ThrottleKbps
		}
		out[p.ID] = u
	}
	return out, nil
}

// GetPlan 后台套餐详情。
func (s *Service) GetPlan(ctx context.Context, planID uuid.UUID) (PlanDetail, error) {
	detail, err := s.loadDetail(ctx, planID)
	return detail, catalogErr(err)
}

// loadDetail 读详情（可在事务里调，创建、发布后据此回显）。
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
	usage, err := s.planUsage(ctx, plans)
	if err != nil {
		return PlanDetail{}, err
	}

	vrows, err := s.repo.VersionsByPlan(ctx, planID)
	if err != nil {
		return PlanDetail{}, err
	}
	versions, err := s.versionsWithChildren(ctx, vrows)
	if err != nil {
		return PlanDetail{}, err
	}
	// 版本行的「谁建的」批量取邮箱（一条 SQL），账号已删的留空。
	creatorIDs := make([]uuid.UUID, 0, len(versions))
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
	return PlanDetail{
		Plan:      plans[0],
		PlanUsage: usage[planID],
		Versions:  versions,
		Prices:    prices,
	}, nil
}

// CreatePlan 新建套餐（先选类型）：套餐、草稿版本 1（用量）、线路、价格、可选发布，一个事务建成。
func (s *Service) CreatePlan(ctx context.Context, in PlanCreate) (PlanDetail, error) {
	if err := validatePlanCreate(&in); err != nil {
		return PlanDetail{}, err
	}
	var planID uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		code, err := s.nextPlanCode(ctx, in.Name)
		if err != nil {
			return err
		}
		m := &model.Plan{
			Code:             code,
			Name:             in.Name,
			Description:      in.Description,
			Kind:             in.Kind,
			Status:           PlanDraft,
			Visibility:       in.Sales.Visibility,
			AllowNewPurchase: in.Sales.AllowNewPurchase,
			AllowRenewal:     in.Sales.AllowRenewal,
			AllowUpgrade:     in.Sales.AllowUpgrade,
			Recommended:      in.Sales.Recommended,
		}
		if in.Reset != nil {
			price, limit := in.Reset.Price, in.Reset.Limit
			m.ResetPrice, m.ResetLimit = &price, limit
		}
		if err := s.repo.InsertPlan(ctx, m); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Newf(errs.Conflict, "同名套餐正在创建，请稍后重试")
			}
			return err
		}
		planID = m.ID
		s.catalogChanged(ctx, rtTablePlans, realtime.OpInsert, m.ID)
		if len(in.Sales.VisibleGroupIDs) > 0 {
			if err := s.repo.SetVisibleGroups(ctx, m.ID, in.Sales.VisibleGroupIDs); err != nil {
				if errors.Is(err, repo.ErrForeignKey) {
					return errs.Invalid(map[string]string{"visible_group_ids": "用户组不存在"})
				}
				return err
			}
		}
		var createdBy *uuid.UUID
		if p, ok := authn.From(ctx); ok && p.UserID != uuid.Nil {
			u := p.UserID
			createdBy = &u
		}
		v, err := s.repo.InsertNextVersion(ctx, m.ID, createdBy)
		if err != nil {
			return err
		}
		s.catalogChanged(ctx, rtTableVersions, realtime.OpInsert, v.ID)
		if err := s.writeUsageTx(ctx, in.Kind, v.ID, in.Usage, nil); err != nil {
			return err
		}
		if err := s.replacePoolsTx(ctx, m.ID, in.PoolIDs); err != nil {
			return err
		}
		if in.Kind == KindSubscription {
			for _, t := range in.Tiers {
				if _, err := s.insertPriceTx(ctx, m.ID, in.Kind, t.BillingInterval, t.IntervalCount, t.UnitAmount, nil); err != nil {
					return err
				}
			}
		} else {
			if _, err := s.insertPriceTx(ctx, m.ID, in.Kind, IntervalOneTime, 1, in.OneTime.UnitAmount, in.OneTime.ValidityDays); err != nil {
				return err
			}
		}
		if err := s.writeAudit(ctx, ActionPlanCreate, ResourcePlan, m.ID, map[string]any{
			"after": map[string]any{"code": code, "name": in.Name, "kind": in.Kind, "publish": in.Publish},
		}); err != nil {
			return err
		}
		if in.Publish {
			// InsertPlan 已回填 RowVersion = 1，直接拿它当发布时的乐观锁值。
			return s.publishTx(ctx, m.ID, v.ID, m.RowVersion)
		}
		return nil
	})
	if err != nil {
		return PlanDetail{}, catalogErr(err)
	}
	return s.GetPlan(ctx, planID)
}

// UpdateBasic 改名称与说明（代码不改），以套餐 row_version 做乐观锁。
func (s *Service) UpdateBasic(ctx context.Context, planID uuid.UUID, in BasicUpdate) (PlanDetail, error) {
	if err := checkExpected(in.ExpectedRowVersion); err != nil {
		return PlanDetail{}, err
	}
	fields := map[string]string{}
	prepareBasic(&in.Name, &in.Description, fields)
	if err := invalid(fields); err != nil {
		return PlanDetail{}, err
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, err := s.lockEditablePlan(ctx, planID, in.ExpectedRowVersion)
		if err != nil {
			return err
		}
		ok, err := s.repo.UpdatePlanBasic(ctx, planID, in.ExpectedRowVersion, in.Name, in.Description)
		if err != nil {
			return err
		}
		if !ok {
			return rowConflict("套餐", cur.RowVersion)
		}
		s.catalogChanged(ctx, rtTablePlans, realtime.OpUpdate, planID)
		return s.writeAudit(ctx, ActionPlanUpdate, ResourcePlan, planID, map[string]any{
			"before": map[string]any{"name": cur.Name, "row_version": cur.RowVersion},
			"after":  map[string]any{"name": in.Name, "row_version": in.ExpectedRowVersion + 1},
		})
	})
	if err != nil {
		return PlanDetail{}, catalogErr(err)
	}
	return s.GetPlan(ctx, planID)
}

// UpdateSales 改销售设置（可见范围、新购、续费、变更、推荐），以套餐 row_version 做乐观锁。
func (s *Service) UpdateSales(ctx context.Context, planID uuid.UUID, in SalesUpdate) (PlanDetail, error) {
	if err := checkExpected(in.ExpectedRowVersion); err != nil {
		return PlanDetail{}, err
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, err := s.lockEditablePlan(ctx, planID, in.ExpectedRowVersion)
		if err != nil {
			return err
		}
		fields := map[string]string{}
		validateSales(cur.Kind, &in.Sales, fields)
		if err := invalid(fields); err != nil {
			return err
		}
		ok, err := s.repo.UpdatePlanSales(ctx, planID, in.ExpectedRowVersion, &model.Plan{
			Visibility:       in.Sales.Visibility,
			AllowNewPurchase: in.Sales.AllowNewPurchase,
			AllowRenewal:     in.Sales.AllowRenewal,
			AllowUpgrade:     in.Sales.AllowUpgrade,
			Recommended:      in.Sales.Recommended,
		})
		if err != nil {
			return err
		}
		if !ok {
			return rowConflict("套餐", cur.RowVersion)
		}
		groups := in.Sales.VisibleGroupIDs
		if in.Sales.Visibility != VisibilityGroup {
			groups = []uuid.UUID{}
		}
		if err := s.repo.SetVisibleGroups(ctx, planID, groups); err != nil {
			if errors.Is(err, repo.ErrForeignKey) {
				return errs.Invalid(map[string]string{"visible_group_ids": "用户组不存在"})
			}
			return err
		}
		s.catalogChanged(ctx, rtTablePlans, realtime.OpUpdate, planID)
		return s.writeAudit(ctx, ActionPlanSales, ResourcePlan, planID, map[string]any{
			"before": map[string]any{
				"visibility": cur.Visibility, "allow_new_purchase": cur.AllowNewPurchase,
				"allow_renewal": cur.AllowRenewal, "allow_upgrade": cur.AllowUpgrade,
				"recommended": cur.Recommended, "row_version": cur.RowVersion,
			},
			"after": map[string]any{
				"visibility": in.Sales.Visibility, "allow_new_purchase": in.Sales.AllowNewPurchase,
				"allow_renewal": in.Sales.AllowRenewal, "allow_upgrade": in.Sales.AllowUpgrade,
				"recommended": in.Sales.Recommended, "row_version": in.ExpectedRowVersion + 1,
			},
		})
	})
	if err != nil {
		return PlanDetail{}, catalogErr(err)
	}
	return s.GetPlan(ctx, planID)
}

// ArchivePlan 归档套餐（不可恢复）：线路不动，老用户到期前照常用。
func (s *Service) ArchivePlan(ctx context.Context, planID uuid.UUID, expected int64) (PlanDetail, error) {
	if err := checkExpected(expected); err != nil {
		return PlanDetail{}, err
	}
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
			return ErrPlanArchived
		}
		ok, err := s.repo.ArchivePlan(ctx, planID, expected)
		if err != nil {
			return err
		}
		if !ok {
			return rowConflict("套餐", cur.RowVersion)
		}
		s.catalogChanged(ctx, rtTablePlans, realtime.OpUpdate, planID)
		return s.writeAudit(ctx, ActionPlanArchive, ResourcePlan, planID, map[string]any{
			"before": map[string]any{"status": cur.Status, "row_version": cur.RowVersion},
			"after":  map[string]any{"status": PlanArchived, "row_version": expected + 1},
		})
	})
	if err != nil {
		return PlanDetail{}, catalogErr(err)
	}
	return s.GetPlan(ctx, planID)
}
