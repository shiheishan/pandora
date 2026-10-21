package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
)

// Lock 在 ctx 的事务里锁住一份订阅（FOR NO KEY UPDATE）并返回；owner 非 nil 时校验归属（不符回 ErrNotFound）。
// 给 order、giftcard 在同一事务里先锁订阅、再算价与记账用（锁序：订单 → 订阅）。必须在事务里调用。
func (s *Service) Lock(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (Subscription, error) {
	m, err := s.lockOwned(ctx, id, owner)
	if err != nil {
		return Subscription{}, err
	}
	return toSubscription(m), nil
}

// Get 读一份订阅（不加锁）；owner 非 nil 时校验归属。
func (s *Service) Get(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (Subscription, error) {
	m, err := s.loadSub(ctx, id, owner, repo.NoLock)
	if err != nil {
		return Subscription{}, err
	}
	return toSubscription(m), nil
}

// Quotas 读一份订阅的配额行（按 metric 升序、周期起点倒序；永不为 nil）。
func (s *Service) Quotas(ctx context.Context, subID uuid.UUID) ([]Quota, error) {
	rows, err := s.repo.QuotasBySubscriptions(ctx, []uuid.UUID{subID}, false)
	if err != nil {
		return nil, subErr(err)
	}
	out := make([]Quota, 0, len(rows))
	for _, q := range rows {
		out = append(out, toQuota(q))
	}
	sortQuotas(out)
	return out, nil
}

// ByUser 读一个用户的全部订阅摘要。
func (s *Service) ByUser(ctx context.Context, userID uuid.UUID) (UserSubscriptions, error) {
	all, err := s.SummariesByUsers(ctx, []uuid.UUID{userID})
	if err != nil {
		return UserSubscriptions{}, err
	}
	return all[userID], nil
}

// RenewableForPlan 取用户在这个套餐上可以原地续费的订阅（没停用的里面生效中优先、到期晚优先、创建晚优先）；
// 没有返回 false。lock 为真时锁住选中的那一行并复核（调用方随后要在它上面续费），必须在事务里调用。
// 门户新购同套餐时 order 用它拦截改走续费；套餐卡、后台人工单用它定默认续哪一份。
// 一次性套餐时它就是用户手里「可叠加的那一份」（同套餐、未停用、到期最晚优先）。
func (s *Service) RenewableForPlan(ctx context.Context, userID, planID uuid.UUID, lock bool) (Subscription, bool, error) {
	rows, err := s.repo.SubscriptionsByUserPlan(ctx, userID, planID)
	if err != nil {
		return Subscription{}, false, subErr(err)
	}
	now := s.clock()
	cands := make([]Summary, 0, len(rows))
	for _, m := range rows {
		sub := toSubscription(m)
		st := sub.StateAt(now)
		if st.AcceptsChange() {
			cands = append(cands, Summary{Subscription: sub, State: st})
		}
	}
	i := currentIndex(cands)
	if i < 0 {
		return Subscription{}, false, nil
	}
	if !lock {
		return cands[i].Subscription, true, nil
	}
	m, err := s.lockOwned(ctx, cands[i].ID, &userID)
	if err != nil {
		return Subscription{}, false, err
	}
	// 锁住后复核：读和锁之间可能刚被关窗，或被并发的换套餐换到了别的套餐
	if m.PlanID != planID || !StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, s.clock()).AcceptsChange() {
		return Subscription{}, false, nil
	}
	return toSubscription(m), true, nil
}

// SummariesByUsers 批量取一组用户的订阅摘要；每个入参用户都在结果里（没有订阅的是空列表）。
// SQL 条数与用户数无关：订阅、配额、重置次数各一条，加 catalog 的版本与套餐。
func (s *Service) SummariesByUsers(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]UserSubscriptions, error) {
	out, _, err := s.summarize(ctx, userIDs)
	return out, err
}

