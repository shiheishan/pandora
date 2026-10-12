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
// SQL 条数与用户数无关：订阅、配额、两种流量包余量各一条，加 catalog 的版本与套餐。
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
	unattached, err := s.repo.UnattachedRemainingByUsers(ctx, userIDs)
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
	packs, err := s.repo.PackRemainingBySubscriptions(ctx, subIDs)
	if err != nil {
		return nil, nil, subErr(err)
	}
	plans, err := s.catalog.PlansByIDs(ctx, keys(planSet))
	if err != nil {
		return nil, nil, err
	}
	versions, err := s.catalog.VersionsByIDs(ctx, keys(versionSet))
	if err != nil {
		return nil, nil, err
	}
	quotas := make(map[uuid.UUID][]Quota, len(subs))
	for _, q := range qrows {
		quotas[q.SubscriptionID] = append(quotas[q.SubscriptionID], toQuota(q))
	}
	now := s.clock()
	for _, m := range subs {
		us := out[m.UserID]
		us.Subscriptions = append(us.Subscriptions, summaryOf(m, plans[m.PlanID], versions[m.PlanVersionID], quotas[m.ID], packs[m.ID], now))
		out[m.UserID] = us
	}
	for id, us := range out {
		us.UnattachedPackBytes = unattached[id]
		if i := currentIndex(us.Subscriptions); i >= 0 {
			cur := us.Subscriptions[i]
			us.Current = &cur
		}
		out[id] = us
	}
	return out, plans, nil
}

// summaryOf 拼一份订阅的摘要。
func summaryOf(m model.Subscription, plan catalogsvc.Plan, v catalogsvc.Version, qs []Quota, packLeft int64, now time.Time) Summary {
	sub := toSubscription(m)
	if qs == nil {
		qs = []Quota{}
	}
	sortQuotas(qs)
	return Summary{
		Subscription:       sub,
		State:              sub.StateAt(now),
		PlanName:           plan.Name,
		PlanVersion:        v.Version,
		PlanMaxDevices:     v.MaxDevices,
		QuotaResetStrategy: v.QuotaResetStrategy,
		Quotas:             qs,
		PackRemainingBytes: packLeft,
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
	ids, err := s.repo.PeriodOrderIDs(ctx, subID)
	if err != nil {
		return nil, subErr(err)
	}
	return ids, nil
}
