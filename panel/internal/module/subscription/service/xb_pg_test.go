package service

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// sqlState 取库错误的 SQLSTATE（GORM 翻译后仍包着原 PgError）。
func sqlState(err error) string {
	v, _ := db.AsViolation(err)
	return v.Code
}

func TestPGSignupTrialGrant(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	e.exec(t, ctx, `UPDATE plans SET current_version_id = ?, status = 'active' WHERE id = ?`, versionID, planID)

	before := time.Now().UTC()
	if err := e.svc.GrantSignupTrial(ctx, user, planID, 48); err != nil {
		t.Fatal(err)
	}

	subs, err := e.svc.ByUser(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Subscriptions) != 1 {
		t.Fatalf("订阅份数 %d", len(subs.Subscriptions))
	}
	sub := subs.Subscriptions[0]
	if sub.Status != StatusActive {
		t.Fatalf("状态 %s", sub.Status)
	}
	wantEnd := before.Add(48 * time.Hour)
	if d := sub.PeriodEnd.Sub(wantEnd); d < -time.Minute || d > time.Minute {
		t.Fatalf("到期 %s 与 now+48h 差 %s", sub.PeriodEnd, d)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_trials WHERE user_id = ? AND hours = 48`, user); n != 1 {
		t.Fatalf("试用记录 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM orders WHERE user_id = ?`, user); n != 0 {
		t.Fatalf("订单 %d", n)
	}
	if err := e.svc.GrantSignupTrial(ctx, user, planID, 48); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscriptions WHERE user_id = ?`, user); n != 1 {
		t.Fatalf("重复发放后订阅 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_trials WHERE user_id = ?`, user); n != 1 {
		t.Fatalf("重复发放后试用记录 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`,
		ActionTrialGranted, sub.ID); n != 1 {
		t.Fatalf("审计 %d", n)
	}
}

func TestPGSignupTrialSkips(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	e.exec(t, ctx, `UPDATE plans SET current_version_id = ?, status = 'active' WHERE id = ?`, versionID, planID)

	for _, hours := range []int{0, 8761} {
		if err := e.svc.GrantSignupTrial(ctx, user, planID, hours); err != nil {
			t.Fatalf("hours=%d: %v", hours, err)
		}
		if n := countRows(t, e, ctx, `SELECT count(*) FROM subscriptions WHERE user_id = ?`, user); n != 0 {
			t.Fatalf("hours=%d 不应开通订阅", hours)
		}
	}

	user2 := newUser(t, e, ctx)
	planArchived, verArchived := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	e.exec(t, ctx, `UPDATE plans SET current_version_id = ?, status = 'archived' WHERE id = ?`, verArchived, planArchived)
	if err := e.svc.GrantSignupTrial(ctx, user2, planArchived, 24); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscriptions WHERE user_id = ?`, user2); n != 0 {
		t.Fatal("归档套餐不应开通")
	}

	user3 := newUser(t, e, ctx)
	planDraft, _ := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	if err := e.svc.GrantSignupTrial(ctx, user3, planDraft, 24); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscriptions WHERE user_id = ?`, user3); n != 0 {
		t.Fatal("无 current_version_id 的 draft 套餐不应开通")
	}
}

func TestPGSignupTrialUniqueByUser(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	sub1 := provision(t, e, ctx, user, planID, versionID, "")
	sub2 := provision(t, e, ctx, user, planID, versionID, "二")

	e.exec(t, ctx, `INSERT INTO subscription_trials (user_id, subscription_id, plan_id, hours) VALUES (?, ?, ?, 48)`,
		user, sub1.ID, planID)

	if err := e.rawExec(ctx, "SAVEPOINT sp_dup"); err != nil {
		t.Fatal(err)
	}
	err := e.rawExec(ctx, `INSERT INTO subscription_trials (user_id, subscription_id, plan_id, hours) VALUES (?, ?, ?, 24)`,
		user, sub2.ID, planID)
	if sqlState(err) != "23505" {
		t.Fatalf("同一 user_id 第二次插入应唯一约束: %v", err)
	}
	if err := e.rawExec(ctx, "ROLLBACK TO SAVEPOINT sp_dup"); err != nil {
		t.Fatal(err)
	}

	if err := e.rawExec(ctx, "SAVEPOINT sp_mut"); err != nil {
		t.Fatal(err)
	}
	err = e.rawExec(ctx, `UPDATE subscription_trials SET hours = 1 WHERE user_id = ?`, user)
	if sqlState(err) != "23001" {
		t.Fatalf("试用记录应只追加: %v", err)
	}
	if err := e.rawExec(ctx, "ROLLBACK TO SAVEPOINT sp_mut"); err != nil {
		t.Fatal(err)
	}
}

