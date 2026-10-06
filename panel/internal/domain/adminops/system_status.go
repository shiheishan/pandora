// [INPUT]: 依赖 platform/db 的连接池与租户事务，读 pg_database_size / pg_stat_activity / pg_settings 与 nodes / payment_events / notification_deliveries 的计数
// [OUTPUT]: 对外提供 DatabaseStats、NodeFabricCounts、DeliveryCounts、SystemCounts 与 Service 的 PingDatabase / DatabaseStats / SystemCounts
// [POS]: adminops 的系统状态读模型（从 api/admin 的 system_status.go / system_components.go 下沉，契约后台-01 GET v1/system/status）：数据库三个统计在一个事务里要么全读到要么报错（R52）；组件计数在一个事务里逐项读，哪项失败哪项为 nil、不让整页报错；ok / warn / down 的判定与备份目录探测留在 handler
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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

// NodeFabricCounts 是未退役节点的总数、90 秒内有心跳的数量与配置落后的数量。
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
func (s *Service) DatabaseStats(ctx context.Context, tenantID string) (*DatabaseStats, error) {
	var out DatabaseStats
	err := s.pool.InTx(ctx,
		db.Scope{TenantID: tenantID},
		func(tx pgx.Tx) error {
			var sizeBytes int64
			var conns, maxConns int
			if err := tx.QueryRow(ctx,
				`SELECT pg_database_size(current_database())`).Scan(&sizeBytes); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()`).
				Scan(&conns); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx,
				`SELECT setting::int FROM pg_settings WHERE name = 'max_connections'`).
				Scan(&maxConns); err != nil {
				return err
			}
			out = DatabaseStats{SizeBytes: sizeBytes, Connections: conns, MaxConnections: maxConns}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SystemCounts 在一个租户事务里读节点、支付回调与各渠道通知投递的计数。
// 任何一项失败只让那一项缺席，不返回错误：系统状态页恰恰要在部分组件坏掉时还能打开。
func (s *Service) SystemCounts(ctx context.Context, tenantID string, channels []string) SystemCounts {
	out := SystemCounts{Deliveries: map[string]DeliveryCounts{}}
	_ = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var total, online, lagging int64
		if err := tx.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE last_heartbeat_at >= now() - interval '90 seconds'),
			       count(*) FILTER (WHERE applied_config_version < desired_config_version)
			  FROM nodes
			 WHERE tenant_id = $1 AND status <> 'destroyed' AND serving_status <> 'retired'`,
			tenantID).Scan(&total, &online, &lagging); err == nil {
			out.Nodes = &NodeFabricCounts{Total: total, Online: online, Lagging: lagging}
		}
		// 契约写的来源是 00036 建的回调收据表，没有任何代码写入、登记为孤儿表
		// （RESERVED-TABLES.md），回调收据实际落在 payment_events：pending / failed
		// 且收到超过 1 分钟仍未处理的，才是卡住的回调
		var pending int64
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM payment_events
			 WHERE tenant_id = $1 AND processing_status IN ('pending','failed')
			   AND received_at < now() - interval '1 minute'`, tenantID).Scan(&pending); err == nil {
			out.PendingCallbacks = &pending
		}
		for _, channel := range channels {
			var queued, retrying, failed int64
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FILTER (WHERE status = 'queued'),
				       count(*) FILTER (WHERE status = 'queued' AND attempts > 0),
				       count(*) FILTER (WHERE status = 'failed')
				  FROM notification_deliveries WHERE tenant_id = $1 AND channel = $2`,
				tenantID, channel).Scan(&queued, &retrying, &failed); err != nil {
				continue
			}
			out.Deliveries[channel] = DeliveryCounts{Queued: queued, Retrying: retrying, Failed: failed}
		}
		return nil
	})
	return out
}
