package service

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestReportSubscriptionCountsPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	planID, verID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiringBefore := now.Add(7 * 24 * time.Hour)
	createdSince := now.Add(-7 * 24 * time.Hour)

	c0, err := e.svc.ReportSubscriptionCounts(ctx, now, expiringBefore, createdSince)
	if err != nil {
		t.Fatal(err)
	}

	u1 := newUser(t, e, ctx)
	sExp := provision(t, e, ctx, u1, planID, verID, "快到期")
	expireSetPeriod(t, e, ctx, sExp.ID, StatusActive, now.Add(3*24*time.Hour))

	u2 := newUser(t, e, ctx)
	sFar := provision(t, e, ctx, u2, planID, verID, "远期")
	expireSetPeriod(t, e, ctx, sFar.ID, StatusActive, now.Add(30*24*time.Hour))

	u3 := newUser(t, e, ctx)
	sDead := provision(t, e, ctx, u3, planID, verID, "已过期")
	expireSetPeriod(t, e, ctx, sDead.ID, StatusExpired, now.Add(-time.Hour))

	c1, err := e.svc.ReportSubscriptionCounts(ctx, now, expiringBefore, createdSince)
	if err != nil {
		t.Fatal(err)
	}
	if d := c1.Live - c0.Live; d != 2 {
		t.Fatalf("Live 相对 c0 应 +2，得 %d (c0=%d c1=%d)", d, c0.Live, c1.Live)
	}
	if d := c1.Expiring - c0.Expiring; d != 1 {
		t.Fatalf("Expiring 相对 c0 应 +1，得 %d", d)
	}
	if d := c1.NotLive - c0.NotLive; d != 1 {
		t.Fatalf("NotLive 相对 c0 应 +1，得 %d", d)
	}
	if d := c1.NewLive - c0.NewLive; d != 2 {
		t.Fatalf("NewLive 相对 c0 应 +2，得 %d", d)
	}
}

func TestOwnersBySubscriptionPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	planID, verID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})

	uA := newUser(t, e, ctx)
	sA := provision(t, e, ctx, uA, planID, verID, "a")
	uB := newUser(t, e, ctx)
	sB := provision(t, e, ctx, uB, planID, verID, "b")
	ghost := uuid.New()

	m, err := e.svc.OwnersBySubscription(ctx, []uuid.UUID{sA.ID, sB.ID, ghost})
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("map 应恰好两项，得 %d: %v", len(m), m)
	}
	if m[sA.ID] != uA || m[sB.ID] != uB {
		t.Fatalf("主人不对: %v", m)
	}

	empty, err := e.svc.OwnersBySubscription(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("空切片应空 map，得 %#v", empty)
	}
}

func TestActiveUsersByDayPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	planID, verID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 5, 10, 0, 0, 0, 0, loc)
	to := time.Date(2026, 5, 12, 0, 0, 0, 0, loc)
	fetchAt := time.Date(2026, 5, 10, 12, 0, 0, 0, loc)

	uA := newUser(t, e, ctx)
	sA1 := provision(t, e, ctx, uA, planID, verID, "a1")
	sA2 := provision(t, e, ctx, uA, planID, verID, "a2")
	uB := newUser(t, e, ctx)
	sB := provision(t, e, ctx, uB, planID, verID, "b")

	credOf := func(subID uuid.UUID) uuid.UUID {
		var credID uuid.UUID
		if err := e.row(ctx, `SELECT id FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`, subID).Scan(&credID); err != nil {
			t.Fatalf("读凭据 %s: %v", subID, err)
		}
		return credID
	}
	insertFetch := func(credID, subID uuid.UUID, at time.Time) {
		e.exec(t, ctx, `INSERT INTO subscription_fetch_log (credential_id, subscription_id, ip_hash, ua_family, result, fetched_at)
			VALUES (?, ?, ?, 'clash', ?, ?)`, credID, subID, bytes.Repeat([]byte{0x01}, 32), FetchOK, at)
	}
	insertFetch(credOf(sA1.ID), sA1.ID, fetchAt)
	insertFetch(credOf(sA2.ID), sA2.ID, fetchAt)

	e.exec(t, ctx, `INSERT INTO subscription_usage_daily (subscription_id, day, bytes) VALUES (?, '2026-05-10'::date, ?)`, sB.ID, 100)
	e.exec(t, ctx, `INSERT INTO subscription_usage_daily (subscription_id, day, bytes) VALUES (?, '2026-05-11'::date, 0)`, sA1.ID)

	list, err := e.svc.ActiveUsersByDay(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Day != "2026-05-10" || list[0].Users != 2 {
		t.Fatalf("活跃用户按日: %+v", list)
	}
}
