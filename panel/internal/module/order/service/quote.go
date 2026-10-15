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
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// applyQuoteBalances 给一条报价算「用余额」与「不用余额」两种付法；waive 为真时付不了的零头可以免（只有换套餐）。
func applyQuoteBalances(q *Quote, balance, minPay decimal.Decimal, waive bool) {
	q.WithBalance = WaiveSmallDue(PlanBalance(q.Total, balance, balance, minPay), waive)
	q.WithoutBalance = WaiveSmallDue(PlanBalance(q.Total, balance, decimal.Zero, minPay), waive)
}

// finalizeQuoteResult 补上币种、可用余额与最低付款额，并给每条报价算两种付法；与结果币种不同的报价不计余额。
func finalizeQuoteResult(ctx context.Context, s *Service, userID uuid.UUID, out QuoteResult, waive bool) (QuoteResult, error) {
	if len(out.Quotes) > 0 {
		out.Currency = out.Quotes[0].Currency
	}
	if out.Currency == "" {
		out.Currency = money.CNY
	}
	bal, err := s.ledger.Balance(ctx, userID, out.Currency)
	if err != nil {
		return QuoteResult{}, err
	}
	out.Balance = bal
	minPay, err := s.payments.MinPayment(ctx, out.Currency)
	if err != nil {
		return QuoteResult{}, err
	}
	out.MinPayment = minPay
	for i := range out.Quotes {
		q := &out.Quotes[i]
		avail := bal
		if q.Currency != out.Currency {
			avail = decimal.Zero
		}
		applyQuoteBalances(q, avail, minPay, waive)
	}
	return out, nil
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
			return nil, ErrSamePlanUseRenewal
		}
	}
	if err := pendingNewOrderConflict(ctx, s, userID, planID, op.Plan.Name, now); err != nil {
		return nil, err
	}
	out := make([]Quote, 0, len(op.Prices))
	for _, p := range op.Prices {
		periodEnd := subsvc.AddInterval(now, p.BillingInterval, p.IntervalCount)
		ps := now
		pe := periodEnd
		q := Quote{
			PlanID:        &planID,
			PriceID:       &p.ID,
			Currency:      p.Currency,
			Interval:      p.BillingInterval,
			IntervalCount: p.IntervalCount,
			Subtotal:      p.UnitAmount,
			Credit:        decimal.Zero,
			Refund:        decimal.Zero,
			PeriodStart:   &ps,
			PeriodEnd:     &pe,
		}
		iv := p.BillingInterval
		disc, cc, ce, err := quoteCouponEval(ctx, s, userID, in.CouponCode, &planID, &iv, p.Currency, p.UnitAmount)
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
func quoteCouponEval(ctx context.Context, s *Service, userID uuid.UUID, rawCode string, planID *uuid.UUID, interval *string, currency money.Currency, subtotal decimal.Decimal) (decimal.Decimal, *string, *string, error) {
	code := promosvc.NormalizeCode(rawCode)
	if code == "" {
		return decimal.Zero, nil, nil, nil
	}
	applied, err := s.promo.Evaluate(ctx, code, promosvc.Target{
		UserID:   userID,
		PlanID:   planID,
		Interval: interval,
		Currency: string(currency),
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
		periodEnd := subsvc.AddInterval(base, p.BillingInterval, p.IntervalCount)
		ps := base
		pe := periodEnd
		q := Quote{
			SubscriptionID: &subID,
			PlanID:         &planID,
			PriceID:        &p.ID,
			Currency:       p.Currency,
			Interval:       p.BillingInterval,
			IntervalCount:  p.IntervalCount,
			Subtotal:       p.UnitAmount,
			Credit:         decimal.Zero,
			Refund:         decimal.Zero,
			PeriodStart:    &ps,
			PeriodEnd:      &pe,
			PreviousEnd:    &prev,
		}
		iv := p.BillingInterval
		disc, cc, ce, err := quoteCouponEval(ctx, s, userID, in.CouponCode, &planID, &iv, p.Currency, p.UnitAmount)
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

// quotePackLines 流量包报价：一条；给了目标订阅时校验归属与可用。
func quotePackLines(ctx context.Context, s *Service, userID uuid.UUID, in QuoteInput) ([]Quote, error) {
	if in.PackID == nil || *in.PackID == uuid.Nil {
		return nil, errs.Invalid(map[string]string{"pack_id": "必填"})
	}
	pack, err := s.catalog.PackOffer(ctx, *in.PackID, false)
	if err != nil {
		return nil, err
	}
	if in.SubscriptionID != nil && *in.SubscriptionID != uuid.Nil {
		if err := s.subs.CheckPackTarget(ctx, userID, *in.SubscriptionID); err != nil {
			return nil, err
		}
	}
	packID := pack.ID
	q := Quote{
		PackID:   &packID,
		Currency: pack.Currency,
		Subtotal: pack.UnitAmount,
		Credit:   decimal.Zero,
		Refund:   decimal.Zero,
	}
	if in.SubscriptionID != nil && *in.SubscriptionID != uuid.Nil {
		sid := *in.SubscriptionID
		q.SubscriptionID = &sid
	}
	disc, cc, ce, err := quoteCouponEval(ctx, s, userID, in.CouponCode, nil, nil, pack.Currency, pack.UnitAmount)
	if err != nil {
		return nil, err
	}
	q.Discount = disc
	q.CouponCode = cc
	q.CouponError = ce
	q.Total = OrderTotal(q.Subtotal, q.Discount, decimal.Zero)
	return []Quote{q}, nil
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
	case QuotePack:
		lines, err = quotePackLines(ctx, s, userID, in)
	case QuoteChange:
		lines, err = quoteChangeLines(ctx, s, userID, in, now)
	case QuoteReset:
		lines, err = quoteResetLines(ctx, s, userID, in, now)
	default:
		return QuoteResult{}, errs.Invalid(map[string]string{"action": "只能是 renew、change、new、pack 或 reset"})
	}
	if err != nil {
		return QuoteResult{}, err
	}
	// 会员折扣：一次报价只读一次，每条报价按「券 + 会员」重算总额（与建单 place 同口径）
	if len(lines) > 0 {
		pct, err := s.promo.MemberPercent(ctx, userID)
		if err != nil {
			return QuoteResult{}, err
		}
		for i := range lines {
			withMember(&lines[i], pct)
		}
	}
	out.Quotes = lines
	// 只有换套餐的零头能免；其余入口付不了的标 below_minimum，前端提示先充值或用余额
	return finalizeQuoteResult(ctx, s, userID, out, in.Action == QuoteChange)
}
