package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ServingRow 是一份订阅侧可服务的订阅（节点名单用）。
type ServingRow struct {
	ID            uuid.UUID `gorm:"column:id"`
	UserID        uuid.UUID `gorm:"column:user_id"`
	PlanVersionID uuid.UUID `gorm:"column:plan_version_id"`
	NodeUID       int64     `gorm:"column:node_uid"`
	ProxyUUID     uuid.UUID `gorm:"column:proxy_uuid"`
	DeviceLimit   *int      `gorm:"column:device_limit"`
	PeriodEnd     time.Time `gorm:"column:period_end"`
}

// ServingSubscriptions 一条 SQL 取全部订阅侧可服务的订阅，按 node_uid 升序：
// status = 'active' 且到期时间晚于 now；流量没耗尽——本期窗口（trafficQuotaWindowCond）里没有任何一行
// 限量配额已用满，或者挂在这一份上的流量包还有余量。一个指标可以有多个周期（如 total + day），
// 任一限量行用满都算耗尽，与扣量取「最紧的那行」同一口径。
//
// 计划：subscriptions 按 status 过滤（生效中的几乎就是全部在用订阅，顺序扫或 subscriptions_active_end_idx），
// 每行的配额走 quota_balances_sub_metric_period_key 前缀、流量包走 traffic_pack_grants_open_sub_idx，
// 都是按订阅的索引探测；不需要新索引。
func (r *Repo) ServingSubscriptions(ctx context.Context, now time.Time) ([]ServingRow, error) {
	var out []ServingRow
	wNow, wGrace := trafficQuotaWindow(now)
	err := r.d.Conn(ctx).Raw(`
		SELECT s.id, s.user_id, s.plan_version_id, s.node_uid, s.proxy_uuid, s.device_limit,
		       s.current_period_end AS period_end
		  FROM subscriptions s
		 WHERE s.status = 'active'
		   AND s.current_period_end > ?
		   AND (NOT EXISTS (SELECT 1 FROM quota_balances qb
		                     WHERE qb.subscription_id = s.id AND `+trafficQuotaWindowCond+`
		                       AND qb.limit_value IS NOT NULL AND qb.consumed >= qb.limit_value)
		        OR EXISTS (SELECT 1 FROM traffic_pack_grants g
		                    WHERE g.subscription_id = s.id AND g.consumed_bytes < g.granted_bytes))
		 ORDER BY s.node_uid`, now, wNow, wGrace).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []ServingRow{}, nil
	}
	return out, nil
}
