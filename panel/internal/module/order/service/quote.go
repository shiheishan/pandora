package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// applyQuoteBalances 给一条报价算「用余额」与「不用余额」两种付法；waive 为真时付不了的零头可以免（只有换套餐）。
func applyQuoteBalances(q *Quote, balance, minPay decimal.Decimal, waive bool) {
	q.WithBalance = WaiveSmallDue(PlanBalance(q.Total, balance, balance, minPay), waive)
	q.WithoutBalance = WaiveSmallDue(PlanBalance(q.Total, balance, decimal.Zero, minPay), waive)
}

// finalizeQuoteResult 补上可用余额与最低付款额，并给每条报价算两种付法。
func finalizeQuoteResult(ctx context.Context, s *Service, userID uuid.UUID, out QuoteResult, waive bool) (QuoteResult, error) {
	bal, err := s.ledger.Balance(ctx, userID)
	if err != nil {
		return QuoteResult{}, err
	}
	out.Balance = bal
	minPay, err := s.payments.MinPayment(ctx)
	if err != nil {
		return QuoteResult{}, err
	}
	out.MinPayment = minPay
	for i := range out.Quotes {
		applyQuoteBalances(&out.Quotes[i], bal, minPay, waive)
	}
	return out, nil
}

// priceSnap 把目录价格档映射为订阅的价格快照（用于计算期末时间等）。
func priceSnap(p catalogsvc.Price) subsvc.PriceSnapshot {
	return subsvc.PriceSnapshot{
		PriceID:       &p.ID,
		Amount:        p.UnitAmount,
		Interval:      p.BillingInterval,
		IntervalCount: p.IntervalCount,
		ValidityDays:  p.ValidityDays,
	}
}

// quoteNewLines 新购报价：套餐每个可用价格档一条，周期从现在起算。
func quoteNewLines(ctx context.Context, s *Service, userID uuid.UUID, in QuoteInput, now time.Time) ([]Quote, error) {
	if in.PlanID == nil || *in.PlanID == uuid.Nil {
		return nil, errs.Invalid(map[string]string{"plan_id": "必填"})
	}
	planID := *in.PlanID
	uid := userID
	op, err := s.catalog.PlanOffers(ctx, planID, catalogsvc.OfferRequest{
		Use:    catalogsvc.UseNew,
		UserID: &uid,
		Now:    now,
	})
	if err != nil {
		return nil, err
	}
	if !in.NewCopy {
		if _, ok, err := s.subs.RenewableForPlan(ctx, userID, planID, false); err != nil {
			return nil, err
		} else if ok {
			if op.Plan.Kind == catalogsvc.KindOneTime {
				return nil, ErrSamePlanUseStack
			}
			return nil, ErrSamePlanUseRenewal
		}
	}
	if err := pendingNewOrderConflict(ctx, s, userID, planID, op.Plan.Name, now); err != nil {
		return nil, err
	}
	out := make([]Quote, 0, len(op.Prices))
	for _, p := range op.Prices {
		periodEnd := subsvc.PeriodEndFor(now, priceSnap(p))
		ps := now
		pe := periodEnd
		q := Quote{
			PlanID:        &planID,
			PriceID:       &p.ID,
			Interval:      p.BillingInterval,
			IntervalCount: p.IntervalCount,
			ValidityDays:  p.ValidityDays,
			Subtotal:      p.UnitAmount,
			Credit:        decimal.Zero,
			Refund:        decimal.Zero,
			PeriodStart:   &ps,
			PeriodEnd:     &pe,
		}
		disc, cc, ce, err := quoteCouponEval(ctx, s, userID, in.CouponCode, &planID, p.UnitAmount)
		if err != nil {
			return nil, err
		}
		q.Discount = disc
		q.CouponCode = cc
		q.CouponError = ce
		q.Total = OrderTotal(q.Subtotal, q.Discount, decimal.Zero)
		out = append(out, q)
	}
	return out, nil
}

// quoteCouponEval 试算优惠码：码不能用时只把中文原因放进 CouponError，不让整个报价失败。
func quoteCouponEval(ctx context.Context, s *Service, userID uuid.UUID, rawCode string, planID *uuid.UUID, subtotal decimal.Decimal) (decimal.Decimal, *string, *string, error) {
	code := promosvc.NormalizeCode(rawCode)
	if code == "" {
		return decimal.Zero, nil, nil, nil
	}
	applied, err := s.promo.Evaluate(ctx, code, promosvc.Target{
		UserID:   userID,
		PlanID:   planID,
		Subtotal: subtotal,
	})
	if promosvc.IsRejection(err) {
		msg := errs.As(err).Message
		return decimal.Zero, nil, &msg, nil
	}
	if err != nil {
		return decimal.Zero, nil, nil, err
	}
	if applied != nil {
		c := code
		return applied.Discount, &c, nil, nil
	}
	return decimal.Zero, nil, nil, nil
}

