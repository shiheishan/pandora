package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// MyUsage 读本人一份订阅的按日用量（站点时区切日）。days 为 0 表示覆盖当前流量周期（超过 MaxUsageDays 天的截到最近 MaxUsageDays 天），
// 否则取最近 days 天（1..MaxUsageDays，越界回 422）。不是本人的回 ErrNotFound。
func (s *Service) MyUsage(ctx context.Context, subID uuid.UUID, days int) (DailyUsage, error) {
	p, err := principal(ctx)
	if err != nil {
		return DailyUsage{}, err
	}
	if days != 0 && (days < 1 || days > MaxUsageDays) {
		return DailyUsage{}, errs.Invalid(map[string]string{"days": "必须是 1 到 93 之间的整数"})
	}
	sub, err := s.loadSub(ctx, subID, &p.UserID, repo.NoLock)
	if err != nil {
		return DailyUsage{}, err
	}
	site, err := s.settings.Site(ctx)
	if err != nil {
		return DailyUsage{}, err
	}
	loc := siteLocation(site.Timezone)
	now := s.clock()
	start, end, ok, err := s.repo.TrafficPeriodAt(ctx, sub.ID, now)
	if err != nil {
		return DailyUsage{}, subErr(err)
	}
	if !ok {
		start = sub.CurrentPeriodStart
		end = &sub.CurrentPeriodEnd
	}
	today := dayOf(now, loc)
	from := usageWindow(today, dayOf(start, loc), days)
	byDay, err := s.repo.UsageDays(ctx, sub.ID, from.Format(time.DateOnly), today.Format(time.DateOnly))
	if err != nil {
		return DailyUsage{}, subErr(err)
	}
	points, todayBytes, avg := fillUsageDays(from, today, byDay)
	return DailyUsage{
		Timezone:      loc.String(),
		PeriodStart:   start,
		PeriodEnd:     end,
		Days:          points,
		TodayBytes:    todayBytes,
		AvgDailyBytes: avg,
	}, nil
}

// dayOf 返回 t 在 loc 里的自然日（UTC 零点表示，只用年月日）。
func dayOf(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// usageWindow 返回窗口首日：days > 0 时是最近 days 天；否则从周期起点那天起（含今天）；天数夹在 [1, MaxUsageDays]。
func usageWindow(today, periodStartDay time.Time, days int) time.Time {
	if days <= 0 {
		days = int(today.Sub(periodStartDay).Hours()/24) + 1
	}
	days = min(max(days, 1), MaxUsageDays)
	return today.AddDate(0, 0, -(days - 1))
}

// fillUsageDays 把稀疏的按日行铺成 [from, today] 的连续序列，返回序列、今天的字节数、日均（总量 / 天数，整数除）。
func fillUsageDays(from, today time.Time, byDay map[string]int64) ([]UsageDay, int64, int64) {
	var points []UsageDay
	var total int64
	var todayBytes int64
	for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
		key := d.Format(time.DateOnly)
		b := byDay[key]
		points = append(points, UsageDay{Date: key, Bytes: b})
		total += b
		if d.Equal(today) {
			todayBytes = b
		}
	}
	avg := int64(0)
	if len(points) > 0 {
		avg = total / int64(len(points))
	}
	return points, todayBytes, avg
}
