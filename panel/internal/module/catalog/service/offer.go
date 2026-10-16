package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// offerViewer 由 req 拼 viewer：LoggedIn = req.UserID 非 nil 且非 uuid.Nil；GroupID = s.viewerGroup(ctx, req.UserID)。
func (s *Service) offerViewer(ctx context.Context, req OfferRequest) (viewer, error) {
	loggedIn := req.UserID != nil && *req.UserID != uuid.Nil
	if loggedIn {
		gid, err := s.viewerGroup(ctx, req.UserID)
		if err != nil {
			return viewer{}, err
		}
		return viewer{LoggedIn: true, GroupID: gid}, nil
	}
	return viewer{LoggedIn: false, GroupID: nil}, nil
}

// currentVersions 批量取这些套餐的当前版本：收齐非 nil 的 CurrentVersionID，固定 3 条 SQL（版本、权益、配额），返回 版本 id → Version。
// 库里只保证当前版本属于本套餐，不保证已发布；不是 published 的版本不放进结果，调用方按「没有发布版本」处理。
func (s *Service) currentVersions(ctx context.Context, plans []Plan) (map[uuid.UUID]Version, error) {
	ids := make([]uuid.UUID, 0, len(plans))
	for _, p := range plans {
		if p.CurrentVersionID != nil && *p.CurrentVersionID != uuid.Nil {
			ids = append(ids, *p.CurrentVersionID)
		}
	}
	if len(ids) == 0 {
		return map[uuid.UUID]Version{}, nil
	}
	rows, err := s.repo.VersionsByIDs(ctx, ids)
	if err != nil {
		return nil, catalogErr(err)
	}
	versions, err := s.versionsWithChildren(ctx, rows)
	if err != nil {
		return nil, catalogErr(err)
	}
	out := make(map[uuid.UUID]Version, len(versions))
	for _, v := range versions {
		if v.Status != VersionPublished {
			continue
		}
		out[v.ID] = v
	}
	return out, nil
}

// planLock：建单时套餐行用 FOR SHARE（与后台改目录互斥）。
func planLock(req OfferRequest) repo.Lock {
	if req.Lock {
		return repo.ForShare
	}
	return repo.NoLock
}

// priceLock：建单时价格行用 FOR SHARE（与后台归档价格互斥）。
func priceLock(req OfferRequest) repo.Lock {
	if req.Lock {
		return repo.ForShare
	}
	return repo.NoLock
}

// priceGone 是价格不存在或不属于期望套餐时的错误：续费 ErrRenewPriceGone，其它 ErrNotFound。
func priceGone(use Use) error {
	if use == UseRenewal {
		return ErrRenewPriceGone
	}
	return ErrNotFound
}

// Offer 按价格取下单快照（新购、续费、换套餐目标共用）。
// req.PlanID 非 nil 时校验价格属于这个套餐（续费必须传订阅的套餐）：不属于时续费回 ErrRenewPriceGone，其它用途回 ErrNotFound。
// 返回的 Version 是套餐的当前发布版本；续费时订阅要沿用自己的版本，调用方另用 VersionsByIDs 取。
// req.Lock 为真时：先把套餐行 FOR SHARE，再把价格行 FOR SHARE，锁到事务结束。
func (s *Service) Offer(ctx context.Context, priceID uuid.UUID, req OfferRequest) (Offer, error) {
	v, err := s.offerViewer(ctx, req)
	if err != nil {
		return Offer{}, catalogErr(err)
	}

	mPrice, found, err := s.repo.PriceByID(ctx, priceID, repo.NoLock)
	if err != nil {
		return Offer{}, catalogErr(err)
	}
	if !found {
		return Offer{}, priceGone(req.Use)
	}
	price := priceFromModel(mPrice)
	// 价格的 plan_id 被冻结触发器钉死，这里查一次即可，锁后不必再查。
	if req.PlanID != nil && price.PlanID != *req.PlanID {
		return Offer{}, priceGone(req.Use)
	}

	lk := planLock(req)
	mPlan, found, err := s.repo.PlanByID(ctx, price.PlanID, lk)
	if err != nil {
		return Offer{}, catalogErr(err)
	}
	if !found {
		return Offer{}, ErrNotFound
	}
	plans, err := s.plansWithGroups(ctx, []model.Plan{mPlan})
	if err != nil {
		return Offer{}, catalogErr(err)
	}
	plan := plans[0]

	if req.Lock {
		mPrice, found, err = s.repo.PriceByID(ctx, priceID, priceLock(req))
		if err != nil {
			return Offer{}, catalogErr(err)
		}
		if !found {
			return Offer{}, priceGone(req.Use)
		}
		price = priceFromModel(mPrice)
	}

	if err := planRefusal(plan, req.Use, v, req.Manual); err != nil {
		return Offer{}, err
	}
	if err := priceRefusal(price, req.Use); err != nil {
		return Offer{}, err
	}

	if plan.CurrentVersionID == nil || *plan.CurrentVersionID == uuid.Nil {
		return Offer{}, ErrNoPublishedVersion
	}
	verMap, err := s.currentVersions(ctx, []Plan{plan})
	if err != nil {
		return Offer{}, err
	}
	ver, ok := verMap[*plan.CurrentVersionID]
	if !ok {
		return Offer{}, ErrNoPublishedVersion
	}

	return Offer{Plan: plan, Version: ver, Price: price}, nil
}

