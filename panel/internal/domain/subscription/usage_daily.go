// [INPUT]: 依赖 subscription_usage_daily（迁移 00072，由 nodefabric 流量上报写入）、subscriptions / users / tenants / quota_balances，依赖 nodefabric 的 UsageLocation / UsageDay 日界口径，依赖 platform/db
// [OUTPUT]: 对外提供 DailyUsage、UsageDayPoint、MaxUsageDays 与 Service.DailyUsage
// [POS]: subscription 的门户按日用量读模型（门户-02 `GET v1/me/subscriptions/{id}/usage`），与 service.go 的订阅分发并列；只读不写
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// MaxUsageDays 是一次最多回看的天数：约一个季度，够覆盖最长的按月周期，
// 又不让「从不重置」的订阅把几年的行一次拉出来。
const MaxUsageDays = 93

// defaultUsageDays 是没有任何周期信息（无流量配额行、订阅也没有周期起点）
// 时的缺省窗口。
const defaultUsageDays = 30

// DailyUsage 是一条订阅在一个窗口内的按日用量。Days 覆盖窗口内每一天
// （没有流量的日子补 0），按日期升序，最后一天是用户时区里的今天。
type DailyUsage struct {
	Timezone      string
	PeriodStart   time.Time
	PeriodEnd     *time.Time
	Days          []UsageDayPoint
	TodayBytes    int64
	AvgDailyBytes int64
}

type UsageDayPoint struct {
	Date  string // YYYY-MM-DD
	Bytes int64
}

// usageWindow 算出要展示的日期闭区间 [from, today]（都是 UsageDay 形式）。
// days > 0 时就是最近 days 天；否则从本期流量周期开始那天起，超过
// MaxUsageDays 的截到最近 MaxUsageDays 天。周期起点落在今天之后（不应发生）
// 时只给今天。
func usageWindow(today, periodStartDay time.Time, days int) time.Time {
	if days <= 0 {
		days = int(today.Sub(periodStartDay).Hours()/24) + 1
	}
	days = min(max(days, 1), MaxUsageDays)
	return today.AddDate(0, 0, -(days - 1))
}

// fillUsageDays 把稀疏的按日行铺成 [from, today] 的连续序列并算今天与日均。
// 日均按窗口天数（含今天）平均。
func fillUsageDays(from, today time.Time, byDay map[string]int64) ([]UsageDayPoint, int64, int64) {
	var points []UsageDayPoint
	var total int64
	for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
		key := d.Format(time.DateOnly)
		points = append(points, UsageDayPoint{Date: key, Bytes: byDay[key]})
		total += byDay[key]
	}
	return points, byDay[today.Format(time.DateOnly)], total / int64(len(points))
}

// DailyUsage 读本人一条订阅的按日用量。days 为 0 表示「覆盖当前流量周期」，
// 否则取最近 days 天（调用方已校验 1..MaxUsageDays）。订阅不存在或不属于
// 本人返回 ErrNotFound，与节点预览同一个中性出口。
//
// 本期流量周期取当前生效的 traffic.bytes 配额行；没有配额行时退回订阅的
// current_period_start / end；两者都没有时缺省看最近 defaultUsageDays 天，
// 以窗口首日为起点。
func (s *Service) DailyUsage(ctx context.Context, tenantID, userID, rawSubscriptionID string, days int) (*DailyUsage, error) {
	subscriptionID, err := uuid.Parse(rawSubscriptionID)
	if err != nil {
		return nil, ErrNotFound
	}
	out := &DailyUsage{}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		var userTZ, tenantTZ string
		var periodStart, periodEnd *time.Time
		var now time.Time
		if err := tx.QueryRow(ctx, `
			SELECT u.timezone, t.timezone,
			       COALESCE(q.period_start, s.current_period_start),
			       CASE WHEN q.period_start IS NOT NULL THEN q.period_end
			            ELSE s.current_period_end END,
			       now()
			  FROM subscriptions s
			  JOIN users u   ON u.tenant_id = s.tenant_id AND u.id = s.user_id
			  JOIN tenants t ON t.id = s.tenant_id
			  LEFT JOIN LATERAL (
			    SELECT period_start, period_end FROM quota_balances
			     WHERE tenant_id = s.tenant_id AND subscription_id = s.id
			       AND metric = 'traffic.bytes'
			       AND period_start <= now()
			       AND (period_end IS NULL OR period_end > now())
			     ORDER BY period_start DESC LIMIT 1) q ON true
			 WHERE s.tenant_id = $1 AND s.id = $2::uuid AND s.user_id = $3::uuid`,
			tenantID, subscriptionID, userID).Scan(&userTZ, &tenantTZ, &periodStart, &periodEnd, &now); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		loc := nodefabric.UsageLocation(userTZ, tenantTZ)
		today := nodefabric.UsageDay(now, loc)
		startDay := today
		if periodStart != nil {
			startDay = nodefabric.UsageDay(*periodStart, loc)
		} else if days == 0 {
			days = defaultUsageDays
		}
		from := usageWindow(today, startDay, days)

		rows, err := tx.Query(ctx, `
			SELECT to_char(day, 'YYYY-MM-DD'), bytes FROM subscription_usage_daily
			 WHERE tenant_id = $1 AND subscription_id = $2::uuid
			   AND day BETWEEN $3::date AND $4::date`,
			tenantID, subscriptionID, from, today)
		if err != nil {
			return err
		}
		byDay := map[string]int64{}
		for rows.Next() {
			var day string
			var bytes int64
			if err := rows.Scan(&day, &bytes); err != nil {
				rows.Close()
				return err
			}
			byDay[day] = bytes
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		out.Timezone = loc.String()
		if periodStart != nil {
			out.PeriodStart = *periodStart
		} else {
			// 没有任何周期信息时，以窗口首日在用户时区的零点为起点。
			out.PeriodStart = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
		}
		out.PeriodEnd = periodEnd
		out.Days, out.TodayBytes, out.AvgDailyBytes = fillUsageDays(from, today, byDay)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
