package adminops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// TestDashboardScriptEdgesPG18 承接旧看板脚本里仍是现行 schema 的行为：
// 通知积压的分类与 600 秒整点边界、用户排行的邮箱脱敏、空窗口为零。
// 流量等式与非法报文口径已由 TestDashboardTrafficRollupPG18 对照旧 SQL 覆盖。
// 夹具前缀 c4b…，node_uid 9417xxx，与同库 catalog_sales 域的其它用例错开。
func TestDashboardScriptEdgesPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const (
		tenantMask  = "c4b00000-0000-4000-8000-000000000001"
		tenantOther = "c4b00000-0000-4000-8000-000000000002"
		tenantEmpty = "c4b00000-0000-4000-8000-000000000003"
		tenantQueue = "c4b00000-0000-4000-8000-000000000004"
		tenantHist  = "c4b00000-0000-4000-8000-000000000005"
		tenantClear = "c4b00000-0000-4000-8000-000000000006"
		tenantLate  = "c4b00000-0000-4000-8000-000000000007"
		alice       = "c4b00000-0000-4000-8000-000000000011"
		cat         = "c4b00000-0000-4000-8000-000000000012"
		broken      = "c4b00000-0000-4000-8000-000000000013"
		otherUser   = "c4b00000-0000-4000-8000-000000000021"
	)
	seedDashboardScriptEdges(t, ctx, admin)

	svc := NewService(app)
	query := DashboardTrafficQuery{Range: "24h", Limit: 20}

	t.Run("email mask and tenant isolation", func(t *testing.T) {
		got, err := svc.DashboardUserTraffic(ctx, tenantMask, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != 3 || got.Totals.ReportedBytes != "60" || got.Totals.AttributedBytes != "60" ||
			got.Totals.UnattributedBytes != "0" || got.Ranking.OtherUserBytes != "0" {
			t.Fatalf("mask tenant traffic=%+v items=%d", got.Totals, len(got.Items))
		}
		masks := map[string]string{}
		for _, item := range got.Items {
			masks[item.UserID] = item.EmailMasked
		}
		if masks[alice] != "A***@example.com" || masks[cat] != "猫***@example.com" || masks[broken] != "***" {
			t.Fatalf("masks=%v", masks)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, secret := range []string{"Alice.Secret@Example.COM", "猫咪@Example.COM", "broken-address", "tenant-b@example.com"} {
			if strings.Contains(text, secret) {
				t.Fatalf("full address leaked: %s in %s", secret, text)
			}
		}
		other, err := svc.DashboardUserTraffic(ctx, tenantOther, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(other.Items) != 1 || other.Items[0].EmailMasked != "t***@example.com" ||
			other.Totals.ReportedBytes != "7" || other.Items[0].UserID != otherUser {
			t.Fatalf("other tenant=%#v", other)
		}
	})

	t.Run("empty traffic window", func(t *testing.T) {
		nodes, err := svc.DashboardNodeTraffic(ctx, tenantEmpty, query)
		if err != nil {
			t.Fatal(err)
		}
		users, err := svc.DashboardUserTraffic(ctx, tenantEmpty, query)
		if err != nil {
			t.Fatal(err)
		}
		zero := DashboardTrafficQuality{}
		if len(nodes.Items) != 0 || len(users.Items) != 0 ||
			nodes.Totals.ReportedBytes != "0" || users.Totals.ReportedBytes != "0" ||
			nodes.Quality != zero || users.Quality != zero {
			t.Fatalf("empty window nodes=%#v users=%#v", nodes, users)
		}
	})

	t.Run("notification backlog matrix", func(t *testing.T) {
		got, err := svc.DashboardNotificationBacklog(ctx, tenantQueue)
		if err != nil {
			t.Fatal(err)
		}
		want := DashboardNotificationCounts{
			Ready: 2, ReadyRetry: 1, Scheduled: 2, ScheduledRetry: 1,
			SendingUnobservable: 1, FailedTotal: 1, SuppressedTotal: 1, BouncedTotal: 1,
		}
		if got.BacklogState != "backlogged" || got.ProcessorState != "unobservable" ||
			got.Counts != want || got.MaxReadyLagSeconds <= 600 ||
			got.OldestReadyAt == nil || got.LastSentAt == nil || got.Assessment.Reason != "lag_exceeded" ||
			got.Assessment.ThresholdSeconds != 600 {
			t.Fatalf("queue backlog=%#v", got)
		}
	})

	t.Run("historical failure does not poison backlog", func(t *testing.T) {
		got, err := svc.DashboardNotificationBacklog(ctx, tenantHist)
		if err != nil {
			t.Fatal(err)
		}
		if got.BacklogState != "clear" || got.Counts.FailedTotal != 1 || got.Counts.Ready != 0 ||
			got.Assessment.Reason != "no_due_backlog" || got.MaxReadyLagSeconds != 0 {
			t.Fatalf("history backlog=%#v", got)
		}
	})

	t.Run("empty backlog", func(t *testing.T) {
		got, err := svc.DashboardNotificationBacklog(ctx, tenantEmpty)
		if err != nil {
			t.Fatal(err)
		}
		if got.BacklogState != "clear" || got.ProcessorState != "unobservable" ||
			got.Counts != (DashboardNotificationCounts{}) || got.OldestReadyAt != nil ||
			got.LastSentAt != nil || got.MaxReadyLagSeconds != 0 || got.Assessment.Reason != "no_due_backlog" {
			t.Fatalf("empty backlog=%#v", got)
		}
	})

	t.Run("exact 600 microsecond boundary", func(t *testing.T) {
		asOf := time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
		clear := backlogAt(t, ctx, app, tenantClear, asOf)
		late := backlogAt(t, ctx, app, tenantLate, asOf)
		if clear.BacklogState != "clear" || clear.MaxReadyLagSeconds != 600 || clear.Assessment.Reason != "within_threshold" {
			t.Fatalf("600.000000s = %#v", clear)
		}
		if late.BacklogState != "backlogged" || late.MaxReadyLagSeconds != 601 || late.Assessment.Reason != "lag_exceeded" {
			t.Fatalf("600.000001s = %#v", late)
		}
	})
}

func seedDashboardScriptEdges(t *testing.T, ctx context.Context, admin *pgxpool.Pool) {
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
	for _, row := range []struct{ id, slug string }{
		{"c4b00000-0000-4000-8000-000000000001", "c4b-mask"},
		{"c4b00000-0000-4000-8000-000000000002", "c4b-other"},
		{"c4b00000-0000-4000-8000-000000000003", "c4b-empty"},
		{"c4b00000-0000-4000-8000-000000000004", "c4b-queue"},
		{"c4b00000-0000-4000-8000-000000000005", "c4b-hist"},
		{"c4b00000-0000-4000-8000-000000000006", "c4b-clear"},
		{"c4b00000-0000-4000-8000-000000000007", "c4b-late"},
	} {
		// slug 是 citext、display_name 是 text，同一个占位符无法推出唯一类型。
		exec(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,$2,$3,'CNY')`,
			row.id, row.slug, row.slug)
	}
	for _, row := range []struct{ id, tenant, email string }{
		{"c4b00000-0000-4000-8000-000000000011", "c4b00000-0000-4000-8000-000000000001", "Alice.Secret@Example.COM"},
		{"c4b00000-0000-4000-8000-000000000012", "c4b00000-0000-4000-8000-000000000001", "猫咪@Example.COM"},
		{"c4b00000-0000-4000-8000-000000000013", "c4b00000-0000-4000-8000-000000000001", "broken-address"},
		{"c4b00000-0000-4000-8000-000000000021", "c4b00000-0000-4000-8000-000000000002", "tenant-b@example.com"},
	} {
		exec(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,'C4b','active')`,
			row.id, row.tenant, row.email)
	}
	exec(`INSERT INTO nodes(id,tenant_id,name,status) VALUES
		('c4b00000-0000-4000-8000-0000000000a1','c4b00000-0000-4000-8000-000000000001','c4b-a','active'),
		('c4b00000-0000-4000-8000-0000000000a2','c4b00000-0000-4000-8000-000000000002','c4b-b','active')`)
	exec(`SET LOCAL session_replication_role = replica`)
	exec(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,current_period_end,node_uid) VALUES
		('c4b00000-0000-4000-8000-000000000031','c4b00000-0000-4000-8000-000000000001','c4b00000-0000-4000-8000-000000000011',gen_random_uuid(),gen_random_uuid(),'active','CNY',0,now()+interval '30 days',9417001),
		('c4b00000-0000-4000-8000-000000000032','c4b00000-0000-4000-8000-000000000001','c4b00000-0000-4000-8000-000000000012',gen_random_uuid(),gen_random_uuid(),'active','CNY',0,now()+interval '30 days',9417002),
		('c4b00000-0000-4000-8000-000000000033','c4b00000-0000-4000-8000-000000000001','c4b00000-0000-4000-8000-000000000013',gen_random_uuid(),gen_random_uuid(),'active','CNY',0,now()+interval '30 days',9417003),
		('c4b00000-0000-4000-8000-000000000034','c4b00000-0000-4000-8000-000000000002','c4b00000-0000-4000-8000-000000000021',gen_random_uuid(),gen_random_uuid(),'active','CNY',0,now()+interval '30 days',9417101)`)
	exec(`SET LOCAL session_replication_role = origin`)
	exec(`INSERT INTO node_traffic_hourly(tenant_id,hour_start,node_id,upload_bytes,download_bytes,positive_entry_count,positive_report_count,last_positive_report_at)
		SELECT t, date_trunc('hour', statement_timestamp() - interval '1 hour', 'UTC'), n, up, 0, 1, 1, statement_timestamp()
		  FROM (VALUES
		    ('c4b00000-0000-4000-8000-000000000001'::uuid,'c4b00000-0000-4000-8000-0000000000a1'::uuid,60::numeric),
		    ('c4b00000-0000-4000-8000-000000000002'::uuid,'c4b00000-0000-4000-8000-0000000000a2'::uuid,7::numeric)
		  ) AS v(t,n,up)`)
	exec(`INSERT INTO node_user_traffic_hourly(tenant_id,hour_start,node_id,node_uid,upload_bytes,download_bytes,entry_count,last_report_at)
		SELECT t, date_trunc('hour', statement_timestamp() - interval '1 hour', 'UTC'), n, uid, up, 0, 1, statement_timestamp()
		  FROM (VALUES
		    ('c4b00000-0000-4000-8000-000000000001'::uuid,'c4b00000-0000-4000-8000-0000000000a1'::uuid,9417001::bigint,10::numeric),
		    ('c4b00000-0000-4000-8000-000000000001'::uuid,'c4b00000-0000-4000-8000-0000000000a1'::uuid,9417002::bigint,20::numeric),
		    ('c4b00000-0000-4000-8000-000000000001'::uuid,'c4b00000-0000-4000-8000-0000000000a1'::uuid,9417003::bigint,30::numeric),
		    ('c4b00000-0000-4000-8000-000000000002'::uuid,'c4b00000-0000-4000-8000-0000000000a2'::uuid,9417101::bigint,7::numeric)
		  ) AS v(t,n,uid,up)`)
	exec(`INSERT INTO notification_deliveries(tenant_id,template_code,channel,dedupe_key,status,attempts,next_retry_at,sent_at,created_at) VALUES
		('c4b00000-0000-4000-8000-000000000004','c4b','email','ready','queued',0,NULL,NULL,statement_timestamp()-interval '700 seconds'),
		('c4b00000-0000-4000-8000-000000000004','c4b','email','ready-retry','queued',2,statement_timestamp()-interval '601 seconds',NULL,statement_timestamp()-interval '800 seconds'),
		('c4b00000-0000-4000-8000-000000000004','c4b','email','scheduled','queued',0,statement_timestamp()+interval '1 hour',NULL,statement_timestamp()),
		('c4b00000-0000-4000-8000-000000000004','c4b','email','scheduled-retry','queued',1,statement_timestamp()+interval '2 hours',NULL,statement_timestamp()),
		('c4b00000-0000-4000-8000-000000000004','c4b','email','sending','sending',1,NULL,NULL,statement_timestamp()-interval '1 hour'),
		('c4b00000-0000-4000-8000-000000000004','c4b','email','failed','failed',5,NULL,NULL,statement_timestamp()-interval '2 days'),
		('c4b00000-0000-4000-8000-000000000004','c4b','email','suppressed','suppressed',0,NULL,NULL,statement_timestamp()-interval '2 days'),
		('c4b00000-0000-4000-8000-000000000004','c4b','email','bounced','bounced',1,NULL,NULL,statement_timestamp()-interval '2 days'),
		('c4b00000-0000-4000-8000-000000000004','c4b','email','sent','sent',1,NULL,statement_timestamp()-interval '1 minute',statement_timestamp()-interval '2 minutes'),
		('c4b00000-0000-4000-8000-000000000005','c4b','email','history-failed','failed',5,NULL,NULL,'2026-01-01T00:00:00Z'),
		('c4b00000-0000-4000-8000-000000000006','c4b','email','boundary-clear','queued',0,NULL,NULL,'2026-01-01T00:00:00.000000Z'),
		('c4b00000-0000-4000-8000-000000000007','c4b','email','boundary-late','queued',0,NULL,NULL,'2025-12-31T23:59:59.999999Z')`)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// backlogAt 把积压 SQL 的时钟换成给定时刻，用来卡 600 秒整与多 1 微秒。
func backlogAt(t *testing.T, ctx context.Context, app *db.Pool, tenant string, asOf time.Time) *DashboardNotificationBacklog {
	t.Helper()
	query := strings.Replace(dashboardNotificationBacklogSQL,
		"SELECT statement_timestamp()::timestamptz(6) AS as_of",
		"SELECT $2::timestamptz(6) AS as_of", 1)
	if !strings.Contains(query, "$2::timestamptz(6)") {
		t.Fatal("backlog clock was not substituted")
	}
	out := &DashboardNotificationBacklog{
		ProcessorState: "unobservable", ScannerIntervalSeconds: 300,
		Assessment: DashboardNotificationAssessment{ThresholdSeconds: 600},
	}
	err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, tenant, asOf).Scan(
			&out.AsOf, &out.BacklogState,
			&out.Counts.Ready, &out.Counts.ReadyRetry, &out.Counts.Scheduled, &out.Counts.ScheduledRetry,
			&out.Counts.SendingUnobservable, &out.Counts.FailedTotal, &out.Counts.SuppressedTotal, &out.Counts.BouncedTotal,
			&out.OldestReadyAt, &out.MaxReadyLagSeconds, &out.LastSentAt, &out.Assessment.Reason)
	})
	if err != nil {
		t.Fatalf("fixed backlog: %v", err)
	}
	return out
}
