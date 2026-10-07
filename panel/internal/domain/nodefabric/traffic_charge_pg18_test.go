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

// TestTrafficChargePG18 证明扣量顺序（D-E-1）：本周期先扣套餐额度，扣完再按
// 先到先扣扣流量包；流量包扣光后超出的部分记在套餐上（配额变负、停止下发）；
// 套餐额度的重置不动流量包；扣量写流量包余额不产生变更推送，只有新增一笔
// 余额才推（00070 的 zz_notify_traffic_pack_grants 只挂 INSERT）。
// 由 run-pg18-gates.sh 的 traffic_charge 域驱动。
func TestTrafficChargePG18(t *testing.T) {
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
		tenantID = "75000000-0000-7000-8000-000000000001"
		userID   = "75000000-0000-7000-8000-000000000011"
		subID    = "75000000-0000-7000-8000-000000000021"
		older    = "75000000-0000-7000-8000-000000000031"
		newer    = "75000000-0000-7000-8000-000000000032"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'traffic-charge-pg18', 'Traffic Charge PG18', 'CNY')`, tenantID)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $2, 'traffic-charge@example.test', 'Traffic Charge', 'active')`, userID, tenantID)
	// 订阅只是扣量的挂载点，它背后的套餐与版本与本测试无关：关掉触发器
	// （含外键）直接插一条生效订阅，配额行与流量包照常走约束。
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end)
		VALUES ($1, $2, $3, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0,
		        now() + interval '30 days')`, subID, tenantID, userID)
	must(`SET session_replication_role = origin`)
	must(`INSERT INTO quota_balances (tenant_id, subscription_id, metric, period,
			period_start, period_end, granted, limit_value)
		VALUES ($1, $2, 'traffic.bytes', 'cycle', now() - interval '1 day',
		        now() + interval '30 days', 100, 100)`, tenantID, subID)
	// 夹具连接自己也听变更通道：新增余额要推给用户，扣量不推（00070）。
	must(`LISTEN aegis_change`)
	must(`INSERT INTO traffic_pack_grants (id, tenant_id, user_id, source, source_id,
			granted_bytes, created_at)
		VALUES ($1, $3, $4, 'migration', gen_random_uuid(), 50, now() - interval '2 hours'),
		       ($2, $3, $4, 'migration', gen_random_uuid(), 30, now() - interval '1 hour')`,
		older, newer, tenantID, userID)

	grantNotices := func() (n int) {
		t.Helper()
		// 通知在提交后投递；夹具连接执行一条空语句把积压的通知收进来，再逐条非阻塞读。
		must(`SELECT 1`)
		for {
			waitCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
			note, err := admin.WaitForNotification(waitCtx)
			stop()
			if err != nil {
				if !pgconn.Timeout(err) || ctx.Err() != nil {
					t.Fatalf("wait for notifications: %v", err)
				}
				return n
			}
			var payload struct {
				Table string `json:"tbl"`
			}
			if err := json.Unmarshal([]byte(note.Payload), &payload); err != nil {
				t.Fatalf("decode change notice %q: %v", note.Payload, err)
			}
			if payload.Table == "traffic_pack_grants" {
				n++
			}
		}
	}
	if n := grantNotices(); n != 2 {
		t.Fatalf("new traffic pack grants sent %d change notices, want 2", n)
	}

	charge := func(billed int64) {
		t.Helper()
		if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			return chargeTraffic(ctx, tx, tenantID, subID, userID, billed)
		}); err != nil {
			t.Fatalf("charge %d: %v", billed, err)
		}
	}
	state := func() (plan, packOld, packNew int64) {
		t.Helper()
		if err := admin.QueryRow(ctx, `
			SELECT (SELECT consumed FROM quota_balances WHERE subscription_id = $1),
			       (SELECT consumed_bytes FROM traffic_pack_grants WHERE id = $2),
			       (SELECT consumed_bytes FROM traffic_pack_grants WHERE id = $3)`,
			subID, older, newer).Scan(&plan, &packOld, &packNew); err != nil {
			t.Fatalf("read charge state: %v", err)
		}
		return
	}
	expect := func(step string, plan, packOld, packNew int64) {
		t.Helper()
		p, o, n := state()
		if p != plan || o != packOld || n != packNew {
			t.Fatalf("%s: plan=%d packs=%d/%d want %d %d/%d", step, p, o, n, plan, packOld, packNew)
		}
	}

	charge(80)
	expect("within plan quota", 80, 0, 0)
	charge(40)
	expect("overflow spills into the oldest pack", 100, 20, 0)
	charge(45)
	expect("oldest pack drains before the newer one", 100, 50, 15)
	// 周期重置只清套餐已用量；流量包余量原样保留。
	must(`UPDATE quota_balances SET consumed = 0 WHERE subscription_id = $1`, subID)
	charge(30)
	expect("a reset plan is used before packs again", 30, 50, 15)
	// 套餐还剩 70，超出的 30 里只有较新那包剩下的 15 可扣，另外 15 记在套餐上。
	charge(100)
	expect("packs run out mid-charge, the rest stays on the plan", 115, 50, 30)
	charge(25)
	expect("with no packs left the overage stays on the plan", 140, 50, 30)
	// 上面每一次扣量都 UPDATE 了流量包余额，一条都不该变成推送。
	if n := grantNotices(); n != 0 {
		t.Fatalf("traffic charges sent %d traffic pack change notices, want 0", n)
	}
	t.Log("marker=traffic_charge_pg18_plan_first_then_packs_ok")
	t.Log("marker=traffic_charge_pg18_charges_do_not_notify_ok")

	batchChargeScenario(t, ctx, admin, app)
	rolloverAndForeignUIDScenario(t, ctx, admin, app)
	retentionScenario(t, ctx, admin, app)
	reportIDScenario(t, ctx, admin, app)
	// w5retain：31 天留档清理、删掉的索引无依赖、billed_bytes、按天汇总（traffic_retention_pg18_test.go）
	reportRetentionScenario(t, ctx, admin, app)
	droppedIndexScenario(t, ctx, admin)
	billedBytesScenario(t, ctx, admin, app)
	trafficDailyScenario(t, ctx, admin, app)
	migrationRoundTripScenario(t, ctx, admin)

	// 后台节点列表的通用计划门禁借这个库跑（node_list_admin_pg18_test.go），免改 gates 脚本的过滤
	t.Run("admin_node_list_generic_plan", func(t *testing.T) {
		adminNodeListGenericPlanPG18(t, admin, appDSN)
	})
}

// rolloverAndForeignUIDScenario 证明审计 N1、N2、N3：
//   - 周期滚动空窗：本期 period_end 已过、RollQuotaPeriods 还没推进时照扣在这一行上，
//     更早那一期的行不动；
//   - 节点只能扣自己放行名单里的用户：别池用户的 uid 留档但不扣；
//   - 不合规条目（负数、错长度）不扣、计 invalid，同一报文里的合规条目照扣。
func rolloverAndForeignUIDScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	const (
		tenantID = "75400000-0000-7000-8000-000000000001"
		userID   = "75400000-0000-7000-8000-000000000011"
		subOwn   = "75400000-0000-7000-8000-000000000021"
		subOther = "75400000-0000-7000-8000-000000000022"
		nodeID   = "75400000-0000-7000-8000-000000000041"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'traffic-rollover-pg18', 'Traffic Rollover PG18', 'CNY')`, tenantID)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $2, 'traffic-rollover@example.test', 'Traffic Rollover', 'active')`, userID, tenantID)
	must(`INSERT INTO nodes (id, tenant_id, name, status) VALUES ($1, $2, 'rollover-node', 'active')`, nodeID, tenantID)
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end, node_uid)
		VALUES ($1, $3, $4, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0, now() + interval '30 days', 7540001),
		       ($2, $3, $4, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0, now() + interval '30 days', 7540002)`,
		subOwn, subOther, tenantID, userID)
	must(`SET session_replication_role = origin`)
	// 自己的订阅：上一期已结束（1 分钟前）但还没滚动；更早一期是历史行
	must(`INSERT INTO quota_balances (tenant_id, subscription_id, metric, period,
			period_start, period_end, granted, limit_value)
		VALUES ($1, $2, 'traffic.bytes', 'cycle', now() - interval '62 days', now() - interval '31 days', 1000, 1000),
		       ($1, $2, 'traffic.bytes', 'cycle', now() - interval '31 days', now() - interval '1 minute', 1000, 1000),
		       ($1, $3, 'traffic.bytes', 'cycle', now() - interval '1 day', now() + interval '30 days', 1000, 1000)`,
		tenantID, subOwn, subOther)

	svc := NewService(app, nil)
	node := servingNodeWithUsers(svc, tenantID, nodeID, 1, 7540001) // 7540002 属于别的池
	res, err := svc.ReportTraffic(ctx, tenantID, node,
		[]byte(`{"7540001":[100,20],"7540002":[500,500],"7540003":[-1,5],"7540004":[1]}`))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if res.Accepted != 1 || res.Invalid != 3 || res.Duplicate {
		t.Fatalf("report = %+v, want 1 accepted and 3 invalid", res)
	}
	var lapsed, history, other int64
	if err := admin.QueryRow(ctx, `
		SELECT (SELECT consumed FROM quota_balances WHERE subscription_id = $1 AND period_end < now() - interval '30 days'),
		       (SELECT consumed FROM quota_balances WHERE subscription_id = $1 AND period_end > now() - interval '30 days'),
		       (SELECT consumed FROM quota_balances WHERE subscription_id = $2)`,
		subOwn, subOther).Scan(&history, &lapsed, &other); err != nil {
		t.Fatal(err)
	}
	if lapsed != 120 || history != 0 {
		t.Fatalf("rollover gap: lapsed period consumed=%d, older period=%d; want 120 and 0", lapsed, history)
	}
	if other != 0 {
		t.Fatalf("a node charged a user outside its pool: consumed=%d", other)
	}
	var archived int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM node_traffic_reports WHERE node_id = $1`, nodeID).Scan(&archived); err != nil || archived != 1 {
		t.Fatalf("report archive rows = %d err=%v, want the whole report archived", archived, err)
	}
	t.Log("marker=traffic_charge_pg18_rollover_gap_and_foreign_uid_ok")
}

// batchChargeScenario 证明整份上报批量记账与逐笔记账同一结果：同一用户的两条订阅
// 按 uid 升序依次决定怎么分，前一条扣掉的流量包后一条看得见；找不到订阅的 uid 不计；
// 10 秒内重发的同一报文只留档不记账。
func batchChargeScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	const (
		tenantID = "75100000-0000-7000-8000-000000000001"
		userID   = "75100000-0000-7000-8000-000000000011"
		subA     = "75100000-0000-7000-8000-000000000021"
		subB     = "75100000-0000-7000-8000-000000000022"
		packOld  = "75100000-0000-7000-8000-000000000031"
		packNew  = "75100000-0000-7000-8000-000000000032"
		nodeID   = "75100000-0000-7000-8000-000000000041"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'traffic-batch-pg18', 'Traffic Batch PG18', 'CNY')`, tenantID)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $2, 'traffic-batch@example.test', 'Traffic Batch', 'active')`, userID, tenantID)
	must(`INSERT INTO nodes (id, tenant_id, name, status) VALUES ($1, $2, 'batch-node', 'active')`, nodeID, tenantID)
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end, node_uid)
		VALUES ($1, $3, $4, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0, now() + interval '30 days', 7510001),
		       ($2, $3, $4, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0, now() + interval '30 days', 7510002)`,
		subA, subB, tenantID, userID)
	must(`SET session_replication_role = origin`)
	must(`INSERT INTO quota_balances (tenant_id, subscription_id, metric, period,
			period_start, period_end, granted, limit_value)
		VALUES ($1, $2, 'traffic.bytes', 'cycle', now() - interval '1 day', now() + interval '30 days', 100, 100),
		       ($1, $3, 'traffic.bytes', 'cycle', now() - interval '1 day', now() + interval '30 days', 50, 50)`,
		tenantID, subA, subB)
	must(`INSERT INTO traffic_pack_grants (id, tenant_id, user_id, source, source_id,
			granted_bytes, created_at)
		VALUES ($1, $3, $4, 'migration', gen_random_uuid(), 40, now() - interval '2 hours'),
		       ($2, $3, $4, 'migration', gen_random_uuid(), 100, now() - interval '1 hour')`,
		packOld, packNew, tenantID, userID)

	svc := NewService(app, nil)
	node := servingNodeWithUsers(svc, tenantID, nodeID, 1, 7510001, 7510002, 7519999)
	report := func(payload string) *PushResult {
		t.Helper()
		res, err := svc.ReportTraffic(ctx, tenantID, node, []byte(payload))
		if err != nil {
			t.Fatalf("report %s: %v", payload, err)
		}
		return res
	}
	expect := func(step string, a, b, pOld, pNew, usageA, usageB int64) {
		t.Helper()
		var gotA, gotB, gotOld, gotNew, gotUA, gotUB int64
		if err := admin.QueryRow(ctx, `
			SELECT (SELECT consumed FROM quota_balances WHERE subscription_id = $1),
			       (SELECT consumed FROM quota_balances WHERE subscription_id = $2),
			       (SELECT consumed_bytes FROM traffic_pack_grants WHERE id = $3),
			       (SELECT consumed_bytes FROM traffic_pack_grants WHERE id = $4),
			       (SELECT coalesce(sum(bytes), 0) FROM subscription_usage_daily WHERE subscription_id = $1),
			       (SELECT coalesce(sum(bytes), 0) FROM subscription_usage_daily WHERE subscription_id = $2)`,
			subA, subB, packOld, packNew).Scan(&gotA, &gotB, &gotOld, &gotNew, &gotUA, &gotUB); err != nil {
			t.Fatalf("%s: read state: %v", step, err)
		}
		if gotA != a || gotB != b || gotOld != pOld || gotNew != pNew || gotUA != usageA || gotUB != usageB {
			t.Fatalf("%s: plans=%d/%d packs=%d/%d usage=%d/%d want %d/%d %d/%d %d/%d",
				step, gotA, gotB, gotOld, gotNew, gotUA, gotUB, a, b, pOld, pNew, usageA, usageB)
		}
	}

	// uid 7510001 先记：超出 100 的 50 从流量包扣（旧包 40 扣光、新包扣 10）；
	// uid 7510002 再记：超出 50 的 30 从新包剩下的 90 里扣。未知 uid 不计。
	first := `{"7510002":[0,80],"7510001":[150,0],"7519999":[5,5]}`
	if res := report(first); res.Duplicate || res.Accepted != 2 {
		t.Fatalf("batch report = %+v, want 2 accepted", res)
	}
	expect("two subscriptions share packs in uid order", 100, 50, 40, 40, 150, 80)
	if res := report(first); !res.Duplicate || res.Accepted != 0 {
		t.Fatalf("resent batch report = %+v, want a duplicate", res)
	}
	expect("duplicate report charges nothing", 100, 50, 40, 40, 150, 80)
	report(`{"7510001":[0,10]}`)
	expect("exhausted plan keeps draining the newer pack", 100, 50, 40, 50, 160, 80)
	t.Log("marker=traffic_charge_pg18_batch_matches_sequential_ok")
}
