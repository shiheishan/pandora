package nodefabric

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// trafficRollupSQL 把刚写入的一行上报累加进两张小时汇总表（迁移 00099）。
//
// 口径只有一个出处：上报按 app.node_traffic_payload_entries 分类，那是原看板
// strict_entries CTE 的原文，迁移里的回填也只经它。这里读的是刚写进
// node_traffic_reports 的那一行（同一事务内可见），分类的输入就是留档本身，
// 不是 Go 解析过的 map——两者对非法值的处理不同（Go 会拒收整份或补零），
// 看板要的是留档上的严格口径。
//
// 与上报行同一个事务：回滚两边都不留，提交两边都在，同一行上报不会被加两次。
// 重复上报（duplicate_of 非空）只计入 duplicate_report_count。小时桶按服务器收到
// 的时刻 received_at 的 UTC 整点切。
//
// 节点×小时这一行同一节点的并发上报会在这里排队（各节点互不相干）；它在扣量之前写，
// 不延长配额行锁的持有时间。
const trafficRollupSQL = `
WITH r AS MATERIALIZED (
  SELECT tenant_id, node_id, received_at,
         date_trunc('hour', received_at, 'UTC') AS hour_start,
         duplicate_of IS NOT NULL AS is_dup,
         jsonb_typeof(raw_payload)='object' AS root_is_object,
         total_upload::numeric + total_download::numeric AS raw_bytes,
         raw_payload
    FROM node_traffic_reports
   WHERE tenant_id = $1 AND id = $2
), e AS MATERIALIZED (
  SELECT x.entry_uid, x.entry_upload, x.entry_download, x.entry_valid
    FROM r CROSS JOIN LATERAL app.node_traffic_payload_entries(
           CASE WHEN r.is_dup THEN '{}'::jsonb ELSE r.raw_payload END) x
), per_uid AS MATERIALIZED (
  SELECT entry_uid, sum(entry_upload) AS upload_bytes, sum(entry_download) AS download_bytes,
         count(*) AS entry_count
    FROM e
   WHERE entry_valid AND entry_upload + entry_download > 0
   GROUP BY entry_uid
), totals AS MATERIALIZED (
  SELECT count(*) FILTER (WHERE entry_valid IS NOT TRUE) AS invalid_entries,
         coalesce(sum(entry_upload) FILTER (WHERE entry_valid), 0) AS upload_bytes,
         coalesce(sum(entry_download) FILTER (WHERE entry_valid), 0) AS download_bytes,
         (SELECT coalesce(sum(entry_count), 0) FROM per_uid) AS positive_entries
    FROM e
), node_row AS (
  INSERT INTO node_traffic_hourly AS h
    (tenant_id, hour_start, node_id, report_count, duplicate_report_count,
     invalid_report_count, invalid_entry_count, raw_bytes, upload_bytes, download_bytes,
     positive_entry_count, positive_report_count, last_positive_report_at)
  SELECT r.tenant_id, r.hour_start, r.node_id,
         CASE WHEN r.is_dup THEN 0 ELSE 1 END,
         CASE WHEN r.is_dup THEN 1 ELSE 0 END,
         CASE WHEN NOT r.is_dup AND (NOT r.root_is_object OR t.invalid_entries > 0) THEN 1 ELSE 0 END,
         t.invalid_entries,
         CASE WHEN r.is_dup THEN 0 ELSE r.raw_bytes END,
         t.upload_bytes, t.download_bytes, t.positive_entries,
         CASE WHEN t.positive_entries > 0 THEN 1 ELSE 0 END,
         CASE WHEN t.positive_entries > 0 THEN r.received_at END
    FROM r CROSS JOIN totals t
  ON CONFLICT (tenant_id, hour_start, node_id) DO UPDATE SET
    report_count            = h.report_count + EXCLUDED.report_count,
    duplicate_report_count  = h.duplicate_report_count + EXCLUDED.duplicate_report_count,
    invalid_report_count    = h.invalid_report_count + EXCLUDED.invalid_report_count,
    invalid_entry_count     = h.invalid_entry_count + EXCLUDED.invalid_entry_count,
    raw_bytes               = h.raw_bytes + EXCLUDED.raw_bytes,
    upload_bytes            = h.upload_bytes + EXCLUDED.upload_bytes,
    download_bytes          = h.download_bytes + EXCLUDED.download_bytes,
    positive_entry_count    = h.positive_entry_count + EXCLUDED.positive_entry_count,
    positive_report_count   = h.positive_report_count + EXCLUDED.positive_report_count,
    last_positive_report_at = greatest(h.last_positive_report_at, EXCLUDED.last_positive_report_at)
)
INSERT INTO node_user_traffic_hourly AS u
  (tenant_id, hour_start, node_id, node_uid, upload_bytes, download_bytes, entry_count, last_report_at)
SELECT r.tenant_id, r.hour_start, r.node_id, p.entry_uid,
       p.upload_bytes, p.download_bytes, p.entry_count, r.received_at
  FROM r CROSS JOIN per_uid p
 ORDER BY p.entry_uid
ON CONFLICT (tenant_id, hour_start, node_id, node_uid) DO UPDATE SET
  upload_bytes   = u.upload_bytes + EXCLUDED.upload_bytes,
  download_bytes = u.download_bytes + EXCLUDED.download_bytes,
  entry_count    = u.entry_count + EXCLUDED.entry_count,
  last_report_at = greatest(u.last_report_at, EXCLUDED.last_report_at)`

