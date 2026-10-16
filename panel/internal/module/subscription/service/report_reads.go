package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ReportSubscriptionCounts 是看板的订阅计数；「生效」与 StateAt 的 StateLive 同一口径。
type ReportSubscriptionCounts struct {
	Live     int64 // 生效：status = active 且 current_period_end > now 且 renewal_closed_at 为空
	Expiring int64 // 生效且 current_period_end < expiringBefore
	NotLive  int64 // 其余全部（已到期、可救回、已停用）
	NewLive  int64 // 生效且 created_at >= createdSince
}

// ReportSubscriptionCounts 一条 SQL 数出看板的订阅计数。now、expiringBefore（now + 7 天）、createdSince（now − 7 天）由调用方给。
func (s *Service) ReportSubscriptionCounts(ctx context.Context, now, expiringBefore, createdSince time.Time) (ReportSubscriptionCounts, error) {
	row, err := s.repo.ReportSubscriptionCounts(ctx, now, expiringBefore, createdSince)
	if err != nil {
		return ReportSubscriptionCounts{}, subErr(err)
	}
	return ReportSubscriptionCounts{
		Live:     row.Live,
		Expiring: row.Expiring,
		NotLive:  row.NotLive,
		NewLive:  row.NewLive,
	}, nil
}

// LiveSubscriptionCounts 是每个套餐的生效订阅数（后台套餐列表「有效订阅」，catalog 经 SubscriptionCounter 调）；没有生效订阅的套餐不在结果里。
func (s *Service) LiveSubscriptionCounts(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	rows, err := s.repo.LiveCountsByPlan(ctx, planIDs, s.clock())
	if err != nil {
		return nil, subErr(err)
	}
	out := make(map[uuid.UUID]int64, len(rows))
	for _, r := range rows {
		out[r.PlanID] = r.N
	}
	return out, nil
}

// OwnersBySubscription 批量取订阅的主人：订阅 id → 用户 id（一条 SQL）；不存在的 id 不在结果里，ids 为空不查库。
// 调用方负责把 ids 分批（每批不超过 5000）。
func (s *Service) OwnersBySubscription(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]uuid.UUID{}, nil
	}
	rows, err := s.repo.OwnersBySubscription(ctx, ids)
	if err != nil {
		return nil, subErr(err)
	}
	out := make(map[uuid.UUID]uuid.UUID, len(rows))
	for _, r := range rows {
		out[r.ID] = r.UserID
	}
	return out, nil
}

// ActiveUsersDay 是站点时区某一天的活跃用户数。
type ActiveUsersDay struct {
	Day   string // YYYY-MM-DD
	Users int64
}

// ActiveUsersByDay 数 [from, to) 每一天的活跃用户：当天有成功的订阅拉取（按站点时区切日），或当天按日用量 > 0（usage_daily 的 day 本来就是站点时区的日），两路按用户去重。
// 没有活跃用户的日子不在结果里，按日子升序。
func (s *Service) ActiveUsersByDay(ctx context.Context, from, to time.Time) ([]ActiveUsersDay, error) {
	tz := from.Location().String()
	fromDay := from.Format("2006-01-02")
	toDay := to.Format("2006-01-02")
	rows, err := s.repo.ActiveUsersByDay(ctx, tz, from, to, fromDay, toDay)
	if err != nil {
		return nil, subErr(err)
	}
	out := make([]ActiveUsersDay, len(rows))
	for i, r := range rows {
		out[i] = ActiveUsersDay{Day: r.Day, Users: r.Users}
	}
	return out, nil
}
