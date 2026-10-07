package adminops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// 临时诊断（第二轮，量 00099 回填耗时），拿到数字后删除。
// 造 3000 份近 25 小时的上报（每份 8 项，进严格档）与 3000 份 3–30 天前的（只进原始合计档），
// 比较原看板解析与迁移回填两条语句，并打印计划。
func TestRollupBackfillDiagPG18(t *testing.T) {
	ctx, admin, _ := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const (
		tenant = "7c000000-0000-4000-8000-000000000001"
		node   = "7c000000-0000-4000-8000-0000000000a1"
	)
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "00099_node_traffic_hourly.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(migration)
	backfill := text[strings.Index(text, "-- rollup-backfill:begin"):strings.Index(text, "-- rollup-backfill:end")]
	var stmts []string
	for _, s := range strings.Split(backfill, ";") {
		lines := []string{}
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "--") {
				lines = append(lines, l)
			}
		}
		if q := strings.TrimSpace(strings.Join(lines, "\n")); q != "" {
			stmts = append(stmts, q)
		}
	}
	if len(stmts) != 2 {
		t.Fatalf("backfill statements=%d", len(stmts))
	}

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\nSQL: %s", err, sql)
		}
	}
	must(`SET LOCAL jit = off`)
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'diag-backfill','Diag','CNY')`, tenant)
	must(`INSERT INTO nodes(id,tenant_id,name,status) VALUES($2,$1,'diag-node','active')`, tenant, node)
	for _, gen := range []struct{ tag, at string }{
		{"new", "now() - make_interval(secs => g * 30)"},
		{"old", "now() - interval '3 days' - make_interval(secs => g * 777)"},
	} {
		must(`INSERT INTO node_traffic_reports (tenant_id, node_id, raw_payload, content_hash, received_at, total_upload, total_download)
			SELECT $1, $2,
			       (SELECT jsonb_object_agg((7400000 + ((g*7 + i) % 500))::text,
			                                jsonb_build_array((g*i) % 100000, (g+i) % 50000))
			          FROM generate_series(1, 8) i),
			       decode(md5('`+gen.tag+`' || g::text), 'hex'), `+gen.at+`, g, g
			  FROM generate_series(1, 3000) g`, tenant, node)
	}
	must(`ANALYZE node_traffic_reports`)

	timed := func(label, sql string) {
		t.Helper()
		start := time.Now()
		must(sql)
		t.Logf("DIAG %-28s %8.1f ms", label, float64(time.Since(start).Microseconds())/1000)
	}
	plan := func(label, sql string) {
		t.Helper()
		rows, err := tx.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS) `+sql)
		if err != nil {
			t.Fatalf("explain %s: %v", label, err)
		}
		var lines []string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, l)
		}
		rows.Close()
		t.Logf("DIAG PLAN %s\n%s", label, strings.Join(lines, "\n"))
	}
	reset := func() {
		must(`DELETE FROM node_user_traffic_hourly`)
		must(`DELETE FROM node_traffic_hourly`)
	}

	legacy := strings.NewReplacer("$1", "'"+tenant+"'::uuid", "$2", "now() - interval '48 hours'", "$3", "now()", "$4", "5").
		Replace(legacyDashboardNodeTrafficSQL)
	timed("legacy dashboard parse 48h", `SELECT count(*) FROM (`+legacy+`) x`)
	reset()
	timed("backfill stmt1 (uid, 48h)", stmts[0])
	timed("backfill stmt2 (node, 31d)", stmts[1])
	reset()
	plan("backfill stmt1", stmts[0])
	reset()
	plan("backfill stmt2", stmts[1])
	var reports, entries int
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM node_traffic_reports WHERE tenant_id=$1),
		       (SELECT count(*) FROM node_traffic_reports r, jsonb_each(r.raw_payload)
		         WHERE r.tenant_id=$1 AND r.received_at >= now() - interval '48 hours')`, tenant).Scan(&reports, &entries); err != nil {
		t.Fatal(err)
	}
	t.Logf("DIAG data reports=%d entries_48h=%d", reports, entries)
}
