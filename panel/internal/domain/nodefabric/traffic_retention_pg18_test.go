package nodefabric

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// w5retain 的 PG18 用例，挂在 TestTrafficChargePG18 里跑（同一个套了 configure-app-role 的库）。
// 夹具前缀 75f5…，node_uid 759xxxx。

// reportRetentionScenario 证明上报留档的 31 天清理（00131）：
//   - PurgeTrafficReports 经 app.purge_node_traffic_reports 删本租户 31 天以前的留档（跨多个节点），
//     近期的与别的租户的不动；
//   - duplicate_of 不再有自引用外键：原报文删掉后，近期的重复件留着悬空引用，不报错、不被置空；
//   - 追加写保护不变：运行角色直接 DELETE 仍被拒；函数拒收短于 31 天的保留期、越界批量与无租户作用域。
func reportRetentionScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	const (
		tenantA = "75f50000-0000-7000-8000-000000000001"
		tenantB = "75f50000-0000-7000-8000-000000000002"
		nodeA1  = "75f50000-0000-7000-8000-000000000031"
		nodeA2  = "75f50000-0000-7000-8000-000000000032"
		nodeB   = "75f50000-0000-7000-8000-000000000033"
		oldA1   = "75f50000-0000-7000-8000-000000000051"
		dupA1   = "75f50000-0000-7000-8000-000000000052"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'retain-reports-a-pg18', 'Retain Reports A', 'CNY'),
		       ($2, 'retain-reports-b-pg18', 'Retain Reports B', 'CNY')`, tenantA, tenantB)
	must(`INSERT INTO nodes (id, tenant_id, name, status)
		VALUES ($1, $4, 'retain-a1', 'active'), ($2, $4, 'retain-a2', 'active'), ($3, $5, 'retain-b', 'active')`,
		nodeA1, nodeA2, nodeB, tenantA, tenantB)
	// 本租户：节点 1 的 40 天、32 天两份与 32 天前对 40 天那份的重复件，节点 2 的 35 天一份 → 共 4 份到期；
	// 节点 1 的 1 天一份与 1 天前对 40 天那份的重复件 → 留下。别的租户 40 天一份 → 不动。
	must(`INSERT INTO node_traffic_reports (id, tenant_id, node_id, raw_payload, content_hash, received_at)
		VALUES ($1, $2, $3, '{}', '\x01', now() - interval '40 days')`, oldA1, tenantA, nodeA1)
	must(`INSERT INTO node_traffic_reports (tenant_id, node_id, raw_payload, content_hash, received_at, duplicate_of)
		VALUES ($1, $2, '{}', '\x02', now() - interval '32 days', NULL),
		       ($1, $2, '{}', '\x01', now() - interval '32 days', $4),
		       ($1, $3, '{}', '\x03', now() - interval '35 days', NULL),
		       ($1, $2, '{}', '\x04', now() - interval '1 day', NULL)`, tenantA, nodeA1, nodeA2, oldA1)
	must(`INSERT INTO node_traffic_reports (id, tenant_id, node_id, raw_payload, content_hash, received_at, duplicate_of)
		VALUES ($1, $2, $3, '{}', '\x01', now() - interval '1 day', $4)`, dupA1, tenantA, nodeA1, oldA1)
	must(`INSERT INTO node_traffic_reports (tenant_id, node_id, raw_payload, content_hash, received_at)
		VALUES ($1, $2, '{}', '\x05', now() - interval '40 days')`, tenantB, nodeB)

	var selfFKs int
	if err := admin.QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint
		 WHERE contype = 'f' AND conrelid = 'public.node_traffic_reports'::regclass
		   AND confrelid = 'public.node_traffic_reports'::regclass`).Scan(&selfFKs); err != nil || selfFKs != 0 {
		t.Fatalf("node_traffic_reports self foreign keys = %d err=%v, want none (00131)", selfFKs, err)
	}

	// 函数本身：一批只删一行
	var one int64
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantA}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT app.purge_node_traffic_reports(31, 1)`).Scan(&one)
	}); err != nil || one != 1 {
		t.Fatalf("purge_node_traffic_reports(31, 1) = %d err=%v, want exactly one row", one, err)
	}
	svc := NewService(app, nil)
	if n, err := svc.PurgeTrafficReports(ctx, tenantA); err != nil || n != 3 {
		t.Fatalf("PurgeTrafficReports deleted=%d err=%v, want the remaining three expired reports", n, err)
	}
	var leftA, leftB int
	var dangling *string
	if err := admin.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM node_traffic_reports WHERE tenant_id = $1),
		       (SELECT count(*) FROM node_traffic_reports WHERE tenant_id = $2),
		       (SELECT duplicate_of::text FROM node_traffic_reports WHERE id = $3)`,
		tenantA, tenantB, dupA1).Scan(&leftA, &leftB, &dangling); err != nil {
		t.Fatal(err)
	}
	if leftA != 2 || leftB != 1 {
		t.Fatalf("reports left tenantA=%d tenantB=%d, want the two recent rows and the other tenant untouched", leftA, leftB)
	}
	if dangling == nil || *dangling != oldA1 {
		t.Fatalf("recent duplicate lost its duplicate_of (%v); want the dangling original id kept", dangling)
	}

	for name, sql := range map[string]string{
		"direct delete":   `DELETE FROM node_traffic_reports`,
		"short retention": `SELECT app.purge_node_traffic_reports(30, 10)`,
		"zero batch":      `SELECT app.purge_node_traffic_reports(31, 0)`,
		"huge batch":      `SELECT app.purge_node_traffic_reports(31, 50001)`,
	} {
		err := app.InTx(ctx, platformdb.Scope{TenantID: tenantA}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql)
			return err
		})
		if err == nil {
			t.Fatalf("%s on node_traffic_reports was accepted for the app role", name)
		}
	}
	if _, err := app.Exec(ctx, `SELECT app.purge_node_traffic_reports(31, 10)`); err == nil {
		t.Fatal("purge_node_traffic_reports ran without a tenant scope")
	}
	t.Log("marker=retain_pg18_traffic_reports_purged_ok")
}

