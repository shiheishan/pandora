package nodefabric

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 流量数据的保留期（用户定：原始数据 31 天、汇总 400 天，每天清理）。由 aegis-admin 的保留期
// 任务定时调用（清理一小时一次，cmd/aegis-admin/retention.go）；每批一个短事务，每次调用的批数
// 有上限，积压由下一轮接着清。

const (
	// TrafficReportRetentionDays 是上报留档（原始报文）的保留期，也是 app.purge_node_traffic_reports
	// 接受的下限（00131）。留档没有后台读路径，只作争议时逐笔回溯的证据。
	TrafficReportRetentionDays = 31
	// NodeTrafficHourlyRetentionDays 是节点级小时汇总的保留期（400 天约 200 节点 × 24 × 400 ≈ 190 万行）。
	NodeTrafficHourlyRetentionDays = 400
	// UserTrafficHourlyRetentionDays 是节点 × uid 小时汇总的保留期：读得最远的是看板（snapshot_at
	// 不早于 31 天前、区间最长 30 天，即 61 天前），留 70 天给余量。更长的趋势读按天表。
	UserTrafficHourlyRetentionDays = 70
	// UserTrafficDailyRetentionDays 是节点 × uid 按天汇总的保留期。
	UserTrafficDailyRetentionDays = 400
)

// 清理的批量：rollupPurgeBatch 行一批、每张表一次调用最多 rollupPurgeMaxBatches 批。
// 容量按一小时一轮核算：留档每次调用最多 reportPurgeBatch × reportPurgeMaxBatches = 20 万行，
// 一天 24 轮上限约 480 万行；稳态每天的进量是节点数 × 1440（每分钟一报）：200 节点约 28.8 万行，
// 1000 节点约 144 万行，3000 节点约 432 万行，仍在上限之内（节点再多就要加批数或缩短间隔）。
// 订阅拉取日志的批量相同（subscription/fetch_log_retention.go），上限同为每天约 480 万行。
// 汇总表每张每次调用最多 5000 × 200 = 100 万行。上线后第一次清积压时一轮最多删这么多，不会
// 长时间占着 IO，剩下的由后面几轮接着清。
const (
	rollupPurgeBatch      = 5000
	rollupPurgeMaxBatches = 200
	reportPurgeBatch      = 5000
	reportPurgeMaxBatches = 40
)

// trafficDailyMaxDaysPerRun 是一次调用最多汇总几天。稳态每天只有一天要汇总；上线后积压的历史
// 日子（小时表里现有的 70 天）从新到旧每轮补几天，补齐用不了一天。
const trafficDailyMaxDaysPerRun = 3

// PurgeTrafficRollups 删除超出保留期的流量汇总：节点 × uid 小时表（70 天）、按天表（400 天）、
// 节点小时表（400 天）。幂等；分批删，每批一个短事务。
func (s *Service) PurgeTrafficRollups(ctx context.Context, tenantID string) (int64, error) {
	var total int64
	for _, step := range []struct {
		sql  string
		days int
	}{{`
		DELETE FROM node_user_traffic_hourly t
		 USING (SELECT tenant_id, hour_start, node_id, node_uid
		          FROM node_user_traffic_hourly
		         WHERE tenant_id = $1 AND hour_start < now() - make_interval(days => $2)
		         ORDER BY hour_start
		         LIMIT $3) d
		 WHERE t.tenant_id = d.tenant_id AND t.hour_start = d.hour_start
		   AND t.node_id = d.node_id AND t.node_uid = d.node_uid`, UserTrafficHourlyRetentionDays}, {`
		DELETE FROM node_user_traffic_daily t
		 USING (SELECT tenant_id, day, node_id, node_uid
		          FROM node_user_traffic_daily
		         WHERE tenant_id = $1 AND day < (now() AT TIME ZONE 'UTC')::date - $2::int
		         ORDER BY day
		         LIMIT $3) d
		 WHERE t.tenant_id = d.tenant_id AND t.day = d.day
		   AND t.node_id = d.node_id AND t.node_uid = d.node_uid`, UserTrafficDailyRetentionDays}, {`
		DELETE FROM node_traffic_hourly t
		 USING (SELECT tenant_id, hour_start, node_id
		          FROM node_traffic_hourly
		         WHERE tenant_id = $1 AND hour_start < now() - make_interval(days => $2)
		         ORDER BY hour_start
		         LIMIT $3) d
		 WHERE t.tenant_id = d.tenant_id AND t.hour_start = d.hour_start AND t.node_id = d.node_id`,
		NodeTrafficHourlyRetentionDays},
	} {
		for range rollupPurgeMaxBatches {
			var n int64
			err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
				ct, err := tx.Exec(ctx, step.sql, tenantID, step.days, rollupPurgeBatch)
				n = ct.RowsAffected()
				return err
			})
			total += n
			if err != nil {
				return total, err
			}
			if n < rollupPurgeBatch {
				break
			}
		}
	}
	return total, nil
}

