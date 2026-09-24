// [INPUT]: 依赖 usage_daily.go 的 Service.DailyUsage，依赖 nodefabric 的 UsageLocation / UsageDay，依赖迁移 00072 的 subscription_usage_daily
// [OUTPUT]: 对外提供 TestUsageDailyReadPG18（run-pg18-gates.sh 的 usage_daily 域）
// [POS]: domain/subscription 按日用量读模型的 PG18 集成门禁：本期窗口、补零、今天与日均、显式天数、无周期缺省、归属 404；写入路径见 nodefabric/usage_daily_pg18_test.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// TestUsageDailyReadPG18 以运行时角色读真实的 subscription_usage_daily：缺省
// 窗口从本期流量周期开始那天（用户时区）到今天、没数据的日子补 0、窗口外的
// 行不计；?days 覆盖窗口；没有任何周期信息的订阅缺省看 30 天；别人的订阅与
// 非法 ID 一律 ErrNotFound。由 run-pg18-gates.sh 的 usage_daily 域驱动。
func TestUsageDailyReadPG18(t *testing.T) {
	appDSN := os.Getenv("AEGIS_USAGE_DAILY_PG18_DSN")
	adminDSN := os.Getenv("AEGIS_USAGE_DAILY_PG18_ADMIN_DSN")
	if appDSN == "" || adminDSN == "" {
		t.Skip("AEGIS_USAGE_DAILY_PG18_DSN and AEGIS_USAGE_DAILY_PG18_ADMIN_DSN are required")
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
		database != os.Getenv("AEGIS_USAGE_DAILY_PG18_DATABASE") {
		t.Fatalf("refusing unexpected fixture database=%q err=%v", database, err)
	}
	if err := app.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil || role != "aegis_app" {
		t.Fatalf("application role=%q err=%v", role, err)
	}

	const (
		tenantID = "75300000-0000-7000-8000-000000000001"
		owner    = "75300000-0000-7000-8000-000000000011"
		stranger = "75300000-0000-7000-8000-000000000012"
		subCycle = "75300000-0000-7000-8000-000000000021"
		subBare  = "75300000-0000-7000-8000-000000000022"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency, timezone)
		VALUES ($1, 'usage-read-pg18', 'Usage Read PG18', 'CNY', 'UTC')`, tenantID)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status, timezone)
		VALUES ($1, $3, 'usage-owner@example.test', 'Owner', 'active', 'Asia/Shanghai'),
		       ($2, $3, 'usage-stranger@example.test', 'Stranger', 'active', 'UTC')`,
		owner, stranger, tenantID)
	must(`SET session_replication_role = replica`)
	// subBare 没有周期起点、也没有流量配额行。
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_start, current_period_end)
		VALUES ($1, $3, $4, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0,
		        now() - interval '10 days', now() + interval '20 days'),
		       ($2, $3, $4, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0, NULL, NULL)`,
		subCycle, subBare, tenantID, owner)
	must(`SET session_replication_role = origin`)
	// 流量周期（2 天前起）比订阅周期（10 天前起）短：窗口以流量周期为准。
	must(`INSERT INTO quota_balances (tenant_id, subscription_id, metric, period,
			period_start, period_end, granted, limit_value)
		VALUES ($1, $2, 'traffic.bytes', 'cycle', now() - interval '2 days',
		        now() + interval '28 days', 1000, 1000)`, tenantID, subCycle)
	var periodStart time.Time
	if err := admin.QueryRow(ctx, `SELECT period_start FROM quota_balances WHERE subscription_id = $1`,
		subCycle).Scan(&periodStart); err != nil {
		t.Fatalf("read period start: %v", err)
	}

	today := nodefabric.UsageDay(time.Now(), nodefabric.UsageLocation("Asia/Shanghai", ""))
	dayKey := func(offset int) string { return today.AddDate(0, 0, offset).Format(time.DateOnly) }
	must(`INSERT INTO subscription_usage_daily (tenant_id, subscription_id, day, bytes)
		VALUES ($1, $2, $3::date, 700), ($1, $2, $4::date, 200), ($1, $2, $5::date, 999),
		       ($1, $6, $3::date, 42)`,
		tenantID, subCycle, dayKey(0), dayKey(-1), dayKey(-5), subBare)

	svc := New(app, nil, nil)
	read := func(user, sub string, days int) *DailyUsage {
		t.Helper()
		out, err := svc.DailyUsage(ctx, tenantID, user, sub, days)
		if err != nil {
			t.Fatalf("daily usage %s days=%d: %v", sub, days, err)
		}
		return out
	}
	expectDays := func(step string, got []UsageDayPoint, want []UsageDayPoint) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: days = %v, want %v", step, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: days[%d] = %v, want %v", step, i, got[i], want[i])
			}
		}
	}

	cur := read(owner, subCycle, 0)
	if cur.Timezone != "Asia/Shanghai" || !cur.PeriodStart.Equal(periodStart) || cur.PeriodEnd == nil {
		t.Fatalf("period = tz %q start %v end %v, want Asia/Shanghai, %v and a period end",
			cur.Timezone, cur.PeriodStart, cur.PeriodEnd, periodStart)
	}
	// 流量周期 2 天前起（用户时区里恰好是前天）：3 天，5 天前那行在窗口外。
	expectDays("current period", cur.Days, []UsageDayPoint{{dayKey(-2), 0}, {dayKey(-1), 200}, {dayKey(0), 700}})
	if cur.TodayBytes != 700 || cur.AvgDailyBytes != 300 {
		t.Fatalf("today=%d avg=%d, want 700 and 300", cur.TodayBytes, cur.AvgDailyBytes)
	}

	week := read(owner, subCycle, 7)
	if len(week.Days) != 7 || week.Days[1] != (UsageDayPoint{dayKey(-5), 999}) {
		t.Fatalf("explicit 7 days = %v, want 7 days including the 999 five days ago", week.Days)
	}
	if week.AvgDailyBytes != (700+200+999)/7 {
		t.Fatalf("explicit window avg = %d, want %d", week.AvgDailyBytes, (700+200+999)/7)
	}

	bare := read(owner, subBare, 0)
	if len(bare.Days) != defaultUsageDays || bare.TodayBytes != 42 || bare.PeriodEnd != nil {
		t.Fatalf("no-period subscription = %d days today=%d end=%v, want %d days, 42, no end",
			len(bare.Days), bare.TodayBytes, bare.PeriodEnd, defaultUsageDays)
	}

	for _, tc := range []struct{ user, sub string }{
		{stranger, subCycle},                            // 别人的订阅
		{owner, "75300000-0000-7000-8000-000000000099"}, // 不存在
		{owner, "not-a-uuid"},                           // 非法 ID
	} {
		if _, err := svc.DailyUsage(ctx, tenantID, tc.user, tc.sub, 0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("daily usage user=%s sub=%s err = %v, want ErrNotFound", tc.user, tc.sub, err)
		}
	}
	t.Log("marker=usage_daily_read_pg18_period_window_ok")
}
