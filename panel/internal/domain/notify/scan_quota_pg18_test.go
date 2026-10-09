package notify

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// legacyQuotaScanSQL 是改成一遍扫描之前每档阈值各扫一遍的那条语句（原文），留作对照。
const legacyQuotaScanSQL = `
	WITH usage AS (
		SELECT q.subscription_id, s.user_id, p.name, q.consumed,
		       COALESCE(q.granted,0) + COALESCE(q.granted_addon,0) AS plan_total,
		       COALESCE(q.granted,0) + COALESCE(q.granted_addon,0) + pk.pack_left AS total
		  FROM quota_balances q
		  JOIN subscriptions s ON s.id = q.subscription_id
		  JOIN plans p ON p.id = s.plan_id
		 CROSS JOIN LATERAL (
		       SELECT COALESCE(sum(g.granted_bytes - g.consumed_bytes), 0)::bigint AS pack_left
		         FROM traffic_pack_grants g
		        WHERE g.tenant_id = q.tenant_id AND g.subscription_id = q.subscription_id
		          AND g.consumed_bytes < g.granted_bytes) pk
		 WHERE q.tenant_id = $1
		   AND q.metric = 'traffic.bytes'
		   AND s.status IN ('active','trialing'))
	SELECT subscription_id::text, user_id::text, name, consumed, total
	  FROM usage
	 WHERE plan_total > 0
	   AND consumed * 100 >= total * $2
	   AND ($2 = 95 OR consumed * 100 < total * 95)`

