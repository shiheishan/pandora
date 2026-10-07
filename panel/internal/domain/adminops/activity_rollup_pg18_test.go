package adminops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// 原行为趋势 SQL（66a2043 的 user_profile.go:ActivityTimeseries 原文），只作 PG18 对照：
// 改成按天汇总（00107）之后，同一批数据用它现场聚合的结果必须与新读法逐天相同。
// 不要「顺手」改它——它的价值就在于是旧口径的原文。
const legacyActivityTimeseriesSQL = `
			WITH d AS (
			  SELECT generate_series(
			    date_trunc('day', now()) - make_interval(days => $2 - 1),
			    date_trunc('day', now()), '1 day')::date AS day
			), active AS (
			  -- 两路来源去重：成功的订阅拉取（按会话时区切日，与本接口其余列一致），
			  -- 与按日流量（00072，按用户 / 站点时区记的日）
			  SELECT day, count(DISTINCT user_id)::int AS n FROM (
			    SELECT date_trunc('day', f.fetched_at)::date AS day, s.user_id
			      FROM subscription_fetch_log f
			      JOIN subscriptions s ON s.tenant_id = f.tenant_id AND s.id = f.subscription_id
			     WHERE f.tenant_id = $1 AND f.result = 'ok'
			       AND f.fetched_at >= date_trunc('day', now()) - make_interval(days => $2 - 1)
			    UNION
			    SELECT u.day, s.user_id
			      FROM subscription_usage_daily u
			      JOIN subscriptions s ON s.tenant_id = u.tenant_id AND s.id = u.subscription_id
			     WHERE u.tenant_id = $1 AND u.bytes > 0
			       AND u.day >= (date_trunc('day', now()) - make_interval(days => $2 - 1))::date
			  ) x GROUP BY day
			)
			SELECT to_char(d.day, 'MM-DD'),
			  count(*) FILTER (WHERE a.action = 'user.registered'),
			  count(*) FILTER (WHERE a.action = 'user.login' AND a.outcome = 'success'),
			  count(*) FILTER (WHERE a.action = 'order.created'),
			  count(DISTINCT a.source_ip_hash),
			  coalesce(max(ac.n), 0)
			  FROM d
			  LEFT JOIN active ac ON ac.day = d.day
			  -- occurred_at 的下界与 d 的第一天同一个日界（会话时区），不改变能连上的行，
			  -- 只让它走时间索引；原来只有按天相等的条件，要扫这个租户的全部审计
			  LEFT JOIN audit_events a
			    ON a.tenant_id = $1 AND date_trunc('day', a.occurred_at)::date = d.day
			   AND a.occurred_at >= date_trunc('day', now()) - make_interval(days => $2 - 1)
			 GROUP BY d.day ORDER BY d.day`

