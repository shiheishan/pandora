package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// maxPackList 是门户流量包明细的最多笔数。
const maxPackList = 100

// MySubscriptions 是门户「我的套餐」：本人的全部订阅（新建在前）与未分配的流量包余量。
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
	out := MySubscriptions{Subscriptions: make([]MySubscription, 0, len(us.Subscriptions)), UnattachedPackBytes: us.UnattachedPackBytes}
	if len(us.Subscriptions) == 0 {
		return out, nil
	}
	site, err := s.settings.Site(ctx)
	if err != nil {
		return MySubscriptions{}, err
	}
	groupID, err := s.users.UserGroupID(ctx, p.UserID)
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
		out.Subscriptions = append(out.Subscriptions, mySubscription(sm, plans[sm.PlanID], prices, groupID, site.Name, now))
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

// mySubscription 拼门户的一份订阅。
func mySubscription(sm Summary, plan catalogsvc.Plan, prices map[uuid.UUID]catalogsvc.Price, groupID *uuid.UUID, site string, now time.Time) MySubscription {
	v := MySubscription{Summary: sm, EffectiveDeviceLimit: sm.PlanMaxDevices}
	if sm.DeviceLimit != nil {
		v.EffectiveDeviceLimit = sm.DeviceLimit
	}
	v.NextResetAt = nextResetAt(sm.Quotas)
	v.Changeable = sm.State.AcceptsChange()
	v.Renewable = v.Changeable && plan.AllowRenewal
	if sm.Price.PriceID != nil {
		if pr, ok := prices[*sm.Price.PriceID]; ok {
			v.RenewalPrice = &RenewalPrice{
				ID:              pr.ID,
				Currency:        pr.Currency,
				UnitAmount:      pr.UnitAmount,
				BillingInterval: pr.BillingInterval,
				IntervalCount:   pr.IntervalCount,
				Available:       renewalPriceUsable(pr, sm.PlanID, groupID, now),
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
	v.ClientName = ProfileName(site, label, sm.PlanName)
	return v
}

// renewalPriceUsable 是门户展示用的「这个价格档还能续」：在售、属于本套餐、组价与用户组相符、在有效期内。
// 真正下单时 order 经 catalog.Offer 再判一次，以那边为准。
func renewalPriceUsable(pr catalogsvc.Price, planID uuid.UUID, groupID *uuid.UUID, now time.Time) bool {
	if pr.Status != catalogsvc.PriceActive || pr.PlanID != planID || !pr.Currency.Valid() {
		return false
	}
	if pr.UserGroupID != nil && (groupID == nil || *groupID != *pr.UserGroupID) {
		return false
	}
	if pr.ValidFrom != nil && pr.ValidFrom.After(now) {
		return false
	}
	return pr.ValidUntil == nil || pr.ValidUntil.After(now)
}

// nextResetAt 取第一条流量配额行（已按 sortQuotas 排好：周期起点最晚的在前）的周期末，与前端显示的那一行是同一行。
func nextResetAt(qs []Quota) *time.Time {
	for i := range qs {
		if qs[i].Metric == MetricTraffic {
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
		plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{m.PlanID})
		if err != nil {
			return err
		}
		site, err := s.settings.Site(ctx)
		if err != nil {
			return err
		}
		out = LabelResult{Label: newLabel, ClientName: ProfileName(site.Name, norm, plans[m.PlanID].Name)}
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
			msg = "这个名字已经用在「" + *other.Label + profileNameSeparator + plans[other.PlanID].Name + "」上了，换一个吧"
		}
	}
	e := errs.Newf(errs.Conflict, msg)
	e.Fields = map[string]string{"label": msg}
	return e
}

// MyTrafficPacks 是门户「我的流量包」：全部剩余与最近 100 笔明细。
func (s *Service) MyTrafficPacks(ctx context.Context) (PackBalance, error) {
	p, err := principal(ctx)
	if err != nil {
		return PackBalance{}, err
	}
	total, err := s.repo.PackRemainingByUser(ctx, p.UserID)
	if err != nil {
		return PackBalance{}, subErr(err)
	}
	rows, err := s.repo.PackGrantsByUser(ctx, p.UserID, maxPackList)
	if err != nil {
		return PackBalance{}, subErr(err)
	}
	out := PackBalance{RemainingBytesTotal: total, Packs: make([]Pack, 0, len(rows))}
	for _, g := range rows {
		out.Packs = append(out.Packs, toPack(g))
	}
	return out, nil
}

// MyTransferPacks 是门户把本人未分配（from 为 nil）或已停用那一份上的流量包转到另一份上；返回挪过去的字节。
func (s *Service) MyTransferPacks(ctx context.Context, from *uuid.UUID, to uuid.UUID) (int64, error) {
	p, err := principal(ctx)
	if err != nil {
		return 0, err
	}
	actor := actorFrom(p)
	var moved int64
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		n, err := s.TransferPacks(ctx, Transfer{UserID: p.UserID, From: from, To: to, Actor: actor})
		if err != nil {
			return err
		}
		moved = n
		if n == 0 {
			return nil
		}
		data := map[string]any{"to_subscription_id": to, "moved_bytes": n}
		if from != nil {
			data["from_subscription_id"] = *from
		}
		return s.writeAudit(ctx, ActionPackTransferred, to, data)
	})
	return moved, err
}