// quoteRenewLines 续费报价：生效中的从原到期时间接着算，否则从现在起算。
func quoteRenewLines(ctx context.Context, s *Service, userID uuid.UUID, in QuoteInput, now time.Time) ([]Quote, error) {
	if in.SubscriptionID == nil || *in.SubscriptionID == uuid.Nil {
		return nil, errs.Invalid(map[string]string{"subscription_id": "必填"})
	}
	sub, err := s.subs.Get(ctx, *in.SubscriptionID, &userID)
	if err != nil {
		return nil, err
	}
	if !sub.StateAt(now).AcceptsChange() {
		return nil, subsvc.ErrRenewalWindowClosed
	}
	uid := userID
	op, err := s.catalog.PlanOffers(ctx, sub.PlanID, catalogsvc.OfferRequest{
		Use:    catalogsvc.UseRenewal,
		UserID: &uid,
		PlanID: &sub.PlanID,
		Now:    now,
	})
	if err != nil {
		return nil, err
	}
	base := now
	if sub.Status == subsvc.StatusActive && sub.PeriodEnd.After(now) {
		base = sub.PeriodEnd
	}
	prev := sub.PeriodEnd
	planID := sub.PlanID
	subID := sub.ID
	out := make([]Quote, 0, len(op.Prices))
	for _, p := range op.Prices {
		periodEnd := subsvc.PeriodEndFor(base, priceSnap(p))
		ps := base
		pe := periodEnd
		q := Quote{
			SubscriptionID: &subID,
			PlanID:         &planID,
			PriceID:        &p.ID,
			Interval:       p.BillingInterval,
			IntervalCount:  p.IntervalCount,
			ValidityDays:   p.ValidityDays,
			Subtotal:       p.UnitAmount,
			Credit:         decimal.Zero,
			Refund:         decimal.Zero,
			PeriodStart:    &ps,
			PeriodEnd:      &pe,
			PreviousEnd:    &prev,
		}
		disc, cc, ce, err := quoteCouponEval(ctx, s, userID, in.CouponCode, &planID, p.UnitAmount)
		if err != nil {
			return nil, err
		}
		q.Discount = disc
		q.CouponCode = cc
		q.CouponError = ce
		q.Total = OrderTotal(q.Subtotal, q.Discount, decimal.Zero)
		out = append(out, q)
	}
	return out, nil
}

// quoteStackLines 叠加报价：一次性套餐叠加到原订阅。
func quoteStackLines(ctx context.Context, s *Service, userID uuid.UUID, in QuoteInput, now time.Time) ([]Quote, error) {
	if in.SubscriptionID == nil || *in.SubscriptionID == uuid.Nil {
		return nil, errs.Invalid(map[string]string{"subscription_id": "必填"})
	}
	sub, err := s.subs.Get(ctx, *in.SubscriptionID, &userID)
	if err != nil {
		return nil, err
	}
	if !sub.StateAt(now).AcceptsChange() {
		return nil, subsvc.ErrRenewalWindowClosed
	}
	uid := userID
	op, err := s.catalog.PlanOffers(ctx, sub.PlanID, catalogsvc.OfferRequest{
		Use:    catalogsvc.UseStack,
		UserID: &uid,
		PlanID: &sub.PlanID,
		Now:    now,
	})
	if err != nil {
		return nil, err
	}
	planID := sub.PlanID
	subID := sub.ID
	prev := sub.PeriodEnd
	out := make([]Quote, 0, len(op.Prices))
	for _, p := range op.Prices {
		snap := priceSnap(p)
		pe := subsvc.StackEnd(sub.PeriodEnd, now, snap)
		ps := sub.PeriodStart
		q := Quote{
			SubscriptionID: &subID,
			PlanID:         &planID,
			PriceID:        &p.ID,
			Interval:       p.BillingInterval,
			IntervalCount:  p.IntervalCount,
			ValidityDays:   p.ValidityDays,
			Subtotal:       p.UnitAmount,
			Credit:         decimal.Zero,
			Refund:         decimal.Zero,
			PeriodStart:    &ps,
			PeriodEnd:      &pe,
			PreviousEnd:    &prev,
		}
		disc, cc, ce, err := quoteCouponEval(ctx, s, userID, in.CouponCode, &planID, p.UnitAmount)
		if err != nil {
			return nil, err
		}
		q.Discount = disc
		q.CouponCode = cc
		q.CouponError = ce
		q.Total = OrderTotal(q.Subtotal, q.Discount, decimal.Zero)
		out = append(out, q)
	}
	return out, nil
}

// Quote 门户统一报价（不加锁、不写库）。
func (s *Service) Quote(ctx context.Context, in QuoteInput) (QuoteResult, error) {
	userID, err := portalUser(ctx)
	if err != nil {
		return QuoteResult{}, err
	}
	if err := rejectNUL("coupon_code", in.CouponCode); err != nil {
		return QuoteResult{}, err
	}
	now := s.clock()
	out := QuoteResult{AsOf: now, Quotes: []Quote{}}

	var lines []Quote
	switch in.Action {
	case QuoteNew:
		lines, err = quoteNewLines(ctx, s, userID, in, now)
	case QuoteRenew:
		lines, err = quoteRenewLines(ctx, s, userID, in, now)
	case QuoteStack:
		lines, err = quoteStackLines(ctx, s, userID, in, now)
	case QuoteChange:
		lines, err = quoteChangeLines(ctx, s, userID, in, now)
	case QuoteReset:
		lines, err = quoteResetLines(ctx, s, userID, in, now)
	default:
		return QuoteResult{}, errs.Invalid(map[string]string{"action": "只能是 new、renew、stack、change 或 reset"})
	}
	if err != nil {
		return QuoteResult{}, err
	}
	out.Quotes = lines
	// 只有换套餐的零头能免；其余入口付不了的标 below_minimum，前端提示先充值或用余额
	return finalizeQuoteResult(ctx, s, userID, out, in.Action == QuoteChange)
}