// TestActivityDailyRollupPG18 证明行为趋势改读按天汇总（00107）后口径不变：
//
//  1. 没有汇总行时全部实时算，与原 SQL 逐天相同（1、2、3、7、14、90 天）；
//  2. 跑迁移原文的回填段后，定稿行被读到（篡改一行，输出跟着变），结果仍与原 SQL 相同；
//     未定稿的行、别的会话时区算的行都不用，回到实时算；
//  3. 换会话时区（上海、洛杉矶）切日跟着会话时区走，与原 SQL 在同一时区下相同；
//  4. 定时重算写最近 2 个已结束日并吸收迟到写入；400 天以前的行被清理；
//  5. 口径函数被内联（不是逐行调用的 Function Scan）。
func TestActivityDailyRollupPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const (
		tenant = "7c000000-0000-4000-8000-000000000001"
		other  = "7c000000-0000-4000-8000-000000000002"
	)
	seedActivity(t, ctx, admin, tenant, other)
	svc := NewService(app)

	compareFor := func(tenant, label, tz string, days int) []TimeseriesPoint {
		t.Helper()
		var got, want []TimeseriesPoint
		if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
			if tz != "" {
				if _, err := tx.Exec(ctx, `SELECT set_config('TimeZone', $1, true)`, tz); err != nil {
					return err
				}
			}
			var err error
			if got, err = activityTimeseriesTx(ctx, tx, tenant, days); err != nil {
				return err
			}
			want, err = legacyActivityTimeseries(ctx, tx, tenant, days)
			return err
		}); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s (tz=%q days=%d) differs from legacy\nnew:    %+v\nlegacy: %+v", label, tz, days, got, want)
		}
		if len(got) != max(days, 0) {
			t.Fatalf("%s: %d points for %d days", label, len(got), days)
		}
		return got
	}
	compare := func(label, tz string, days int) []TimeseriesPoint {
		t.Helper()
		return compareFor(tenant, label, tz, days)
	}
	dayList := []int{0, 1, 2, 3, 7, 14, 90}

	// 1. 没有汇总行：全部实时算
	for _, d := range dayList {
		compare("live only", "", d)
	}
	full := compare("live only", "", 90)
	if nonZero := countNonZero(full); nonZero < 6 {
		t.Fatalf("activity comparison is vacuous: %d non-empty days in %+v", nonZero, full)
	}

	// 2. 迁移回填：近 90 天的已结束日，前天及更早的定稿
	if _, err := admin.Exec(ctx, migrationSection(t, "00107_activity_daily.sql", "activity-backfill")); err != nil {
		t.Fatalf("activity backfill: %v", err)
	}
	var finalRows, allRows int
	if err := admin.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE computed_at >= (day + 2)::timestamptz), count(*)
		  FROM activity_daily WHERE tenant_id = $1 AND tz = current_setting('TimeZone')`, tenant).Scan(&finalRows, &allRows); err != nil {
		t.Fatal(err)
	}
	if finalRows != 89 || allRows != 90 {
		t.Fatalf("backfill wrote %d rows (%d final), want 90 (89 final)", allRows, finalRows)
	}
	for _, d := range dayList {
		compare("after backfill", "", d)
		// 别的租户各有自己的汇总行，互不串
		compareFor(other, "other tenant after backfill", "", d)
	}

	// 定稿行确实被读到：篡改 10 天前那一行，输出跟着变
	mustAdmin := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\nSQL: %s", err, sql)
		}
	}
	mustAdmin(`UPDATE activity_daily SET logins = logins + 1000 WHERE tenant_id = $1 AND day = current_date - 10`, tenant)
	tampered, err := svc.ActivityTimeseries(ctx, tenant, 14)
	if err != nil {
		t.Fatal(err)
	}
	if tampered[3].Logins != full[len(full)-11].Logins+1000 {
		t.Fatalf("final rollup row was not read: got %+v, live %+v", tampered[3], full[len(full)-11])
	}
	// 同一行改成未定稿：不用，回到实时算
	mustAdmin(`UPDATE activity_daily SET computed_at = (day + 1)::timestamptz WHERE tenant_id = $1 AND day = current_date - 10`, tenant)
	compare("non-final row", "", 14)
	// 改成别的会话时区算的：同样不用
	mustAdmin(`UPDATE activity_daily SET computed_at = now(), tz = 'Asia/Tokyo' WHERE tenant_id = $1 AND day = current_date - 10`, tenant)
	compare("other tz row", "", 14)
	mustAdmin(`UPDATE activity_daily SET logins = logins - 1000, tz = current_setting('TimeZone')
		WHERE tenant_id = $1 AND day = current_date - 10`, tenant)
	compare("restored", "", 14)

	// 3. 会话时区：切日跟着会话时区走；UTC 的汇总行不被别的时区借用
	for _, tz := range []string{"Asia/Shanghai", "America/Los_Angeles"} {
		for _, d := range []int{1, 3, 14, 90} {
			compare("session tz", tz, d)
		}
		// 在这个时区下重算一次：写出的行带这个时区，再读仍与原 SQL 相同
		if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('TimeZone', $1, true)`, tz); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, refreshActivityDailySQL, tenant)
			return err
		}); err != nil {
			t.Fatalf("refresh under %s: %v", tz, err)
		}
		// 3 天：前天读这个时区刚写的定稿行，昨天与今天实时算
		compare("session tz after refresh", tz, 3)
		compare("session tz after refresh", tz, 14)
	}

	// 4. 定时重算：最近 2 个已结束日；迟到写入在下一轮被吸收
	if n, err := svc.RefreshActivityDaily(ctx, tenant); err != nil || n != 2 {
		t.Fatalf("RefreshActivityDaily wrote %d err=%v, want 2", n, err)
	}
	mustAdmin(`INSERT INTO subscription_usage_daily (tenant_id, subscription_id, day, bytes)
		VALUES ($1, $2, current_date - 2, 5)
		ON CONFLICT (tenant_id, subscription_id, day) DO UPDATE SET bytes = 5`, tenant, activitySub(tenant, 3))
	before := activeOn(t, ctx, admin, tenant, 2)
	if _, err := svc.RefreshActivityDaily(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if after := activeOn(t, ctx, admin, tenant, 2); after != before+1 {
		t.Fatalf("late write not absorbed: active_users %d -> %d", before, after)
	}
	for _, d := range dayList {
		compare("after refresh", "", d)
	}
	// 400 天保留期
	mustAdmin(`INSERT INTO activity_daily (tenant_id, tz, day, registered, logins, orders, unique_ips, active_users, computed_at)
		VALUES ($1, current_setting('TimeZone'), current_date - 401, 0, 0, 0, 0, 0, now()),
		       ($1, current_setting('TimeZone'), current_date - 399, 0, 0, 0, 0, 0, now())`, tenant)
	if n, err := svc.PurgeActivityDaily(ctx, tenant); err != nil || n != 1 {
		t.Fatalf("PurgeActivityDaily deleted %d err=%v, want 1", n, err)
	}

	// 5. 口径函数被内联：实时部分的计划里没有对它的 Function Scan
	var plan strings.Builder
	if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `EXPLAIN `+activityLiveSQL, tenant, time.Now().AddDate(0, 0, -3))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line + "\n")
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.String(), "activity_daily_compute") {
		t.Fatalf("app.activity_daily_compute was not inlined:\n%s", plan.String())
	}
	t.Log("marker=activity_daily_rollup_pg18_matches_legacy_ok")
}

