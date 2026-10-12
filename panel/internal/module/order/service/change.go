package service

// 换套餐是在原订阅上换一个套餐，不是开第二条订阅（对照旧 plan_change*.go）：
//
//	金额   新价（优惠后）减原订阅的剩余价值：为正就补差价；为负就 total = 0，差额在履约时退进余额
//	周期   从履约那一刻按新价格档重开一整期（subscription.ChangePlan）
//	零头   门户换套餐抵扣后付不了的零头（≤ 0.99）可以免，单独记 waived_amount，不并进券折扣
//
// 升级与降级都是 kind='upgrade'；方向由金额决定。剩余价值在下单那一刻按报价时刻（as_of）算定、写进订单
// （proration_credit_amount），之后随订单冻结；为了让这个数在付款前不失效，同一条订阅同时只允许一张
// 未完结的续费或变更单（库里 orders_one_open_subscription_change 兜底）。
//
// 锁序：订单（结算时）→ 订阅 → 券 → 账务科目。建单没有订单锁，先锁订阅；目标套餐行不锁（价格行冻结不可改，
// 换套餐也不动库存与限购，与续费同一取舍）。

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// paidKinds 是参与剩余价值基数的订单种类（买下订阅时长的那几种）。
var paidKinds = map[string]bool{KindNew: true, KindRenewal: true, KindUpgrade: true}

// loadBasis 读原订阅本周期的折算输入：subscription 给出本周期挂了订单的事件（按先后），本模块读这些订单的付费与计费周期；
// 流量取 cycle / total 的 traffic.bytes 配额（有上限的），cycle 行加上提前续费后还没开始的几期。
// 本周期付费单币种不一致回 errProrationMixedCurrency。
func (s *Service) loadBasis(ctx context.Context, sub subsvc.Subscription) (prorationBasis, error) {
	b := prorationBasis{PeriodStart: sub.PeriodStart, PeriodEnd: sub.PeriodEnd, Value: decimal.Zero}
	ids, err := s.subs.PeriodOrderIDs(ctx, sub.ID)
	if err != nil {
		return b, err
	}
	orders, err := s.repo.OrdersByIDs(ctx, ids)
	if err != nil {
		return b, err
	}
	byID := make(map[uuid.UUID]model.Order, len(orders))
	for _, o := range orders {
		byID[o.ID] = o
	}
	items, err := s.repo.ItemsByOrders(ctx, ids)
	if err != nil {
		return b, err
	}
	// 每张单取第一条套餐行（按创建先后）的计费周期
	planItem := make(map[uuid.UUID]model.Item, len(items))
	for _, it := range items {
		if it.PlanID == nil {
			continue
		}
		if _, seen := planItem[it.OrderID]; !seen {
			planItem[it.OrderID] = it
		}
	}
	paidUntil := sub.PeriodStart
	for _, id := range ids {
		o, ok := byID[id]
		if !ok || !paidKinds[o.Kind] || o.Status != StatusFulfilled {
			continue
		}
		it, ok := planItem[id]
		if !ok || it.SnapshotInterval == nil || it.SnapshotIntervalCount == nil {
			continue
		}
		cur := money.Currency(o.Currency)
		if b.Currency != "" && b.Currency != cur {
			return b, errProrationMixedCurrency
		}
		b.Currency = cur
		// 付费 = max(小计 − 折扣 − 免掉的零头, 0)：0 元单、全额券、人工赠送单为 0
		b.Value = b.Value.Add(decimal.Max(o.SubtotalAmount.Sub(o.DiscountAmount).Sub(o.WaivedAmount), decimal.Zero))
		paidUntil = subsvc.AddInterval(paidUntil, *it.SnapshotInterval, *it.SnapshotIntervalCount)
	}
	b.PaidSpan = paidUntil.Sub(sub.PeriodStart)

	quotas, err := s.subs.Quotas(ctx, sub.ID)
	if err != nil {
		return b, err
	}
	for _, q := range quotas {
		if q.Metric != catalogsvc.MetricTraffic || q.Limit == nil ||
			(q.Period != catalogsvc.PeriodCycle && q.Period != catalogsvc.PeriodTotal) {
			continue
		}
		t := trafficAllowance{Cap: *q.Limit, Consumed: q.Consumed}
		if q.Period == catalogsvc.PeriodCycle && q.PeriodEnd != nil {
			t.Cap += pendingCycleAllowance(t.Cap, q.PeriodStart, *q.PeriodEnd, sub.PeriodEnd)
		}
		b.Traffic = append(b.Traffic, t)
	}
	return b, nil
}