// PlanOffers 取一个套餐对这次用途能用的价格（req.PlanID 不看，套餐由 planID 指定）。
func (s *Service) PlanOffers(ctx context.Context, planID uuid.UUID, req OfferRequest) (OfferPlan, error) {
	v, err := s.offerViewer(ctx, req)
	if err != nil {
		return OfferPlan{}, catalogErr(err)
	}

	lk := planLock(req)
	mPlan, found, err := s.repo.PlanByID(ctx, planID, lk)
	if err != nil {
		return OfferPlan{}, catalogErr(err)
	}
	if !found {
		return OfferPlan{}, ErrNotFound
	}
	plans, err := s.plansWithGroups(ctx, []model.Plan{mPlan})
	if err != nil {
		return OfferPlan{}, catalogErr(err)
	}
	plan := plans[0]

	if err := planRefusal(plan, req.Use, v, req.Manual); err != nil {
		return OfferPlan{}, err
	}

	mPrices, err := s.repo.ActivePrices(ctx, []uuid.UUID{planID}, priceLock(req))
	if err != nil {
		return OfferPlan{}, catalogErr(err)
	}
	prices := make([]Price, 0, len(mPrices))
	for _, mp := range mPrices {
		prices = append(prices, priceFromModel(mp))
	}
	prices = usablePrices(prices, req.Use)
	sortPricesByTier(prices)

	if plan.CurrentVersionID == nil || *plan.CurrentVersionID == uuid.Nil {
		return OfferPlan{}, ErrNoPublishedVersion
	}
	verMap, err := s.currentVersions(ctx, []Plan{plan})
	if err != nil {
		return OfferPlan{}, err
	}
	ver, ok := verMap[*plan.CurrentVersionID]
	if !ok {
		return OfferPlan{}, ErrNoPublishedVersion
	}

	return OfferPlan{Plan: plan, Version: ver, Prices: prices}, nil
}

func groupActivePricesByPlan(mPrices []model.Price) map[uuid.UUID][]Price {
	byPlan := make(map[uuid.UUID][]Price)
	for _, mp := range mPrices {
		p := priceFromModel(mp)
		byPlan[p.PlanID] = append(byPlan[p.PlanID], p)
	}
	return byPlan
}

// ListOffers 列出对这次用途可用的套餐（换套餐目标列表等）；顺序沿用 SellablePlans（推荐在前，再按 created_at）。
func (s *Service) ListOffers(ctx context.Context, req OfferRequest) ([]OfferPlan, error) {
	v, err := s.offerViewer(ctx, req)
	if err != nil {
		return nil, catalogErr(err)
	}

	rows, err := s.repo.SellablePlans(ctx)
	if err != nil {
		return nil, catalogErr(err)
	}
	plans, err := s.plansWithGroups(ctx, rows)
	if err != nil {
		return nil, catalogErr(err)
	}

	eligible := make([]Plan, 0, len(plans))
	planIDs := make([]uuid.UUID, 0, len(plans))
	for _, p := range plans {
		if planRefusal(p, req.Use, v, req.Manual) != nil {
			continue
		}
		eligible = append(eligible, p)
		planIDs = append(planIDs, p.ID)
	}

	mPrices, err := s.repo.ActivePrices(ctx, planIDs, repo.NoLock)
	if err != nil {
		return nil, catalogErr(err)
	}
	byPlan := groupActivePricesByPlan(mPrices)

	verMap, err := s.currentVersions(ctx, eligible)
	if err != nil {
		return nil, err
	}

	out := make([]OfferPlan, 0, len(eligible))
	for _, p := range eligible {
		if p.CurrentVersionID == nil || *p.CurrentVersionID == uuid.Nil {
			continue
		}
		ver, ok := verMap[*p.CurrentVersionID]
		if !ok {
			continue
		}
		prices := usablePrices(byPlan[p.ID], req.Use)
		sortPricesByTier(prices)
		out = append(out, OfferPlan{Plan: p, Version: ver, Prices: prices})
	}
	return out, nil
}

