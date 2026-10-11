package notify

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestScanQuotaCountsTrafficPacksPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "NOTIFY", DatabasePrefix: "pandora_notify_",
		MarkerTable: "pandora_notify_test_marker", CommentTag: "pandora-notify-pg18",
	})
	const (
		tenant  = "86000000-0000-4000-8000-000000000001"
		product = "86000000-0000-4000-8000-000000000002"
		plan    = "86000000-0000-4000-8000-000000000003"
		prefix  = "86000000-0000-4000-8000-0000000000"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'notify-scan-pg18','Notify','CNY')`, tenant)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'notify-product','Notify Product','active')`, tenant, product)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'notify-plan','Notify Plan','draft')`, tenant, product, plan)
	// 模板由建租户触发器种下：只留站内信一个渠道（本测试数的是流量包口径，
	// 不是渠道），并换成只含两个变量的正文，便于断言渲染结果
	must(`DELETE FROM notification_templates
		WHERE tenant_id=$1 AND code='quota.warning' AND channel<>'inapp'`, tenant)
	must(`UPDATE notification_templates SET body='{{percent}} {{remaining}}'
		WHERE tenant_id=$1 AND code='quota.warning' AND channel='inapp'`, tenant)

	// 每个用户一条生效订阅、本期套餐额度 100 字节；pack 为 [授予, 已用]，nil 表示没买。
	// 流量包按份挂（购买模型统一）：unattached 的那笔没加到任何一份，不算这份的余量
	users := []struct {
		key        string
		consumed   int64
		pack       []int64
		unattached bool
	}{
		{"a", 85, nil, false},              // 85%：预警
		{"b", 85, []int64{100, 0}, false},  // 85 / 200：有余量，不预警
		{"c", 85, []int64{50, 50}, false},  // 流量包用光：85%，预警
		{"d", 100, []int64{40, 30}, false}, // 100 / 110 ≈ 91%：落在 80 档，剩余 10 字节
		{"e", 85, []int64{100, 0}, true},   // 包没挂在这份上：85%，预警
	}
	subs := map[string]string{}
	for i, u := range users {
		user := prefix + "1" + string(rune('0'+i))
		sub := prefix + "2" + string(rune('0'+i))
		subs[sub] = u.key
		must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,$4,'active')`,
			user, tenant, u.key+"@notify-scan.invalid", u.key)
		// 订阅背后的版本与本测试无关：关掉触发器（含外键）直接插一条生效订阅
		must(`SET session_replication_role = replica`)
		must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,
				snapshot_currency,snapshot_amount,current_period_end)
			VALUES($1,$2,$3,$4,gen_random_uuid(),'active','CNY',0,now() + interval '30 days')`, sub, tenant, user, plan)
		must(`SET session_replication_role = origin`)
		must(`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,
				granted,limit_value,consumed)
			VALUES($1,$2,'traffic.bytes','cycle',now() - interval '1 day',now() + interval '30 days',100,100,$3)`,
			tenant, sub, u.consumed)
		if u.pack != nil {
			attachTo := &sub
			if u.unattached {
				attachTo = nil
			}
			must(`INSERT INTO traffic_pack_grants(tenant_id,user_id,subscription_id,source,source_id,granted_bytes,consumed_bytes)
				VALUES($1,$2,$5,'migration',gen_random_uuid(),$3,$4)`, tenant, user, u.pack[0], u.pack[1], attachTo)
		}
	}

	svc := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("notify-scan-salt"))
	queued, err := svc.ScanQuota(ctx, tenant)
	if err != nil || queued != 4 {
		t.Fatalf("ScanQuota queued=%d err=%v, want 4", queued, err)
	}
	rows, err := admin.Query(ctx, `SELECT dedupe_key, payload->>'remaining' FROM notification_deliveries
		WHERE tenant_id=$1 AND template_code='quota.warning' ORDER BY dedupe_key`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	type delivery struct{ key, remaining string }
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (delivery, error) {
		var d delivery
		return d, r.Scan(&d.key, &d.remaining)
	})
	if err != nil {
		t.Fatal(err)
	}
	var warned []string
	remaining := map[string]string{}
	for _, d := range got {
		// 键形如 quota:<订阅>:80:<周期起点秒>:inapp —— 四条都应落在 80 档
		parts := strings.Split(d.key, ":")
		if len(parts) != 5 || parts[0] != "quota" || parts[2] != "80" || parts[4] != "inapp" || subs[parts[1]] == "" {
			t.Fatalf("unexpected dedupe key %q", d.key)
		}
		sub := parts[1]
		warned = append(warned, subs[sub])
		remaining[subs[sub]] = d.remaining
	}
	sort.Strings(warned)
	if len(warned) != 4 || warned[0] != "a" || warned[1] != "c" || warned[2] != "d" || warned[3] != "e" {
		t.Fatalf("warned users=%v, want [a c d e]", warned)
	}
	if remaining["a"] != "15 B" || remaining["c"] != "15 B" || remaining["d"] != "10 B" || remaining["e"] != "15 B" {
		t.Fatalf("remaining=%v", remaining)
	}

	// 同一轮再扫：去重键挡住，不重复入队
	if _, err := svc.ScanQuota(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE tenant_id=$1`, tenant).Scan(&total); err != nil || total != 4 {
		t.Fatalf("deliveries after rescan=%d err=%v", total, err)
	}
}

