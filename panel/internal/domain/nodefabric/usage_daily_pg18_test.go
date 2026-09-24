// [INPUT]: 依赖 uniproxy.go 的 ReportTraffic、usage_daily.go 的 chargeReportEntry / UsageLocation / UsageDay，依赖 platform/db 的 InTx，依赖迁移 00072 的 subscription_usage_daily
// [OUTPUT]: 对外提供 TestUsageDailyWritePG18（run-pg18-gates.sh 的 traffic_charge 域，与 TestTrafficChargePG18 共用一个库）
// [POS]: domain/nodefabric 按日流量写入的 PG18 集成门禁：真实上报路径的倍率、累加、重试去重、按用户时区切日（默认 UTC 跟随站点时区）、租户隔离与不推送
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// TestUsageDailyWritePG18 证明按日流量（00072）与扣量同源：同一次上报在同一
// 事务里扣配额并累加当日用量，计的是乘过倍率的字节；被判为重试的重复报文
// 两边都不记；日界按用户时区切（用户时区无效或为默认 UTC 时退回租户时区，R50）；运行时角色
// 看不到别的租户的行；写入不产生变更推送。
// 由 run-pg18-gates.sh 的 traffic_charge 域驱动（复用该域的环境变量与库）。
func TestUsageDailyWritePG18(t *testing.T) {
	appDSN := os.Getenv("AEGIS_TRAFFIC_CHARGE_PG18_DSN")
	adminDSN := os.Getenv("AEGIS_TRAFFIC_CHARGE_PG18_ADMIN_DSN")
	if appDSN == "" || adminDSN == "" {
		t.Skip("AEGIS_TRAFFIC_CHARGE_PG18_DSN and AEGIS_TRAFFIC_CHARGE_PG18_ADMIN_DSN are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open fixture connection: %v", err)
	}
	defer admin.Close(ctx)
	app, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatalf("open aegis_app pool: %v", err)
	}
	defer app.Close()
	var database, role string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil ||
		database != os.Getenv("AEGIS_TRAFFIC_CHARGE_PG18_DATABASE") {
		t.Fatalf("refusing unexpected fixture database=%q err=%v", database, err)
	}
	if err := app.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil || role != "aegis_app" {
		t.Fatalf("application role=%q err=%v", role, err)
	}

	const (
		tenantID = "75200000-0000-7000-8000-000000000001"
		otherTen = "75200000-0000-7000-8000-000000000002"
		userNY   = "75200000-0000-7000-8000-000000000011"
		userBad  = "75200000-0000-7000-8000-000000000012"
		userUTC  = "75200000-0000-7000-8000-000000000013"
		subNY    = "75200000-0000-7000-8000-000000000021"
		subBad   = "75200000-0000-7000-8000-000000000022"
		subUTC   = "75200000-0000-7000-8000-000000000023"
		nodeID   = "75200000-0000-7000-8000-000000000031"
		uidNY    = int64(7520001)
		uidBad   = int64(7520002)
		uidUTC   = int64(7520003)
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency, timezone)
		VALUES ($1, 'usage-daily-pg18', 'Usage Daily PG18', 'CNY', 'Asia/Shanghai'),
		       ($2, 'usage-daily-pg18-other', 'Usage Daily PG18 Other', 'CNY', 'UTC')`, tenantID, otherTen)
	// 一个用户有合法时区，另一个是脏值：后者按租户时区（上海）切日。
	must(`INSERT INTO users (id, tenant_id, email, display_name, status, timezone)
		VALUES ($1, $3, 'usage-ny@example.test', 'Usage NY', 'active', 'America/New_York'),
		       ($2, $3, 'usage-bad@example.test', 'Usage Bad', 'active', 'Not/AZone')`,
		userNY, userBad, tenantID)
	// 第三个用户没设过时区（列默认 'UTC'）：R50 视同未设，跟随站点时区（上海）。
	must(`INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $2, 'usage-utc@example.test', 'Usage UTC', 'active')`, userUTC, tenantID)
	must(`INSERT INTO nodes (id, tenant_id, name, status) VALUES ($1, $2, 'usage-node', 'active')`, nodeID, tenantID)
	// 订阅只是扣量的挂载点：关掉触发器（含外键）直接插，套餐与版本与本测试无关。
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end, node_uid)
		VALUES ($1, $3, $4, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0,
		        now() + interval '30 days', $5),
		       ($2, $3, $6, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0,
		        now() + interval '30 days', $7)`,
		subNY, subBad, tenantID, userNY, uidNY, userBad, uidBad)
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end, node_uid)
		VALUES ($1, $2, $3, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0,
		        now() + interval '30 days', $4)`, subUTC, tenantID, userUTC, uidUTC)
	must(`SET session_replication_role = origin`)
	// 纽约用户有限量配额（扣量要落在这里），另一个不限量（照样记按日用量）。
	must(`INSERT INTO quota_balances (tenant_id, subscription_id, metric, period,
			period_start, period_end, granted, limit_value)
		VALUES ($1, $2, 'traffic.bytes', 'cycle', now() - interval '1 day',
		        now() + interval '30 days', 100000, 100000)`, tenantID, subNY)
	must(`LISTEN aegis_change`)

	type usageRow struct {
		day   string
		bytes int64
	}
	rowsOf := func(subID string) []usageRow {
		t.Helper()
		rows, err := admin.Query(ctx, `
			SELECT to_char(day, 'YYYY-MM-DD'), bytes FROM subscription_usage_daily
			 WHERE subscription_id = $1 ORDER BY day`, subID)
		if err != nil {
			t.Fatalf("read usage rows: %v", err)
		}
		defer rows.Close()
		var out []usageRow
		for rows.Next() {
			var r usageRow
			if err := rows.Scan(&r.day, &r.bytes); err != nil {
				t.Fatalf("scan usage row: %v", err)
			}
			out = append(out, r)
		}
		return out
	}
	// 上报用的是墙上时钟：前后各取一次，跨午夜时两天都算对。
	dayIn := func(zone string, before, after time.Time) map[string]bool {
		loc := UsageLocation(zone, "")
		return map[string]bool{
			UsageDay(before, loc).Format(time.DateOnly): true,
			UsageDay(after, loc).Format(time.DateOnly):  true,
		}
	}

	// --- 真实上报路径：倍率、未知 uid、去重、累加 ---
	svc := NewService(app, nil)
	node := &ServingNode{ID: nodeID, TrafficRate: 1.5}
	report := func(payload map[string][2]int64) *PushResult {
		t.Helper()
		raw, _ := json.Marshal(payload)
		res, err := svc.ReportTraffic(ctx, tenantID, node, raw)
		if err != nil {
			t.Fatalf("report traffic: %v", err)
		}
		return res
	}
	before := time.Now()
	first := map[string][2]int64{"7520001": {100, 100}, "7520002": {10, 30}, "7529999": {5, 5}}
	if res := report(first); res.Duplicate || res.Accepted != 2 {
		t.Fatalf("first report = %+v, want 2 accepted and not a duplicate", res)
	}
	after := time.Now()
	ny, bad := rowsOf(subNY), rowsOf(subBad)
	if len(ny) != 1 || ny[0].bytes != 300 || !dayIn("America/New_York", before, after)[ny[0].day] {
		t.Fatalf("new york usage = %+v, want one row of 300 billed bytes on the New York day", ny)
	}
	if len(bad) != 1 || bad[0].bytes != 60 || !dayIn("Asia/Shanghai", before, after)[bad[0].day] {
		t.Fatalf("invalid-timezone usage = %+v, want one row of 60 on the tenant (Shanghai) day", bad)
	}
	// 同一报文 10 秒内重发是重试：留档但既不扣量也不记按日用量。
	if res := report(first); !res.Duplicate {
		t.Fatalf("resent report = %+v, want a duplicate", res)
	}
	if got := rowsOf(subNY); len(got) != 1 || got[0].bytes != 300 {
		t.Fatalf("duplicate report changed usage: %+v", got)
	}
	report(map[string][2]int64{"7520001": {50, 0}})
	if got := rowsOf(subNY); len(got) != 1 || got[0].bytes != 375 {
		t.Fatalf("same-day reports must accumulate into one row: %+v", got)
	}
	var consumed int64
	if err := admin.QueryRow(ctx, `SELECT consumed FROM quota_balances WHERE subscription_id = $1`,
		subNY).Scan(&consumed); err != nil || consumed != 375 {
		t.Fatalf("quota consumed = %d err=%v, want 375 (same source as the daily row)", consumed, err)
	}

	// --- 日界：同一时刻，纽约还是 24 日，上海（租户回退）已是 25 日 ---
	at := time.Date(2030, 9, 24, 17, 30, 0, 0, time.UTC)
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		for _, uid := range []int64{uidNY, uidBad, uidUTC} {
			if ok, err := chargeReportEntry(ctx, tx, tenantID, uid, 7, at); err != nil || !ok {
				t.Errorf("charge uid %d at %v: ok=%v err=%v", uid, at, ok, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("charge at fixed time: %v", err)
	}
	if got := rowsOf(subNY); got[len(got)-1] != (usageRow{"2030-09-24", 7}) {
		t.Fatalf("new york day cut = %+v, want 2030-09-24", got)
	}
	if got := rowsOf(subBad); got[len(got)-1] != (usageRow{"2030-09-25", 7}) {
		t.Fatalf("tenant-timezone day cut = %+v, want 2030-09-25", got)
	}
	var userTZ string
	if err := admin.QueryRow(ctx, `SELECT timezone FROM users WHERE id = $1`, userUTC).Scan(&userTZ); err != nil || userTZ != "UTC" {
		t.Fatalf("default user timezone = %q err=%v, want the column default UTC", userTZ, err)
	}
	if got := rowsOf(subUTC); len(got) != 1 || got[0] != (usageRow{"2030-09-25", 7}) {
		t.Fatalf("default-UTC user day cut = %+v, want 2030-09-25 on the site (Shanghai) day", got)
	}

	// --- 租户隔离：别的租户作用域里一行都看不到 ---
	var visible int
	if err := app.InTx(ctx, platformdb.Scope{TenantID: otherTen}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM subscription_usage_daily`).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Fatalf("other tenant sees %d usage rows err=%v, want 0", visible, err)
	}

	// --- 不推送：高频写入不挂 zz_notify ---
	must(`SELECT 1`)
	for {
		waitCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		note, err := admin.WaitForNotification(waitCtx)
		stop()
		if err != nil {
			if !pgconn.Timeout(err) || ctx.Err() != nil {
				t.Fatalf("wait for notifications: %v", err)
			}
			break
		}
		var payload struct {
			Table string `json:"tbl"`
		}
		if err := json.Unmarshal([]byte(note.Payload), &payload); err != nil {
			t.Fatalf("decode change notice %q: %v", note.Payload, err)
		}
		if payload.Table == "subscription_usage_daily" {
			t.Fatalf("daily usage writes must not send change notices: %s", note.Payload)
		}
	}
	t.Log("marker=usage_daily_pg18_same_tx_as_charge_ok")
	t.Log("marker=usage_daily_pg18_user_timezone_day_cut_ok")
}
