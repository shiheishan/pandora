package service

// 换套餐是在原订阅上换一个套餐，不是开第二条订阅，可以跨类型（订阅 ↔ 一次性，即落点里的「换掉」）：
//
//	金额   新价（优惠后）减原订阅的剩余价值：为正就补差价；为负就 total = 0，差额在履约时退进余额
//	周期   从履约那一刻按新价格档重开一整期（subscription.ChangePlan）
//	零头   门户换套餐抵扣后付不了的零头（≤ 0.99）可以免，单独记 waived_amount，不并进券折扣
//
// 剩余价值口径：原订阅是订阅套餐按剩余付费时间比例，是一次性套餐按剩余流量比例、有天数时再按剩余天数、取较小（细节在 proration.go）。
// 升级与降级都是 kind='upgrade'；方向由金额决定。剩余价值在下单那一刻按报价时刻（as_of）算定、写进订单
// （proration_credit_amount），之后随订单冻结；为了让这个数在付款前不失效，同一条订阅同时只允许一张
// 未完结的续费或变更单（库里 orders_one_open_subscription_change 兜底）。
//
// 锁序：订单（结算时）→ 订阅 → 券 → 账务科目。建单没有订单锁，先锁订阅；目标套餐行不锁（价格行冻结不可改，
// 与续费同一取舍）。

