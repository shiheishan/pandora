package service

import (
	"context"
	"time"
)

// ActivityActions 是按天行为统计要数的三个动作名（由 report 传入，口径在各属主模块的常量）。
type ActivityActions struct {
	Registered   string // 注册成功，如 "user.registered"
	Login        string // 登录成功（只数 outcome = success），如 "user.login"
	OrderCreated string // 下单，如 "order.created"
}

// ActivityDay 是站点时区某一天的行为统计。
type ActivityDay struct {
	Day        string // YYYY-MM-DD
	Registered int64
	Logins     int64
	Orders     int64
	UniqueIPs  int64 // 当天全部审计事件里不同的来源 IP（按 ip_hash 去重，NULL 不计）
}

// ActivityByDay 按站点时区的日汇总 [from, to) 内的审计事件；没有事件的日子不在结果里，按日子升序。
func (s *Service) ActivityByDay(ctx context.Context, from, to time.Time, a ActivityActions) ([]ActivityDay, error) {
	tz := from.Location().String()
	rows, err := s.repo.ActivityByDay(ctx, tz, from, to, a.Registered, a.Login, a.OrderCreated)
	if err != nil {
		return nil, err
	}
	out := make([]ActivityDay, len(rows))
	for i, row := range rows {
		out[i] = ActivityDay{
			Day:        row.Day,
			Registered: row.Registered,
			Logins:     row.Logins,
			Orders:     row.Orders,
			UniqueIPs:  row.UniqueIPs,
		}
	}
	return out, nil
}