// PortalCatalog 门户目录（匿名也能看）；用 UseNew 筛价格，没有可用价格的套餐不出现。
func (s *Service) PortalCatalog(ctx context.Context, userID *uuid.UUID) ([]OfferPlan, error) {
	req := OfferRequest{UserID: userID}
	v, err := s.offerViewer(ctx, req)
	if err != nil {
		// 令牌还有效但用户已被删：目录只是浏览，按匿名处理；下单路径仍报错。
		if errs.As(err).Code != errs.NotFound {
			return nil, catalogErr(err)
		}
		v = viewer{}
	}

	rows, err := s.repo.SellablePlans(ctx)
	if err != nil {
		return nil, catalogErr(err)
	}
	plans, err := s.plansWithGroups(ctx, rows)
	if err != nil {
		return nil, catalogErr(err)
	}

	visible := make([]Plan, 0, len(plans))
	planIDs := make([]uuid.UUID, 0, len(plans))
	for _, p := range plans {
		if !visibleTo(p, v) {
			continue
		}
		visible = append(visible, p)
		planIDs = append(planIDs, p.ID)
	}

	mPrices, err := s.repo.ActivePrices(ctx, planIDs, repo.NoLock)
	if err != nil {
		return nil, catalogErr(err)
	}
	byPlan := groupActivePricesByPlan(mPrices)

	verMap, err := s.currentVersions(ctx, visible)
	if err != nil {
		return nil, err
	}

	out := make([]OfferPlan, 0, len(visible))
	for _, p := range visible {
		if p.CurrentVersionID == nil || *p.CurrentVersionID == uuid.Nil {
			continue
		}
		ver, ok := verMap[*p.CurrentVersionID]
		if !ok {
			continue
		}
		prices := usablePrices(byPlan[p.ID], UseNew)
		sortPricesByTier(prices)
		if len(prices) == 0 {
			continue
		}
		out = append(out, OfferPlan{Plan: p, Version: ver, Prices: prices})
	}
	if out == nil {
		return []OfferPlan{}, nil
	}
	return out, nil
}

// PlansByIDs 批量取套餐（含已归档），给订单、订阅列表展示用；与 id 个数无关，固定 2 条 SQL（套餐、可见组）。
func (s *Service) PlansByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Plan, error) {
	out := make(map[uuid.UUID]Plan)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.repo.PlansByIDs(ctx, ids)
	if err != nil {
		return nil, catalogErr(err)
	}
	plans, err := s.plansWithGroups(ctx, rows)
	if err != nil {
		return nil, catalogErr(err)
	}
	for _, p := range plans {
		out[p.ID] = p
	}
	return out, nil
}

// VersionsByIDs 批量取版本（含权益与配额）；与 id 个数无关，固定 3 条 SQL（版本、权益、配额）。
func (s *Service) VersionsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Version, error) {
	out := make(map[uuid.UUID]Version)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.repo.VersionsByIDs(ctx, ids)
	if err != nil {
		return nil, catalogErr(err)
	}
	versions, err := s.versionsWithChildren(ctx, rows)
	if err != nil {
		return nil, catalogErr(err)
	}
	for _, v := range versions {
		out[v.ID] = v
	}
	return out, nil
}

// PricesByIDs 批量取价格（含已归档）；一条 SQL。
func (s *Service) PricesByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Price, error) {
	out := make(map[uuid.UUID]Price)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.repo.PricesByIDs(ctx, ids)
	if err != nil {
		return nil, catalogErr(err)
	}
	for _, mp := range rows {
		p := priceFromModel(mp)
		out[p.ID] = p
	}
	return out, nil
}