// sourceCredit 是原订阅在 asOf 时刻的剩余价值与明细；本期付费单的币种与新价不同就不能换。
func sourceCredit(b prorationBasis, asOf time.Time, cur money.Currency) (decimal.Decimal, *CreditDetail, error) {
	if b.Currency != "" && b.Currency != cur {
		return decimal.Zero, nil, ErrPlanChangeCurrency
	}
	credit, d := prorationCredit(b, asOf)
	return credit, &d, nil
}

// checkChangeSource 校验原订阅此刻能换：未停用（已停用回 ErrRenewalWindowClosed），且没有未完结的续费或变更单。
func (s *Service) checkChangeSource(ctx context.Context, sub subsvc.Subscription, now time.Time) error {
	if !sub.StateAt(now).AcceptsChange() {
		return subsvc.ErrRenewalWindowClosed
	}
	open, err := s.repo.OpenChangeOrderExists(ctx, sub.ID)
	if err != nil {
		return err
	}
	if open {
		return ErrOpenChange
	}
	return nil
}

// quoteChangeLines 换套餐报价：给了订阅没给套餐，按这份能换成的每个套餐展开；给了套餐没给订阅，按每份能换的订阅展开
// （「把哪一份换成它」）；都给就一个套餐。每个价格档一条，周期从现在起算，剩余价值按现在算。
func quoteChangeLines(ctx context.Context, s *Service, userID uuid.UUID, in QuoteInput, now time.Time) ([]Quote, error) {
	hasSub := in.SubscriptionID != nil && *in.SubscriptionID != uuid.Nil
	hasPlan := in.PlanID != nil && *in.PlanID != uuid.Nil
	if !hasSub && !hasPlan {
		return nil, errs.Invalid(map[string]string{"subscription_id": "订阅与套餐至少给一个"})
	}
	type source struct {
		sub   subsvc.Subscription
		basis prorationBasis
	}
	var sources []source
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
		sources = append(sources, source{sub: sub, basis: basis})
	} else {
		us, err := s.subs.ByUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		for _, sum := range us.Subscriptions {
			if len(sources) >= maxQuoteRows {
				break
			}
			if sum.State == subsvc.StateEnded || sum.PlanID == *in.PlanID {
				continue
			}
			open, err := s.repo.OpenChangeOrderExists(ctx, sum.ID)
			if err != nil {
				return nil, err
			}
			if open {
				continue // 有未完结的续费或变更单：这一份现在不能换，不列
			}
			basis, err := s.loadBasis(ctx, sum.Subscription)
			if errors.Is(err, errProrationMixedCurrency) {
				continue
			}
			if err != nil {
				return nil, err
			}
			sources = append(sources, source{sub: sum.Subscription, basis: basis})
		}
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
				credit, detail, err := sourceCredit(src.basis, now, p.Currency)
				if err != nil {
					continue // 原订阅本期币种与这个价格不同，换不过去
				}
				subID, planID, priceID := src.sub.ID, op.Plan.ID, p.ID
				prev, start := src.sub.PeriodEnd, now
				end := subsvc.AddInterval(now, p.BillingInterval, p.IntervalCount)
				q := Quote{
					SubscriptionID: &subID,
					PlanID:         &planID,
					PriceID:        &priceID,
					Currency:       p.Currency,
					Interval:       p.BillingInterval,
					IntervalCount:  p.IntervalCount,
					Subtotal:       p.UnitAmount,
					Credit:         credit,
					CreditDetail:   detail,
					PeriodStart:    &start,
					PeriodEnd:      &end,
					PreviousEnd:    &prev,
				}
				disc, cc, ce, err := quoteCouponEval(ctx, s, userID, in.CouponCode, &planID, p.Currency, p.UnitAmount)
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
	credit, _, err := sourceCredit(basis, d.AsOf, offer.Price.Currency)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	item, err := newOrderItemFromOffer(offer, offer.Price)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	subID := d.Sub.ID
	auditData := map[string]any{
		"subscription_id":  subID.String(),
		"to_plan_id":       planID.String(),
		"proration_credit": credit.StringFixed(2),
	}
	for k, v := range d.AuditData {
		auditData[k] = v
	}
	return s.place(ctx, draft{
		Kind:            KindUpgrade,
		UserID:          d.UserID,
		Currency:        offer.Price.Currency,
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
