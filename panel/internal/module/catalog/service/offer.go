package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
)

// offerNow 返回 req.Now，为零值时 s.now()。
func (s *Service) offerNow(req OfferRequest) time.Time {
	if req.Now.IsZero() {
		return s.now()
	}
	return req.Now
}

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

// currentVersions 批量取这些套餐的当前版本：收齐非 nil 的 CurrentVersionID，一次 repo.VersionsByIDs → s.versionsWithChildren，返回 版本 id → Version。
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
		out[v.ID] = v
	}
	return out, nil
}

func planLock(req OfferRequest) repo.Lock {
	if req.Lock {
		return repo.ForShare
	}
	return repo.NoLock
}

func packLock(lock bool) repo.Lock {
	if lock {
		return repo.ForShare
	}
	return repo.NoLock
}

// Offer 按价格取下单快照（新购、续费、换套餐目标共用）。
func (s *Service) Offer(ctx context.Context, priceID uuid.UUID, req OfferRequest) (Offer, error) {
	now := s.offerNow(req)
	v, err := s.offerViewer(ctx, req)
	if err != nil {
		return Offer{}, catalogErr(err)
	}

	mPrice, found, err := s.repo.PriceByID(ctx, priceID, repo.NoLock)
	if err != nil {
		return Offer{}, catalogErr(err)
	}
	if !found {
		if req.Use == UseRenewal {
			return Offer{}, ErrRenewPriceGone
		}
		return Offer{}, ErrNotFound
	}
	price := priceFromModel(mPrice)

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
		mPrice, found, err = s.repo.PriceByID(ctx, priceID, repo.ForShare)
		if err != nil {
			return Offer{}, catalogErr(err)
		}
		if !found {
			if req.Use == UseRenewal {
				return Offer{}, ErrRenewPriceGone
			}
			return Offer{}, ErrNotFound
		}
		price = priceFromModel(mPrice)
	}

	if err := planRefusal(plan, req.Use, v, req.Manual, now); err != nil {
		return Offer{}, err
	}
	if err := priceRefusal(price, req.Use, v, req.Manual, now); err != nil {
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

// PlanOffers 取一个套餐对这次用途能用的价格。
func (s *Service) PlanOffers(ctx context.Context, planID uuid.UUID, req OfferRequest) (OfferPlan, error) {
	now := s.offerNow(req)
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

	if err := planRefusal(plan, req.Use, v, req.Manual, now); err != nil {
		return OfferPlan{}, err
	}

	mPrices, err := s.repo.ActivePrices(ctx, []uuid.UUID{planID}, lk)
	if err != nil {
		return OfferPlan{}, catalogErr(err)
	}
	prices := make([]Price, 0, len(mPrices))
	for _, mp := range mPrices {
		prices = append(prices, priceFromModel(mp))
	}
	prices = usablePrices(prices, req.Use, v, req.Manual, now)

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

// ListOffers 列出对这次用途可用的套餐（换套餐目标列表等）。
func (s *Service) ListOffers(ctx context.Context, req OfferRequest) ([]OfferPlan, error) {
	now := s.offerNow(req)
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
		if planRefusal(p, req.Use, v, req.Manual, now) != nil {
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
		prices := usablePrices(byPlan[p.ID], req.Use, v, req.Manual, now)
		if prices == nil {
			prices = []Price{}
		}
		out = append(out, OfferPlan{Plan: p, Version: ver, Prices: prices})
	}
	return out, nil
}

// PortalCatalog 门户目录（匿名也能看）。
func (s *Service) PortalCatalog(ctx context.Context, userID *uuid.UUID) ([]OfferPlan, error) {
	req := OfferRequest{UserID: userID}
	now := s.offerNow(req)
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

	visible := make([]Plan, 0, len(plans))
	planIDs := make([]uuid.UUID, 0, len(plans))
	for _, p := range plans {
		if !visibleTo(p, v, now) {
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
		prices := usablePrices(byPlan[p.ID], UseNew, v, false, now)
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

// PlansByIDs 批量取套餐（含已归档），给订单、订阅列表展示用；一条 SQL 读套餐 + 一次可见组。
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

// VersionsByIDs 批量取版本（含权益与配额）。
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

// PricesByIDs 批量取价格（含已归档）。
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

// TrafficPacksByIDs 批量取流量包（含已下架）。
func (s *Service) TrafficPacksByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]TrafficPack, error) {
	out := make(map[uuid.UUID]TrafficPack)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.repo.PacksByIDs(ctx, ids)
	if err != nil {
		return nil, catalogErr(err)
	}
	for _, mp := range rows {
		p := packFromModel(mp)
		out[p.ID] = p
	}
	return out, nil
}

// PackOffer 取一个可买的流量包。
func (s *Service) PackOffer(ctx context.Context, packID uuid.UUID, lock bool) (TrafficPack, error) {
	mp, found, err := s.repo.PackByID(ctx, packID, packLock(lock))
	if err != nil {
		return TrafficPack{}, catalogErr(err)
	}
	if !found {
		return TrafficPack{}, ErrNotFound
	}
	p := packFromModel(mp)
	if p.Status != PackActive {
		return TrafficPack{}, ErrPackArchived
	}
	return p, nil
}
