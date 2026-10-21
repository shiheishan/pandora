package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// MySubscriptions 是门户「我的套餐」：本人的全部订阅（新建在前）。
func (s *Service) MySubscriptions(ctx context.Context) (MySubscriptions, error) {
	p, err := principal(ctx)
	if err != nil {
		return MySubscriptions{}, err
	}
	all, plans, err := s.summarize(ctx, []uuid.UUID{p.UserID})
	if err != nil {
		return MySubscriptions{}, err
	}
	us := all[p.UserID]
	out := MySubscriptions{Subscriptions: make([]MySubscription, 0, len(us.Subscriptions))}
	if len(us.Subscriptions) == 0 {
		return out, nil
	}
	trialSubs, err := s.repo.TrialSubscriptionIDs(ctx, []uuid.UUID{p.UserID})
	if err != nil {
		return MySubscriptions{}, subErr(err)
	}
	site, err := s.settings.Site(ctx)
	if err != nil {
		return MySubscriptions{}, err
	}
	priceSet := map[uuid.UUID]struct{}{}
	for _, sm := range us.Subscriptions {
		if sm.Price.PriceID != nil {
			priceSet[*sm.Price.PriceID] = struct{}{}
		}
	}
	prices, err := s.catalog.PricesByIDs(ctx, keys(priceSet))
	if err != nil {
		return MySubscriptions{}, err
	}
	now := s.clock()
	for _, sm := range us.Subscriptions {
		v := mySubscription(sm, plans[sm.PlanID], prices, site.Name, now)
		if _, ok := trialSubs[sm.ID]; ok {
			v.IsTrial = true
		}
		out.Subscriptions = append(out.Subscriptions, v)
	}
	s.fillOnlineDevices(ctx, out.Subscriptions)
	return out, nil
}

// fillOnlineDevices 按订阅批量取在线设备数（一次调用）。计数口没装配或读失败时记 warn、照常返回 0：
// 在线数只是展示，不能让「我的套餐」整页失败。
func (s *Service) fillOnlineDevices(ctx context.Context, subs []MySubscription) {
	if len(subs) == 0 || s.online == nil {
		return
	}
	ids := make([]uuid.UUID, len(subs))
	for i := range subs {
		ids[i] = subs[i].ID
	}
	counts, err := s.online.OnlineDeviceCounts(ctx, ids)
	if err != nil {
		plog.From(ctx).Warn("读取在线设备数失败，按 0 显示", zap.Error(err))
		return
	}
	for i := range subs {
		subs[i].OnlineDevices = counts[subs[i].ID]
	}
}

// mySubscription 拼门户的一份订阅。Renewable = 没停用、订阅套餐、套餐没归档且允许续费；
// Stackable = 没停用、一次性套餐、套餐没归档且接受新购（一次性不能续费，只能叠加或另开一份）。
func mySubscription(sm Summary, plan catalogsvc.Plan, prices map[uuid.UUID]catalogsvc.Price, site string, now time.Time) MySubscription {
	v := MySubscription{Summary: sm, EffectiveDeviceLimit: sm.PlanMaxDevices}
	if sm.DeviceLimit != nil {
		v.EffectiveDeviceLimit = sm.DeviceLimit
	}
	v.NextResetAt = nextResetAt(sm.Quotas)
	v.Changeable = sm.State.AcceptsChange()
	v.Renewable = v.Changeable && plan.Kind == catalogsvc.KindSubscription && plan.Status != catalogsvc.PlanArchived && plan.AllowRenewal
	v.Stackable = v.Changeable && plan.Kind == catalogsvc.KindOneTime && plan.Status != catalogsvc.PlanArchived && plan.AllowNewPurchase
	if sm.Price.PriceID != nil {
		if pr, ok := prices[*sm.Price.PriceID]; ok {
			v.RenewalPrice = &RenewalPrice{
				ID:              pr.ID,
				UnitAmount:      pr.UnitAmount,
				BillingInterval: pr.BillingInterval,
				IntervalCount:   pr.IntervalCount,
				Available:       renewalPriceUsable(pr, sm.PlanID),
			}
		}
	}
	if v.Renewable && v.RenewalPrice != nil && v.RenewalPrice.Available {
		until := RenewUntil(sm.PeriodEnd, now, v.RenewalPrice.BillingInterval, v.RenewalPrice.IntervalCount)
		v.RenewUntil = &until
	}
	label := ""
	if sm.Label != nil {
		label = *sm.Label
	}
	v.ClientName = render.ProfileName(site, label, sm.PlanName)
	return v
}

