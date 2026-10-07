package adminops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// TestDashboardTrafficRollupPG18 证明看板改读小时汇总（00099）后口径不变：
//
//  1. 入库路径：一批刁钻上报（零流量项、"+7"/"007" 同一 uid、非数字键、超 19 位键、
//     负数、一元 / 三元数组、null 值与 null 元素、根为 null、空对象、未知 uid、10 秒内
//     重发的重复报文）经 nodefabric.ReportTraffic 入库并累加汇总；看板两条排行（limit 5
//     截断与 20 全量）与原 SQL 直接展开 raw_payload 的结果逐项相同，后台节点列表的
//     24h / 30 天流量与原来对上报合计求和相同。
//  2. 回填路径：在回滚的事务里再塞一批 Go 收不进来、但留档里可能有的报文（小数、字符串、
//     1e2、根为数组 / 字符串、int64 边界、标了 duplicate_of 的行），清空汇总后重跑迁移
//     原文里的回填段，结果同样与原 SQL 相同。
func TestDashboardTrafficRollupPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const tenant = "7d000000-0000-4000-8000-000000000001"
	nodeIDs := []string{
		"7d000000-0000-4000-8000-0000000000a1",
		"7d000000-0000-4000-8000-0000000000a2",
		"7d000000-0000-4000-8000-0000000000a3",
	}
	// 用户 0..7 各一条订阅（uid 7300001..7300008）；用户 0 另有第二条订阅（uid 7300009）
	uid := func(i int) string { return fmt.Sprint(7300001 + i) }
	userID := func(i int) string { return fmt.Sprintf("7d000000-0000-4000-8000-%012d", 100+i) }
	subID := func(i int) string { return fmt.Sprintf("7d000000-0000-4000-8000-%012d", 200+i) }
	seedDashboardTraffic(t, ctx, admin, tenant, nodeIDs, userID, subID)

	nodes := nodefabric.NewService(app, nil)
	push := func(node int, payload string) {
		t.Helper()
		if _, err := nodes.ReportTraffic(ctx, tenant, &nodefabric.ServingNode{ID: nodeIDs[node], TrafficRate: 1}, []byte(payload)); err != nil {
			t.Fatalf("report %s: %v", payload, err)
		}
	}
	u := func(i int) string { return `"` + uid(i) + `"` }
	first := `{` + u(0) + `:[100,200],` + u(8) + `:[0,0],` + u(1) + `:[50,0],"9999991":[7,7],` + u(2) + `:[300,1],` +
		u(3) + `:[10,10],` + u(4) + `:[5,0],` + u(5) + `:[1,1],` + u(6) + `:[2000,0],` + u(7) + `:[0,9]}`
	push(0, first)
	push(0, first) // 10 秒内重发：重复上报
	push(0, `{"+`+uid(0)+`":[1,1],"00`+uid(0)+`":[2,2],"abc":[5,5],"99999999999999999999":[1,1],`+u(1)+`:[-5,10],"-0":[3,3]}`)
	push(1, `{`+u(8)+`:[1000,0],`+u(1)+`:[1],`+u(0)+`:[1,2,3],`+u(2)+`:[40,40]}`)
	push(1, `{`+u(1)+`:null,`+u(8)+`:[null,5],`+u(3)+`:[7,0]}`)
	push(1, `null`)
	push(1, `{}`)
	push(2, `{`+u(0)+`:[0,0]}`)

	svc := NewService(app)
	for _, limit := range []int{5, 20} {
		gotNodes, err := svc.DashboardNodeTraffic(ctx, tenant, DashboardTrafficQuery{Range: "24h", Limit: limit})
		if err != nil {
			t.Fatalf("node traffic: %v", err)
		}
		gotUsers, err := svc.DashboardUserTraffic(ctx, tenant, DashboardTrafficQuery{Range: "24h", Limit: limit, SnapshotAt: gotNodes.SnapshotAt})
		if err != nil {
			t.Fatalf("user traffic: %v", err)
		}
		snapshot, err := time.Parse(time.RFC3339Nano, gotNodes.SnapshotAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
			compareDashboardTraffic(t, ctx, tx, tenant, snapshot, limit, gotNodes, gotUsers)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// 不是两边都空：重复 1 份，非法上报 4 份（非法键/负数、一元/三元数组、null、根 null），
		// 非法项 7 个；未归属 = uid 9999991 的 14 + uid 0（"-0"）的 6
		if q := gotNodes.Quality; q != (DashboardTrafficQuality{DuplicateReportCount: 1, InvalidReportCount: 4, InvalidEntryCount: 7}) ||
			gotNodes.Totals.UnattributedBytes != "20" || len(gotNodes.Items) != 2 {
			t.Fatalf("rollup reading is vacuous or wrong: quality=%+v totals=%+v items=%d", q, gotNodes.Totals, len(gotNodes.Items))
		}
		// 排名：用户 6（2000）、用户 0（两条订阅合计 1306）、用户 2、1、3、7、4、5
		if want := min(limit, 8); len(gotUsers.Items) != want || gotUsers.Items[1].UserID != userID(0) ||
			gotUsers.Items[1].SubscriptionCount != 2 || gotUsers.Items[1].TotalBytes != "1306" {
			t.Fatalf("user ranking items=%d want %d: %+v", len(gotUsers.Items), want, gotUsers.Items)
		}
	}

	// 后台节点列表：24h 与 30 天流量与原来「非重复上报的 total_upload + total_download 之和」相同
	listed, _, err := nodes.ListAdminNodes(ctx, tenant, true, 100, 0)
	if err != nil {
		t.Fatalf("list admin nodes: %v", err)
	}
	legacyBytes := map[string]int64{}
	rows, err := admin.Query(ctx, `
		SELECT node_id::text, sum(total_upload + total_download)::bigint FROM node_traffic_reports
		 WHERE tenant_id = $1 AND duplicate_of IS NULL AND received_at > now() - interval '30 days'
		 GROUP BY node_id`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var bytes int64
		if err := rows.Scan(&id, &bytes); err != nil {
			t.Fatal(err)
		}
		legacyBytes[id] = bytes
	}
	rows.Close()
	if len(listed) != len(nodeIDs) {
		t.Fatalf("listed %d nodes", len(listed))
	}
	for _, n := range listed {
		if n.TrafficBytes != legacyBytes[n.ID] || n.TrafficBytes24h != legacyBytes[n.ID] {
			t.Fatalf("node %s traffic=%d/24h=%d, legacy=%d", n.ID, n.TrafficBytes, n.TrafficBytes24h, legacyBytes[n.ID])
		}
	}
	if legacyBytes[nodeIDs[0]] == 0 || legacyBytes[nodeIDs[1]] == 0 {
		t.Fatalf("node list comparison is vacuous: %v", legacyBytes)
	}

	backfillMatchesLegacy(t, ctx, admin, tenant, nodeIDs[2], uid)
	t.Log("marker=dashboard_traffic_rollup_pg18_matches_legacy_ok")
}

