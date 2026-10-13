package service

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// expireSetPeriod 把订阅改成指定状态与周期末（起点为终点减一个月，满足 period_end > period_start）。
func expireSetPeriod(t *testing.T, e *env, ctx context.Context, subID uuid.UUID, status string, end time.Time) {
	t.Helper()
	start := end.AddDate(0, -1, 0)
	e.exec(t, ctx, `UPDATE subscriptions SET status = ?, current_period_start = ?, current_period_end = ? WHERE id = ?`, status, start, end, subID)
}

func TestExpireDuePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	now := time.Now().UTC().Truncate(time.Microsecond)

	a := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, a.ID, StatusActive, now.Add(-1*time.Hour))

	b := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, b.ID, StatusActive, now.Add(1*time.Hour))

	st, err := e.svc.ExpireDue(ctx)
	if err != nil {
		t.Fatalf("ExpireDue: %v", err)
	}
	if st.Expired < 1 {
		t.Fatalf("Expired 应 >= 1，得 %d", st.Expired)
	}

	var statusA string
	var closedA sql.NullTime
	if err := e.row(ctx, `SELECT status, renewal_closed_at FROM subscriptions WHERE id = ?`, a.ID).Scan(&statusA, &closedA); err != nil {
		t.Fatal(err)
	}
	if statusA != StatusExpired {
		t.Fatalf("a 应为 expired，得 %s", statusA)
	}
	if closedA.Valid {
		t.Fatalf("a 的 renewal_closed_at 仍应为空")
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'expired' AND from_status = 'active' AND to_status = 'expired' AND actor_kind = 'scheduler'`, a.ID); n != 1 {
		t.Fatalf("a 的 expired 事件应为 1 条，得 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = 'subscription.expired' AND resource_id = ?`, a.ID); n != 1 {
		t.Fatalf("a 的 subscription.expired 审计应为 1 条，得 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = 'subscription.expired' AND resource_id = ? AND actor_kind = 'system'`, a.ID); n != 1 {
		t.Fatalf("a 的审计 actor_kind 应为 system，得 %d", n)
	}

	var statusB string
	if err := e.row(ctx, `SELECT status FROM subscriptions WHERE id = ?`, b.ID).Scan(&statusB); err != nil {
		t.Fatal(err)
	}
	if statusB != StatusActive {
		t.Fatalf("b 应仍 active，得 %s", statusB)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'expired'`, b.ID); n != 0 {
		t.Fatalf("b 不应有 expired 事件，得 %d", n)
	}

	_, err = e.svc.ExpireDue(ctx)
	if err != nil {
		t.Fatalf("第二次 ExpireDue: %v", err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'expired'`, a.ID); n != 1 {
		t.Fatalf("幂等：a 的 expired 事件仍应为 1 条，得 %d", n)
	}
}

func TestExpireClosesRenewalWindowPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	now := time.Now().UTC().Truncate(time.Microsecond)

	c := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, c.ID, StatusExpired, now.AddDate(0, 0, -31))

	d := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, d.ID, StatusExpired, now.AddDate(0, 0, -29))

	f := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, f.ID, StatusActive, now.AddDate(0, 0, -31))

	_, err := e.svc.ExpireDue(ctx)
	if err != nil {
		t.Fatalf("ExpireDue: %v", err)
	}

	var closedC sql.NullTime
	var statusC string
	if err := e.row(ctx, `SELECT status, renewal_closed_at FROM subscriptions WHERE id = ?`, c.ID).Scan(&statusC, &closedC); err != nil {
		t.Fatal(err)
	}
	if statusC != StatusExpired || !closedC.Valid {
		t.Fatalf("c 应 expired 且 renewal_closed_at 非空，得 status=%s closed=%v", statusC, closedC.Valid)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`, c.ID); n != 0 {
		t.Fatalf("c 不应再有 active 凭据，得 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_credentials WHERE subscription_id = ? AND status = 'revoked' AND revoked_reason = 'renewal_window_closed'`, c.ID); n != 1 {
		t.Fatalf("c 应有 1 条关窗吊销凭据，得 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'renewal_closed' AND from_status = 'expired' AND to_status = 'expired' AND actor_kind = 'scheduler'`, c.ID); n != 1 {
		t.Fatalf("c 的 renewal_closed 事件应为 1 条，得 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = 'subscription.renewal_closed' AND resource_id = ?`, c.ID); n != 1 {
		t.Fatalf("c 的 renewal_closed 审计应为 1 条，得 %d", n)
	}

	var closedD sql.NullTime
	if err := e.row(ctx, `SELECT renewal_closed_at FROM subscriptions WHERE id = ?`, d.ID).Scan(&closedD); err != nil {
		t.Fatal(err)
	}
	if closedD.Valid {
		t.Fatalf("d 的 renewal_closed_at 仍应为空")
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`, d.ID); n != 1 {
		t.Fatalf("d 凭据应仍 active，得 %d", n)
	}

	var statusF string
	var closedF sql.NullTime
	if err := e.row(ctx, `SELECT status, renewal_closed_at FROM subscriptions WHERE id = ?`, f.ID).Scan(&statusF, &closedF); err != nil {
		t.Fatal(err)
	}
	if statusF != StatusExpired || !closedF.Valid {
		t.Fatalf("f 同一轮应先 expired 再关窗：status=%s closed=%v", statusF, closedF.Valid)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'expired'`, f.ID); n != 1 {
		t.Fatalf("f 的 expired 事件应为 1 条，得 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'renewal_closed'`, f.ID); n != 1 {
		t.Fatalf("f 的 renewal_closed 事件应为 1 条，得 %d", n)
	}

	_, err = e.svc.ExpireDue(ctx)
	if err != nil {
		t.Fatalf("第二次 ExpireDue: %v", err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'renewal_closed'`, c.ID); n != 1 {
		t.Fatalf("幂等：c 的 renewal_closed 仍应为 1 条，得 %d", n)
	}
}

func TestExpireConcurrentPG(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	now := time.Now().UTC().Truncate(time.Microsecond)

	g := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, g.ID, StatusActive, now.Add(-1*time.Hour))
	before := e.roster.count(g.ID)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.svc.ExpireDue(ctx); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发 ExpireDue: %v", err)
	}

	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'expired'`, g.ID); n != 1 {
		t.Fatalf("g 的 expired 事件应为 1 条，得 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = 'subscription.expired' AND resource_id = ?`, g.ID); n != 1 {
		t.Fatalf("g 的 subscription.expired 审计应为 1 条，得 %d", n)
	}
	if e.roster.count(g.ID)-before != 1 {
		t.Fatalf("名单通知应恰好增加 1 次，before=%d after=%d", before, e.roster.count(g.ID))
	}
	var statusG string
	if err := e.row(ctx, `SELECT status FROM subscriptions WHERE id = ?`, g.ID).Scan(&statusG); err != nil {
		t.Fatal(err)
	}
	if statusG != StatusExpired {
		t.Fatalf("g 应为 expired，得 %s", statusG)
	}
}
