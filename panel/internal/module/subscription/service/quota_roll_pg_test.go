package service

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// 测试库是共享的，别的用例提交的到期行也会被一起滚：这里只对本用例建的订阅断言，不断言 RollStats 的精确值。

// rollNow 是测试的时间基准（UTC、截到微秒）。
func rollNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// rollSetQuota 改一条配额行的周期起止。
func rollSetQuota(t *testing.T, e *env, ctx context.Context, subID uuid.UUID, period string, start, end time.Time) {
	t.Helper()
	e.exec(t, ctx, `UPDATE quota_balances SET period_start = ?, period_end = ? WHERE subscription_id = ? AND period = ?`, start, end, subID, period)
}

// rollSetSub 改订阅的状态与周期起止。
func rollSetSub(t *testing.T, e *env, ctx context.Context, subID uuid.UUID, status string, start, end time.Time) {
	t.Helper()
	e.exec(t, ctx, `UPDATE subscriptions SET status = ?, current_period_start = ?, current_period_end = ? WHERE id = ?`, status, start, end, subID)
}

// rollResetLogs 按 consumed_before 升序取本订阅某原因的重置日志。
func rollResetLogs(t *testing.T, e *env, ctx context.Context, subID uuid.UUID, reason string) []int64 {
	t.Helper()
	var joined string
	if err := e.row(ctx, `SELECT coalesce(string_agg(consumed_before::text, ',' ORDER BY consumed_before), '')
		FROM traffic_reset_logs WHERE subscription_id = ? AND reason = ?`, subID, reason).Scan(&joined); err != nil {
		t.Fatal(err)
	}
	out := []int64{}
	if joined == "" {
		return out
	}
	for _, part := range strings.Split(joined, ",") {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// rollAssertQuota 断言一条配额行的周期与已用量。
func rollAssertQuota(t *testing.T, label string, q Quota, start, end time.Time, consumed int64) {
	t.Helper()
	if !q.PeriodStart.Equal(start) || q.PeriodEnd == nil || !q.PeriodEnd.Equal(end) || q.Consumed != consumed {
		t.Fatalf("%s: got start=%s end=%v consumed=%d, want start=%s end=%s consumed=%d",
			label, q.PeriodStart, q.PeriodEnd, q.Consumed, start, end, consumed)
	}
}

func TestRollDayMonthPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx,
		quotaSpec{MetricTraffic, PeriodDay, i64(5 * gib)},
		quotaSpec{MetricTraffic, PeriodMonth, i64(50 * gib)},
		quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	now := rollNow()

	dayEnd := now.Add(-time.Hour)
	rollSetQuota(t, e, ctx, sub.ID, PeriodDay, now.Add(-25*time.Hour), dayEnd)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodDay, 123)
	monthEnd := now.Add(-2 * time.Hour)
	rollSetQuota(t, e, ctx, sub.ID, PeriodMonth, monthEnd.AddDate(0, -1, 0), monthEnd)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodMonth, 456)
	cycleBefore := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)

	if _, err := e.svc.RollQuotas(ctx); err != nil {
		t.Fatalf("RollQuotas: %v", err)
	}
	rollAssertQuota(t, "day 行", quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodDay), dayEnd, dayEnd.AddDate(0, 0, 1), 0)
	rollAssertQuota(t, "month 行", quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodMonth), monthEnd, monthEnd.AddDate(0, 1, 0), 0)
	cycleAfter := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if !cycleAfter.PeriodStart.Equal(cycleBefore.PeriodStart) || !cycleAfter.PeriodEnd.Equal(*cycleBefore.PeriodEnd) || cycleAfter.Consumed != cycleBefore.Consumed {
		t.Fatalf("cycle 行不该动: before=%+v after=%+v", cycleBefore, cycleAfter)
	}
	if got := rollResetLogs(t, e, ctx, sub.ID, ResetCycleRoll); len(got) != 2 || got[0] != 123 || got[1] != 456 {
		t.Fatalf("cycle_roll 日志的 consumed_before: %v", got)
	}

	// 再跑一次不重复滚
	if _, err := e.svc.RollQuotas(ctx); err != nil {
		t.Fatalf("第二次 RollQuotas: %v", err)
	}
	if got := rollResetLogs(t, e, ctx, sub.ID, ResetCycleRoll); len(got) != 2 {
		t.Fatalf("幂等：cycle_roll 日志应仍为 2 条，得 %v", got)
	}
}

func TestRollCatchUpPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodDay, i64(5 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	now := rollNow()

	end := now.Add(-73 * time.Hour)
	rollSetQuota(t, e, ctx, sub.ID, PeriodDay, end.AddDate(0, 0, -1), end)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodDay, 9)

	if _, err := e.svc.RollQuotas(ctx); err != nil {
		t.Fatalf("RollQuotas: %v", err)
	}
	rollAssertQuota(t, "落后三期的 day 行", quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodDay), end.AddDate(0, 0, 3), end.AddDate(0, 0, 4), 0)
	if got := rollResetLogs(t, e, ctx, sub.ID, ResetCycleRoll); len(got) != 1 || got[0] != 9 {
		t.Fatalf("一步追上只写一条日志: %v", got)
	}
}

func TestRollOverlongPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodMonth, i64(50 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	now := rollNow()

	start := now.AddDate(0, 0, -40)
	rollSetSub(t, e, ctx, sub.ID, StatusActive, start, now.AddDate(0, 0, 330))
	// 周期末被错写成接近订阅到期
	rollSetQuota(t, e, ctx, sub.ID, PeriodMonth, start, now.AddDate(0, 0, 300))
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodMonth, 7)

	if _, err := e.svc.RollQuotas(ctx); err != nil {
		t.Fatalf("RollQuotas: %v", err)
	}
	newStart := start.AddDate(0, 1, 0)
	rollAssertQuota(t, "过长的 month 行", quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodMonth), newStart, newStart.AddDate(0, 1, 0), 0)
	if got := rollResetLogs(t, e, ctx, sub.ID, ResetCycleRoll); len(got) != 1 || got[0] != 7 {
		t.Fatalf("过长行追上的日志: %v", got)
	}
}

func TestRollCyclePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	now := rollNow()

	// 提前续费之后：订阅到期已推后一期，cycle 行还停在原到期（刚过）
	oldEnd := now.Add(-time.Hour)
	subEnd := oldEnd.AddDate(0, 1, 0)
	rollSetSub(t, e, ctx, sub.ID, StatusActive, oldEnd.AddDate(0, -1, 0), subEnd)
	rollSetQuota(t, e, ctx, sub.ID, PeriodCycle, oldEnd.AddDate(0, -1, 0), oldEnd)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 77)
	// 救回折算加过的额度只属于上一期
	e.exec(t, ctx, `UPDATE quota_balances SET limit_value = limit_value + 10 WHERE subscription_id = ? AND period = 'cycle'`, sub.ID)

	if _, err := e.svc.RollQuotas(ctx); err != nil {
		t.Fatalf("RollQuotas: %v", err)
	}
	q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	rollAssertQuota(t, "cycle 行", q, oldEnd, NextCycleEnd(oldEnd, subEnd, "month", 1), 0)
	if q.Limit == nil || *q.Limit != 100*gib {
		t.Fatalf("cycle 上限应按版本写回 100 GiB，得 %v", q.Limit)
	}
	if got := rollResetLogs(t, e, ctx, sub.ID, ResetRenewal); len(got) != 1 || got[0] != 77 {
		t.Fatalf("renewal 日志: %v", got)
	}
}

func TestRollSkipsExpiredPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodDay, i64(5 * gib)})
	now := rollNow()
	subEnd := now.Add(-time.Hour)
	dayEnd := now.Add(-2 * time.Hour)

	for _, status := range []string{StatusExpired, StatusActive} {
		sub := provision(t, e, ctx, user, planID, versionID, "")
		// expired：已过期；active：已走过到期日、到期扫描还没赶上
		rollSetSub(t, e, ctx, sub.ID, status, subEnd.AddDate(0, -1, 0), subEnd)
		rollSetQuota(t, e, ctx, sub.ID, PeriodDay, dayEnd.AddDate(0, 0, -1), dayEnd)
		setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodDay, 5)

		if _, err := e.svc.RollQuotas(ctx); err != nil {
			t.Fatalf("RollQuotas: %v", err)
		}
		rollAssertQuota(t, status+" 订阅的 day 行不该滚", quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodDay), dayEnd.AddDate(0, 0, -1), dayEnd, 5)
		if n := countRows(t, e, ctx, `SELECT count(*) FROM traffic_reset_logs WHERE subscription_id = ?`, sub.ID); n != 0 {
			t.Fatalf("%s 订阅不该有重置日志，得 %d", status, n)
		}
	}
}

// TestRollSkipsLockedPG 用真实提交：配额行被别的事务（模拟节点扣量）锁着时这一轮跳过、补一遍仍跳过；放锁后下一轮滚动并通知名单。
func TestRollSkipsLockedPG(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodDay, i64(5 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	now := rollNow()
	end := now.Add(-time.Hour)
	rollSetQuota(t, e, ctx, sub.ID, PeriodDay, end.AddDate(0, 0, -1), end)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodDay, 55)

	d := testkit.DB(t)
	g := d.Base().Begin()
	if g.Error != nil {
		t.Fatal(g.Error)
	}
	t.Cleanup(func() { g.Rollback() })
	if err := g.Exec(`SELECT 1 FROM quota_balances WHERE subscription_id = ? AND period = 'day' FOR UPDATE`, sub.ID).Error; err != nil {
		t.Fatal(err)
	}

	st, err := e.svc.RollQuotas(ctx)
	if err != nil {
		t.Fatalf("RollQuotas（锁着）: %v", err)
	}
	rollAssertQuota(t, "锁着的 day 行不该滚", quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodDay), end.AddDate(0, 0, -1), end, 55)
	if st.Passes != 2 {
		t.Fatalf("探测到被锁跳过的行应补一遍，Passes=%d", st.Passes)
	}

	if err := g.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	before := e.roster.count(sub.ID)
	if _, err := e.svc.RollQuotas(ctx); err != nil {
		t.Fatalf("RollQuotas（放锁后）: %v", err)
	}
	rollAssertQuota(t, "放锁后的 day 行", quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodDay), end, end.AddDate(0, 0, 1), 0)
	if got := rollResetLogs(t, e, ctx, sub.ID, ResetCycleRoll); len(got) != 1 || got[0] != 55 {
		t.Fatalf("cycle_roll 日志: %v", got)
	}
	if n := e.roster.count(sub.ID) - before; n != 1 {
		t.Fatalf("滚动后应通知名单 1 次，得 %d", n)
	}
}
