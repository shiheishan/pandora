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
// status = 'active' 且到期时间晚于 now；流量没耗尽——最近一期的流量配额（取法与 PullByTokenHash 相同）不存在、
// 不限或还有余量，或者挂在这一份上的流量包还有余量。
//
// 计划：subscriptions 按 status 过滤（生效中的几乎就是全部在用订阅，顺序扫或 subscriptions_active_end_idx），
// 每行的配额走 quota_balances_sub_metric_period_key 前缀、流量包走 traffic_pack_grants_open_sub_idx，
// 都是按订阅的索引探测；不需要新索引。
func (r *Repo) ServingSubscriptions(ctx context.Context, now time.Time) ([]ServingRow, error) {
	var out []ServingRow
	err := r.d.Conn(ctx).Raw(`
		SELECT s.id, s.user_id, s.plan_version_id, s.node_uid, s.proxy_uuid, s.device_limit,
		       s.current_period_end AS period_end
		  FROM subscriptions s
		  LEFT JOIN LATERAL (
			SELECT qb.limit_value, qb.consumed
			  FROM quota_balances qb
			 WHERE qb.subscription_id = s.id AND qb.metric = 'traffic.bytes'
			 ORDER BY qb.period_end DESC NULLS LAST
			 LIMIT 1
		  ) q ON true
		 WHERE s.status = 'active'
		   AND s.current_period_end > ?
		   AND (q.limit_value IS NULL
		        OR q.consumed < q.limit_value
		        OR EXISTS (SELECT 1 FROM traffic_pack_grants g
		                    WHERE g.subscription_id = s.id AND g.consumed_bytes < g.granted_bytes))
		 ORDER BY s.node_uid`, now).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []ServingRow{}, nil
	}
	return out, nil
}