// renewalPriceUsable 是门户展示用的「这个价格档还在售、属于本套餐」。真正下单时 order 经 catalog.Offer
// 再判一次，以那边为准。
func renewalPriceUsable(pr catalogsvc.Price, planID uuid.UUID) bool {
	return pr.Status == catalogsvc.PriceActive && pr.PlanID == planID
}

// nextResetAt 取 cycle 周期流量配额行的周期末（已按 sortQuotas 排好：周期起点最晚的在前），与前端显示的
// 那一行是同一行。一次性套餐的流量不清零（只有 total 行），返回 nil。
func nextResetAt(qs []Quota) *time.Time {
	for i := range qs {
		if qs[i].Metric == MetricTraffic && qs[i].Period == PeriodCycle {
			return qs[i].PeriodEnd
		}
	}
	return nil
}

// SetMyLabel 给本人的一份订阅起名、改名或清除名字（label 为 nil 或空白）。不论状态都能改。
// 校验不过回 422（字段 label）；不是本人的回 404；与本人另一份重名回 409，文案点出那一份。
func (s *Service) SetMyLabel(ctx context.Context, subID uuid.UUID, label *string) (LabelResult, error) {
	p, err := principal(ctx)
	if err != nil {
		return LabelResult{}, err
	}
	norm := ""
	if label != nil {
		if norm, err = NormalizeLabel(*label); err != nil {
			return LabelResult{}, err
		}
	}
	var newLabel *string
	if norm != "" {
		newLabel = &norm
	}
	var out LabelResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockOwned(ctx, subID, &p.UserID)
		if err != nil {
			return err
		}
		ok, err := s.repo.SetLabel(ctx, m.ID, p.UserID, newLabel)
		if err != nil {
			return subErr(err)
		}
		if !ok {
			return s.labelTaken(ctx, p.UserID, m.ID, norm)
		}
		s.rtChanged(ctx, tableSubscriptions, realtime.OpUpdate, m.ID, m.UserID)
		plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{m.PlanID})
		if err != nil {
			return err
		}
		site, err := s.settings.Site(ctx)
		if err != nil {
			return err
		}
		out = LabelResult{Label: newLabel, ClientName: render.ProfileName(site.Name, norm, plans[m.PlanID].Name)}
		return s.writeAudit(ctx, ActionLabelChanged, m.ID, map[string]any{"label": newLabel})
	})
	return out, err
}

// labelTaken 是撞了同名的 409：查出占着这个名字的那一份，文案里点出它；查不到也照样 409。
func (s *Service) labelTaken(ctx context.Context, userID, subID uuid.UUID, label string) error {
	msg := "这个名字已经用在你的另一份套餐上了，换一个吧"
	other, ok, err := s.repo.SubscriptionByLabel(ctx, userID, label, subID)
	if err == nil && ok && other.Label != nil {
		if plans, perr := s.catalog.PlansByIDs(ctx, []uuid.UUID{other.PlanID}); perr == nil {
			msg = "这个名字已经用在「" + *other.Label + render.ProfileNameSeparator + plans[other.PlanID].Name + "」上了，换一个吧"
		}
	}
	e := errs.Newf(errs.Conflict, msg)
	e.Fields = map[string]string{"label": msg}
	return e
}