// 三个扫描的返回值是「实际新排的行数」：2 小时窗口、到期区间、预警档位里
// 每轮都会扫到同一批数据，撞了去重键的不算，第二遍必须是 0（冒烟查出日志
// 每轮都报「已排队 1 条」）。
func TestScanCountsOnlyInsertedRowsPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "NOTIFY", DatabasePrefix: "pandora_notify_",
		MarkerTable: "pandora_notify_test_marker", CommentTag: "pandora-notify-pg18",
	})
	const (
		tenant  = "8e000000-0000-4000-8000-000000000001"
		product = "8e000000-0000-4000-8000-000000000002"
		plan    = "8e000000-0000-4000-8000-000000000003"
		user    = "8e000000-0000-4000-8000-000000000011"
		sub     = "8e000000-0000-4000-8000-000000000021"
		order   = "8e000000-0000-4000-8000-000000000031"
	)
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenant + `','notify-rescan-pg18','Rescan','CNY')`,
		`SET LOCAL session_replication_role = replica`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + user + `','` + tenant + `','u@notify-rescan.invalid','U','active')`,
		`INSERT INTO products(id,tenant_id,code,name,status) VALUES('` + product + `','` + tenant + `','rescan-product','Rescan','active')`,
		`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES('` + plan + `','` + tenant + `','` + product + `','rescan-plan','Rescan Plan','draft')`,
		// 5 天后到期、不自动续费：落在 7 天区间
		`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
			current_period_start,current_period_end,auto_renew)
		 VALUES('` + sub + `','` + tenant + `','` + user + `','` + plan + `',gen_random_uuid(),'active','CNY',0,
			now()-interval '25 days',now()+interval '5 days',false)`,
		// 用了 85%：落在 80 档
		`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed)
		 VALUES('` + tenant + `','` + sub + `','traffic.bytes','cycle',now()-interval '1 day',now()+interval '5 days',100,100,85)`,
		// 刚履约的订单：落在 2 小时窗口
		`INSERT INTO orders(id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,discount_amount,tax_amount,
			total_amount,balance_applied,payable_amount,paid_amount,paid_at,fulfilled_at,subscription_id,expires_at,business_request_id)
		 VALUES('` + order + `','` + tenant + `','RESCAN-1','` + user + `','new','fulfilled','CNY',100,0,0,100,0,100,100,
			now(),now(),'` + sub + `',now(),gen_random_uuid())`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	svc := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("notify-rescan-salt"))
	scans := []struct {
		code string
		scan func(context.Context, string) (int, error)
	}{
		{"subscription.expiring", svc.ScanExpiring},
		{"quota.warning", svc.ScanQuota},
		{"order.paid", svc.ScanPaidOrders},
	}
	rowsOf := func(code string) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries
			WHERE tenant_id=$1 AND template_code=$2`, tenant, code).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, sc := range scans {
		// 第一遍：返回值等于这个模板真正落库的行数（每个渠道一行）
		first, err := sc.scan(ctx, tenant)
		if err != nil || first == 0 || first != rowsOf(sc.code) {
			t.Fatalf("%s first scan=%d rows=%d err=%v", sc.code, first, rowsOf(sc.code), err)
		}
		// 第二遍：全部撞去重键，返回 0、不多一行
		second, err := sc.scan(ctx, tenant)
		if err != nil || second != 0 || rowsOf(sc.code) != first {
			t.Fatalf("%s rescan=%d rows=%d err=%v, want 0 and %d rows", sc.code, second, rowsOf(sc.code), err, first)
		}
	}
	t.Log("marker=notify_rescan_counts_zero_ok")
}

// TestScanExpiryNoticesPG18 钉住到期类通知（w5expiry，用户 2026-10-07）：
//   - 7/3/1 天到期提醒不再被 auto_renew 挡掉（这一列默认 true、没人改过）；
//   - 到期当时一条 subscription.expired，过期第 1 天、第 7 天各一条召回；
//   - 名下另有在用订阅的用户不召回，原地续费窗口已关的不召回；
//   - 时刻按用户时区显示到分钟；重扫不重复。
func TestScanExpiryNoticesPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "NOTIFY", DatabasePrefix: "pandora_notify_",
		MarkerTable: "pandora_notify_test_marker", CommentTag: "pandora-notify-pg18",
	})
	const (
		tenant  = "e7e10000-0000-4000-8000-000000000001"
		product = "e7e10000-0000-4000-8000-000000000002"
		plan    = "e7e10000-0000-4000-8000-000000000003"
		prefix  = "e7e10000-0000-4000-8000-0000000001"
	)
	// 每个用户一条订阅：状态、周期末相对现在的偏移、是否关窗；other 另给一条在用订阅
	cases := []struct {
		key, status, end string
		closed, other    bool
	}{
		{"expiring", "active", "now() + interval '5 days'", false, false}, // 7 天档提醒
		{"justexpired", "expired", "now() - interval '2 hours'", false, false},
		{"day1", "expired", "now() - interval '3 days'", false, false},
		{"day7", "expired", "now() - interval '10 days'", false, false},
		{"switched", "expired", "now() - interval '3 days'", false, true}, // 已换了别的在用订阅
		{"closed", "expired", "now() - interval '40 days'", true, false},
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency,timezone) VALUES('` + tenant + `','notify-expiry-pg18','Expiry','CNY','Asia/Shanghai')`,
		`SET LOCAL session_replication_role = replica`,
		`INSERT INTO products(id,tenant_id,code,name,status) VALUES('` + product + `','` + tenant + `','expiry-product','Expiry','active')`,
		`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES('` + plan + `','` + tenant + `','` + product + `','expiry-plan','Expiry Plan','draft')`,
	}
	for i, c := range cases {
		user := prefix + "1" + string(rune('0'+i))
		sub := prefix + "2" + string(rune('0'+i))
		closed := "NULL"
		if c.closed {
			closed = "now()"
		}
		seed = append(seed,
			`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+user+`','`+tenant+`','`+c.key+`@notify-expiry.invalid','`+c.key+`','active')`,
			`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
				current_period_start,current_period_end,renewal_closed_at)
			 VALUES('`+sub+`','`+tenant+`','`+user+`','`+plan+`',gen_random_uuid(),'`+c.status+`','CNY',0,
				`+c.end+` - interval '30 days',`+c.end+`,`+closed+`)`)
		if c.other {
			seed = append(seed, `INSERT INTO subscriptions(tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,
				snapshot_amount,current_period_start,current_period_end)
			 VALUES('`+tenant+`','`+user+`','`+plan+`',gen_random_uuid(),'active','CNY',0,now(),now()+interval '30 days')`)
		}
	}
	for _, sql := range seed {
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	svc := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("notify-expiry-salt"))
	first, err := svc.ScanExpiring(ctx, tenant)
	if err != nil || first == 0 {
		t.Fatalf("expiry scan queued=%d err=%v", first, err)
	}
	rows, err := admin.Query(ctx, `SELECT d.template_code, u.display_name, d.dedupe_key, d.payload
		FROM notification_deliveries d JOIN users u ON u.id = d.user_id
		WHERE d.tenant_id=$1 AND d.channel='inapp' ORDER BY u.display_name`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	type delivery struct {
		code, user, key string
		payload         map[string]any
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (delivery, error) {
		var d delivery
		return d, r.Scan(&d.code, &d.user, &d.key, &d.payload)
	})
	if err != nil {
		t.Fatal(err)
	}
	byUser := map[string]delivery{}
	for _, d := range got {
		if _, dup := byUser[d.user]; dup {
			t.Fatalf("user %s got more than one in-app notice: %+v", d.user, got)
		}
		byUser[d.user] = d
	}
	want := map[string]string{
		"expiring": "subscription.expiring", "justexpired": "subscription.expired",
		"day1": "subscription.recall", "day7": "subscription.recall",
	}
	if len(byUser) != len(want) {
		t.Fatalf("in-app notices=%+v want users %v", got, want)
	}
	for user, code := range want {
		d, ok := byUser[user]
		if !ok || d.code != code {
			t.Fatalf("user %s notice=%+v want %s", user, d, code)
		}
	}
	// 站点时区 Asia/Shanghai，到分钟：形如 2026-10-07 14:32
	at, _ := byUser["justexpired"].payload["expired_at"].(string)
	if len(at) != len("2006-01-02 15:04") || byUser["day1"].payload["days"] != "1" || byUser["day7"].payload["days"] != "7" ||
		!strings.HasPrefix(byUser["day7"].key, "recall:") || !strings.Contains(byUser["day7"].key, ":7d:") {
		t.Fatalf("notice payloads=%+v", byUser)
	}
	if at, _ := byUser["expiring"].payload["expires_at"].(string); len(at) != len("2006-01-02 15:04") {
		t.Fatalf("expiring reminder time=%q, want minutes", at)
	}
	if again, err := svc.ScanExpiring(ctx, tenant); err != nil || again != 0 {
		t.Fatalf("rescan queued=%d err=%v, want 0", again, err)
	}
	t.Log("marker=notify_expiry_notices_ok")
}
