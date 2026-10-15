package service

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 后台改到期、改上限各写一条订阅事件（状态不变，操作者是管理员）。
func TestPGAdminEditsWriteEvents(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")

	if _, err := e.svc.AdminSetPeriodEnd(actx, sub.ID, time.Now().UTC().AddDate(0, 0, 60), "后台调整到期补偿"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AdminSetQuotaLimit(actx, sub.ID, MetricTraffic, PeriodCycle, i64(20*gib), "后台调整流量上限"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{EventPeriodEndSet, EventQuotaLimitSet} {
		if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events
			WHERE subscription_id = ? AND event_type = ? AND from_status = 'active' AND to_status = 'active'
			  AND actor_kind = 'admin' AND actor_id = ? AND payload->>'reason' <> ''`, sub.ID, kind, admin); n != 1 {
			t.Fatalf("%s 事件 %d 条", kind, n)
		}
	}
}

// 属主已注销：后台改订阅的每个入口都回 409（账号已注销），订阅不动。
func TestPGAdminEditsRejectDeletedOwner(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	e.users.markDeleted(user)
	reason := "给已注销账号改订阅"
	devices := 3

	calls := map[string]func() error{
		"改到期": func() error {
			_, err := e.svc.AdminSetPeriodEnd(actx, sub.ID, time.Now().UTC().AddDate(0, 0, 60), reason)
			return err
		},
		"改上限": func() error {
			return e.svc.AdminSetQuotaLimit(actx, sub.ID, MetricTraffic, PeriodCycle, i64(gib), reason)
		},
		"加时长": func() error {
			_, err := e.svc.AdminExtend(actx, sub.ID, 7, reason)
			return err
		},
		"加流量": func() error {
			_, err := e.svc.AdminGrantTraffic(actx, sub.ID, gib, reason)
			return err
		},
		"重置流量": func() error {
			_, err := e.svc.AdminResetTraffic(actx, sub.ID, reason)
			return err
		},
		"改设备数": func() error {
			return e.svc.AdminSetDeviceLimit(actx, sub.ID, &devices)
		},
		"换链接": func() error {
			_, err := e.svc.AdminRotate(actx, sub.ID, reason)
			return err
		},
	}
	for name, call := range calls {
		if err := call(); errs.As(err).Code != errs.Conflict {
			t.Fatalf("%s 应 409，得到 %v", name, err)
		}
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type <> 'activated'`, sub.ID); n != 0 {
		t.Fatalf("不该再写订阅事件，得到 %d 条", n)
	}
}
