package adminops

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// DatabaseStats 是数据库体积与连接数：扩容和排查慢查询时最先要看的两个数。
type DatabaseStats struct {
	SizeBytes      int64
	Connections    int
	MaxConnections int
}

// NodeFabricCounts 是未退役节点的总数、90 秒内有心跳的数量与配置落后的数量（落后按生效版本算，见 SystemCounts）。
type NodeFabricCounts struct {
	Total, Online, Lagging int64
}

// DeliveryCounts 是某个通知渠道的排队、正在重试与累计失败数。
type DeliveryCounts struct {
	Queued, Retrying, Failed int64
}

// SystemCounts 是系统状态页的库内计数；读失败的那一项为 nil。
type SystemCounts struct {
	Nodes *NodeFabricCounts
	// PendingCallbacks 是收到超过 1 分钟仍未处理的支付回调数
	PendingCallbacks *int64
	// Deliveries 按渠道（notification_deliveries.channel）给计数，读失败的渠道不在表里
	Deliveries map[string]DeliveryCounts
}

// PingDatabase 做一次 SELECT 1 的往返，不进租户事务。
func (s *Service) PingDatabase(ctx context.Context) error {
	var one int
	return s.pool.QueryRow(ctx, `SELECT 1`).Scan(&one)
}

// DatabaseStats 读数据库体积、当前连接数与上限；三个数要么一起读到，要么返回错误。
//
// 一条语句一次往返（原来 InTx 里三条，共 5 次往返）。pg_database_size 要逐个 stat 库里的
// 全部文件，是这页最贵的一项，结果按租户缓存 dashboardCacheTTL（体积与连接数不要求秒级）。
func (s *Service) DatabaseStats(ctx context.Context, tenantID string) (*DatabaseStats, error) {
	return cachedRead(s.dash, "dbstats:"+tenantID, func() (*DatabaseStats, error) {
		var out DatabaseStats
		err := s.pool.QueryRowScoped(ctx, db.Scope{TenantID: tenantID}, `
			SELECT pg_database_size(current_database()),
			       (SELECT count(*) FROM pg_stat_activity WHERE datname = current_database())::int,
			       (SELECT setting::int FROM pg_settings WHERE name = 'max_connections')`,
			nil, &out.SizeBytes, &out.Connections, &out.MaxConnections)
		if err != nil {
			return nil, err
		}
		return &out, nil
	})
}

// SystemCounts 在一个租户事务里读节点、支付回调与各渠道通知投递的计数。
// 任何一项失败只让那一项缺席，不返回错误：系统状态页恰恰要在部分组件坏掉时还能打开。
//
// 各项互不依赖，排进一个批次一次往返（原来 InTx 里逐条执行）。与原来一样，前面某条出错后
// 隐式事务已中止，后面的项也读不到、一并缺席。
func (s *Service) SystemCounts(ctx context.Context, tenantID string, channels []string) SystemCounts {
	out := SystemCounts{Deliveries: map[string]DeliveryCounts{}}
	b := &pgx.Batch{}
	b.Queue(`
		SELECT count(*),
		       count(*) FILTER (WHERE last_heartbeat_at >= now() - interval '90 seconds'),
		       -- 配置落后：拉过生效配置的节点（签名通道）按生效版本比，期望与已应用不同即落后；
		       -- 没有生效版本的老节点仍按旧的整数版本比。原先只看整数版本，签名节点报的是 0、
		       -- 存成 NULL，永远算不落后
		       count(*) FILTER (WHERE CASE
		         WHEN desired_effective_generation IS NOT NULL THEN
		           (desired_effective_release_id, desired_effective_generation)
		             IS DISTINCT FROM (applied_effective_release_id, applied_effective_generation)
		         ELSE applied_config_version < desired_config_version END)
		  FROM nodes
		 WHERE tenant_id = $1 AND status <> 'destroyed' AND serving_status <> 'retired'`,
		tenantID).QueryRow(func(row pgx.Row) error {
		var total, online, lagging int64
		if row.Scan(&total, &online, &lagging) == nil {
			out.Nodes = &NodeFabricCounts{Total: total, Online: online, Lagging: lagging}
		}
		return nil
	})
	// 契约写的来源是 00036 建的回调收据表，没有任何代码写入、登记为孤儿表
	// （RESERVED-TABLES.md），回调收据实际落在 payment_events：pending / failed
	// 且收到超过 1 分钟仍未处理的，才是卡住的回调
	b.Queue(`
		SELECT count(*) FROM payment_events
		 WHERE tenant_id = $1 AND processing_status IN ('pending','failed')
		   AND received_at < now() - interval '1 minute'`, tenantID).QueryRow(func(row pgx.Row) error {
		var pending int64
		if row.Scan(&pending) == nil {
			out.PendingCallbacks = &pending
		}
		return nil
	})
	for _, channel := range channels {
		b.Queue(`
			SELECT count(*) FILTER (WHERE status = 'queued'),
			       count(*) FILTER (WHERE status = 'queued' AND attempts > 0),
			       count(*) FILTER (WHERE status = 'failed')
			  FROM notification_deliveries WHERE tenant_id = $1 AND channel = $2`,
			tenantID, channel).QueryRow(func(row pgx.Row) error {
			var queued, retrying, failed int64
			if row.Scan(&queued, &retrying, &failed) == nil {
				out.Deliveries[channel] = DeliveryCounts{Queued: queued, Retrying: retrying, Failed: failed}
			}
			return nil
		})
	}
	_ = s.pool.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{}, b)
	return out
}