func TestPGCloseUserSubscriptions(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	target := newUser(t, e, ctx)
	other := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	provision(t, e, ctx, target, planID, versionID, "a")
	provision(t, e, ctx, target, planID, versionID, "b")
	otherSub := provision(t, e, ctx, other, planID, versionID, "")

	n, err := e.svc.CloseUserSubscriptions(ctx, target, "用户注销账号")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("关闭份数 %d", n)
	}
	now := time.Now().UTC()
	for _, label := range []string{"a", "b"} {
		var status string
		var end time.Time
		if err := e.row(ctx, `SELECT status, current_period_end FROM subscriptions WHERE user_id = ? AND label = ?`,
			target, label).Scan(&status, &end); err != nil {
			t.Fatal(err)
		}
		if status != StatusExpired || end.After(now) {
			t.Fatalf("%s: status=%s end=%s", label, status, end)
		}
	}
	got, err := e.svc.Get(ctx, otherSub.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusActive {
		t.Fatalf("他人订阅 %s", got.Status)
	}
	n2, err := e.svc.CloseUserSubscriptions(ctx, target, "再次调用")
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("再次关闭 %d", n2)
	}
}

func TestPGAdminSetPeriodEnd(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	// 原到期从库里读（库里是微秒精度，与 service 锁行读到的同一个值）
	before, err := e.svc.Get(ctx, sub.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	prevEnd := before.PeriodEnd

	newEnd := time.Now().UTC().AddDate(0, 0, 90).Truncate(time.Microsecond)
	res, err := e.svc.AdminSetPeriodEnd(actx, sub.ID, newEnd, "后台调整到期补偿")
	if err != nil {
		t.Fatal(err)
	}
	if !res.PreviousEnd.Equal(prevEnd) || !res.PeriodEnd.Equal(newEnd) {
		t.Fatalf("结果 %+v", res)
	}
	got, err := e.svc.Get(ctx, sub.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.PeriodEnd.Equal(newEnd) {
		t.Fatalf("库到期 %s", got.PeriodEnd)
	}

	if _, err := e.svc.AdminSetPeriodEnd(actx, sub.ID, time.Now().UTC().Add(-time.Hour), "后台调整到期补偿"); errs.As(err).Code != errs.ValidationFailed || errs.As(err).Fields["period_end"] == "" {
		t.Fatalf("过去到期: %v", err)
	}
	if _, err := e.svc.AdminSetPeriodEnd(actx, sub.ID, time.Now().UTC().AddDate(11, 0, 0), "后台调整到期补偿"); errs.As(err).Code != errs.ValidationFailed || errs.As(err).Fields["period_end"] == "" {
		t.Fatalf("超过十年: %v", err)
	}
	if _, err := e.svc.AdminSetPeriodEnd(actx, sub.ID, newEnd, "短"); errs.As(err).Code != errs.ValidationFailed || errs.As(err).Fields["reason"] == "" {
		t.Fatalf("原因太短: %v", err)
	}

	e.exec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = now() - interval '60 days',
		current_period_end = now() - interval '1 day' WHERE id = ?`, sub.ID)
	if _, err := e.svc.AdminSetPeriodEnd(actx, sub.ID, time.Now().UTC().AddDate(0, 0, 30), "后台调整到期补偿"); errs.As(err).Code != errs.Conflict {
		t.Fatalf("已过期: %v", err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`,
		ActionPeriodEndSet, sub.ID); n != 1 {
		t.Fatalf("审计 %d", n)
	}
}

func TestPGAdminSetQuotaLimit(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	reason := "后台调整流量上限"

	if err := e.svc.AdminSetQuotaLimit(actx, sub.ID, MetricTraffic, PeriodCycle, i64(50*gib), reason); err != nil {
		t.Fatal(err)
	}
	q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if q.Limit == nil || *q.Limit != 50*gib {
		t.Fatalf("50GiB: %+v", q)
	}

	if err := e.svc.AdminSetQuotaLimit(actx, sub.ID, MetricTraffic, PeriodCycle, nil, reason); err != nil {
		t.Fatal(err)
	}
	q = quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if q.Limit != nil {
		t.Fatalf("不限: %+v", q)
	}

	if err := e.svc.AdminSetQuotaLimit(actx, sub.ID, MetricTraffic, PeriodCycle, i64(-1), reason); errs.As(err).Code != errs.ValidationFailed || errs.As(err).Fields["limit_bytes"] == "" {
		t.Fatalf("负数: %v", err)
	}
	if err := e.svc.AdminSetQuotaLimit(actx, sub.ID, MetricTraffic, "year", i64(gib), reason); errs.As(err).Code != errs.ValidationFailed || errs.As(err).Fields["period"] == "" {
		t.Fatalf("period year: %v", err)
	}
	if err := e.svc.AdminSetQuotaLimit(actx, sub.ID, "x", PeriodCycle, i64(gib), reason); errs.As(err).Code != errs.ValidationFailed || errs.As(err).Fields["metric"] == "" {
		t.Fatalf("metric x: %v", err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`,
		ActionQuotaLimitSet, sub.ID); n != 2 {
		t.Fatalf("审计 %d", n)
	}
}
