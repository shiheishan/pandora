package service

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// 巡检的计数是全库的（库里可能有别的包提交的行），用例只比前后差，样例按本例建的 id 找。
func integrityDelta(t *testing.T, what string, before, after integrity.Result, want int64, id uuid.UUID) {
	t.Helper()
	if after.Count-before.Count != want {
		t.Fatalf("%s：违例数 %d → %d，期望多 %d", what, before.Count, after.Count, want)
	}
	if got := slices.Contains(after.Sample, id.String()); got != (want > 0) {
		t.Fatalf("%s：样例 %v 里有没有 %s 应为 %v", what, after.Sample, id, want > 0)
	}
}

// 到期超过宽限仍是 active 算一条；还在宽限内的不算。
func TestCheckExpireOverduePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	now := time.Now().UTC().Truncate(time.Microsecond)
	check := func() integrity.Result {
		t.Helper()
		r, err := e.svc.CheckExpireOverdue(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	before := check()
	fresh := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, fresh.ID, StatusActive, now.Add(-IntegrityGrace/2))
	integrityDelta(t, "宽限内", before, check(), 0, fresh.ID)

	stuck := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, stuck.ID, StatusActive, now.Add(-time.Hour))
	integrityDelta(t, "到期一小时仍 active", before, check(), 1, stuck.ID)
}

// 过期满续费窗口加宽限、窗口还没关的算一条；已关窗的、还在窗口里的不算。
func TestCheckCloseOverduePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	now := time.Now().UTC().Truncate(time.Microsecond)
	check := func(ctx context.Context) integrity.Result {
		t.Helper()
		r, err := e.svc.CheckCloseOverdue(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	before := check(ctx)
	inWindow := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, inWindow.ID, StatusExpired, now.Add(-RenewalWindow+time.Hour))
	closed := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, closed.ID, StatusExpired, now.Add(-RenewalWindow-time.Hour))
	e.exec(t, ctx, `UPDATE subscriptions SET renewal_closed_at = ? WHERE id = ?`, now.Add(-time.Minute), closed.ID)
	integrityDelta(t, "窗口里与已关窗", before, check(ctx), 0, closed.ID)

	open := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, open.ID, StatusExpired, now.Add(-RenewalWindow-time.Hour))
	integrityDelta(t, "过期满窗口未关", before, check(ctx), 1, open.ID)
}
