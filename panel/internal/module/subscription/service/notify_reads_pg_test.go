package service

import (
	"slices"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func TestExpiringForNotifyPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	planID, verID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	now := time.Now().UTC().Truncate(time.Microsecond)

	after := now
	until := now.Add(7 * 24 * time.Hour)

	u := newUser(t, e, ctx)
	inWin := provision(t, e, ctx, u, planID, verID, "窗内")
	expireSetPeriod(t, e, ctx, inWin.ID, StatusActive, until.Add(-1*time.Hour))

	atAfter := provision(t, e, ctx, newUser(t, e, ctx), planID, verID, "左边界")
	expireSetPeriod(t, e, ctx, atAfter.ID, StatusActive, after)

	atUntil := provision(t, e, ctx, newUser(t, e, ctx), planID, verID, "右边界")
	expireSetPeriod(t, e, ctx, atUntil.ID, StatusActive, until)

	pastUntil := provision(t, e, ctx, newUser(t, e, ctx), planID, verID, "窗外")
	expireSetPeriod(t, e, ctx, pastUntil.ID, StatusActive, until.Add(time.Hour))

	expiredIn := provision(t, e, ctx, newUser(t, e, ctx), planID, verID, "已过期")
	expireSetPeriod(t, e, ctx, expiredIn.ID, StatusExpired, until.Add(-2*time.Hour))

	list, err := e.svc.ExpiringForNotify(ctx, after, until)
	if err != nil {
		t.Fatal(err)
	}
	ids := candidateSubIDs(list)
	if !slices.Contains(ids, inWin.ID) || !slices.Contains(ids, atUntil.ID) {
		t.Fatalf("窗内与右边界应出现，得 %v", ids)
	}
	if slices.Contains(ids, atAfter.ID) || slices.Contains(ids, pastUntil.ID) || slices.Contains(ids, expiredIn.ID) {
		t.Fatalf("左开边界、窗外、expired 不应出现，得 %v", ids)
	}
}

func TestExpiredForNotifyPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	planID, verID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	now := time.Now().UTC().Truncate(time.Microsecond)

	endedAfter := now.Add(-30 * 24 * time.Hour)
	endedUntil := now

	inWin := provision(t, e, ctx, newUser(t, e, ctx), planID, verID, "窗内")
	endIn := endedUntil.Add(-2 * time.Hour)
	expireSetPeriod(t, e, ctx, inWin.ID, StatusExpired, endIn)

	closed := provision(t, e, ctx, newUser(t, e, ctx), planID, verID, "已关窗")
	expireSetPeriod(t, e, ctx, closed.ID, StatusExpired, endIn)
	e.exec(t, ctx, `UPDATE subscriptions SET renewal_closed_at = now() WHERE id = ?`, closed.ID)

	uLive := newUser(t, e, ctx)
	dead := provision(t, e, ctx, uLive, planID, verID, "过期份")
	expireSetPeriod(t, e, ctx, dead.ID, StatusExpired, endIn)
	live := provision(t, e, ctx, uLive, planID, verID, "仍生效")
	expireSetPeriod(t, e, ctx, live.ID, StatusActive, now.Add(48*time.Hour))

	all, err := e.svc.ExpiredForNotify(ctx, endedAfter, endedUntil, false)
	if err != nil {
		t.Fatal(err)
	}
	allIDs := candidateSubIDs(all)
	if !slices.Contains(allIDs, inWin.ID) || !slices.Contains(allIDs, dead.ID) {
		t.Fatalf("关窗=false 应含窗内与另有生效订阅用户的过期份，得 %v", allIDs)
	}
	if slices.Contains(allIDs, closed.ID) {
		t.Fatal("renewal_closed_at 非空不应出现")
	}

	onlyDead, err := e.svc.ExpiredForNotify(ctx, endedAfter, endedUntil, true)
	if err != nil {
		t.Fatal(err)
	}
	onlyIDs := candidateSubIDs(onlyDead)
	if slices.Contains(onlyIDs, dead.ID) {
		t.Fatalf("onlyWithoutLive 应排除另有生效订阅的用户，得 %v", onlyIDs)
	}
	if !slices.Contains(onlyIDs, inWin.ID) {
		t.Fatalf("无其它生效订阅的过期份应保留，得 %v", onlyIDs)
	}
}

func TestQuotaUsageForNotifyPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	planLim, verLim := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	planUnlim, verUnlim := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, nil})

	below := provision(t, e, ctx, newUser(t, e, ctx), planLim, verLim, "未达线")
	setConsumed(t, e, ctx, below.ID, MetricTraffic, PeriodCycle, 50*gib)

	onLine := provision(t, e, ctx, newUser(t, e, ctx), planLim, verLim, "达线")
	setConsumed(t, e, ctx, onLine.ID, MetricTraffic, PeriodCycle, 80*gib)

	uPack := newUser(t, e, ctx)
	withPack := provision(t, e, ctx, uPack, planLim, verLim, "包拉回")
	setConsumed(t, e, ctx, withPack.ID, MetricTraffic, PeriodCycle, 90*gib)
	e.exec(t, ctx, `INSERT INTO traffic_pack_grants (user_id, subscription_id, source, source_id, granted_bytes, consumed_bytes)
		VALUES (?, ?, 'admin', ?, ?, 0)`, uPack, withPack.ID, uuid.New(), 50*gib)

	uPackHit := newUser(t, e, ctx)
	packStillHigh := provision(t, e, ctx, uPackHit, planLim, verLim, "包仍跨线")
	setConsumed(t, e, ctx, packStillHigh.ID, MetricTraffic, PeriodCycle, 90*gib)
	e.exec(t, ctx, `INSERT INTO traffic_pack_grants (user_id, subscription_id, source, source_id, granted_bytes, consumed_bytes)
		VALUES (?, ?, 'admin', ?, ?, 0)`, uPackHit, packStillHigh.ID, uuid.New(), 10*gib)

	unlim := provision(t, e, ctx, newUser(t, e, ctx), planUnlim, verUnlim, "不限量")
	setConsumed(t, e, ctx, unlim.ID, MetricTraffic, PeriodCycle, 90*gib)

	list, err := e.svc.QuotaUsageForNotify(ctx, 80)
	if err != nil {
		t.Fatal(err)
	}
	bySub := quotaBySubID(list)
	if _, ok := bySub[below.ID]; ok {
		t.Fatal("未达 80% 不应出现")
	}
	if _, ok := bySub[unlim.ID]; ok {
		t.Fatal("limit_value 为空不应出现")
	}
	if _, ok := bySub[withPack.ID]; ok {
		t.Fatal("流量包把可用总量抬高后不应再跨 80% 线")
	}
	qOn, ok := bySub[onLine.ID]
	if !ok || qOn.Total != 100*gib || qOn.Consumed != 80*gib {
		t.Fatalf("达线行: ok=%v q=%+v", ok, qOn)
	}
	qPack, ok := bySub[packStillHigh.ID]
	if !ok || qPack.Total != 100*gib+10*gib {
		t.Fatalf("跨线且 Total 含包余量: ok=%v q=%+v", ok, qPack)
	}

	_, errPct := e.svc.QuotaUsageForNotify(ctx, 0)
	if errPct == nil {
		t.Fatal("minPercent=0 应报错")
	}
	if errs.As(errPct).Code != errs.Internal {
		t.Fatalf("期望 internal_error，得 %v", errPct)
	}
}

func TestLivePlanIDsExpiryPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	planA, verA := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	planB, verB := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	now := time.Now().UTC().Truncate(time.Microsecond)

	emptyUser := newUser(t, e, ctx)
	ids0, err := e.svc.LivePlanIDs(ctx, emptyUser)
	if err != nil {
		t.Fatal(err)
	}
	if ids0 == nil || len(ids0) != 0 {
		t.Fatalf("无订阅应为空切片，得 %v", ids0)
	}

	u := newUser(t, e, ctx)
	s1 := provision(t, e, ctx, u, planA, verA, "a1")
	s2 := provision(t, e, ctx, u, planA, verA, "a2")
	s3 := provision(t, e, ctx, u, planB, verB, "b1")
	expireSetPeriod(t, e, ctx, s3.ID, StatusExpired, now.Add(-time.Hour))
	expireSetPeriod(t, e, ctx, s2.ID, StatusActive, now.Add(-time.Hour)) // 已到期，不算生效

	ids, err := e.svc.LivePlanIDs(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != planA {
		t.Fatalf("应只剩 planA 一份生效，得 %v (s1=%s s2=%s)", ids, s1.ID, s2.ID)
	}
}

func candidateSubIDs(list []NotifyCandidate) []uuid.UUID {
	out := make([]uuid.UUID, len(list))
	for i, c := range list {
		out[i] = c.SubscriptionID
	}
	return out
}

func quotaBySubID(list []QuotaNotice) map[uuid.UUID]QuotaNotice {
	m := make(map[uuid.UUID]QuotaNotice, len(list))
	for _, q := range list {
		m[q.SubscriptionID] = q
	}
	return m
}
