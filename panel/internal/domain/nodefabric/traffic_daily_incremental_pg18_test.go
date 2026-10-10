package nodefabric

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// w12period 的 PG18 用例，挂在 TestTrafficChargePG18 里跑（同一个套了 configure-app-role 的库）。
// 夹具前缀 7a5e12…。

// legacyTrafficDailyPendingSQL 是改成增量之前每 10 分钟全量重扫 70 天的那条语句（原文），留在这里
// 当对照：增量进度机器必须写出与它逐行相同的按天表。
const legacyTrafficDailyPendingSQL = `
SELECT (s.day_start AT TIME ZONE 'UTC')::date
  FROM generate_series(
         date_trunc('day', now() - make_interval(days => $2), 'UTC') + interval '24 hours',
         date_trunc('day', now() - interval '24 hours 10 minutes', 'UTC'),
         interval '24 hours') AS s(day_start)
 WHERE NOT EXISTS (
         SELECT 1 FROM node_user_traffic_daily d
          WHERE d.tenant_id = $1 AND d.day = (s.day_start AT TIME ZONE 'UTC')::date)
   AND EXISTS (
         SELECT 1 FROM node_user_traffic_hourly h
          WHERE h.tenant_id = $1 AND h.hour_start >= s.day_start
            AND h.hour_start < s.day_start + interval '24 hours')
 ORDER BY s.day_start DESC
 LIMIT $3`