import (
	"context"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// checkChangeSource 校验原订阅此刻能换：未停用（已停用回 ErrRenewalWindowClosed），且没有未完结的续费或变更单。
func (s *Service) checkChangeSource(ctx context.Context, sub subsvc.Subscription, now time.Time) error {
	if !sub.StateAt(now).AcceptsChange() {
		return subsvc.ErrRenewalWindowClosed
	}
	return s.openChangeConflict(ctx, sub.ID, now)
}

// changeKindWords 是续费、换套餐、叠加单在提示里的叫法。
var changeKindWords = map[string]string{KindRenewal: "续费", KindUpgrade: "换套餐", KindStack: "叠加"}

// openChangeConflict 这份订阅挂着未完结的续费、叠加或换套餐单时拒绝再建：
// 待付的回 order_pending 带 fields.order_id（过了付款期限再带 lapsed），门户据此给「去付款 / 取消它」；
// paid 只在结算事务里短暂出现、别的事务读不到，那一支只是防御，回普通冲突。没有就回 nil。
func (s *Service) openChangeConflict(ctx context.Context, subID uuid.UUID, now time.Time) error {
	o, ok, err := s.repo.OpenChangeOrder(ctx, subID)
	if err != nil || !ok {
		return err
	}
	word := changeKindWords[o.Kind]
	if o.Status != StatusPending {
		return errs.Newf(errs.Conflict, "这份套餐有一张"+word+"订单正在入账，稍后再试")
	}
	lapsed := !o.ExpiresAt.After(now)
	msg := "这份套餐还有一张没付款的" + word + "订单，继续付款或取消后再操作"
	if lapsed {
		msg = "这份套餐还有一张已超过付款期限的" + word + "订单，取消后再操作"
	}
	e := errs.Newf(errs.OrderPending, msg)
	e.Fields = map[string]string{"order_id": o.ID.String()}
	if lapsed {
		e.Fields["lapsed"] = "true"
	}
	return e
}

// quoteSource 是换套餐报价的一份原订阅与它的折算输入。
type quoteSource struct {
	sub   subsvc.Subscription
	basis prorationBasis
}

// changeSourcesFor 是「把哪一份换成 planID」的候选：本人未停用、不是这个套餐、没有未完结续费或变更单的订阅，
// 最多 maxQuoteRows 份。未完结单与折算基数都按批读。
func (s *Service) changeSourcesFor(ctx context.Context, userID, planID uuid.UUID) ([]quoteSource, error) {
	us, err := s.subs.ByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	var cands []subsvc.Summary
	ids := make([]uuid.UUID, 0, len(us.Subscriptions))
	for _, sum := range us.Subscriptions {
		if sum.State == subsvc.StateEnded || sum.PlanID == planID {
			continue
		}
		cands = append(cands, sum)
		ids = append(ids, sum.ID)
	}
	open, err := s.repo.OpenChangeSubscriptions(ctx, ids)
	if err != nil {
		return nil, err
	}
	srcs := make([]basisSource, 0, len(cands))
	for _, sum := range cands {
		if open[sum.ID] {
			continue // 有未完结的续费或变更单：这一份现在不能换，不列
		}
		srcs = append(srcs, basisSource{Sub: sum.Subscription, Kind: sum.PlanKind, Quotas: sum.Quotas})
	}
	bases, err := s.loadBases(ctx, srcs)
	if err != nil {
		return nil, err
	}
	out := []quoteSource{}
	for _, src := range srcs {
		if len(out) >= maxQuoteRows {
			break
		}
		out = append(out, quoteSource{sub: src.Sub, basis: bases[src.Sub.ID]})
	}
	return out, nil
}

// quoteChangeLines 换套餐报价：给了订阅没给套餐，按这份能换成的每个套餐展开；给了套餐没给订阅，按每份能换的订阅展开
// （「把哪一份换成它」）；都给就一个套餐。每个价格档一条，周期从现在起算，剩余价值按现在算。
func quoteChangeLines(ctx context.Context, s *Service, userID uuid.UUID, in QuoteInput, now time.Time) ([]Quote, error) {
	hasSub := in.SubscriptionID != nil && *in.SubscriptionID != uuid.Nil
	hasPlan := in.PlanID != nil && *in.PlanID != uuid.Nil
	if !hasSub && !hasPlan {
		return nil, errs.Invalid(map[string]string{"subscription_id": "要换的那一份与目标套餐至少给一个"})
	}
	var sources []quoteSource
	if hasSub {
		sub, err := s.subs.Get(ctx, *in.SubscriptionID, &userID)
		if err != nil {
			return nil, err
		}
		if err := s.checkChangeSource(ctx, sub, now); err != nil {
			return nil, err
		}
		if hasPlan && *in.PlanID == sub.PlanID {
			return nil, ErrPlanChangeSamePlan
		}
		basis, err := s.loadBasis(ctx, sub)
		if err != nil {
			return nil, err
		}
		sources = append(sources, quoteSource{sub: sub, basis: basis})
	} else {
		srcs, err := s.changeSourcesFor(ctx, userID, *in.PlanID)
		if err != nil {
			return nil, err
		}
		sources = srcs
	}
	if len(sources) == 0 {
		return []Quote{}, nil
	}

	uid := userID
	offers, err := s.catalog.ListOffers(ctx, catalogsvc.OfferRequest{Use: catalogsvc.UseChange, UserID: &uid, Now: now})
	if err != nil {
		return nil, err
	}
	out := []Quote{}
	for _, src := range sources {
		targets := 0
		for _, op := range offers {
			if op.Plan.ID == src.sub.PlanID || (hasPlan && op.Plan.ID != *in.PlanID) {
				continue
			}
			if targets >= maxQuoteRows {
				break
			}
			targets++
			for _, p := range op.Prices {
				credit, detail := sourceCredit(src.basis, now)
				subID, planID, priceID := src.sub.ID, op.Plan.ID, p.ID
				prev, start := src.sub.PeriodEnd, now
				end := subsvc.PeriodEndFor(now, priceSnap(p))
				q := Quote{
					SubscriptionID: &subID,
					PlanID:         &planID,
					PriceID:        &priceID,
					Interval:       p.BillingInterval,
					IntervalCount:  p.IntervalCount,
					ValidityDays:   p.ValidityDays,
					Subtotal:       p.UnitAmount,
					Credit:         credit,
					CreditDetail:   detail,
					PeriodStart:    &start,
					PeriodEnd:      &end,
					PreviousEnd:    &prev,
				}
				disc, cc, ce, err := quoteCouponEval(ctx, s, userID, in.CouponCode, &planID, p.UnitAmount)
				if err != nil {
					return nil, err
				}
				q.Discount, q.CouponCode, q.CouponError = disc, cc, ce
				q.Total = OrderTotal(q.Subtotal, q.Discount, q.Credit)
				q.Refund = planChangeRefund(q.Credit, q.Subtotal, q.Discount)
				out = append(out, q)
			}
		}
	}
	return out, nil
}

// changeDraft 是一张换套餐单的输入（门户与后台人工单共用）。调用方已锁住 sub。
type changeDraft struct {
	Sub      subsvc.Subscription
	PlanID   uuid.UUID
	PriceID  uuid.UUID
	UserID   uuid.UUID
	Checkout Checkout
	// AsOf 是剩余价值的计算时刻（门户取报价时刻，后台取现在）
	AsOf        time.Time
	Manual      *manualDraft
	Actor       Actor
	AuditAction string
	AuditData   map[string]any
}

// placeChange 在调用方事务里建一张换套餐单：校验原订阅、取目标价格快照、算剩余价值，交给 place。
func (s *Service) placeChange(ctx context.Context, d changeDraft) (Created, model.Order, error) {
	now := s.clock()
	if err := s.checkChangeSource(ctx, d.Sub, now); err != nil {
		return Created{}, model.Order{}, err
	}
	if d.PlanID == d.Sub.PlanID {
		return Created{}, model.Order{}, ErrPlanChangeSamePlan
	}
	uid := d.UserID
	planID := d.PlanID
	offer, err := s.catalog.Offer(ctx, d.PriceID, catalogsvc.OfferRequest{
		Use:    catalogsvc.UseChange,
		UserID: &uid,
		PlanID: &planID,
		Manual: d.Manual != nil,
		Lock:   false,
		Now:    now,
	})
	if err != nil {
		return Created{}, model.Order{}, err
	}
	basis, err := s.loadBasis(ctx, d.Sub)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	credit, _ := sourceCredit(basis, d.AsOf)
	item, err := newOrderItemFromOffer(offer, offer.Price)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	subID := d.Sub.ID
	auditData := map[string]any{
		"subscription_id":  subID.String(),
		"to_plan_id":       planID.String(),
		"price_id":         d.PriceID.String(),
		"proration_credit": credit.StringFixed(2),
	}
	for k, v := range d.AuditData {
		auditData[k] = v
	}
	return s.place(ctx, draft{
		Kind:            KindUpgrade,
		UserID:          d.UserID,
		Subtotal:        offer.Price.UnitAmount,
		CouponPlanID:    &planID,
		SubscriptionID:  &subID,
		Item:            item,
		Checkout:        d.Checkout,
		ProrationCredit: credit,
		AllowWaive:      d.Manual == nil,
		Manual:          d.Manual,
		Actor:           d.Actor,
		AuditAction:     d.AuditAction,
		AuditData:       auditData,
	})
}

// CreatePlanChangeOrder 门户换套餐：应付为 0（剩余价值抵完、余额全额付或零头免掉）时当场生效。
func (s *Service) CreatePlanChangeOrder(ctx context.Context, in PlanChangeOrderInput) (Created, error) {
	userID, err := portalUser(ctx)
	if err != nil {
		return Created{}, err
	}
	p, err := principal(ctx)
	if err != nil {
		return Created{}, err
	}
	if err := rejectNUL("coupon_code", in.CouponCode); err != nil {
		return Created{}, err
	}
	var created Created
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.rejectDeleted(ctx, userID); err != nil {
			return err
		}
		asOf, err := quoteTime(in.Expect, s.clock())
		if err != nil {
			return err
		}
		sub, err := s.subs.Lock(ctx, in.SubscriptionID, &userID)
		if err != nil {
			return err
		}
		created, _, err = s.placeChange(ctx, changeDraft{
			Sub:         sub,
			PlanID:      in.PlanID,
			PriceID:     in.PriceID,
			UserID:      userID,
			Checkout:    in.Checkout,
			AsOf:        asOf,
			Actor:       actorFrom(p),
			AuditAction: ActionPlanChangeCreated,
		})
		if err != nil {
			return err
		}
		return s.completeCreate(ctx, created.OrderID, func() (idemsvc.Response, error) {
			if in.Reply == nil {
				return idemsvc.Response{}, nil
			}
			return in.Reply(created)
		})
	})
	if err != nil {
		return Created{}, err
	}
	return created, nil
}