// PurgeTrafficReports 删除超出保留期（31 天）的上报留档。
//
// 留档是追加写表，运行角色不能直接删；删除只经 app.purge_node_traffic_reports（00131：定义者
// 权限、只删当前租户、保留期不少于 31 天、每次最多一批）。
func (s *Service) PurgeTrafficReports(ctx context.Context, tenantID string) (int64, error) {
	var total int64
	for range reportPurgeMaxBatches {
		var n int64
		err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT app.purge_node_traffic_reports($1, $2)`,
				TrafficReportRetentionDays, reportPurgeBatch).Scan(&n)
		})
		total += n
		if err != nil || n < reportPurgeBatch {
			return total, err
		}
	}
	return total, nil
}

// trafficDailyRollupSQL 把一个 UTC 自然日的节点 × uid 小时汇总并成一行一天。这一天有任一小时
// 的 billed_bytes 未知（00133 之前的桶），整天的 billed_bytes 记 NULL。一天只写一次：
// 并发的两轮或重跑都落在 DO NOTHING 上。
const trafficDailyRollupSQL = `
INSERT INTO node_user_traffic_daily AS d
  (tenant_id, day, node_id, node_uid, upload_bytes, download_bytes, billed_bytes, entry_count, last_report_at)
SELECT h.tenant_id, $2::date, h.node_id, h.node_uid,
       sum(h.upload_bytes), sum(h.download_bytes),
       CASE WHEN bool_and(h.billed_bytes IS NOT NULL) THEN sum(h.billed_bytes) END,
       sum(h.entry_count), max(h.last_report_at)
  FROM node_user_traffic_hourly h
 WHERE h.tenant_id = $1
   AND h.hour_start >= ($2::date)::timestamp AT TIME ZONE 'UTC'
   AND h.hour_start < ($2::date + 1)::timestamp AT TIME ZONE 'UTC'
 GROUP BY h.tenant_id, h.node_id, h.node_uid
ON CONFLICT (tenant_id, day, node_id, node_uid) DO NOTHING`

// RefreshTrafficDaily 把已结束、还没汇总的 UTC 自然日从节点 × uid 小时表汇总进按天表（00133），
// 返回写入的行数。入库热路径不碰按天表；每轮最多补 trafficDailyMaxDaysPerRun 天，每天一个短
// 事务。幂等。
//
// 进度记在进程内（trafficDailyRoller）：启动和每个 UTC 日界之后核对一次库（一批按天探测，
// 找出缺的日子），其余的轮次只比较时钟，不碰库。这一步原先每轮都用一条语句重扫 70 天，静默时
// 也要读 80 多万个缓冲块。
func (s *Service) RefreshTrafficDaily(ctx context.Context, tenantID string) (int64, error) {
	return s.trafficDaily.refresh(ctx, s, tenantID)
}

// trafficDayNeedsRollupSQL 判断一个 UTC 自然日要不要汇总：按天表里还没有这一天（一天只写一次），
// 而小时表里有。$1 租户、$2 日期、$3 当天零点（UTC）。两个都是按主键前缀的存在性探测，
// 每天各读一两个索引页；日界一律按 UTC 的 24 小时算（不用 '1 day'：会话时区有夏令时时，
// timestamptz 加 1 天不一定是 24 小时）。
const trafficDayNeedsRollupSQL = `
SELECT NOT EXISTS (
         SELECT 1 FROM node_user_traffic_daily d
          WHERE d.tenant_id = $1 AND d.day = $2::date)
   AND EXISTS (
         SELECT 1 FROM node_user_traffic_hourly h
          WHERE h.tenant_id = $1 AND h.hour_start >= $3::timestamptz
            AND h.hour_start < $3::timestamptz + interval '24 hours')`

// trafficDailyPendingDays 在 [first, last] 的每个 UTC 日里找出要汇总的（见 trafficDayNeedsRollupSQL），
// 从新到旧。一次往返、一个只读隐式事务。
func (s *Service) trafficDailyPendingDays(ctx context.Context, tenantID string, first, last time.Time) ([]time.Time, error) {
	var days []time.Time
	for d := last; !d.Before(first); d = d.Add(-24 * time.Hour) {
		days = append(days, d)
	}
	need := make([]bool, len(days))
	b := &pgx.Batch{}
	for i, d := range days {
		b.Queue(trafficDayNeedsRollupSQL, tenantID, d, d).QueryRow(func(row pgx.Row) error {
			return row.Scan(&need[i])
		})
	}
	if err := s.pool.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{}, b); err != nil {
		return nil, err
	}
	var out []time.Time
	for i, d := range days {
		if need[i] {
			out = append(out, d)
		}
	}
	return out, nil
}

// rollupTrafficDay 汇总一个 UTC 自然日，返回写入的行数。
func (s *Service) rollupTrafficDay(ctx context.Context, tenantID string, day time.Time) (int64, error) {
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, trafficDailyRollupSQL, tenantID, day)
		n = ct.RowsAffected()
		return err
	})
	return n, err
}
