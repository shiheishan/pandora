package service

import (
	"testing"

	"github.com/google/uuid"
)

func TestMySubscriptionsNextUsagePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionV1 := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	e.exec(t, ctx, `UPDATE plans SET current_version_id = ?, status = 'active' WHERE id = ?`, versionV1, planID)
	provision(t, e, ctx, user, planID, versionV1, "")
	newVersion(t, e, ctx, planID, 2, quotaSpec{MetricTraffic, PeriodCycle, i64(200 * gib)})

	mine, err := e.svc.MySubscriptions(userCtx(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Subscriptions) != 1 {
		t.Fatalf("订阅数 %d", len(mine.Subscriptions))
	}
	s := mine.Subscriptions[0]
	if s.Usage.TrafficBytes == nil || *s.Usage.TrafficBytes != 100*gib {
		t.Fatalf("Usage 流量 %+v", s.Usage.TrafficBytes)
	}
	if s.NextUsage == nil || s.NextUsage.TrafficBytes == nil || *s.NextUsage.TrafficBytes != 200*gib {
		t.Fatalf("NextUsage %+v", s.NextUsage)
	}
}

func TestMySubscriptionsNextUsageOneTimePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionV1 := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(100 * gib)})
	e.exec(t, ctx, `UPDATE plans SET current_version_id = ?, status = 'active' WHERE id = ?`, versionV1, planID)
	provisionOneTime(t, e, ctx, user, planID, versionV1, nil)
	newVersion(t, e, ctx, planID, 2, quotaSpec{MetricTraffic, PeriodTotal, i64(300 * gib)})

	mine, err := e.svc.MySubscriptions(userCtx(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Subscriptions) != 1 {
		t.Fatalf("订阅数 %d", len(mine.Subscriptions))
	}
	if mine.Subscriptions[0].NextUsage != nil {
		t.Fatalf("一次性应有 nil NextUsage，得 %+v", mine.Subscriptions[0].NextUsage)
	}
}

func TestMySubscriptionsIsTrialPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	e.exec(t, ctx, `UPDATE plans SET current_version_id = ?, status = 'active' WHERE id = ?`, versionID, planID)
	if err := e.svc.GrantSignupTrial(ctx, user, planID, 48); err != nil {
		t.Fatal(err)
	}
	provision(t, e, ctx, user, planID, versionID, "")

	var trialSubID uuid.UUID
	if err := e.row(ctx, `SELECT subscription_id FROM subscription_trials WHERE user_id = ?`, user).Scan(&trialSubID); err != nil {
		t.Fatal(err)
	}

	mine, err := e.svc.MySubscriptions(userCtx(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Subscriptions) != 2 {
		t.Fatalf("订阅数 %d", len(mine.Subscriptions))
	}
	for _, s := range mine.Subscriptions {
		want := s.ID == trialSubID
		if s.IsTrial != want {
			t.Fatalf("订阅 %s IsTrial=%v 期望 %v", s.ID, s.IsTrial, want)
		}
	}
}
