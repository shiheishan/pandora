package service

// 后台开单预览：落点规则同 planPlacements，每一项按建单同样的规则先试一遍——
// 目录（续一期 UseRenewal、叠加 UseStack、换套餐 UseChange 不看可见性，另开一份 UseNew 按这个用户看可见性）、未完结的续费、叠加或变更单——
// 开不了的项在 Unavailable 上写明建单会回的那句话，默认值不落在开不了的项上。
// 折算基数、未完结单都按批读（候选最多 maxCandidates 份）。

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// previewPrice 是预览用的价格档：给了且属于这个套餐就用它（ok 为真，报应付）；
// 周期取它，没给取套餐最早建的在售价格，都没有按一个月。
func previewPrice(plan catalogsvc.PlanDetail, priceID *uuid.UUID) (catalogsvc.Price, string, int, bool) {
	if priceID != nil {
		for _, pr := range plan.Prices {
			if pr.ID == *priceID {
				return pr, pr.BillingInterval, pr.IntervalCount, true
			}
		}
		return catalogsvc.Price{}, "month", 1, false
	}
	var first *catalogsvc.Price
	for i := range plan.Prices {
		pr := &plan.Prices[i]
		if pr.Status != catalogsvc.PriceActive {
			continue
		}
		if first == nil || pr.CreatedAt.Before(first.CreatedAt) {
			first = pr
		}
	}
	if first == nil {
		return catalogsvc.Price{}, "month", 1, false
	}
	return catalogsvc.Price{}, first.BillingInterval, first.IntervalCount, false
}

// refusal 把目录或业务拒绝转成预览上的一句话；内部错误照常返回。
func refusal(err error) (string, error) {
	if err == nil {
		return "", nil
	}
	e := errs.As(err)
	if e.Code == errs.Internal {
		return "", err
	}
	return e.Message, nil
}

// catalogRefusals 按建单同样的 Offer 用途试一遍价格档：每种落点动作一个拒绝原因（空串是能开）。没给价格档时不查。
func (s *Service) catalogRefusals(ctx context.Context, in ManualPreviewInput, opts []Placement) (map[string]string, error) {
	out := map[string]string{}
	if in.PriceID == nil {
		return out, nil
	}
	uses := map[string]catalogsvc.OfferRequest{}
	uid, planID, now := in.UserID, in.PlanID, s.clock()
	for _, op := range opts {
		if _, done := uses[op.Kind]; done {
			continue
		}
		req := catalogsvc.OfferRequest{UserID: &uid, PlanID: &planID, Now: now}
		switch op.Kind {
		case PlacementRenew:
			req.Use, req.Manual = catalogsvc.UseRenewal, true
		case PlacementStack:
			req.Use, req.Manual = catalogsvc.UseStack, true
		case PlacementChange:
			req.Use, req.Manual = catalogsvc.UseChange, true
		default:
			req.Use, req.Manual = catalogsvc.UseNew, false
		}
		uses[op.Kind] = req
	}
	for kind, req := range uses {
		_, err := s.catalog.Offer(ctx, *in.PriceID, req)
		msg, err := refusal(err)
		if err != nil {
			return nil, err
		}
		out[kind] = msg
	}
	return out, nil
}

// ManualPreview 后台开单前看「这单落到哪一份」：从订阅行进来时预选那一份。只读。
func (s *Service) ManualPreview(ctx context.Context, in ManualPreviewInput) (ManualPreview, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return ManualPreview{}, err
	}
	if err := s.requireUser(ctx, in.UserID); err != nil {
		return ManualPreview{}, err
	}
	plan, err := s.catalog.GetPlan(ctx, in.PlanID)
	if err != nil {
		return ManualPreview{}, err
	}
	price, interval, count, priced := previewPrice(plan, in.PriceID)
	out := ManualPreview{Options: []Placement{}, MinPayment: decimal.Zero}
	if priced {
		if out.MinPayment, err = s.payments.MinPayment(ctx); err != nil {
			return ManualPreview{}, err
		}
	}
	us, err := s.subs.ByUser(ctx, in.UserID)
	if err != nil {
		return ManualPreview{}, err
	}
	cands := candidatesFrom(us)
	opts, def := planPlacements(in.PlanID, plan.Plan.Kind, cands, in.EntrySubscriptionID)

	refused, err := s.catalogRefusals(ctx, in, opts)
	if err != nil {
		return ManualPreview{}, err
	}
	byID := make(map[uuid.UUID]candidate, len(cands))
	var subIDs []uuid.UUID
	var changeSrcs []basisSource
	for _, c := range cands {
		byID[c.Sub.ID] = c
	}
	for _, op := range opts {
		if op.SubscriptionID == nil {
			continue
		}
		subIDs = append(subIDs, *op.SubscriptionID)
		if op.Kind == PlacementChange {
			c := byID[*op.SubscriptionID]
			changeSrcs = append(changeSrcs, basisSource{Sub: c.Sub, Kind: c.PlanKind, Quotas: c.Quotas})
		}
	}
	open, err := s.repo.OpenChangeSubscriptions(ctx, subIDs)
	if err != nil {
		return ManualPreview{}, err
	}
	bases, err := s.loadBases(ctx, changeSrcs)
	if err != nil {
		return ManualPreview{}, err
	}

	now := s.clock()
	snapshot := subsvc.PriceSnapshot{Interval: interval, IntervalCount: count}
	if priced {
		snapshot.PriceID = &price.ID
		snapshot.Amount = price.UnitAmount
		snapshot.ValidityDays = price.ValidityDays
	}
	for _, op := range opts {
		end := newPeriodEnd(op, now, snapshot)
		op.NewPeriodEnd = &end
		op.Credit, op.Due = decimal.Zero, decimal.Zero
		op.Unavailable = refused[op.Kind]
		if op.SubscriptionID != nil && open[*op.SubscriptionID] && op.Unavailable == "" {
			op.Unavailable = ErrOpenChange.Message
		}
		if op.Kind == PlacementChange && op.SubscriptionID != nil {
			b := bases[*op.SubscriptionID]
			op.Credit, _ = prorationCredit(b, now)
			op.GiftDaysLost = giftedDaysLeft(b, now)
		}
		if priced {
			credit := decimal.Zero
			if op.Kind == PlacementChange {
				credit = op.Credit
			}
			op.Due = OrderTotal(price.UnitAmount, decimal.Zero, credit)
			op.BelowMinimum = manualDueBelowMinimum(op.Due, out.MinPayment)
		}
		if op.Key == def && op.Unavailable != "" {
			def = ""
		}
		out.Options = append(out.Options, op)
	}
	out.DefaultKey = def
	return out, nil
}
