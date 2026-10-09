package notify

import (
	"io"
	"log/slog"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// TestScanQuotaWarnsAgainEachPeriodPG18 钉住流量预警的去重键带着配额行当期的起点：
//   - 同一期同一档内反复扫描只一条（站内信与插件钩子投递各一条）；
//   - 用量在同一期继续涨到下一档，再来一条；
//   - 配额滚进下一期（period_start 换了、已用量清零）后，再次涨到 80% 是新的一次提醒——
//     键不带周期的话，投递表（tenant_id, dedupe_key 唯一、行不清理）让每个订阅每一档一辈子只提醒一次；
//   - 已经过去的周期（订阅续费后留下的旧 cycle 行，已用量停在线上）不再被扫出来。
func TestScanQuotaWarnsAgainEachPeriodPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "NOTIFY", DatabasePrefix: "pandora_notify_",
		MarkerTable: "pandora_notify_test_marker", CommentTag: "pandora-notify-pg18",
	})
	const (
		tenant  = "7a5e1204-0000-4000-8000-000000000001"
		product = "7a5e1204-0000-4000-8000-000000000002"
		plan    = "7a5e1204-0000-4000-8000-000000000003"
		user    = "7a5e1204-0000-4000-8000-000000000011"
		sub     = "7a5e1204-0000-4000-8000-000000000021"
	)
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenant + `','notify-quota-period-pg18','Quota Period','CNY')`,
		`SET LOCAL session_replication_role = replica`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + user + `','` + tenant + `','u@notify-quota-period.invalid','U','active')`,
		`INSERT INTO products(id,tenant_id,code,name,status) VALUES('` + product + `','` + tenant + `','qpd-product','QPD','active')`,
		`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES('` + plan + `','` + tenant + `','` + product + `','qpd-plan','QPD Plan','draft')`,
		`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,current_period_end)
		 VALUES('` + sub + `','` + tenant + `','` + user + `','` + plan + `',gen_random_uuid(),'active','CNY',0,now()+interval '60 days')`,
		// 第一期：起点 20 天前，已用 85%
		`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed)
		 VALUES('` + tenant + `','` + sub + `','traffic.bytes','month',now()-interval '20 days',now()+interval '10 days',100,100,85)`,
		`DELETE FROM notification_templates WHERE tenant_id='` + tenant + `' AND code='quota.warning' AND channel<>'inapp'`,
		`INSERT INTO plugin_hooks(tenant_id,code,name,enabled,events,endpoint_url)
		 VALUES('` + tenant + `','qperiod','Quota Period',true,'{traffic.exhausted}','http://127.0.0.1:9/hook')`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	svc := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("notify-quota-period-salt"))

	rows := func() (notices, hooks int) {
		t.Helper()
		if err := admin.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM notification_deliveries WHERE tenant_id=$1 AND template_code='quota.warning'),
			       (SELECT count(*) FROM plugin_hook_deliveries WHERE tenant_id=$1 AND event='traffic.exhausted')`,
			tenant).Scan(&notices, &hooks); err != nil {
			t.Fatal(err)
		}
		return notices, hooks
	}
	scan := func(step string, wantQueued, wantNotices, wantHooks int) {
		t.Helper()
		n, err := svc.ScanQuota(ctx, tenant)
		if err != nil || n != wantQueued {
			t.Fatalf("%s: queued=%d err=%v, want %d", step, n, err, wantQueued)
		}
		if notices, hooks := rows(); notices != wantNotices || hooks != wantHooks {
			t.Fatalf("%s: notices=%d plugin deliveries=%d, want %d/%d", step, notices, hooks, wantNotices, wantHooks)
		}
	}
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\nSQL: %s", err, sql)
		}
	}

	// 第一期到 80%：一条；同一期重扫不再来
	scan("period 1 at 85%", 1, 1, 1)
	scan("period 1 rescan", 0, 1, 1)
	// 同一期涨到 95%：下一档，再来一条
	must(`UPDATE quota_balances SET consumed = 96 WHERE subscription_id = $1`, sub)
	scan("period 1 at 96%", 1, 2, 2)
	scan("period 1 at 96% rescan", 0, 2, 2)
	// 滚进下一期（滚动的写法：已用量清零、起点换成新周期）：没到线上不提醒
	must(`UPDATE quota_balances SET consumed = 0, period_start = now() - interval '1 hour',
		       period_end = now() + interval '30 days' WHERE subscription_id = $1`, sub)
	scan("period 2 fresh", 0, 2, 2)
	// 第二期再到 80%：新的一次提醒（站内信与插件各一条）；同一期重扫不重复
	must(`UPDATE quota_balances SET consumed = 85 WHERE subscription_id = $1`, sub)
	scan("period 2 at 85%", 1, 3, 3)
	scan("period 2 rescan", 0, 3, 3)
	// 第二期也到 95%：再一条
	must(`UPDATE quota_balances SET consumed = 99 WHERE subscription_id = $1`, sub)
	scan("period 2 at 99%", 1, 4, 4)

	// 已经过去的周期（续费后留下的旧 cycle 行，已用量停在线上）不被扫出来
	must(`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed)
		VALUES($1,$2,'traffic.bytes','cycle',now()-interval '90 days',now()-interval '60 days',100,100,99)`, tenant, sub)
	scan("expired period row", 0, 4, 4)
	t.Log("marker=notify_quota_warns_each_period_ok")
}