// trafficDailyIncrementalScenario 证明按天汇总的增量进度机器（traffic_daily_roller.go）：
//   - 同样的小时数据，在租户 X 上走增量、在租户 Y 上走原来的全量重扫，两边的按天表逐行相同
//     （含：有空洞的日子、日界上的 0 点与 23 点小时、窗口外的日子不汇总）；
//   - 增量也等于直接按天聚合小时表的结果（全量重算）；
//   - 稳态下的轮次不再核对库；进程重启（新进度）补洞后不改变任何行；
//   - 跨过 UTC 日界后新结束的一天被汇总，已有的行不变。
func trafficDailyIncrementalScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	const (
		tenantX = "7a5e1201-0000-7000-8000-000000000001"
		tenantY = "7a5e1202-0000-7000-8000-000000000001"
		nodeX1  = "7a5e1201-0000-7000-8000-000000000031"
		nodeX2  = "7a5e1201-0000-7000-8000-000000000032"
		nodeY1  = "7a5e1202-0000-7000-8000-000000000031"
		nodeY2  = "7a5e1202-0000-7000-8000-000000000032"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'daily-incr-x-pg18', 'Daily Incremental X', 'CNY'),
		       ($2, 'daily-incr-y-pg18', 'Daily Incremental Y', 'CNY')`, tenantX, tenantY)
	must(`INSERT INTO nodes (id, tenant_id, name, status)
		VALUES ($1, $5, 'daily-a', 'active'), ($2, $5, 'daily-b', 'active'),
		       ($3, $6, 'daily-a', 'active'), ($4, $6, 'daily-b', 'active')`,
		nodeX1, nodeX2, nodeY1, nodeY2, tenantX, tenantY)

	// 夹具：距今 ago 天（UTC 自然日）的若干小时。第 3、6 天故意没有数据（空洞）；
	// 0 点与 23 点的小时检验日界；69 天不放（新旧窗口在这一天的取舍不同），72 天在窗口外。
	// billed_bytes 每行都有数（00164 起非空），按天汇总直接相加。
	type hourRow struct {
		ago, hour int
		node      int // 1 或 2
		uid       int64
		billed    string // 非空才有计费字节；空串不再表示未知
	}
	var rows []hourRow
	for _, ago := range []int{1, 2, 4, 5, 7, 10, 20, 40, 65, 68, 72} {
		for _, h := range []int{0, 13, 23} {
			rows = append(rows, hourRow{ago, h, 1, 8120001, "1"}, hourRow{ago, h, 2, 8120002, "1"})
		}
		rows = append(rows, hourRow{ago, 13, 1, 8120003, "1"})
	}
	seed := func(tenant, n1, n2 string) {
		t.Helper()
		var vals []string
		for _, r := range rows {
			node := n1
			if r.node == 2 {
				node = n2
			}
			if r.billed == "" {
				t.Fatal("hourly fixture must set billed_bytes")
			}
			billed := fmt.Sprintf("%d", int64(r.ago)*1000+int64(r.hour)*10+r.uid%7)
			up, down := int64(r.ago)*100+int64(r.hour)+r.uid%13, int64(r.ago)*7+int64(r.hour)*3+r.uid%11
			vals = append(vals, fmt.Sprintf("(%d, %d, '%s'::uuid, %d::bigint, %d::numeric, %d::numeric, %d::bigint, %s::numeric)",
				r.ago, r.hour, node, r.uid, up, down, r.ago%5+1, billed))
		}
		must(`INSERT INTO node_user_traffic_hourly (tenant_id, hour_start, node_id, node_uid,
				upload_bytes, download_bytes, entry_count, last_report_at, billed_bytes)
			SELECT $1::uuid, w.hs, w.node, w.uid, w.up, w.down, w.n, w.hs + interval '5 minutes', w.billed
			  FROM (SELECT v.*, date_trunc('day', now(), 'UTC') - v.ago * interval '24 hours'
			               + v.hr * interval '1 hour' AS hs
			          FROM (VALUES `+strings.Join(vals, ",")+`) AS v(ago, hr, node, uid, up, down, n, billed)) w`, tenant)
	}
	seed(tenantX, nodeX1, nodeX2)
	seed(tenantY, nodeY1, nodeY2)

	// 原来的做法：每轮重扫，直到没有要补的日子（每轮至多三天）
	legacyRoll := func(tenant string) {
		t.Helper()
		for round := 0; round < 40; round++ {
			var days []time.Time
			if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
				rs, err := tx.Query(ctx, legacyTrafficDailyPendingSQL, tenant, UserTrafficHourlyRetentionDays, trafficDailyMaxDaysPerRun)
				if err != nil {
					return err
				}
				days, err = pgx.CollectRows(rs, pgx.RowTo[time.Time])
				return err
			}); err != nil {
				t.Fatalf("legacy pending: %v", err)
			}
			if len(days) == 0 {
				return
			}
			for _, d := range days {
				if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, trafficDailyRollupSQL, tenant, d)
					return err
				}); err != nil {
					t.Fatalf("legacy rollup %v: %v", d, err)
				}
			}
		}
		t.Fatal("legacy roll did not converge")
	}
	legacyRoll(tenantY)

	// 增量：一个新进程的进度（第一轮补洞），每轮至多三天
	svc := NewService(app, nil)
	rounds, written := 0, int64(0)
	for ; rounds < 40; rounds++ {
		n, err := svc.RefreshTrafficDaily(ctx, tenantX)
		if err != nil {
			t.Fatalf("RefreshTrafficDaily round %d: %v", rounds, err)
		}
		if n == 0 {
			break
		}
		written += n
		if rounds == 0 {
			var days int
			if err := admin.QueryRow(ctx, `SELECT count(DISTINCT day) FROM node_user_traffic_daily WHERE tenant_id = $1`, tenantX).Scan(&days); err != nil || days != trafficDailyMaxDaysPerRun {
				t.Fatalf("first round rolled %d days (err=%v), want exactly %d (newest first)", days, err, trafficDailyMaxDaysPerRun)
			}
		}
	}
	if rounds < 2 || written == 0 {
		t.Fatalf("incremental roll finished in %d rounds with %d rows, want a multi-round backfill", rounds, written)
	}
	if svc.trafficDaily.checks != 1 {
		t.Fatalf("incremental roll checked the database %d times, want once (startup)", svc.trafficDaily.checks)
	}

	// 每一边读出来的按天表（节点用名字对齐），两边逐行比较
	const dailyRows = `
		SELECT d.day, n.name, d.node_uid, d.upload_bytes::text, d.download_bytes::text,
		       coalesce(d.billed_bytes::text, 'NULL'), d.entry_count, d.last_report_at
		  FROM node_user_traffic_daily d JOIN nodes n ON n.id = d.node_id
		 WHERE d.tenant_id = `
	diff := func(a, b string) (onlyA, onlyB int) {
		t.Helper()
		if err := admin.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM (`+dailyRows+`$1 EXCEPT `+dailyRows+`$2) x),
			       (SELECT count(*) FROM (`+dailyRows+`$2 EXCEPT `+dailyRows+`$1) x)`, a, b).Scan(&onlyA, &onlyB); err != nil {
			t.Fatal(err)
		}
		return onlyA, onlyB
	}
	var total int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM node_user_traffic_daily WHERE tenant_id = $1`, tenantX).Scan(&total); err != nil || total < 20 {
		t.Fatalf("incremental daily rows = %d (err=%v), want a real backfill", total, err)
	}
	if x, y := diff(tenantX, tenantY); x != 0 || y != 0 {
		t.Fatalf("incremental vs legacy full rescan differ: only-incremental=%d only-legacy=%d rows", x, y)
	}

	// 等于直接按天聚合小时表（全量重算）：窗口内每个有数据的已结束日
	reference := func(tenant string, first, last time.Time) {
		t.Helper()
		var onlyRef, onlyGot int
		const agg = `
			SELECT (h.hour_start AT TIME ZONE 'UTC')::date AS day, n.name, h.node_uid,
			       sum(h.upload_bytes)::text, sum(h.download_bytes)::text,
			       coalesce(sum(h.billed_bytes)::text, 'NULL'),
			       sum(h.entry_count)::bigint, max(h.last_report_at)
			  FROM node_user_traffic_hourly h JOIN nodes n ON n.id = h.node_id
			 WHERE h.tenant_id = $1
			   AND (h.hour_start AT TIME ZONE 'UTC')::date BETWEEN $2::date AND $3::date
			 GROUP BY 1, 2, 3`
		if err := admin.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM (`+agg+` EXCEPT `+dailyRows+`$1) x),
			       (SELECT count(*) FROM (`+dailyRows+`$1 EXCEPT `+agg+`) x)`, tenant, first, last).Scan(&onlyRef, &onlyGot); err != nil {
			t.Fatal(err)
		}
		if onlyRef != 0 || onlyGot != 0 {
			t.Fatalf("daily table vs full recompute differ: only-recompute=%d only-table=%d rows", onlyRef, onlyGot)
		}
	}
	first, last := trafficDailyWindow(time.Now())
	reference(tenantX, first, last)
	var holes, outside int
	if err := admin.QueryRow(ctx, `
		SELECT count(DISTINCT day) FILTER (WHERE day = (now() AT TIME ZONE 'UTC')::date - 3 OR day = (now() AT TIME ZONE 'UTC')::date - 6),
		       count(*) FILTER (WHERE day < $2::date)
		  FROM node_user_traffic_daily WHERE tenant_id = $1`, tenantX, first).Scan(&holes, &outside); err != nil || holes != 0 || outside != 0 {
		t.Fatalf("days without data / outside the window were written: holes=%d outside=%d err=%v", holes, outside, err)
	}

	// 稳态：不再核对库，也不写
	for i := 0; i < 5; i++ {
		if n, err := svc.RefreshTrafficDaily(ctx, tenantX); err != nil || n != 0 {
			t.Fatalf("idle round %d: wrote %d err=%v", i, n, err)
		}
	}
	if svc.trafficDaily.checks != 1 {
		t.Fatalf("idle rounds checked the database (checks=%d)", svc.trafficDaily.checks)
	}

	// 进程重启：新进度补洞一次，库里什么也不变
	restarted := NewService(app, nil)
	if n, err := restarted.RefreshTrafficDaily(ctx, tenantX); err != nil || n != 0 || restarted.trafficDaily.checks != 1 {
		t.Fatalf("restarted roller: wrote %d err=%v checks=%d, want one check and no writes", n, err, restarted.trafficDaily.checks)
	}
	if x, y := diff(tenantX, tenantY); x != 0 || y != 0 {
		t.Fatalf("after restart incremental vs legacy differ: %d/%d", x, y)
	}

	// 跨过 UTC 日界：今天的小时数据在日界之后（时钟走到明天 00:10 以后）才汇总；之前的行不变
	must(`INSERT INTO node_user_traffic_hourly (tenant_id, hour_start, node_id, node_uid,
			upload_bytes, download_bytes, entry_count, last_report_at, billed_bytes)
		SELECT $1, date_trunc('day', now(), 'UTC') + h * interval '1 hour', $2, 8120001, 10 + h, 20 + h, 1,
		       date_trunc('day', now(), 'UTC') + h * interval '1 hour' + interval '3 minutes', h
		  FROM generate_series(0, 2) h`, tenantX, nodeX1)
	tomorrow := utcDay(time.Now()).Add(24 * time.Hour)
	// 日界前 1 分钟：时钟走到明天 00:09，今天还没结束满 10 分钟。（若测试恰好跑在 UTC 零点后 10 分钟内，
	// 真实时钟的昨天还没汇总，此刻会先补昨天，所以只在两个时钟的"最后一个已结束日"相同时断言零写入。）
	early := tomorrow.Add(9 * time.Minute)
	svc.trafficDaily.now = func() time.Time { return early }
	sameLast := utcDay(early.Add(-(24*time.Hour + trafficDailyEndMargin))).Equal(
		utcDay(time.Now().Add(-(24*time.Hour + trafficDailyEndMargin))))
	if n, err := svc.RefreshTrafficDaily(ctx, tenantX); err != nil || (sameLast && n != 0) {
		t.Fatalf("before the day boundary (+9min): wrote %d err=%v, want nothing", n, err)
	}
	checksBefore := svc.trafficDaily.checks
	svc.trafficDaily.now = func() time.Time { return tomorrow.Add(11 * time.Minute) }
	if n, err := svc.RefreshTrafficDaily(ctx, tenantX); err != nil || n != 1 || svc.trafficDaily.checks != checksBefore+1 {
		t.Fatalf("after the day boundary: wrote %d err=%v checks=%d (was %d), want today's single row after one more check",
			n, err, svc.trafficDaily.checks, checksBefore)
	}
	// 窗口左缘随时钟前移，已汇总的最老几天要等保留期清理才删；对照仍从原来的左缘起
	_, last = trafficDailyWindow(tomorrow.Add(11 * time.Minute))
	reference(tenantX, first, last)
	t.Log("marker=w12period_pg18_traffic_daily_incremental_ok")
}