// droppedIndexScenario 证明 00132 删掉的索引没有查询依赖：每条读 node_traffic_reports 的 SQL
// （入库去重、按编号判重、汇总回读、挪节点前计数、31 天清理）与按操作者读审计的 SQL，在
// enable_seqscan=off 下都有剩下的索引可走，计划里不出现被删的索引名，也不出现顺序扫描。
func droppedIndexScenario(t *testing.T, ctx context.Context, admin *pgx.Conn) {
	t.Helper()
	const (
		tenant = "75f50000-0000-7000-8000-000000000001"
		node   = "75f50000-0000-7000-8000-000000000031"
		report = "75f50000-0000-7000-8000-000000000052"
		actor  = "75f50000-0000-7000-8000-000000000071"
	)
	dropped := []string{
		"idx_node_traffic_reports_dashboard_window",
		"idx_node_traffic_reports_dashboard_duplicate_window",
		"idx_node_traffic_reports_dedup",
		"idx_audit_events_actor",
	}
	for _, name := range dropped {
		var present bool
		if err := admin.QueryRow(ctx, `SELECT to_regclass('public.'||$1) IS NOT NULL`, name).Scan(&present); err != nil || present {
			t.Fatalf("index %s still present=%v err=%v (00132)", name, present, err)
		}
	}
	var kept bool
	if err := admin.QueryRow(ctx, `SELECT to_regclass('public.audit_events_actor_time_idx') IS NOT NULL`).Scan(&kept); err != nil || !kept {
		t.Fatalf("audit_events_actor_time_idx missing (err=%v); 00132 must keep one actor index", err)
	}

	queries := map[string]struct {
		sql, table string
		wantIndex  []string
	}{
		"ten-second dedup": {fmt.Sprintf(`SELECT d.id FROM node_traffic_reports d
			WHERE d.node_id = '%s' AND d.content_hash = '\x01'::bytea
			  AND d.received_at > now() - interval '10 seconds'
			ORDER BY d.received_at DESC LIMIT 1`, node), "node_traffic_reports", []string{"idx_node_traffic_reports_node"}},
		"report id conflict": {fmt.Sprintf(`SELECT d.id FROM node_traffic_reports d
			WHERE d.node_id = '%s' AND d.client_report_id = 'r-1'`, node), "node_traffic_reports", []string{"node_traffic_reports_client_report_unique", "idx_node_traffic_reports_node"}},
		"rollup read-back": {fmt.Sprintf(`SELECT raw_payload FROM node_traffic_reports
			WHERE tenant_id = '%s' AND id = '%s'`, tenant, report), "node_traffic_reports", []string{"node_traffic_reports_pkey"}},
		"placement count": {fmt.Sprintf(`SELECT count(*)::int FROM node_traffic_reports
			WHERE tenant_id = '%s' AND node_id = '%s'::uuid`, tenant, node), "node_traffic_reports", []string{"idx_node_traffic_reports_node"}},
		"retention purge": {fmt.Sprintf(`SELECT o.id FROM nodes n CROSS JOIN LATERAL (
			  SELECT t.id FROM node_traffic_reports t
			   WHERE t.node_id = n.id AND t.received_at < now() - make_interval(days => 31)
			   ORDER BY t.received_at LIMIT 5000) o
			WHERE n.tenant_id = '%s' LIMIT 5000`, tenant), "node_traffic_reports", []string{"idx_node_traffic_reports_node"}},
		"activity by actor": {fmt.Sprintf(`SELECT action FROM audit_events
			WHERE tenant_id = '%s' AND actor_id = '%s'::uuid
			ORDER BY occurred_at DESC LIMIT 80`, tenant, actor), "audit_events", []string{"audit_events_actor_time_idx"}},
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	for name, q := range queries {
		rows, err := tx.Query(ctx, "EXPLAIN "+q.sql)
		if err != nil {
			t.Fatalf("%s: explain: %v", name, err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("%s: explain rows: %v", name, err)
		}
		plan := strings.Join(lines, "\n")
		if strings.Contains(plan, "Seq Scan on "+q.table) {
			t.Fatalf("%s needs a dropped index (sequential scan with enable_seqscan=off):\n%s", name, plan)
		}
		uses := false
		for _, idx := range q.wantIndex {
			uses = uses || strings.Contains(plan, idx)
		}
		if !uses {
			t.Fatalf("%s does not use any of %v:\n%s", name, q.wantIndex, plan)
		}
		for _, idx := range dropped {
			if strings.Contains(plan, idx) {
				t.Fatalf("%s still plans with dropped index %s:\n%s", name, idx, plan)
			}
		}
		t.Logf("%s plan:\n%s", name, plan)
	}
	t.Log("marker=retain_pg18_dropped_indexes_unused_ok")
}

// billedBytesScenario 证明 00133 的 billed_bytes：入库时按节点倍率折算、只计放行名单内的合规项，
// 节点小时表是各 uid 之和；重复上报不计；跨迁移的桶（NULL）加了仍是 NULL。
func billedBytesScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	const (
		tenantID = "75f60000-0000-7000-8000-000000000001"
		nodeID   = "75f60000-0000-7000-8000-000000000031"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'retain-billed-pg18', 'Retain Billed', 'CNY')`, tenantID)
	must(`INSERT INTO nodes (id, tenant_id, name, status) VALUES ($1, $2, 'retain-billed', 'active')`, nodeID, tenantID)

	svc := NewService(app, nil)
	// 倍率 1.5；7590003 不在放行名单里（留档、进小时表的原始字节，但不计费）
	node := servingNodeWithUsers(svc, tenantID, nodeID, 1.5, 7590001, 7590002)
	payload := []byte(`{"7590001":[100,0],"7590002":[10,10],"7590003":[50,50]}`)
	if res, err := svc.ReportTraffic(ctx, tenantID, node, payload); err != nil || res.Duplicate {
		t.Fatalf("report = %+v err=%v", res, err)
	}
	if res, err := svc.ReportTraffic(ctx, tenantID, node, payload); err != nil || !res.Duplicate {
		t.Fatalf("resent report = %+v err=%v, want a duplicate", res, err)
	}
	readBilled := func() (nodeBilled *string, perUID map[int64]*string) {
		t.Helper()
		// 按桶合计（三次上报碰巧跨整点时落在两个桶里）：任一桶未知即未知
		if err := admin.QueryRow(ctx, `
			SELECT CASE WHEN bool_and(billed_bytes IS NOT NULL) THEN sum(billed_bytes)::text END
			  FROM node_traffic_hourly WHERE node_id = $1`, nodeID).Scan(&nodeBilled); err != nil {
			t.Fatal(err)
		}
		rows, err := admin.Query(ctx, `
			SELECT node_uid, CASE WHEN bool_and(billed_bytes IS NOT NULL) THEN sum(billed_bytes)::text END
			  FROM node_user_traffic_hourly WHERE node_id = $1 GROUP BY node_uid`, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		perUID = map[int64]*string{}
		for rows.Next() {
			var uid int64
			var billed *string
			if err := rows.Scan(&uid, &billed); err != nil {
				t.Fatal(err)
			}
			perUID[uid] = billed
		}
		return nodeBilled, perUID
	}
	str := func(p *string) string {
		if p == nil {
			return "NULL"
		}
		return *p
	}
	nodeBilled, perUID := readBilled()
	if str(nodeBilled) != "180" || str(perUID[7590001]) != "150" || str(perUID[7590002]) != "30" || str(perUID[7590003]) != "0" {
		t.Fatalf("billed node=%s uid1=%s uid2=%s uid3=%s, want 180/150/30/0 (rate 1.5, duplicate not billed)",
			str(nodeBilled), str(perUID[7590001]), str(perUID[7590002]), str(perUID[7590003]))
	}
	// 跨迁移的桶：NULL 表示未知，之后再加也保持未知
	must(`UPDATE node_traffic_hourly SET billed_bytes = NULL WHERE node_id = $1`, nodeID)
	must(`UPDATE node_user_traffic_hourly SET billed_bytes = NULL WHERE node_id = $1 AND node_uid = 7590001`, nodeID)
	if _, err := svc.ReportTraffic(ctx, tenantID, node, []byte(`{"7590001":[2,0],"7590002":[2,0]}`)); err != nil {
		t.Fatal(err)
	}
	nodeBilled, perUID = readBilled()
	if nodeBilled != nil || perUID[7590001] != nil || str(perUID[7590002]) != "33" {
		t.Fatalf("after NULL buckets: node=%s uid1=%s uid2=%s, want NULL/NULL/33",
			str(nodeBilled), str(perUID[7590001]), str(perUID[7590002]))
	}
	t.Log("marker=retain_pg18_billed_bytes_ok")
}

// trafficDailyScenario 证明 00133 的节点 × uid 按天汇总：只汇总已结束的 UTC 自然日、整天都在
// 小时表保留期内的日子；任一小时 billed_bytes 未知则整天未知；一天只写一次（重跑零写入）；
// 保留期任务删 400 天以前的按天行与 400 天以前的节点小时行，70 天以前的节点 × uid 小时行。
func trafficDailyScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	const (
		tenantID = "75f70000-0000-7000-8000-000000000001"
		nodeID   = "75f70000-0000-7000-8000-000000000031"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'retain-daily-pg18', 'Retain Daily', 'CNY')`, tenantID)
	must(`INSERT INTO nodes (id, tenant_id, name, status) VALUES ($1, $2, 'retain-daily', 'active')`, nodeID, tenantID)
	// 前天（UTC）的三个小时：uid1 两小时、uid2 一小时、uid3 一小时且 billed 未知；今天一小时（没结束，
	// 不汇总）；75 天前一小时（不完整的保留期外，不汇总）。
	must(`
		WITH d AS (SELECT ((now() AT TIME ZONE 'UTC')::date - 2)::timestamp AT TIME ZONE 'UTC' AS day_start)
		INSERT INTO node_user_traffic_hourly (tenant_id, hour_start, node_id, node_uid,
			upload_bytes, download_bytes, entry_count, last_report_at, billed_bytes)
		SELECT $1, d.day_start + v.h * interval '1 hour', $2, v.uid, v.up, v.down, v.n,
		       d.day_start + v.h * interval '1 hour' + interval '5 minutes', v.billed
		  FROM d CROSS JOIN (VALUES
		    (1, 7590011::bigint, 100::numeric, 10::numeric, 2::bigint, 110::numeric),
		    (5, 7590011, 1, 1, 1, 2),
		    (5, 7590012, 7, 0, 1, 14),
		    (6, 7590013, 3, 3, 1, NULL)) AS v(h, uid, up, down, n, billed)`, tenantID, nodeID)
	must(`INSERT INTO node_user_traffic_hourly (tenant_id, hour_start, node_id, node_uid,
			upload_bytes, download_bytes, entry_count, last_report_at, billed_bytes)
		VALUES ($1, date_trunc('hour', now(), 'UTC'), $2, 7590011, 1, 1, 1, now(), 1),
		       ($1, date_trunc('hour', now() - interval '75 days', 'UTC'), $2, 7590011, 1, 1, 1, now() - interval '75 days', 1)`,
		tenantID, nodeID)

	svc := NewService(app, nil)
	if n, err := svc.RefreshTrafficDaily(ctx, tenantID); err != nil || n != 3 {
		t.Fatalf("RefreshTrafficDaily wrote %d err=%v, want three uid rows for the finished day", n, err)
	}
	type dayRow struct {
		up, down, n int64
		billed      *int64
		daysAgo     int
	}
	read := func() map[int64]dayRow {
		t.Helper()
		rows, err := admin.Query(ctx, `
			SELECT node_uid, upload_bytes::bigint, download_bytes::bigint, entry_count, billed_bytes::bigint,
			       (now() AT TIME ZONE 'UTC')::date - day
			  FROM node_user_traffic_daily WHERE tenant_id = $1`, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[int64]dayRow{}
		for rows.Next() {
			var uid int64
			var r dayRow
			if err := rows.Scan(&uid, &r.up, &r.down, &r.n, &r.billed, &r.daysAgo); err != nil {
				t.Fatal(err)
			}
			out[uid] = r
		}
		return out
	}
	got := read()
	if len(got) != 3 {
		t.Fatalf("daily rows = %+v, want three", got)
	}
	u1, u2, u3 := got[7590011], got[7590012], got[7590013]
	if u1.daysAgo != 2 || u1.up != 101 || u1.down != 11 || u1.n != 3 || u1.billed == nil || *u1.billed != 112 {
		t.Fatalf("uid1 daily = %+v, want day-2 101/11 n=3 billed=112", u1)
	}
	if u2.up != 7 || u2.billed == nil || *u2.billed != 14 {
		t.Fatalf("uid2 daily = %+v, want 7 bytes billed 14", u2)
	}
	if u3.billed != nil {
		t.Fatalf("uid3 daily billed = %d, want NULL (an hour of the day is unknown)", *u3.billed)
	}
	if n, err := svc.RefreshTrafficDaily(ctx, tenantID); err != nil || n != 0 {
		t.Fatalf("second RefreshTrafficDaily wrote %d err=%v, want nothing (a day is written once)", n, err)
	}

	// 保留期：按天 400 天、节点小时 400 天、节点 × uid 小时 70 天
	must(`INSERT INTO node_user_traffic_daily (tenant_id, day, node_id, node_uid, upload_bytes, download_bytes,
			billed_bytes, entry_count, last_report_at)
		VALUES ($1, (now() AT TIME ZONE 'UTC')::date - 401, $2, 7590011, 1, 1, 1, 1, now() - interval '401 days'),
		       ($1, (now() AT TIME ZONE 'UTC')::date - 399, $2, 7590011, 1, 1, 1, 1, now() - interval '399 days')`,
		tenantID, nodeID)
	must(`INSERT INTO node_traffic_hourly (tenant_id, hour_start, node_id, report_count)
		VALUES ($1, date_trunc('hour', now() - interval '401 days', 'UTC'), $2, 1),
		       ($1, date_trunc('hour', now() - interval '399 days', 'UTC'), $2, 1)`, tenantID, nodeID)
	// 75 天前的节点 × uid 小时行 + 400/401 天的两行各删一行 = 3
	if n, err := svc.PurgeTrafficRollups(ctx, tenantID); err != nil || n != 3 {
		t.Fatalf("PurgeTrafficRollups deleted=%d err=%v, want the 75-day uid hour, the 401-day daily row and node hour", n, err)
	}
	var daily, nodeHours, uidHours int
	if err := admin.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM node_user_traffic_daily WHERE tenant_id = $1),
		       (SELECT count(*) FROM node_traffic_hourly WHERE tenant_id = $1),
		       (SELECT count(*) FROM node_user_traffic_hourly WHERE tenant_id = $1)`, tenantID).Scan(&daily, &nodeHours, &uidHours); err != nil {
		t.Fatal(err)
	}
	if daily != 4 || nodeHours != 1 || uidHours != 5 {
		t.Fatalf("after purge daily=%d nodeHours=%d uidHours=%d, want 4/1/5", daily, nodeHours, uidHours)
	}
	t.Log("marker=retain_pg18_traffic_daily_ok")
}

// migrationRoundTripScenario 证明 00131–00133 的 Down 可执行、Down 之后能再 Up（总协调要求）：在一个回滚的
// 事务里按 00133 → 00131 的顺序跑 Down，核对还原（自引用外键加回：无悬空引用时已校验、否则保持 NOT VALID；四条索引重建、旧的
// (interval) 清理函数回来、billed_bytes 列与按天表消失），再按 00131 → 00133 跑 Up，核对回到迁移后的形状。
func migrationRoundTripScenario(t *testing.T, ctx context.Context, admin *pgx.Conn) {
	t.Helper()
	section := func(file, which string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		up, down := strings.Index(text, "-- +goose Up"), strings.Index(text, "-- +goose Down")
		if up < 0 || down < up {
			t.Fatalf("%s: goose markers missing", file)
		}
		body := text[down:]
		if which == "up" {
			body = text[up:down]
		}
		return strings.NewReplacer("-- +goose StatementBegin", "", "-- +goose StatementEnd", "").Replace(body)
	}
	files := []string{"00131_append_only_retention.sql", "00132_drop_unused_indexes.sql", "00133_traffic_daily_rollup.sql"}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	type shape struct {
		selfFK, oldPurge, newReportPurge, newFetchPurge, dailyTable bool
		indexes, billedCols                                         int
	}
	read := func() (s shape) {
		t.Helper()
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE contype = 'f'
			                 AND conrelid = 'public.node_traffic_reports'::regclass
			                 AND confrelid = 'public.node_traffic_reports'::regclass),
			       to_regprocedure('app.purge_subscription_fetch_log(interval)') IS NOT NULL,
			       to_regprocedure('app.purge_node_traffic_reports(int, int)') IS NOT NULL,
			       to_regprocedure('app.purge_subscription_fetch_log(int, int)') IS NOT NULL,
			       to_regclass('public.node_user_traffic_daily') IS NOT NULL,
			       (SELECT count(*) FROM pg_class WHERE relkind = 'i' AND relname IN (
			          'idx_node_traffic_reports_dashboard_window', 'idx_node_traffic_reports_dashboard_duplicate_window',
			          'idx_node_traffic_reports_dedup', 'idx_audit_events_actor')),
			       (SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public'
			          AND table_name IN ('node_traffic_hourly', 'node_user_traffic_hourly') AND column_name = 'billed_bytes')`).
			Scan(&s.selfFK, &s.oldPurge, &s.newReportPurge, &s.newFetchPurge, &s.dailyTable, &s.indexes, &s.billedCols); err != nil {
			t.Fatal(err)
		}
		return s
	}
	migrated := shape{newReportPurge: true, newFetchPurge: true, dailyTable: true, billedCols: 2}
	if got := read(); got != migrated {
		t.Fatalf("before round trip: %+v, want %+v", got, migrated)
	}
	for i := len(files) - 1; i >= 0; i-- {
		if _, err := tx.Exec(ctx, section(files[i], "down")); err != nil {
			t.Fatalf("%s Down: %v", files[i], err)
		}
	}
	if got, want := read(), (shape{selfFK: true, oldPurge: true, indexes: 4}); got != want {
		t.Fatalf("after Down: %+v, want %+v", got, want)
	}
	for _, f := range files {
		if _, err := tx.Exec(ctx, section(f, "up")); err != nil {
			t.Fatalf("%s Up again: %v", f, err)
		}
	}
	if got := read(); got != migrated {
		t.Fatalf("after Down then Up: %+v, want %+v", got, migrated)
	}
	t.Log("marker=retain_pg18_migrations_down_up_ok")
}