// summarize 是 SummariesByUsers 的实现，顺带返回读到的套餐（门户要套餐的 AllowRenewal）。
func (s *Service) summarize(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]UserSubscriptions, map[uuid.UUID]catalogsvc.Plan, error) {
	out := make(map[uuid.UUID]UserSubscriptions, len(userIDs))
	for _, id := range userIDs {
		out[id] = UserSubscriptions{Subscriptions: []Summary{}}
	}
	if len(userIDs) == 0 {
		return out, map[uuid.UUID]catalogsvc.Plan{}, nil
	}
	subs, err := s.repo.SubscriptionsByUsers(ctx, userIDs)
	if err != nil {
		return nil, nil, subErr(err)
	}
	subIDs := make([]uuid.UUID, 0, len(subs))
	planSet := map[uuid.UUID]struct{}{}
	versionSet := map[uuid.UUID]struct{}{}
	for _, m := range subs {
		subIDs = append(subIDs, m.ID)
		planSet[m.PlanID] = struct{}{}
		versionSet[m.PlanVersionID] = struct{}{}
	}
	qrows, err := s.repo.QuotasBySubscriptions(ctx, subIDs, false)
	if err != nil {
		return nil, nil, subErr(err)
	}
	plans, err := s.catalog.PlansByIDs(ctx, keys(planSet))
	if err != nil {
		return nil, nil, err
	}
	for _, m := range subs {
		plan := plans[m.PlanID]
		if plan.Kind == catalogsvc.KindSubscription && plan.CurrentVersionID != nil {
			versionSet[*plan.CurrentVersionID] = struct{}{}
		}
	}
	versions, err := s.catalog.VersionsByIDs(ctx, keys(versionSet))
	if err != nil {
		return nil, nil, err
	}
	quotas := make(map[uuid.UUID][]Quota, len(subs))
	for _, q := range qrows {
		quotas[q.SubscriptionID] = append(quotas[q.SubscriptionID], toQuota(q))
	}
	// 每份订阅按套餐类型算付费重置的起数时刻，一次批量数完全部次数
	since := make([]repo.ResetSince, 0, len(subs))
	for _, m := range subs {
		since = append(since, repo.ResetSince{
			SubscriptionID: m.ID,
			Since:          resetSince(plans[m.PlanID].Kind, m, quotas[m.ID]),
		})
	}
	resets, err := s.repo.PaidResetCounts(ctx, since)
	if err != nil {
		return nil, nil, subErr(err)
	}
	now := s.clock()
	for _, m := range subs {
		us := out[m.UserID]
		us.Subscriptions = append(us.Subscriptions, summaryOf(m, plans[m.PlanID], versions, quotas[m.ID], resets[m.ID], now))
		out[m.UserID] = us
	}
	for id, us := range out {
		if i := currentIndex(us.Subscriptions); i >= 0 {
			cur := us.Subscriptions[i]
			us.Current = &cur
		}
		out[id] = us
	}
	return out, plans, nil
}

// eqPtr 比较两个可比较类型的指针是否相等（两边都 nil 为真）。
func eqPtr[T comparable](a, b *T) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// usageOf 从套餐版本拼一份用量（设备覆盖优先）。
func usageOf(v catalogsvc.Version, kind string, deviceOverride *int) Usage {
	v = withKind(v, kind)
	var traffic *int64
	if q, ok := quotaDef(v, MetricTraffic, trafficPeriod(kind)); ok {
		traffic = q.Limit
	}
	dev := v.MaxDevices
	if deviceOverride != nil {
		dev = deviceOverride
	}
	return Usage{
		TrafficBytes: traffic,
		DeviceLimit:  dev,
		ThrottleKbps: v.ThrottleKbps,
	}
}

// sameUsage 比较两份用量三项是否相同。
func sameUsage(a, b Usage) bool {
	return eqPtr(a.TrafficBytes, b.TrafficBytes) && eqPtr(a.DeviceLimit, b.DeviceLimit) && eqPtr(a.ThrottleKbps, b.ThrottleKbps)
}