// rollupTrafficReport 在调用方的事务里把一行上报累加进小时汇总。
func rollupTrafficReport(ctx context.Context, tx pgx.Tx, tenantID, reportID string) error {
	_, err := tx.Exec(ctx, trafficRollupSQL, tenantID, reportID)
	return err
}

// TrafficRollupRetentionDays 是小时汇总的保留期：读得最远的是看板（snapshot_at 不早于
// 31 天前、区间最长 30 天，即 61 天前），节点列表 30 天；留 70 天给余量。更早的桶
// 没有任何读路径会读到，汇总也能从上报留档重算。
const TrafficRollupRetentionDays = 70

// rollupPurgeBatch 是每个短事务最多删的行数，rollupPurgeMaxBatches 是一次调用每张表最多
// 跑几批；积压由下一轮继续清。
const (
	rollupPurgeBatch      = 5000
	rollupPurgeMaxBatches = 200
)

// PurgeTrafficRollups 删除超出保留期的小时汇总（先节点×uid 表，再节点表）。由 aegis-admin 的
// 保留期任务定时调用，幂等；分批删，每批一个短事务。
func (s *Service) PurgeTrafficRollups(ctx context.Context, tenantID string) (int64, error) {
	var total int64
	for _, sql := range []string{`
		DELETE FROM node_user_traffic_hourly t
		 USING (SELECT tenant_id, hour_start, node_id, node_uid
		          FROM node_user_traffic_hourly
		         WHERE tenant_id = $1 AND hour_start < now() - make_interval(days => $2)
		         ORDER BY hour_start
		         LIMIT $3) d
		 WHERE t.tenant_id = d.tenant_id AND t.hour_start = d.hour_start
		   AND t.node_id = d.node_id AND t.node_uid = d.node_uid`, `
		DELETE FROM node_traffic_hourly t
		 USING (SELECT tenant_id, hour_start, node_id
		          FROM node_traffic_hourly
		         WHERE tenant_id = $1 AND hour_start < now() - make_interval(days => $2)
		         ORDER BY hour_start
		         LIMIT $3) d
		 WHERE t.tenant_id = d.tenant_id AND t.hour_start = d.hour_start AND t.node_id = d.node_id`,
	} {
		for range rollupPurgeMaxBatches {
			var n int64
			err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
				ct, err := tx.Exec(ctx, sql, tenantID, TrafficRollupRetentionDays, rollupPurgeBatch)
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