// TestScanQuotaOnePassMatchesPerThresholdScansPG18 证明阈值扫描合成一遍（w12period）之后口径不变：
// 每条订阅只落在它已跨过的最高一档，结果与原来每档各扫一遍逐条相同（含流量包余量、套餐额度为 0、
// 订阅不在用、恰好压线的边界），96% 的订阅只有 95 档一条通知，没有 80 档。
func TestScanQuotaOnePassMatchesPerThresholdScansPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "NOTIFY", DatabasePrefix: "pandora_notify_",
		MarkerTable: "pandora_notify_test_marker", CommentTag: "pandora-notify-pg18",
	})
	const (
		tenant  = "7a5e1203-0000-4000-8000-000000000001"
		product = "7a5e1203-0000-4000-8000-000000000002"
		plan    = "7a5e1203-0000-4000-8000-000000000003"
		prefix  = "7a5e1203-0000-4000-8000-0000000000"
	)
	// 套餐额度 granted；pack 为 [授予, 已用]；status 缺省 active；want 是期望的档位，0 表示不预警
	cases := []struct {
		name     string
		granted  int64
		consumed int64
		pack     []int64
		status   string
		want     int
	}{
		{"below", 1000, 799, nil, "", 0},
		{"exactly-80", 1000, 800, nil, "", 80},
		{"just-below-95", 1000, 949, nil, "", 80},
		{"exactly-95", 1000, 950, nil, "", 95},
		{"96-percent", 1000, 960, nil, "", 95},
		{"full", 1000, 1000, nil, "", 95},
		{"pack-covers", 1000, 1500, []int64{1000, 0}, "", 0},         // 1500 / 2000 = 75%
		{"pack-nearly-out", 1000, 1500, []int64{1100, 1000}, "", 95}, // 余量 100，1500 / 1100
		{"pack-80", 1000, 1000, []int64{250, 0}, "", 80},             // 1000 / 1250 = 80%
		{"no-plan-quota", 0, 50, nil, "", 0},
		{"expired-subscription", 1000, 990, nil, "expired", 0},
		{"trialing", 1000, 900, nil, "trialing", 80},
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	exec(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'notify-quota-onepass-pg18','Quota One Pass','CNY')`, tenant)
	exec(`SET LOCAL session_replication_role = replica`)
	exec(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'qop-product','QOP','active')`, tenant, product)
	exec(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'qop-plan','QOP Plan','draft')`, tenant, product, plan)
	subs := map[string]int{} // 订阅 id → 期望档位
	names := map[string]string{}
	for i, c := range cases {
		user := fmt.Sprintf("%s%02x", prefix, 0x10+i)
		sub := fmt.Sprintf("%s%02x", prefix, 0x40+i)
		status := c.status
		if status == "" {
			status = "active"
		}
		exec(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,$4,'active')`,
			user, tenant, c.name+"@notify-quota-onepass.invalid", c.name)
		exec(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,
				snapshot_currency,snapshot_amount,current_period_end)
			VALUES($1,$2,$3,$4,gen_random_uuid(),$5,'CNY',0,now() + interval '30 days')`, sub, tenant, user, plan, status)
		exec(`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,
				granted,limit_value,consumed)
			VALUES($1,$2,'traffic.bytes','cycle',now() - interval '1 day',now() + interval '30 days',$3,$3,$4)`,
			tenant, sub, c.granted, c.consumed)
		if c.pack != nil {
			exec(`INSERT INTO traffic_pack_grants(tenant_id,user_id,subscription_id,source,source_id,granted_bytes,consumed_bytes)
				VALUES($1,$2,$5,'migration',gen_random_uuid(),$3,$4)`, tenant, user, c.pack[0], c.pack[1], sub)
		}
		subs[sub], names[sub] = c.want, c.name
	}
	// 站内信一个渠道就够数
	exec(`DELETE FROM notification_templates WHERE tenant_id=$1 AND code='quota.warning' AND channel<>'inapp'`, tenant)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	svc := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("notify-quota-onepass-salt"))

	// 对照：原来每档各扫一遍，与现在的一遍扫描
	legacy := map[string]int{}
	var onePass []quotaCrossing
	if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		for _, pct := range []int{80, 95} {
			rows, err := tx.Query(ctx, legacyQuotaScanSQL, tenant, pct)
			if err != nil {
				return err
			}
			for rows.Next() {
				var sub, user, plan string
				var consumed, total int64
				if err := rows.Scan(&sub, &user, &plan, &consumed, &total); err != nil {
					rows.Close()
					return err
				}
				if _, dup := legacy[sub]; dup {
					rows.Close()
					return fmt.Errorf("legacy scans put %s in two buckets", sub)
				}
				legacy[sub] = pct
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		var err error
		onePass, err = scanQuotaCrossings(ctx, tx, tenant)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, it := range onePass {
		got[it.subID] = it.pct
	}
	wantAlerts := 0
	for sub, want := range subs {
		if want > 0 {
			wantAlerts++
		}
		if legacy[sub] != want {
			t.Fatalf("fixture check: legacy per-threshold scans put %s in bucket %d, fixture says %d", names[sub], legacy[sub], want)
		}
		if got[sub] != want {
			t.Fatalf("%s: one-pass bucket %d, want %d (legacy %d)", names[sub], got[sub], want, legacy[sub])
		}
	}
	if len(got) != wantAlerts || len(onePass) != wantAlerts || wantAlerts < 6 {
		t.Fatalf("one-pass crossings=%d (%v), want %d distinct subscriptions", len(onePass), got, wantAlerts)
	}

	// 入队：每条跨线的订阅一条站内信，键里的档位就是最高一档；重扫不重复
	queued, err := svc.ScanQuota(ctx, tenant)
	if err != nil || queued != wantAlerts {
		t.Fatalf("ScanQuota queued=%d err=%v, want %d", queued, err, wantAlerts)
	}
	rows, err := admin.Query(ctx, `SELECT dedupe_key FROM notification_deliveries
		WHERE tenant_id=$1 AND template_code='quota.warning'`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != wantAlerts {
		t.Fatalf("deliveries=%d (%v), want %d", len(keys), keys, wantAlerts)
	}
	for _, key := range keys {
		parts := strings.Split(key, ":") // quota:<订阅>:<档>:inapp
		if len(parts) != 4 || fmt.Sprint(subs[parts[1]]) != parts[2] {
			t.Fatalf("delivery key %q does not match the fixture bucket %d", key, subs[parts[1]])
		}
	}
	if again, err := svc.ScanQuota(ctx, tenant); err != nil || again != 0 {
		t.Fatalf("rescan queued=%d err=%v, want 0", again, err)
	}
	t.Log("marker=notify_quota_one_pass_matches_legacy_ok")
}
