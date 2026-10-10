package nodefabric

import (
	"context"

	"github.com/jackc/pgx/v5"
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
//
// billed_bytes（00133）是乘过节点倍率的计费字节：$3 / $4 是 Go 记账算出的 uid 与计费量，
// 与传给扣量（chargeReportEntries）的是同一组数（放行名单内、合规、按倍率折算），重复上报
// 传空。小时汇总因此留下对账要的口径，原始留档过了 31 天被清理也不丢。00164 起这列非空、
// 没有默认值：两条 INSERT 都显式写它（没有计费项时写 0），ON CONFLICT 两边相加。
const trafficRollupSQL = `
WITH b AS MATERIALIZED (
  SELECT uid, billed FROM unnest($3::bigint[], $4::bigint[]) AS b(uid, billed)
), r AS MATERIALIZED (
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
     positive_entry_count, positive_report_count, last_positive_report_at, billed_bytes)
  SELECT r.tenant_id, r.hour_start, r.node_id,
         CASE WHEN r.is_dup THEN 0 ELSE 1 END,
         CASE WHEN r.is_dup THEN 1 ELSE 0 END,
         CASE WHEN NOT r.is_dup AND (NOT r.root_is_object OR t.invalid_entries > 0) THEN 1 ELSE 0 END,
         t.invalid_entries,
         CASE WHEN r.is_dup THEN 0 ELSE r.raw_bytes END,
         t.upload_bytes, t.download_bytes, t.positive_entries,
         CASE WHEN t.positive_entries > 0 THEN 1 ELSE 0 END,
         CASE WHEN t.positive_entries > 0 THEN r.received_at END,
         (SELECT coalesce(sum(billed), 0) FROM b)
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
    last_positive_report_at = greatest(h.last_positive_report_at, EXCLUDED.last_positive_report_at),
    billed_bytes            = h.billed_bytes + EXCLUDED.billed_bytes
)
INSERT INTO node_user_traffic_hourly AS u
  (tenant_id, hour_start, node_id, node_uid, upload_bytes, download_bytes, entry_count, last_report_at,
   billed_bytes)
SELECT r.tenant_id, r.hour_start, r.node_id, p.entry_uid,
       p.upload_bytes, p.download_bytes, p.entry_count, r.received_at,
       coalesce((SELECT sum(b.billed) FROM b WHERE b.uid = p.entry_uid), 0)
  FROM r CROSS JOIN per_uid p
 ORDER BY p.entry_uid
ON CONFLICT (tenant_id, hour_start, node_id, node_uid) DO UPDATE SET
  upload_bytes   = u.upload_bytes + EXCLUDED.upload_bytes,
  download_bytes = u.download_bytes + EXCLUDED.download_bytes,
  entry_count    = u.entry_count + EXCLUDED.entry_count,
  last_report_at = greatest(u.last_report_at, EXCLUDED.last_report_at),
  billed_bytes   = u.billed_bytes + EXCLUDED.billed_bytes`

// rollupTrafficReport 在调用方的事务里把一行上报累加进小时汇总。billed 是这份上报计费的
// uid 与计费量（重复上报传 nil）。
func rollupTrafficReport(ctx context.Context, tx pgx.Tx, tenantID, reportID string, billed []billedEntry) error {
	uids := make([]int64, 0, len(billed))
	amounts := make([]int64, 0, len(billed))
	for _, e := range billed {
		uids = append(uids, e.uid)
		amounts = append(amounts, e.billed)
	}
	_, err := tx.Exec(ctx, trafficRollupSQL, tenantID, reportID, uids, amounts)
	return err
}