func seedDashboardTraffic(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenant string, nodeIDs []string,
	userID, subID func(int) string) {
	t.Helper()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	exec(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'dash-traffic-pg18','Dash Traffic PG18','CNY')`, tenant)
	for i, id := range nodeIDs {
		var display *string
		if i == 0 {
			name := "香港 01"
			display = &name
		}
		exec(`INSERT INTO nodes(id,tenant_id,name,status,display_name) VALUES($1,$2,$3,'active',$4)`,
			id, tenant, fmt.Sprintf("dash-node-%d", i), display)
	}
	for i := 0; i < 8; i++ {
		exec(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,'Dash','active')`,
			userID(i), tenant, fmt.Sprintf("user%d@Dash-Traffic.invalid", i))
	}
	// 订阅只是归属的挂载点：replica 模式跳过触发器与外键，套餐与版本与本测试无关
	exec(`SET LOCAL session_replication_role = replica`)
	for i := 0; i < 9; i++ {
		owner := userID(i)
		if i == 8 {
			owner = userID(0)
		}
		exec(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,current_period_end,node_uid)
		      VALUES($1,$2,$3,gen_random_uuid(),gen_random_uuid(),'active','CNY',0,now()+interval '30 days',$4)`,
			subID(i), tenant, owner, int64(7300001+i))
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// compareDashboardTraffic 在同一个事务里用原 SQL 按精确窗口 [snapshot-24h, snapshot) 重算，
// 与服务读汇总的结果逐项比对（from 不比：新读法报的是对齐到整点的实际下界）。
func compareDashboardTraffic(t *testing.T, ctx context.Context, tx pgx.Tx, tenant string, snapshot time.Time, limit int,
	gotNodes *DashboardNodeTraffic, gotUsers *DashboardUserTraffic) {
	t.Helper()
	from := snapshot.Add(-24 * time.Hour)
	wantNodes := &DashboardNodeTraffic{Items: []DashboardNodeTrafficItem{}}
	if err := scanDashboardNodeTraffic(ctx, tx, legacyDashboardNodeTrafficSQL, tenant, from, snapshot, limit, wantNodes); err != nil {
		t.Fatalf("legacy node traffic: %v", err)
	}
	wantUsers := &DashboardUserTraffic{Items: []DashboardUserTrafficItem{}}
	if err := scanDashboardUserTraffic(ctx, tx, legacyDashboardUserTrafficSQL, tenant, from, snapshot, limit, wantUsers); err != nil {
		t.Fatalf("legacy user traffic: %v", err)
	}
	if !reflect.DeepEqual(gotNodes.Items, wantNodes.Items) || gotNodes.Totals != wantNodes.Totals ||
		gotNodes.Ranking != wantNodes.Ranking || gotNodes.Quality != wantNodes.Quality {
		t.Fatalf("node traffic (limit %d) differs from legacy\nrollup: %+v\nlegacy: %+v", limit, *gotNodes, *wantNodes)
	}
	if !reflect.DeepEqual(gotUsers.Items, wantUsers.Items) || gotUsers.Totals != wantUsers.Totals ||
		gotUsers.Ranking != wantUsers.Ranking || gotUsers.Quality != wantUsers.Quality {
		t.Fatalf("user traffic (limit %d) differs from legacy\nrollup: %+v\nlegacy: %+v", limit, *gotUsers, *wantUsers)
	}
}

// backfillMatchesLegacy 在回滚的事务里补一批只可能出现在历史留档里的报文，清空汇总，
// 跑迁移 00099 原文的回填段，再比对。
func backfillMatchesLegacy(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenant, node string, uid func(int) string) {
	t.Helper()
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "00099_node_traffic_hourly.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(migration)
	begin, end := strings.Index(text, "-- rollup-backfill:begin"), strings.Index(text, "-- rollup-backfill:end")
	if begin < 0 || end <= begin {
		t.Fatal("00099 lost its rollup-backfill markers")
	}
	backfill := text[begin:end]

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	u := func(i int) string { return `"` + uid(i) + `"` }
	var prevID string
	for _, h := range []struct {
		payload string
		dupPrev bool // 标成上一行的重复
	}{
		{payload: `{` + u(0) + `:[1.5,2]}`},
		{payload: `{` + u(0) + `:["1",2]}`},
		{payload: `{` + u(1) + `:[1e2,0]}`},
		{payload: `[1,2]`},
		{payload: `"str"`},
		{payload: `{` + u(2) + `:[9223372036854775807,0]}`},
		{payload: `{` + u(2) + `:[9223372036854775808,0]}`},
		{payload: `{` + u(4) + `:[1,1],"` + uid(4) + ` ":[1,1]}`},
		{payload: `{` + u(3) + `:[2.0,3.0]}`},
		{payload: `{` + u(3) + `:[100,100]}`, dupPrev: true},
	} {
		hash := sha256.Sum256([]byte(h.payload))
		var dupOf *string
		if h.dupPrev {
			prev := prevID
			dupOf = &prev
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO node_traffic_reports (tenant_id, node_id, raw_payload, content_hash, duplicate_of, received_at)
			VALUES ($1, $2, $3::jsonb, $4, $5::uuid, now() - interval '2 minutes') RETURNING id::text`,
			tenant, node, h.payload, hash[:], dupOf).Scan(&prevID); err != nil {
			t.Fatalf("plant historic report %s: %v", h.payload, err)
		}
	}
	// 两档窗口：5 天前的上报只进原始合计（节点列表），不解析；40 天前的什么都不进
	for _, old := range []struct {
		age     string
		payload string
		dup     bool
	}{
		{age: "5 days", payload: `{` + u(5) + `:[5,5]}`},
		{age: "5 days", payload: `{` + u(5) + `:[7,7]}`, dup: true},
		{age: "40 days", payload: `{` + u(5) + `:[9,9]}`},
	} {
		hash := sha256.Sum256([]byte(old.payload + old.age))
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_traffic_reports (tenant_id, node_id, raw_payload, content_hash, duplicate_of,
			                                  total_upload, total_download, received_at)
			VALUES ($1, $2, $3::jsonb, $4, CASE WHEN $5 THEN $6::uuid END, 1000, 24,
			        date_trunc('hour', now() - $7::interval, 'UTC') + interval '10 minutes')`,
			tenant, node, old.payload, hash[:], old.dup, prevID, old.age); err != nil {
			t.Fatalf("plant %s old report: %v", old.age, err)
		}
	}
	for _, sql := range []string{`DELETE FROM node_user_traffic_hourly`, `DELETE FROM node_traffic_hourly`, backfill} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatalf("rerun backfill: %v\nSQL: %s", err, sql)
		}
	}
	var reports, dups, invalid int64
	var raw, upload string
	var positive int64
	if err := tx.QueryRow(ctx, `
		SELECT report_count, duplicate_report_count, invalid_report_count, raw_bytes::text,
		       upload_bytes::text, positive_entry_count
		  FROM node_traffic_hourly
		 WHERE tenant_id = $1 AND node_id = $2
		   AND hour_start = date_trunc('hour', now() - interval '5 days', 'UTC')`, tenant, node).Scan(
		&reports, &dups, &invalid, &raw, &upload, &positive); err != nil {
		t.Fatalf("5-day-old bucket: %v", err)
	}
	if reports != 1 || dups != 0 || invalid != 0 || raw != "1024" || upload != "0" || positive != 0 {
		t.Fatalf("5-day-old bucket = reports %d dups %d invalid %d raw %s upload %s positive %d, want raw totals only",
			reports, dups, invalid, raw, upload, positive)
	}
	var oldRows int
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM node_user_traffic_hourly WHERE tenant_id = $1 AND hour_start < now() - interval '3 days')
		     + (SELECT count(*) FROM node_traffic_hourly WHERE tenant_id = $1 AND hour_start < now() - interval '35 days')`,
		tenant).Scan(&oldRows); err != nil || oldRows != 0 {
		t.Fatalf("rows outside the backfill windows = %d err=%v", oldRows, err)
	}
	window, err := resolveDashboardWindow(ctx, tx, DashboardTrafficQuery{Range: "24h"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{5, 20} {
		gotNodes := &DashboardNodeTraffic{Items: []DashboardNodeTrafficItem{}}
		if err := scanDashboardNodeTraffic(ctx, tx, dashboardNodeTrafficSQL, tenant, window.from, window.to, limit, gotNodes); err != nil {
			t.Fatalf("backfilled node traffic: %v", err)
		}
		gotUsers := &DashboardUserTraffic{Items: []DashboardUserTrafficItem{}}
		if err := scanDashboardUserTraffic(ctx, tx, dashboardUserTrafficSQL, tenant, window.from, window.to, limit, gotUsers); err != nil {
			t.Fatalf("backfilled user traffic: %v", err)
		}
		compareDashboardTraffic(t, ctx, tx, tenant, window.to, limit, gotNodes, gotUsers)
		// 历史报文确实进来了：重复 2 份；非法上报再加 6 份（小数、字符串、根数组、根字符串、
		// 超 int64、带空格的键）
		if q := gotNodes.Quality; q.DuplicateReportCount != 2 || q.InvalidReportCount != 10 || len(gotNodes.Items) != 3 {
			t.Fatalf("backfill comparison is vacuous: quality=%+v items=%d", q, len(gotNodes.Items))
		}
	}
}
