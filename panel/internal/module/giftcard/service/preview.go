package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
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
	planKind := ""
	if t.Type == TypePlan {
		plan, err := s.previewPlan(ctx, t, &out)
		if err != nil {
			return CardPreview{}, err
		}
		planKind = plan.Kind
	}
	o := offerOf(t, planKind)
	if o.Kind == offerNone {
		return out, nil
	}
	us, err := s.subs.ByUser(ctx, user)
	if err != nil {
		return CardPreview{}, err
	}
	cands := candidatesOf(us)
	opts, def := placementOptions(o, cands, s.valuer != nil)
	views, err := s.placementViews(ctx, t, o, opts, cands, now)
	if err != nil {
		return CardPreview{}, err
	}
	if !hasKey(views, def) {
		def = ""
	}
	out.Placement = &CardPlacement{Question: placementQuestion(o), Options: views, DefaultKey: def}
	return out, nil
}

// previewPlan 给套餐卡补上套餐名、类型与周期。套餐或价格后来被删掉时不报错：卡面照样能看，兑换那一步给出明确原因。
func (s *Service) previewPlan(ctx context.Context, t Template, out *CardPreview) (catalogsvc.Plan, error) {
	plan, err := s.cardPlan(ctx, t)
	if errors.Is(err, errPlanUnavailable) {
		return catalogsvc.Plan{}, nil
	}
	if err != nil {
		return catalogsvc.Plan{}, err
	}
	out.PlanName = plan.Name
	out.PlanKind = plan.Kind
	if t.Rewards.PriceID == nil {
		return plan, nil
	}
	prices, err := s.catalog.PricesByIDs(ctx, []uuid.UUID{*t.Rewards.PriceID})
	if err != nil {
		return plan, err
	}
	if pr, ok := prices[*t.Rewards.PriceID]; ok {
		out.Interval, out.IntervalCount = pr.BillingInterval, pr.IntervalCount
		out.ValidityDays = pr.ValidityDays
	}
	return plan, nil
}

// placementViews 把选项展开成「这一份现在的样子」与「落地之后到哪天」。
func (s *Service) placementViews(ctx context.Context, t Template, o offer, opts []Option,
	cands []candidate, now time.Time) ([]Placement, error) {
	byID := make(map[uuid.UUID]candidate, len(cands))
	for _, c := range cands {
		byID[c.SubscriptionID] = c
	}
	// 套餐卡的价格快照只取一次（所有落点共用卡上那一档价）；价格已下架时预览也给出同一句原因
	var price subsvc.PriceSnapshot
	if o.Kind == offerPlan {
		var err error
		if price, err = s.giftPrice(ctx, t.Rewards.PriceID); err != nil {
			return nil, err
		}
	}
	out := make([]Placement, 0, len(opts))
	for _, op := range opts {
		p := Placement{Option: op}
		var c candidate
		if op.SubscriptionID != nil {
			c = byID[*op.SubscriptionID]
			planID, end := c.PlanID, c.PeriodEnd
			p.Label, p.PlanID, p.PlanName, p.State, p.PeriodEnd = c.Label, &planID, c.PlanName, c.State, &end
			p.TrafficUsed, p.TrafficCap = c.TrafficUsed, c.TrafficCap
		}
		switch op.Kind {
		case KindNew, KindRenew, KindChange, KindStack:
			if price.Interval == catalogsvc.IntervalOneTime {
				if (op.Kind == KindStack && c.Permanent) || price.ValidityDays == nil {
					p.NewPermanent = true
				} else {
					from := now
					if op.Kind == KindStack && c.PeriodEnd.After(now) {
						from = c.PeriodEnd
					}
					end := from.AddDate(0, 0, *price.ValidityDays)
					p.NewPeriodEnd = &end
				}
			} else {
				// 续生效中的那份从原到期日接着算；其余（新开、换掉、续已过期的）从现在起算一整期
				from := now
				if op.Kind == KindRenew {
					from, _ = subsvc.RenewalBase(c.PeriodEnd, now, nil)
				}
				end := subsvc.AddInterval(from, price.Interval, price.IntervalCount)
				p.NewPeriodEnd = &end
			}
			if op.Kind == KindChange && s.valuer != nil {
				credit, lost, err := s.valuer.RemainingValue(ctx, c.SubscriptionID, now)
				if e := errs.As(err); e != nil && e.Code == errs.Conflict {
					// 这份订阅上有没完结的续费或换套餐单：兑换时会被拒，这里干脆不给「换掉」
					continue
				}
				if err != nil {
					return nil, err
				}
				if credit.IsPositive() {
					p.Credit = credit
				}
				p.GiftDaysLost = lost
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

// hasKey 报告选项里有没有这个 Key（预览剔掉了不可选的「换掉」后，默认项可能已不在）。
func hasKey(views []Placement, key string) bool {
	for _, v := range views {
		if v.Key == key {
			return true
		}
	}
	return false
}