// migrationSection 截取迁移原文里两行标记之间的一段（回填段），PG18 测试在回滚或一次性库里重跑它。
func migrationSection(t *testing.T, file, marker string) string {
	t.Helper()
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", file))
	if err != nil {
		t.Fatal(err)
	}
	text := string(migration)
	begin, end := strings.Index(text, "-- "+marker+":begin"), strings.Index(text, "-- "+marker+":end")
	if begin < 0 || end <= begin {
		t.Fatalf("%s lost its %s markers", file, marker)
	}
	return text[begin:end]
}

func legacyActivityTimeseries(ctx context.Context, tx pgx.Tx, tenant string, days int) ([]TimeseriesPoint, error) {
	out := []TimeseriesPoint{}
	rows, err := tx.Query(ctx, legacyActivityTimeseriesSQL, tenant, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p TimeseriesPoint
		if err := rows.Scan(&p.Day, &p.Registered, &p.Logins, &p.Orders, &p.UniqueIPs, &p.ActiveUsers); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func countNonZero(points []TimeseriesPoint) int {
	n := 0
	for _, p := range points {
		if p.Registered+p.Logins+p.Orders+p.UniqueIPs+p.ActiveUsers > 0 {
			n++
		}
	}
	return n
}

func activeOn(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenant string, ago int) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(ctx, `
		SELECT active_users FROM activity_daily
		 WHERE tenant_id = $1 AND tz = current_setting('TimeZone') AND day = current_date - $2::int`,
		tenant, ago).Scan(&n); err != nil {
		t.Fatalf("activity_daily row %d days ago: %v", ago, err)
	}
	return n
}

// 两个租户的用户与订阅 id 按租户 id 的末两位错开，主键不撞
func activityUser(tenant string, i int) string {
	return fmt.Sprintf("7c0000%s-0000-4000-8000-%012d", tenant[len(tenant)-2:], 100+i)
}

func activitySub(tenant string, i int) string {
	return fmt.Sprintf("7c0000%s-0000-4000-8000-%012d", tenant[len(tenant)-2:], 200+i)
}

// seedActivity 造两个租户的审计、订阅拉取与按日流量：各种动作与结果、日界两侧（零点整与前一刻）、
// 窗口外（95 天前）、明天的按日流量（站点时区早于会话时区）、零流量与失败拉取。
// 时间都相对 now() 的会话时区零点给出；回放迁移时的会话时区就是库的 TimeZone。
func seedActivity(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenant, other string) {
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
	// 审计是追加写、带哈希链触发器；趋势只读计数列，replica 模式下直接按时刻写入历史行
	exec(`SET LOCAL session_replication_role = replica`)
	for i, id := range []string{tenant, other} {
		exec(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,$2,'Activity PG18','CNY')`,
			id, fmt.Sprintf("activity-pg18-%d", i))
		for u := 0; u < 4; u++ {
			exec(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,'Act','active')`,
				activityUser(id, u), id, fmt.Sprintf("act%d-%d@activity.invalid", i, u))
			exec(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,current_period_end)
			      VALUES($1,$2,$3,gen_random_uuid(),gen_random_uuid(),'active','CNY',0,now()+interval '30 days')`,
				activitySub(id, u), id, activityUser(id, u))
		}
	}
	type ev struct {
		ago     int    // 几天前（会话时区的日）
		at      string // 当天零点之后的偏移
		action  string
		outcome string
		ip      string
	}
	events := []ev{
		{0, "1 hour", "user.registered", "success", "a"}, {0, "2 hours", "user.login", "success", "a"},
		{0, "3 hours", "user.login", "failure", "b"}, {0, "4 hours", "order.created", "success", "c"},
		{1, "0 seconds", "user.login", "success", "a"}, {1, "23:59:59.999999", "user.login", "success", "d"},
		{1, "5 hours", "user.registered", "success", "e"}, {1, "6 hours", "ticket.created", "success", ""},
		{2, "12 hours", "order.created", "success", "a"}, {2, "13 hours", "order.created", "success", "a"},
		{3, "0 seconds", "user.registered", "success", "f"}, {3, "23:00", "user.login", "denied", "f"},
		{10, "8 hours", "user.login", "success", "g"}, {10, "9 hours", "user.login", "success", "h"},
		{10, "10 hours", "user.registered", "success", "g"},
		{40, "7 hours", "order.created", "success", "i"}, {89, "1 hour", "user.login", "success", "j"},
		{90, "1 hour", "user.login", "success", "k"}, {95, "1 hour", "user.registered", "success", "l"},
	}
	for i, e := range events {
		var ip any
		if e.ip != "" {
			ip = []byte("activity-ip-" + e.ip)
		}
		for j, id := range []string{tenant, other} {
			// 另一个租户只写一半，数字与本租户不同，串了就对不上
			if j == 1 && i%2 == 0 {
				continue
			}
			exec(`INSERT INTO audit_events (tenant_id, actor_kind, actor_id, action, outcome, source_ip_hash, occurred_at)
			      VALUES ($1, 'user', $2, $3, $4, $5, date_trunc('day', now()) - make_interval(days => $6) + $7::interval)`,
				id, activityUser(id, i%4), e.action, e.outcome, ip, e.ago, e.at)
		}
	}
	fetches := []struct {
		ago    int
		at     string
		sub    int
		result string
	}{
		{0, "1 hour", 0, "ok"}, {0, "2 hours", 0, "ok"}, {0, "3 hours", 1, "not_found"},
		{1, "0 seconds", 1, "ok"}, {1, "23:59:59", 2, "ok"}, {2, "4 hours", 2, "ok"},
		{5, "4 hours", 3, "rate_limited"}, {10, "11 hours", 3, "ok"}, {60, "1 hour", 0, "ok"},
		{91, "1 hour", 1, "ok"},
	}
	for _, f := range fetches {
		for _, id := range []string{tenant, other} {
			exec(`INSERT INTO subscription_fetch_log (tenant_id, subscription_id, result, fetched_at)
			      VALUES ($1, $2, $3, date_trunc('day', now()) - make_interval(days => $4) + $5::interval)`,
				id, activitySub(id, f.sub), f.result, f.ago, f.at)
		}
	}
	usage := []struct {
		ago   int
		sub   int
		bytes int64
	}{
		{0, 3, 10}, {-1, 2, 10}, {1, 0, 7}, {1, 3, 0}, {2, 1, 3}, {10, 2, 4}, {30, 3, 9}, {92, 0, 1},
	}
	for _, u := range usage {
		exec(`INSERT INTO subscription_usage_daily (tenant_id, subscription_id, day, bytes)
		      VALUES ($1, $2, current_date - $3::int, $4)`, tenant, activitySub(tenant, u.sub), u.ago, u.bytes)
	}
	exec(`INSERT INTO subscription_usage_daily (tenant_id, subscription_id, day, bytes)
	      VALUES ($1, $2, current_date - 1, 99)`, other, activitySub(other, 1))
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
