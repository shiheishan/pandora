package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 重置日志分页的默认与上限。
const (
	defaultResetLogLimit = 50
	maxResetLogLimit     = 200
	// resetStatsWindowDuration 是重置统计的时间窗
	resetStatsWindowDuration = 30 * 24 * time.Hour
)

// ListTrafficResets 是后台流量重置日志（创建时间倒序），带用户邮箱、操作人邮箱与套餐名；同时返回总数。
// 用户、操作人邮箱与套餐名按批取，SQL 条数与页大小无关。
func (s *Service) ListTrafficResets(ctx context.Context, f ResetLogFilter) ([]ResetLogView, int64, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return nil, 0, err
	}
	switch f.Reason {
	case "", ResetRenewal, ResetCycleRoll, ResetManual, ResetGiftCard, ResetPlanChange:
	default:
		return nil, 0, errs.Invalid(map[string]string{"reason": "不支持的重置原因"})
	}
	if f.Limit <= 0 || f.Limit > maxResetLogLimit {
		f.Limit = defaultResetLogLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	rows, total, err := s.repo.ListResetLogs(ctx, repo.ResetLogQuery{UserID: f.UserID, Reason: f.Reason, Limit: f.Limit, Offset: f.Offset})
	if err != nil {
		return nil, 0, subErr(err)
	}
	out := make([]ResetLogView, 0, len(rows))
	if len(rows) == 0 {
		return out, total, nil
	}
	userSet := map[uuid.UUID]struct{}{}
	subSet := map[uuid.UUID]struct{}{}
	for _, r := range rows {
		userSet[r.UserID] = struct{}{}
		if r.ActorID != nil {
			userSet[*r.ActorID] = struct{}{}
		}
		subSet[r.SubscriptionID] = struct{}{}
	}
	emails, err := s.users.Emails(ctx, keys(userSet))
	if err != nil {
		return nil, 0, err
	}
	subs, err := s.repo.SubscriptionsByIDs(ctx, keys(subSet))
	if err != nil {
		return nil, 0, subErr(err)
	}
	planOf := make(map[uuid.UUID]uuid.UUID, len(subs))
	planSet := map[uuid.UUID]struct{}{}
	for _, m := range subs {
		planOf[m.ID] = m.PlanID
		planSet[m.PlanID] = struct{}{}
	}
	plans, err := s.catalog.PlansByIDs(ctx, keys(planSet))
	if err != nil {
		return nil, 0, err
	}
	for _, r := range rows {
		v := ResetLogView{
			ID:             r.ID,
			UserEmail:      emails[r.UserID],
			PlanName:       plans[planOf[r.SubscriptionID]].Name,
			Metric:         r.Metric,
			Reason:         r.Reason,
			ConsumedBefore: r.ConsumedBefore,
			CreatedAt:      r.CreatedAt,
		}
		if r.ActorID != nil {
			v.ActorEmail = emails[*r.ActorID]
		}
		if r.Note != nil {
			v.Note = *r.Note
		}
		out = append(out, v)
	}
	return out, total, nil
}

// TrafficResetStats 是后台近 30 天的重置统计：总次数、按原因的次数、释放的已用量合计、手动重置次数。
func (s *Service) TrafficResetStats(ctx context.Context) (ResetStats, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return ResetStats{}, err
	}
	since := s.clock().Add(-resetStatsWindowDuration)
	rows, err := s.repo.ResetStatsSince(ctx, since)
	if err != nil {
		return ResetStats{}, subErr(err)
	}
	out := ResetStats{ByReason: map[string]int{}}
	for _, r := range rows {
		out.Last30Days += r.Count
		out.ByReason[r.Reason] = r.Count
		out.FreedBytes += r.Freed
		if r.Reason == ResetManual {
			out.ManualCount = r.Count
		}
	}
	return out, nil
}
