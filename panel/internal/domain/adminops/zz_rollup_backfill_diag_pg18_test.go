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

// 临时诊断（第二轮，定位 00099 回填慢），结论落定后删除。
// 在一次性库里造 3000 份上报（每份 8 项），比较：原看板解析、现迁移回填（V0）、
// 分层加 OFFSET 0 的分类函数（V1）、不经函数直接内联分层表达式（V2），并打印计划。

const diagFencedClassifier = `
SELECT CASE WHEN v.is_valid THEN v.parsed_uid_numeric::bigint END AS entry_uid,
       CASE WHEN v.is_valid THEN trunc(v.upload_numeric) END AS entry_upload,
       CASE WHEN v.is_valid THEN trunc(v.download_numeric) END AS entry_download,
       v.is_valid AS entry_valid
  FROM (
    SELECT p.parsed_uid_numeric, p.upload_numeric, p.download_numeric,
           coalesce(
             p.parsed_uid_numeric IS NOT NULL AND p.shape_ok IS TRUE
             AND p.upload_numeric=trunc(p.upload_numeric)
             AND p.download_numeric=trunc(p.download_numeric)
             AND p.upload_numeric BETWEEN 0 AND 9223372036854775807::numeric
             AND p.download_numeric BETWEEN 0 AND 9223372036854775807::numeric,
             false
           ) AS is_valid
      FROM (
        SELECT CASE WHEN b.bounded_key_text IS NOT NULL
                         AND b.bounded_key_text::numeric BETWEEN
                             -9223372036854775808::numeric AND 9223372036854775807::numeric
                    THEN b.bounded_key_text::numeric END AS parsed_uid_numeric,
               b.shape_ok,
               CASE WHEN jsonb_typeof(b.upload_json)='number'
                    THEN (b.upload_json #>> '{}')::numeric END AS upload_numeric,
               CASE WHEN jsonb_typeof(b.download_json)='number'
                    THEN (b.download_json #>> '{}')::numeric END AS download_numeric
          FROM (
            SELECT CASE WHEN k.key_syntax_ok IS TRUE
                             AND length(ltrim(k.normalized_key_text,'-')) <= 19
                        THEN k.normalized_key_text END AS bounded_key_text,
                   k.array_len=2 AS shape_ok,
                   CASE WHEN k.array_len=2 THEN k.value_json->0 END AS upload_json,
                   CASE WHEN k.array_len=2 THEN k.value_json->1 END AS download_json
              FROM (
                SELECT s.key_syntax_ok,
                       CASE WHEN s.key_syntax_ok THEN
                         CASE WHEN left(s.key_text,1)='-' THEN '-' ELSE '' END ||
                         coalesce(nullif(regexp_replace(ltrim(s.key_text,'+-'), '^0+', ''), ''), '0')
                       END AS normalized_key_text,
                       s.value_json, s.array_len
                  FROM (
                    SELECT entry.key_text, entry.value_json,
                           entry.key_text ~ '^[+-]?[0-9]+$' AS key_syntax_ok,
                           CASE WHEN jsonb_typeof(entry.value_json)='array'
                                THEN jsonb_array_length(entry.value_json) END AS array_len
                      FROM jsonb_each(CASE WHEN jsonb_typeof(PAYLOAD)='object' THEN PAYLOAD ELSE '{}'::jsonb END)
                           entry(key_text,value_json)
                    OFFSET 0) s
                OFFSET 0) k
            OFFSET 0) b
        OFFSET 0) p
    OFFSET 0) v`

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
	must(`INSERT INTO node_traffic_reports (tenant_id, node_id, raw_payload, content_hash, received_at)
		SELECT $1, $2,
		       (SELECT jsonb_object_agg((7400000 + ((g*7 + i) % 500))::text,
		                                jsonb_build_array((g*i) % 100000, (g+i) % 50000))
		          FROM generate_series(1, 8) i),
		       decode(md5(g::text), 'hex'), now() - make_interval(secs => g * 30)
		  FROM generate_series(1, 3000) g`, tenant, node)
	must(`ANALYZE node_traffic_reports`)

	timed := func(label, sql string, args ...any) {
		t.Helper()
		start := time.Now()
		must(sql, args...)
		t.Logf("DIAG %-28s %8.1f ms", label, float64(time.Since(start).Microseconds())/1000)
	}
	plan := func(label, sql string) {
		t.Helper()
		rows, err := tx.Query(ctx, `EXPLAIN (ANALYZE, VERBOSE, BUFFERS) `+sql)
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
		if len(lines) > 40 {
			lines = append(lines[:40], "…", lines[len(lines)-2], lines[len(lines)-1])
		}
		t.Logf("DIAG PLAN %s\n%s", label, strings.Join(lines, "\n"))
	}

	from := "now() - interval '3 days'"
	legacy := strings.NewReplacer("$1", "'"+tenant+"'::uuid", "$2", from, "$3", "now()", "$4", "5").Replace(legacyDashboardNodeTrafficSQL)
	timed("legacy dashboard parse", `SELECT count(*) FROM (`+legacy+`) x`)
	plan("legacy", legacy)

	reset := func() {
		must(`DELETE FROM node_user_traffic_hourly`)
		must(`DELETE FROM node_traffic_hourly`)
	}
	reset()
	timed("V0 stmt1 (uid)", stmts[0])
	timed("V0 stmt2 (node)", stmts[1])
	reset()
	plan("V0 stmt1", stmts[0])
	reset()

	// V1：同名函数换成分层 OFFSET 0 的写法（事务内 DDL，回滚即还原）
	must(`CREATE OR REPLACE FUNCTION app.node_traffic_payload_entries(p_payload jsonb)
		RETURNS TABLE (entry_uid bigint, entry_upload numeric, entry_download numeric, entry_valid boolean)
		LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $fn$` +
		strings.ReplaceAll(diagFencedClassifier, "PAYLOAD", "p_payload") + `$fn$`)
	timed("V1 stmt1 (uid)", stmts[0])
	timed("V1 stmt2 (node)", stmts[1])
	reset()
	plan("V1 stmt1", stmts[0])
	reset()

	// V2：不经函数，分层表达式直接内联进回填
	v2 := strings.Replace(stmts[0], "app.node_traffic_payload_entries(r.raw_payload) e",
		"("+strings.ReplaceAll(diagFencedClassifier, "PAYLOAD", "r.raw_payload")+") e", 1)
	if v2 == stmts[0] {
		t.Fatal("V2 rewrite did not apply")
	}
	timed("V2 stmt1 (uid, no func)", v2)
	reset()
	plan("V2 stmt1", v2)

	// 单份上报的入库汇总（ReportTraffic 用的那条），看函数在单行场景里是否内联
	var rid string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM node_traffic_reports WHERE tenant_id=$1 LIMIT 1`, tenant).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	t.Logf("DIAG done report=%s", rid)
}
