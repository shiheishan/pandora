package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TrafficWindowEnd 返回一份订阅本期流量配额行的周期末（下次重置时刻）；没有配额行或该行没有周期末（total 类）返回 nil。
// 取行口径与 PullByTokenHash 的 LATERAL 相同（同一窗口、同一排序取第一行），拉取时展示的用量与重置时刻对得上。
// 只在后台开了「信息节点」时由拉取路径调用；走 quota_balances_sub_metric_period_key 前缀，按订阅的索引探测。
func (r *Repo) TrafficWindowEnd(ctx context.Context, subID uuid.UUID, now time.Time) (*time.Time, error) {
	wNow, wGrace := trafficQuotaWindow(now)
	var rows []struct {
		PeriodEnd *time.Time `gorm:"column:period_end"`
	}
	err := r.d.Conn(ctx).Raw(`
		SELECT qb.period_end
		  FROM quota_balances qb
		 WHERE qb.subscription_id = ? AND `+trafficQuotaWindowCond+`
		 ORDER BY (qb.limit_value IS NULL), (qb.limit_value - qb.consumed), qb.period_end DESC NULLS LAST
		 LIMIT 1`, subID, wNow, wGrace).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].PeriodEnd, nil
}