// nextUsageOf 算下个周期会用的用量；不满足条件时返回 nil。
func nextUsageOf(m model.Subscription, plan catalogsvc.Plan, state State, cur Usage, versions map[uuid.UUID]catalogsvc.Version) *Usage {
	if plan.Kind != catalogsvc.KindSubscription || state == StateEnded {
		return nil
	}
	if plan.CurrentVersionID == nil || *plan.CurrentVersionID == m.PlanVersionID {
		return nil
	}
	nv, ok := versions[*plan.CurrentVersionID]
	if !ok {
		return nil
	}
	next := usageOf(nv, plan.Kind, m.DeviceLimit)
	if sameUsage(cur, next) {
		return nil
	}
	return &next
}

// summaryOf 拼一份订阅的摘要。流量三项取套餐类型对应的流量配额行（订阅套餐 cycle、一次性 total）里
// 周期起点最晚的那一行；没有这行上限为 nil（不限或没有流量配额）、其余按 0。
func summaryOf(m model.Subscription, plan catalogsvc.Plan, versions map[uuid.UUID]catalogsvc.Version, qs []Quota, resetsUsed int, now time.Time) Summary {
	sub := toSubscription(m)
	if qs == nil {
		qs = []Quota{}
	}
	sortQuotas(qs)
	var trafficLimit *int64
	var trafficBonus, trafficUsed int64
	for _, q := range qs {
		if q.Metric == MetricTraffic && q.Period == trafficPeriod(plan.Kind) {
			trafficLimit = q.Limit
			trafficBonus = q.Bonus
			trafficUsed = q.Consumed
			break
		}
	}
	v := versions[m.PlanVersionID]
	st := sub.StateAt(now)
	cur := usageOf(v, plan.Kind, m.DeviceLimit)
	return Summary{
		Subscription:   sub,
		State:          st,
		PlanName:       plan.Name,
		PlanKind:       plan.Kind,
		PlanVersion:    v.Version,
		PlanMaxDevices: v.MaxDevices,
		Permanent:      plan.Kind == catalogsvc.KindOneTime && IsPermanent(m.CurrentPeriodEnd),
		TrafficLimit:   trafficLimit,
		TrafficBonus:   trafficBonus,
		TrafficUsed:    trafficUsed,
		ResetsUsed:     resetsUsed,
		ResetLimit:     plan.ResetLimit,
		ResetPrice:     plan.ResetPrice,
		Quotas:         qs,
		Usage:          cur,
		NextUsage:      nextUsageOf(m, plan, st, cur, versions),
	}
}

// keys 返回集合的元素。
func keys(set map[uuid.UUID]struct{}) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// PeriodOrderIDs 是本周期的付费来源订单（按发生先后，永不为 nil）：order 换套餐时据此算剩余价值的基数与付费总时长。
// 礼品卡、后台加时长没有订单，不在里面（它们加的天数折不成钱）。调用方建单时已锁订阅行。
func (s *Service) PeriodOrderIDs(ctx context.Context, subID uuid.UUID) ([]uuid.UUID, error) {
	m, err := s.PeriodOrderIDsBySubscriptions(ctx, []uuid.UUID{subID})
	if err != nil {
		return nil, err
	}
	return m[subID], nil
}

// PeriodOrderIDsBySubscriptions 是 PeriodOrderIDs 的批量版（报价、后台开单预览一次读多份）：每个传入的订阅都有一项，
// 没有订单时是空切片（永不为 nil）。
func (s *Service) PeriodOrderIDsBySubscriptions(ctx context.Context, subIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	m, err := s.repo.PeriodOrderIDsBySubscriptions(ctx, subIDs)
	if err != nil {
		return nil, subErr(err)
	}
	for _, id := range subIDs {
		if m[id] == nil {
			m[id] = []uuid.UUID{}
		}
	}
	return m, nil
}
