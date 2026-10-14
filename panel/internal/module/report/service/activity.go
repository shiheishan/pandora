package service

import (
	"context"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/aegispanel/aegis/internal/module/report/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// LoginAction 是 auth 模块写登录成功审计时用的动作名（auth 没有导出常量，这里与它保持一致）。
const LoginAction = "user.login"

// ActivityRetentionDays 是 activity_daily 的保留天数。
const ActivityRetentionDays = 400

// activityActions 是行为趋势要数的三个动作。
var activityActions = auditsvc.ActivityActions{
	Registered:   identitysvc.ActionRegistered,
	Login:        LoginAction,
	OrderCreated: ordersvc.ActionOrderCreated,
}

// finalized 判断汇总行是否已定稿（算的时刻不早于该日结束后整一天）。
func finalized(row model.ActivityDaily, loc *time.Location) bool {
	d, err := time.ParseInLocation(time.DateOnly, row.Day, loc)
	if err != nil {
		return false
	}
	return !row.ComputedAt.Before(addDays(d, 2))
}

// liveActivity 现算 [from, to) 内每一天的行为汇总（站点时区零点）。
func (s *Service) liveActivity(ctx context.Context, from, to time.Time) (map[string]model.ActivityDaily, error) {
	loc := from.Location()
	tz := loc.String()
	computedAt := s.now()

	actRows, err := s.deps.Activity.ActivityByDay(ctx, from, to, activityActions)
	if err != nil {
		return nil, err
	}
	userRows, err := s.deps.Subscriptions.ActiveUsersByDay(ctx, from, to)
	if err != nil {
		return nil, err
	}

	out := map[string]model.ActivityDaily{}
	for _, r := range actRows {
		out[r.Day] = model.ActivityDaily{
			TZ: tz, Day: r.Day,
			Registered: r.Registered, Logins: r.Logins, Orders: r.Orders, UniqueIPs: r.UniqueIPs,
			ComputedAt: computedAt,
		}
	}
	for _, r := range userRows {
		row, ok := out[r.Day]
		if !ok {
			row = model.ActivityDaily{TZ: tz, Day: r.Day, ComputedAt: computedAt}
		}
		row.ActiveUsers = r.Users
		out[r.Day] = row
	}
	return out, nil
}

// ActivityTimeseries 给最近 days 天（含今天，站点时区）的行为趋势，缺的日子补 0。
// 读路径用库里的行（今天、昨天也是定时任务算好的，最多晚一个任务周期），只有没有行的日子才现算（刚装好、任务还没跑过、站点时区刚改过）。
func (s *Service) ActivityTimeseries(ctx context.Context, days int) ([]ActivityPoint, error) {
	if days == 0 {
		days = 14
	}
	if days < 1 || days > 90 {
		return nil, errs.Invalid(map[string]string{"days": "只支持 1–90 天"})
	}

	loc, err := s.location(ctx)
	if err != nil {
		return nil, err
	}
	tz := loc.String()
	now := s.now()
	today := midnight(now, loc)
	from := addDays(today, -(days - 1))
	end := addDays(today, 1)

	rows, err := s.repo.ActivityRows(ctx, tz, dayString(from), dayString(end))
	if err != nil {
		return nil, err
	}
	byDay := map[string]model.ActivityDaily{}
	for _, r := range rows {
		byDay[r.Day] = r
	}

	var missing []time.Time
	for d := from; d.Before(end); d = addDays(d, 1) {
		if _, ok := byDay[dayString(d)]; !ok {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		liveFrom := missing[0]
		live, err := s.liveActivity(ctx, liveFrom, end)
		if err != nil {
			return nil, err
		}
		for _, d := range missing {
			key := dayString(d)
			if row, ok := live[key]; ok {
				byDay[key] = row
			}
		}
	}

	out := make([]ActivityPoint, 0, days)
	for d := from; !d.After(today); d = addDays(d, 1) {
		key := dayString(d)
		pt := ActivityPoint{Day: d.Format("01-02")}
		if row, ok := byDay[key]; ok {
			pt.Registered = row.Registered
			pt.Logins = row.Logins
			pt.Orders = row.Orders
			pt.UniqueIPs = row.UniqueIPs
			pt.ActiveUsers = row.ActiveUsers
		}
		out = append(out, pt)
	}
	return out, nil
}

// RefreshActivity 补齐漏算的日子，并重算前天、昨天与今天写进 activity_daily（已定稿的跳过）；定时任务调。
func (s *Service) RefreshActivity(ctx context.Context) error {
	loc, err := s.location(ctx)
	if err != nil {
		return err
	}
	tz := loc.String()
	now := s.now()
	today := midnight(now, loc)
	windowStart := addDays(today, -90)

	rows, err := s.repo.ActivityRows(ctx, tz, dayString(windowStart), dayString(today))
	if err != nil {
		return err
	}
	stored := map[string]model.ActivityDaily{}
	for _, r := range rows {
		stored[r.Day] = r
	}

	// 查读窗口（90 天）里已结束、但未定稿的历史日子（大前天及更早），从新到旧最多补 7 天
	var missing []time.Time
	for d := addDays(today, -3); !d.Before(windowStart); d = addDays(d, -1) {
		r, ok := stored[dayString(d)]
		if !ok || !finalized(r, loc) {
			missing = append(missing, d)
			if len(missing) == 7 {
				break
			}
		}
	}

	daysToCompute := map[string]time.Time{}
	for _, d := range missing {
		daysToCompute[dayString(d)] = d
	}
	// 再照旧算前天和昨天（未定稿的），以及今天（今天永远未定稿）
	for d := addDays(today, -2); !d.After(addDays(today, -1)); d = addDays(d, 1) {
		r, ok := stored[dayString(d)]
		if !ok || !finalized(r, loc) {
			daysToCompute[dayString(d)] = d
		}
	}
	daysToCompute[dayString(today)] = today

	minDay := today
	for _, d := range daysToCompute {
		if d.Before(minDay) {
			minDay = d
		}
	}

	end := addDays(today, 1)
	live, err := s.liveActivity(ctx, minDay, end)
	if err != nil {
		return err
	}
	computedAt := s.now()
	var upsert []model.ActivityDaily
	for d := minDay; d.Before(end); d = addDays(d, 1) {
		key := dayString(d)
		if _, ok := daysToCompute[key]; !ok {
			continue
		}
		row := model.ActivityDaily{TZ: tz, Day: key, ComputedAt: computedAt}
		if r, ok := live[key]; ok {
			row.Registered = r.Registered
			row.Logins = r.Logins
			row.Orders = r.Orders
			row.UniqueIPs = r.UniqueIPs
			row.ActiveUsers = r.ActiveUsers
		}
		upsert = append(upsert, row)
	}
	return s.repo.UpsertActivity(ctx, upsert)
}

// PurgeActivity 删站点时区今天往前 ActivityRetentionDays 天以前的汇总行，一次至多 batch 行，返回删除行数。
func (s *Service) PurgeActivity(ctx context.Context, batch int) (int, error) {
	loc, err := s.location(ctx)
	if err != nil {
		return 0, err
	}
	before := addDays(midnight(s.now(), loc), -ActivityRetentionDays)
	return s.repo.PurgeActivity(ctx, dayString(before), batch)
}
