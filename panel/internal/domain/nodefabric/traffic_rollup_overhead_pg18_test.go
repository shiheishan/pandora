package nodefabric

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// legacyTrafficRollupSQL 是 66a2043 的入库汇总语句原文（只写节点级与节点×uid 两张小时表），
// 只作计时对照：00106 在同一条语句里多写一张 uid 级小时表，这里量多出来的代价。
// 不要「顺手」改它。
const legacyTrafficRollupSQL = `
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

var errRollbackTiming = errors.New("rollback timing transaction")

// rollupOverheadScenario 量入库路径多写 uid 级小时表（00106）的额外开销：同一份上报（25 个 uid，
// 约 5k 用户 / 200 节点时一个节点一分钟的上报；以及 500 个 uid）分别跑改前、改后的汇总语句，
// 各自在运行角色的事务里执行后回滚，交替计时，日志给出中位数与 p90。稳态下小时桶已经存在，
// 走的是 ON CONFLICT 累加路径，所以先提交一次改后语句把桶建好。
//
// 这是 CI 机器上的粗测，只记日志不设阈值（共享 runner 的抖动比差值大）；判断以 5k 库副本实测为准。
func rollupOverheadScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool, tenant, node string) {
	t.Helper()
	for _, users := range []int{25, 500} {
		parts := make([]string, 0, users)
		for i := range users {
			parts = append(parts, fmt.Sprintf(`"%d":[%d,%d]`, 7600001+i, 1000+i, 8000+i))
		}
		payload := "{" + strings.Join(parts, ",") + "}"
		hash := sha256.Sum256([]byte(payload))
		var reportID string
		if err := admin.QueryRow(ctx, `
			INSERT INTO node_traffic_reports (tenant_id, node_id, user_count, raw_payload, content_hash)
			VALUES ($1, $2, $3, $4::jsonb, $5) RETURNING id::text`,
			tenant, node, users, payload, hash[:]).Scan(&reportID); err != nil {
			t.Fatalf("plant %d-uid report: %v", users, err)
		}
		if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, trafficRollupSQL, tenant, reportID)
			return err
		}); err != nil {
			t.Fatalf("seed rollup buckets: %v", err)
		}
		run := func(sql string) time.Duration {
			t.Helper()
			var took time.Duration
			err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
				start := time.Now()
				if _, err := tx.Exec(ctx, sql, tenant, reportID); err != nil {
					return err
				}
				took = time.Since(start)
				return errRollbackTiming
			})
			if !errors.Is(err, errRollbackTiming) {
				t.Fatalf("timed rollup: %v", err)
			}
			return took
		}
		for range 5 {
			run(legacyTrafficRollupSQL)
			run(trafficRollupSQL)
		}
		var legacy, current []time.Duration
		for range 40 {
			legacy = append(legacy, run(legacyTrafficRollupSQL))
			current = append(current, run(trafficRollupSQL))
		}
		lm, lp := quantiles(legacy)
		cm, cp := quantiles(current)
		t.Logf("marker=rollup_overhead uids=%d legacy_median_us=%d legacy_p90_us=%d with_uid_median_us=%d with_uid_p90_us=%d delta_median_us=%d",
			users, lm.Microseconds(), lp.Microseconds(), cm.Microseconds(), cp.Microseconds(), (cm - lm).Microseconds())
	}
}

func quantiles(d []time.Duration) (median, p90 time.Duration) {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[len(s)/2], s[len(s)*9/10]
}
