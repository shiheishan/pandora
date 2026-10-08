package billing

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// checkQuotaRollPG18：年付套餐的月流量、日限量按自然周期起算与滚动；滚动按 id 加锁、
// SKIP LOCKED 跳过正被记账锁住的行；周期末被旧写法错设成订阅周期末的存量行一步追上；
// 重置日志记下清零前的用量。挂在 TestSubscriptionPeriodPG18 下，用同一个租户。
func checkQuotaRollPG18(t *testing.T, p *subPeriodPG18) {
	ctx := p.ctx

	// 年付套餐：每月 1000、每日 100
	product, plan, version, priceID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	p.must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($1,$2,$3,$3,'active')`,
		product, p.fx.tenant, "sp-yearly-"+p.fx.suffix[:8])
	p.must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($1,$2,$3,$4,$4,'draft')`,
		plan, p.fx.tenant, product, "sp-yearly-plan-"+p.fx.suffix[:8])
	p.must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES($1,$2,$3,1)`, version, p.fx.tenant, plan)
	p.must(`INSERT INTO quota_definitions(tenant_id,plan_version_id,metric,limit_value,unit,period)
		VALUES($1,$2,'traffic.bytes',1000,'bytes','month'),($1,$2,'traffic.bytes',100,'bytes','day')`,
		p.fx.tenant, version)
	p.must(`UPDATE plan_versions SET frozen_at=now(),status='published' WHERE id=$1`, version)
	p.must(`UPDATE plans SET current_version_id=$2,status='active' WHERE id=$1`, plan, version)
	p.must(`INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,
		interval_count,status) VALUES($1,$2,$3,'CNY',12000,'year',1,'active')`, priceID, p.fx.tenant, product)

	// 每次开通用一个新用户：同一用户再兑同一套餐的卡会在原订阅上续费（w5expiry 规则 3），
	// 而这里要的是三条各自独立的订阅
	grant := func() string {
		var sub string
		user := uuid.NewString()
		p.must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,'Quota Roll','active')`,
			user, p.fx.tenant, "quota-roll-"+user[:8]+"@example.test")
		orderReleasePG18InTxAs(t, ctx, p.app, p.fx.tenant, user, func(tx pgx.Tx) error {
			var err error
			sub, _, _, _, err = p.billing.GiftGranter().GrantPlan(ctx, tx, p.fx.tenant, user, "", plan, priceID, "quota-roll")
			return err
		})
		return sub
	}
	rowID := func(sub, period string) string {
		var id string
		if err := p.admin.QueryRow(ctx, `SELECT id::text FROM quota_balances
			WHERE subscription_id=$1::uuid AND period=$2`, sub, period).Scan(&id); err != nil {
			t.Fatalf("quota row %s/%s: %v", sub, period, err)
		}
		return id
	}

	// 1) 开通：day / month 的周期末是起点加一天 / 一个月，不是一年后的订阅周期末
	overlong := grant()
	var monthNatural, dayNatural, yearly bool
	if err := p.admin.QueryRow(ctx, `
		SELECT bool_and(q.period_end = q.period_start + interval '1 month') FILTER (WHERE q.period = 'month'),
		       bool_and(q.period_end = q.period_start + interval '1 day') FILTER (WHERE q.period = 'day'),
		       bool_and(s.current_period_end > q.period_start + interval '360 days')
		  FROM quota_balances q JOIN subscriptions s ON s.id = q.subscription_id
		 WHERE q.subscription_id = $1::uuid`, overlong).Scan(&monthNatural, &dayNatural, &yearly); err != nil ||
		!monthNatural || !dayNatural || !yearly {
		t.Fatalf("yearly plan quota periods month=%v day=%v yearly=%v err=%v", monthNatural, dayNatural, yearly, err)
	}

	// 2) 存量：旧写法把周期末写成订阅周期末，起点 45 天前，已用 123
	p.must(`UPDATE quota_balances q SET period_start = now() - interval '45 days',
		       period_end = s.current_period_end, consumed = 123
		  FROM subscriptions s WHERE s.id = q.subscription_id AND q.subscription_id = $1::uuid`, overlong)
	var overStart time.Time
	if err := p.admin.QueryRow(ctx, `SELECT period_start FROM quota_balances WHERE id=$1::uuid`,
		rowID(overlong, "month")).Scan(&overStart); err != nil {
		t.Fatal(err)
	}

	// 正常到期的行：月行一小时前到期，已用 77
	normal := grant()
	p.must(`UPDATE quota_balances SET period_start = now() - interval '1 month 1 hour',
		       period_end = now() - interval '1 hour', consumed = 77
		 WHERE subscription_id = $1::uuid AND period = 'month'`, normal)
	var normalEnd time.Time
	if err := p.admin.QueryRow(ctx, `SELECT period_end FROM quota_balances WHERE id=$1::uuid`,
		rowID(normal, "month")).Scan(&normalEnd); err != nil {
		t.Fatal(err)
	}

	// 正被记账锁住的到期行：这一轮跳过，不等锁
	locked := grant()
	lockedRow := rowID(locked, "month")
	p.must(`UPDATE quota_balances SET period_start = now() - interval '1 month 1 hour',
		       period_end = now() - interval '1 hour', consumed = 5 WHERE id = $1::uuid`, lockedRow)
	lockTx, err := p.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(ctx, `SELECT 1 FROM quota_balances WHERE id=$1::uuid FOR UPDATE`, lockedRow); err != nil {
		t.Fatal(err)
	}

	rollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	start := time.Now()
	n, err := p.billing.RollQuotaPeriods(rollCtx, p.fx.tenant)
	cancel()
	if err != nil || time.Since(start) > 5*time.Second {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("roll with a locked row: n=%d err=%v took %s", n, err, time.Since(start))
	}
	if n != 3 {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("roll rolled %d rows, want 3 (overlong month+day, normal month)", n)
	}

	var ok bool
	if err := p.admin.QueryRow(ctx, `
		SELECT
		  -- 存量月行：从起点数一个整月，一步追上，下一期在一个月后
		  (SELECT period_start = $2::timestamptz + interval '1 month'
		      AND period_end = $2::timestamptz + interval '2 months' AND consumed = 0
		     FROM quota_balances WHERE id = $3::uuid)
		  -- 存量日行：从起点数 45 个整天
		  AND (SELECT period_start = $2::timestamptz + interval '45 days'
		      AND period_end = $2::timestamptz + interval '46 days' AND consumed = 0
		     FROM quota_balances WHERE id = $4::uuid)
		  -- 正常月行：新周期从旧周期末起
		  AND (SELECT period_start = $5::timestamptz AND period_end = $5::timestamptz + interval '1 month'
		      AND consumed = 0 FROM quota_balances WHERE id = $6::uuid)
		  -- 被锁住的行原样
		  AND (SELECT period_end < now() AND consumed = 5 FROM quota_balances WHERE id = $7::uuid)
		  -- 重置日志：清零前的用量
		  AND (SELECT count(*) = 2 AND bool_and(consumed_before = 123) FROM traffic_reset_logs
		        WHERE subscription_id = $1::uuid AND reason = 'cycle_roll')
		  AND (SELECT count(*) = 1 AND bool_and(consumed_before = 77) FROM traffic_reset_logs
		        WHERE subscription_id = $8::uuid AND reason = 'cycle_roll')`,
		overlong, overStart, rowID(overlong, "month"), rowID(overlong, "day"),
		normalEnd, rowID(normal, "month"), lockedRow, normal).Scan(&ok); err != nil || !ok {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("roll result check ok=%v err=%v", ok, err)
	}

	// 锁放开后下一轮滚它；再跑一轮什么都不动
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := p.billing.RollQuotaPeriods(ctx, p.fx.tenant); err != nil || n != 1 {
		t.Fatalf("roll after unlock: n=%d err=%v, want 1", n, err)
	}
	var lockedLog int64
	if err := p.admin.QueryRow(ctx, `SELECT consumed_before FROM traffic_reset_logs
		WHERE subscription_id=$1::uuid AND reason='cycle_roll'`, locked).Scan(&lockedLog); err != nil || lockedLog != 5 {
		t.Fatalf("unlocked row reset log consumed_before=%d err=%v", lockedLog, err)
	}
	if n, err := p.billing.RollQuotaPeriods(ctx, p.fx.tenant); err != nil || n != 0 {
		t.Fatalf("second roll: n=%d err=%v, want 0", n, err)
	}
	t.Log("marker=sub_period_pg18_quota_roll_ok")
}
