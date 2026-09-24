// [INPUT]: 依赖 uniproxy.go 的 chargeTraffic，依赖 platform/db 的 InTx，依赖迁移 00070 的 traffic_pack_grants 与其变更通知触发器、00076 摘掉 quota_balances 通知
// [OUTPUT]: 对外提供 TestTrafficChargePG18（run-pg18-gates.sh 的 traffic_charge 域）
// [POS]: domain/nodefabric 扣量路径的 PG18 集成门禁，与 traffic_charge_test.go 的单元测试互补：这里证明真实 SQL 的扣量顺序与推送面
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
}
