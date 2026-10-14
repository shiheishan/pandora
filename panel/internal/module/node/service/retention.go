package service

import (
	"context"
	"fmt"
	"time"
)

// 保留期（产品定）：原始追加数据 31 天，小时汇总 400 天。
// 安装令牌：没用过的过期或作废满 30 天就删（10-14 定）；用过的跟身份走，不删。
// 库里的 bootstrap_tokens_guard（迁移 00037）写死了同一个 30 天，改这里要另写迁移改它。
const (
	RawRetentionDays            = 31
	RollupRetentionDays         = 400
	BootstrapTokenRetentionDays = 30
)

// RetentionTables 是保留期清理的表名，定时任务按这个顺序逐表清。
var RetentionTables = []string{"node_traffic_reports", "node_metrics", "node_alive_ips", "node_traffic_hourly", "node_user_traffic_hourly", "bootstrap_tokens"}

// PurgeRetention 删 table 里过了保留期的一批（最多 batch 行），返回删除行数；table 不在 RetentionTables 里返回错误。
func (s *Service) PurgeRetention(ctx context.Context, table string, now time.Time, batch int) (int64, error) {
	var before time.Time
	switch table {
	case "node_traffic_reports", "node_metrics", "node_alive_ips":
		before = now.AddDate(0, 0, -RawRetentionDays)
	case "node_traffic_hourly", "node_user_traffic_hourly":
		before = now.AddDate(0, 0, -RollupRetentionDays)
	case "bootstrap_tokens":
		before = now.AddDate(0, 0, -BootstrapTokenRetentionDays)
	default:
		return 0, fmt.Errorf("未知的保留期清理表: %s", table)
	}

	switch table {
	case "node_traffic_reports":
		return s.repo.PurgeTrafficReports(ctx, before, batch)
	case "node_metrics":
		return s.repo.PurgeMetrics(ctx, before, batch)
	case "node_alive_ips":
		return s.repo.PurgeAliveIPs(ctx, before, batch)
	case "node_traffic_hourly":
		return s.repo.PurgeTrafficHourly(ctx, before, batch)
	case "node_user_traffic_hourly":
		return s.repo.PurgeUserTrafficHourly(ctx, before, batch)
	case "bootstrap_tokens":
		return s.repo.PurgeBootstrapTokens(ctx, before, batch)
	default:
		return 0, fmt.Errorf("未知的保留期清理表: %s", table)
	}
}
