package nodefabric

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// reportIDScenario 证明流量上报按 (节点, X-Report-Id) 幂等（审计 ledger N5，迁移 00123）：
//   - 同一编号再报只留档一行重复件（编号列留空、duplicate_of 指向第一份），不扣费；
//   - 与 10 秒窗口无关：第一份收到时刻挪到一小时前，同一编号照样判重复；
//   - 同一编号并发到达，只有一份入账（唯一部分索引让后到的等前一份提交再判冲突）；
//   - 不带编号的老节点照旧走 10 秒内容哈希去重。
func reportIDScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	const (
		tenantID = "75e00000-0000-7000-8000-000000000001"
		userID   = "75e00000-0000-7000-8000-000000000011"
		subID    = "75e00000-0000-7000-8000-000000000021"
		nodeID   = "75e00000-0000-7000-8000-000000000041"
		uid      = 7560001
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'traffic-report-id-pg18', 'Traffic Report ID PG18', 'CNY')`, tenantID)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $2, 'traffic-report-id@example.test', 'Traffic Report ID', 'active')`, userID, tenantID)
	must(`INSERT INTO nodes (id, tenant_id, name, status) VALUES ($1, $2, 'report-id-node', 'active')`, nodeID, tenantID)
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end, node_uid)
		VALUES ($1, $2, $3, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0, now() + interval '30 days', $4)`,
		subID, tenantID, userID, uid)
	must(`SET session_replication_role = origin`)
	must(`INSERT INTO quota_balances (tenant_id, subscription_id, metric, period,
			period_start, period_end, granted, limit_value)
		VALUES ($1, $2, 'traffic.bytes', 'cycle', now() - interval '1 day', now() + interval '30 days', 100000, 100000)`,
		tenantID, subID)

	svc := NewService(app, nil)
	node := servingNodeWithUsers(svc, tenantID, nodeID, 1, uid)
	push := func(payload, reportID string) *PushResult {
		t.Helper()
		res, err := svc.ReportTrafficWithID(ctx, tenantID, node, []byte(payload), reportID)
		if err != nil {
			t.Fatalf("report %s (%q): %v", payload, reportID, err)
		}
		return res
	}
	consumed := func() (n int64) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT consumed FROM quota_balances WHERE subscription_id = $1`, subID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	const first = "0b6a2c1e-3f4d-4b5a-9c8d-7e6f5a4b3c2d"
	if res := push(`{"7560001":[100,0]}`, first); res.Duplicate || res.Accepted != 1 {
		t.Fatalf("first report = %+v", res)
	}
	if got := consumed(); got != 100 {
		t.Fatalf("first report charged %d, want 100", got)
	}
	// 结果不确定时 pdnd 原样重发：同一编号，只留档
	if res := push(`{"7560001":[100,0]}`, first); !res.Duplicate || res.Accepted != 0 {
		t.Fatalf("resent report = %+v, want a duplicate", res)
	}
	// 窗口无关：把第一份挪到一小时前（绕开追加写触发器只在夹具连接上做）
	must(`SET session_replication_role = replica`)
	must(`UPDATE node_traffic_reports SET received_at = now() - interval '1 hour'
		WHERE node_id = $1 AND client_report_id = $2`, nodeID, first)
	must(`SET session_replication_role = origin`)
	if res := push(`{"7560001":[100,0]}`, first); !res.Duplicate {
		t.Fatalf("report resent an hour later = %+v, want a duplicate", res)
	}
	if got := consumed(); got != 100 {
		t.Fatalf("resent reports charged: consumed=%d, want 100", got)
	}
	var originals, dups, linked int
	if err := admin.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE client_report_id = $2),
		       count(*) FILTER (WHERE duplicate_of IS NOT NULL),
		       count(*) FILTER (WHERE duplicate_of = (SELECT id FROM node_traffic_reports
		                                               WHERE node_id = $1 AND client_report_id = $2)
		                          AND client_report_id IS NULL)
		  FROM node_traffic_reports WHERE node_id = $1`, nodeID, first).Scan(&originals, &dups, &linked); err != nil {
		t.Fatal(err)
	}
	if originals != 1 || dups != 2 || linked != 2 {
		t.Fatalf("archive: originals=%d duplicates=%d linked=%d, want 1/2/2", originals, dups, linked)
	}

	// 并发：同一编号的 8 份同时到达，只有一份入账
	const concurrent = "5d9c2a7e-1b3f-4c6d-8e9a-0f1b2c3d4e5f"
	const workers = 8
	results := make(chan *PushResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := svc.ReportTrafficWithID(ctx, tenantID, node, []byte(`{"7560001":[7,0]}`), concurrent)
			if err != nil {
				errs <- err
				return
			}
			results <- res
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent report: %v", err)
	}
	charged := 0
	for res := range results {
		if !res.Duplicate {
			charged++
		}
	}
	if charged != 1 {
		t.Fatalf("same report id charged %d times concurrently, want 1", charged)
	}
	if got := consumed(); got != 107 {
		t.Fatalf("concurrent reports: consumed=%d, want 107", got)
	}

	// 老节点不带编号：10 秒内同一报文照旧判重复，换了内容照常入账
	if res := push(`{"7560001":[0,3]}`, ""); res.Duplicate {
		t.Fatalf("legacy report = %+v", res)
	}
	if res := push(`{"7560001":[0,3]}`, ""); !res.Duplicate {
		t.Fatalf("legacy resend within 10 seconds = %+v, want a duplicate", res)
	}
	if got := consumed(); got != 110 {
		t.Fatalf("legacy path: consumed=%d, want 110", got)
	}
	t.Log("marker=traffic_charge_pg18_report_id_idempotent_ok")
}
