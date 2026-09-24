// [INPUT]: 依赖 platform/pg18test 打开 notify 域的一次性库，依赖 scan.go 的 ScanQuota
// [OUTPUT]: 对外提供 TestScanQuotaCountsTrafficPacksPG18
// [POS]: domain/notify 的 PG18 测试：流量预警把用户流量包剩余算进可用量，有余量的用户不再收到「流量即将用尽」
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package notify

import (
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
	must(`INSERT INTO notification_templates(tenant_id,code,channel,body,category,status)
		VALUES($1,'quota.warning','inapp','{{percent}} {{remaining}}','service','active')`, tenant)

	// 每个用户一条生效订阅、本期套餐额度 100 字节；pack 为 [授予, 已用]，nil 表示没买
	users := []struct {
		key      string
		consumed int64
		pack     []int64
	}{
		{"a", 85, nil},              // 85%：预警
		{"b", 85, []int64{100, 0}},  // 85 / 200：有余量，不预警
		{"c", 85, []int64{50, 50}},  // 流量包用光：85%，预警
		{"d", 100, []int64{40, 30}}, // 100 / 110 ≈ 91%：落在 80 档，剩余 10 字节
	}
	subs := map[string]string{}
	for i, u := range users {
		user := prefix + "1" + string(rune('0'+i))
		sub := prefix + "2" + string(rune('0'+i))
		subs[sub] = u.key
		must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,$3,'active')`,
			user, tenant, u.key+"@notify-scan.invalid")
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
			must(`INSERT INTO traffic_pack_grants(tenant_id,user_id,source,source_id,granted_bytes,consumed_bytes)
				VALUES($1,$2,'migration',gen_random_uuid(),$3,$4)`, tenant, user, u.pack[0], u.pack[1])
		}
	}

	svc := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("notify-scan-salt"))
	queued, err := svc.ScanQuota(ctx, tenant)
	if err != nil || queued != 3 {
		t.Fatalf("ScanQuota queued=%d err=%v, want 3", queued, err)
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
		// 键形如 quota:<订阅>:80:inapp —— 三条都应落在 80 档
		sub, ok := strings.CutPrefix(d.key, "quota:")
		if sub, ok = strings.CutSuffix(sub, ":80:inapp"); !ok || subs[sub] == "" {
			t.Fatalf("unexpected dedupe key %q", d.key)
		}
		warned = append(warned, subs[sub])
		remaining[subs[sub]] = d.remaining
	}
	sort.Strings(warned)
	if len(warned) != 3 || warned[0] != "a" || warned[1] != "c" || warned[2] != "d" {
		t.Fatalf("warned users=%v, want [a c d]", warned)
	}
	if remaining["a"] != "15 B" || remaining["c"] != "15 B" || remaining["d"] != "10 B" {
		t.Fatalf("remaining=%v", remaining)
	}

	// 同一轮再扫：去重键挡住，不重复入队
	if _, err := svc.ScanQuota(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE tenant_id=$1`, tenant).Scan(&total); err != nil || total != 3 {
		t.Fatalf("deliveries after rescan=%d err=%v", total, err)
	}
}
