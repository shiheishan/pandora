package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// Preview 是门户兑换前的卡面预览：码不能用（不存在、已用、停用、过期、活动不在进行中）一律回 ErrCodeUnusable。
// 落点按完整奖池算（盲盒要知道可能抽到加时长还是流量），但盲盒只回奖品名：
// 权重等于公示中奖概率，各项金额会让人算出期望值再决定兑不兑。只读，不加锁。
func (s *Service) Preview(ctx context.Context, rawCode string) (CardPreview, error) {
	user, err := portalUser(ctx)
	if err != nil {
		return CardPreview{}, err
	}
	code, ok := normalizeCode(rawCode)
	if !ok {
		return CardPreview{}, ErrCodeUnusable
	}
	c, found, err := s.repo.CodeByHash(ctx, s.hashCode(code), false)
	if err != nil {
		return CardPreview{}, gcErr(err)
	}
	now := s.clock()
	if !found || c.Status != CodeUnused || (c.ExpiresAt != nil && !c.ExpiresAt.After(now)) {
		return CardPreview{}, ErrCodeUnusable
	}
	t, err := s.loadTemplate(ctx, c.TemplateID, false)
	if err != nil {
		return CardPreview{}, err
	}
	if t.Status != TemplateActive {
		return CardPreview{}, ErrCodeUnusable
	}
	out := CardPreview{
		ID:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		Type:        t.Type,
		Status:      t.Status,
		Currency:    t.Currency,
		Rewards:     t.Rewards,
		Conditions:  t.Conditions,
		Limits:      t.Limits,
		ThemeColor:  t.ThemeColor,
		CreatedAt:   t.CreatedAt,
	}
	if t.Type == TypeMystery {
		labels := make([]Prize, 0, len(t.Rewards.Pool))
		for _, p := range t.Rewards.Pool {
			labels = append(labels, Prize{Label: p.Label})
		}
		out.Rewards = Rewards{Pool: labels}
	}
	if t.Type == TypePlan {
		if err := s.previewPlan(ctx, t, &out); err != nil {
			return CardPreview{}, err
		}
	}
	o := offerOf(t)
	if o.Kind == offerNone {
		return out, nil
	}
	us, err := s.subs.ByUser(ctx, user)
	if err != nil {
		return CardPreview{}, err
	}
	cands := candidatesOf(us)
	opts, def := placementOptions(o, cands, s.valuer != nil)
	views, err := s.placementViews(ctx, t, o, opts, cands, us, now)
	if err != nil {
		return CardPreview{}, err
	}
	out.Placement = &CardPlacement{Question: placementQuestion(o), Options: views, DefaultKey: def}
	return out, nil
}

// previewPlan 给套餐卡补上套餐名与周期。套餐或价格后来被删掉时不报错：卡面照样能看，兑换那一步给出明确原因。
func (s *Service) previewPlan(ctx context.Context, t Template, out *CardPreview) error {
	r := t.Rewards
	if r.PlanID == nil {
		return nil
	}
	plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{*r.PlanID})
	if err != nil {
		return err
	}
	if p, ok := plans[*r.PlanID]; ok {
		out.PlanName = p.Name
	}
	if r.PriceID == nil {
		return nil
	}
	prices, err := s.catalog.PricesByIDs(ctx, []uuid.UUID{*r.PriceID})
	if err != nil {
		return err
	}
	if pr, ok := prices[*r.PriceID]; ok {
		out.Interval, out.IntervalCount = pr.BillingInterval, pr.IntervalCount
	}
	return nil
}

// placementViews 把选项展开成「这一份现在的样子」与「落地之后到哪天」。
func (s *Service) placementViews(ctx context.Context, t Template, o offer, opts []Option,
	cands []candidate, us subsvc.UserSubscriptions, now time.Time) ([]Placement, error) {
	byID := make(map[uuid.UUID]candidate, len(cands))
	for _, c := range cands {
		byID[c.SubscriptionID] = c
	}
	prices := make(map[uuid.UUID]subsvc.PriceSnapshot, len(us.Subscriptions))
	for _, sub := range us.Subscriptions {
		prices[sub.ID] = sub.Price
	}
	out := make([]Placement, 0, len(opts))
	for _, op := range opts {
		p := Placement{Option: op}
		var c candidate
		if op.SubscriptionID != nil {
			c = byID[*op.SubscriptionID]
			planID, end := c.PlanID, c.PeriodEnd
			p.Label, p.PlanID, p.PlanName, p.State, p.PeriodEnd = c.Label, &planID, c.PlanName, c.State, &end
			p.TrafficUsed, p.TrafficCap, p.PackRemaining = c.TrafficUsed, c.TrafficCap, c.PackRemaining
		}
		switch op.Kind {
		case KindNew, KindRenew, KindChange:
			var fallback *subsvc.PriceSnapshot
			if op.Kind == KindRenew {
				if sp, ok := prices[c.SubscriptionID]; ok {
					fallback = &sp
				}
			}
			price, err := s.giftPrice(ctx, t.Rewards.PriceID, fallback)
			if err != nil {
				return nil, err
			}
			// 续生效中的那份从原到期日接着算；其余（新开、换掉、续已过期的）从现在起算一整期
			from := now
			if op.Kind == KindRenew {
				from, _ = subsvc.RenewalBase(c.PeriodEnd, now, nil)
			}
			end := subsvc.AddInterval(from, price.Interval, price.IntervalCount)
			p.NewPeriodEnd = &end
			if op.Kind == KindChange && s.valuer != nil {
				credit, cur, lost, err := s.valuer.RemainingValue(ctx, c.SubscriptionID, now)
				if err != nil {
					return nil, err
				}
				if credit.IsPositive() {
					p.Credit = credit
				}
				p.Currency, p.GiftDaysLost = cur, lost
			}
		case KindExtendDays:
			// 盲盒抽中几天说不准，不给「之后到哪天」
			if t.Type != TypeMystery && o.Days > 0 {
				from, _ := subsvc.ExtendBase(c.PeriodEnd, now)
				end := from.AddDate(0, 0, o.Days)
				p.NewPeriodEnd = &end
			}
		}
		out = append(out, p)
	}
	return out, nil
}
