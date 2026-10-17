package service

import (
	"context"
	"errors"
	"testing"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/report/model"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestActivityTimeseriesUsesStoredRows(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	e.store.activity["Asia/Shanghai|2026-03-05"] = model.ActivityDaily{
		TZ: "Asia/Shanghai", Day: "2026-03-05", Registered: 9,
		ComputedAt: time.Date(2026, 3, 8, 0, 0, 0, 0, sh),
	}
	e.store.activity["Asia/Shanghai|2026-03-09"] = model.ActivityDaily{
		TZ: "Asia/Shanghai", Day: "2026-03-09", Registered: 99,
		ComputedAt: testNow,
	}
	e.ports.activityDays = []auditsvc.ActivityDay{
		{Day: "2026-03-09", Registered: 4},
		{Day: "2026-03-10", Logins: 2},
	}
	e.ports.activeUsers = []subsvc.ActiveUsersDay{{Day: "2026-03-10", Users: 3}}

	got, err := e.svc.ActivityTimeseries(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("points: got %d want 7", len(got))
	}
	wantDays := []string{"03-04", "03-05", "03-06", "03-07", "03-08", "03-09", "03-10"}
	for i, w := range wantDays {
		if got[i].Day != w {
			t.Fatalf("day[%d]: %s want %s", i, got[i].Day, w)
		}
	}
	if got[1].Registered != 9 {
		t.Fatalf("03-05 registered: %d", got[1].Registered)
	}
	if got[5].Registered != 99 {
		t.Fatalf("03-09 registered: %d want 99", got[5].Registered)
	}
	if got[6].Logins != 2 || got[6].ActiveUsers != 3 {
		t.Fatalf("03-10: logins=%d active=%d", got[6].Logins, got[6].ActiveUsers)
	}
	for i, pt := range got {
		if i == 1 || i == 5 || i == 6 {
			continue
		}
		if pt.Registered != 0 || pt.Logins != 0 || pt.Orders != 0 || pt.UniqueIPs != 0 || pt.ActiveUsers != 0 {
			t.Fatalf("%s should be zero: %+v", pt.Day, pt)
		}
	}

	if len(e.ports.activityCalls) != 1 {
		t.Fatalf("activityCalls: %d want 1", len(e.ports.activityCalls))
	}
	wantFrom := time.Date(2026, 3, 4, 0, 0, 0, 0, sh)
	wantTo := time.Date(2026, 3, 11, 0, 0, 0, 0, sh)
	call := e.ports.activityCalls[0]
	if !call[0].Equal(wantFrom) || !call[1].Equal(wantTo) {
		t.Fatalf("window: from=%v to=%v want %v %v", call[0], call[1], wantFrom, wantTo)
	}
	if len(e.ports.actions) != 1 {
		t.Fatal("expected one actions record")
	}
	if e.ports.actions[0].Login != "user.login" || e.ports.actions[0].Registered != "user.registered" || e.ports.actions[0].OrderCreated != "order.created" {
		t.Fatalf("actions: %+v", e.ports.actions[0])
	}
}

func TestActivityTimeseriesAllStoredNoLiveCall(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	finalAt := time.Date(2026, 3, 12, 0, 0, 0, 0, sh)
	for i, day := range []string{"2026-03-04", "2026-03-05", "2026-03-06", "2026-03-07", "2026-03-08", "2026-03-09", "2026-03-10"} {
		computedAt := finalAt
		if i >= 5 { // 包含未定稿的行
			computedAt = testNow
		}
		e.store.activity["Asia/Shanghai|"+day] = model.ActivityDaily{
			TZ: "Asia/Shanghai", Day: day, ComputedAt: computedAt,
		}
	}
	_, err = e.svc.ActivityTimeseries(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.ports.activityCalls) != 0 {
		t.Fatalf("activityCalls: %d", len(e.ports.activityCalls))
	}
}

func TestActivityTimeseriesTodayFromStore(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{"2026-03-04", "2026-03-05", "2026-03-06", "2026-03-07", "2026-03-08", "2026-03-09"} {
		e.store.activity["Asia/Shanghai|"+day] = model.ActivityDaily{
			TZ: "Asia/Shanghai", Day: day, ComputedAt: time.Date(2026, 3, 10, 0, 0, 0, 0, sh),
		}
	}
	e.store.activity["Asia/Shanghai|2026-03-10"] = model.ActivityDaily{
		TZ: "Asia/Shanghai", Day: "2026-03-10", Logins: 5,
		ComputedAt: testNow.Add(-10 * time.Minute),
	}

	got, err := e.svc.ActivityTimeseries(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("points: got %d want 7", len(got))
	}
	last := got[len(got)-1]
	if last.Day != "03-10" || last.Logins != 5 {
		t.Fatalf("today point: %+v want logins=5", last)
	}
	if len(e.ports.activityCalls) != 0 {
		t.Fatalf("activityCalls: %d want 0", len(e.ports.activityCalls))
	}
}

func TestActivityTimeseriesValidation(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()

	got, err := e.svc.ActivityTimeseries(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 14 {
		t.Fatalf("days 0: got %d want 14", len(got))
	}

	for _, days := range []int{91, -1} {
		_, err := e.svc.ActivityTimeseries(ctx, days)
		if err == nil {
			t.Fatalf("days %d: expected error", days)
		}
		var ve *errs.Error
		if !errors.As(err, &ve) || ve.Code != errs.ValidationFailed {
			t.Fatalf("days %d: %v", days, err)
		}
	}
}

func TestRefreshActivity(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	e.ports.activityDays = []auditsvc.ActivityDay{
		{Day: "2026-03-08", Registered: 1, Logins: 2, Orders: 0, UniqueIPs: 3},
		{Day: "2026-03-09", Orders: 4},
		{Day: "2026-03-10", Logins: 8},
	}
	e.ports.activeUsers = []subsvc.ActiveUsersDay{
		{Day: "2026-03-08", Users: 5},
		{Day: "2026-03-09", Users: 6},
		{Day: "2026-03-10", Users: 9},
	}
	// 大前天及更早的日子都已定稿，本用例只看前天、昨天与今天（补漏见 TestRefreshActivityBackfillMax7Days）
	for d := time.Date(2026, 3, 7, 0, 0, 0, 0, sh); !d.Before(time.Date(2026, 3, 10, 0, 0, 0, 0, sh).AddDate(0, 0, -90)); d = d.AddDate(0, 0, -1) {
		day := d.Format("2006-01-02")
		e.store.activity["Asia/Shanghai|"+day] = model.ActivityDaily{TZ: "Asia/Shanghai", Day: day, ComputedAt: testNow}
	}

	if err := e.svc.RefreshActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.store.upserts) != 1 || len(e.store.upserts[0]) != 3 {
		t.Fatalf("first upsert: %d batches, rows=%v", len(e.store.upserts), e.store.upserts)
	}
	for _, row := range e.store.upserts[0] {
		if row.TZ != "Asia/Shanghai" || row.ComputedAt != testNow {
			t.Fatalf("row meta: %+v", row)
		}
	}
	byDay := map[string]model.ActivityDaily{}
	for _, row := range e.store.upserts[0] {
		byDay[row.Day] = row
	}
	if byDay["2026-03-08"].Registered != 1 || byDay["2026-03-08"].ActiveUsers != 5 {
		t.Fatalf("03-08: %+v", byDay["2026-03-08"])
	}
	if byDay["2026-03-09"].Orders != 4 || byDay["2026-03-09"].ActiveUsers != 6 {
		t.Fatalf("03-09: %+v", byDay["2026-03-09"])
	}
	if byDay["2026-03-10"].Logins != 8 || byDay["2026-03-10"].ActiveUsers != 9 {
		t.Fatalf("03-10: %+v", byDay["2026-03-10"])
	}
	wantFrom := time.Date(2026, 3, 8, 0, 0, 0, 0, sh)
	wantTo := time.Date(2026, 3, 11, 0, 0, 0, 0, sh)
	if len(e.ports.activityCalls) != 1 {
		t.Fatal("expected one activity call")
	}
	call := e.ports.activityCalls[0]
	if !call[0].Equal(wantFrom) || !call[1].Equal(wantTo) {
		t.Fatalf("Refresh window: %v %v want %v %v", call[0], call[1], wantFrom, wantTo)
	}

	e.ports.activityDays = []auditsvc.ActivityDay{
		{Day: "2026-03-09", Registered: 7},
		{Day: "2026-03-10", Logins: 10},
	}
	e.ports.activeUsers = nil
	if err := e.svc.RefreshActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.store.upserts) != 2 || len(e.store.upserts[1]) != 2 {
		t.Fatalf("second upsert: %+v", e.store.upserts)
	}
	secondByDay := map[string]model.ActivityDaily{}
	for _, row := range e.store.upserts[1] {
		secondByDay[row.Day] = row
	}
	if secondByDay["2026-03-09"].Registered != 7 {
		t.Fatalf("second 03-09 row: %+v", secondByDay["2026-03-09"])
	}
	if secondByDay["2026-03-10"].Logins != 10 {
		t.Fatalf("second 03-10 row: %+v", secondByDay["2026-03-10"])
	}

	e.store.activity["Asia/Shanghai|2026-03-09"] = model.ActivityDaily{
		TZ: "Asia/Shanghai", Day: "2026-03-09",
		ComputedAt: time.Date(2026, 3, 11, 0, 0, 0, 0, sh),
	}
	if err := e.svc.RefreshActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.store.upserts) != 3 || len(e.store.upserts[2]) != 1 {
		t.Fatalf("third refresh should upsert 1 row (today): %+v", e.store.upserts)
	}
	if e.store.upserts[2][0].Day != "2026-03-10" {
		t.Fatalf("third upsert row day: %s want 2026-03-10", e.store.upserts[2][0].Day)
	}
	thirdCall := e.ports.activityCalls[len(e.ports.activityCalls)-1]
	wantThirdFrom := time.Date(2026, 3, 10, 0, 0, 0, 0, sh)
	wantThirdTo := time.Date(2026, 3, 11, 0, 0, 0, 0, sh)
	if !thirdCall[0].Equal(wantThirdFrom) || !thirdCall[1].Equal(wantThirdTo) {
		t.Fatalf("third refresh window: %v %v want %v %v", thirdCall[0], thirdCall[1], wantThirdFrom, wantThirdTo)
	}
}

func TestRefreshActivityBackfillMax7Days(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}

	// 昨天（03-09）和前天（03-08）设为已定稿
	finalAt := time.Date(2026, 3, 12, 0, 0, 0, 0, sh)
	e.store.activity["Asia/Shanghai|2026-03-08"] = model.ActivityDaily{
		TZ: "Asia/Shanghai", Day: "2026-03-08", ComputedAt: finalAt,
	}
	e.store.activity["Asia/Shanghai|2026-03-09"] = model.ActivityDaily{
		TZ: "Asia/Shanghai", Day: "2026-03-09", ComputedAt: finalAt,
	}

	// 历史从 02-20 到 03-07 全部缺失（超过 7 天）
	e.ports.activityDays = []auditsvc.ActivityDay{
		{Day: "2026-03-07", Registered: 10},
		{Day: "2026-03-06", Registered: 20},
		{Day: "2026-03-05", Registered: 30},
		{Day: "2026-03-04", Registered: 40},
		{Day: "2026-03-03", Registered: 50},
		{Day: "2026-03-02", Registered: 60},
		{Day: "2026-03-01", Registered: 70},
		{Day: "2026-02-28", Registered: 80},
	}

	if err := e.svc.RefreshActivity(ctx); err != nil {
		t.Fatal(err)
	}

	if len(e.store.upserts) != 1 {
		t.Fatalf("expected 1 upsert batch, got %d", len(e.store.upserts))
	}
	batch := e.store.upserts[0]
	// 前天昨天已定稿，历史缺漏最多补 7 天（03-01 到 03-07）加上今天（03-10），共 8 行
	if len(batch) != 8 {
		t.Fatalf("expected 8 rows (7 backfilled + today), got %d", len(batch))
	}
	wantDays := []string{
		"2026-03-01", "2026-03-02", "2026-03-03", "2026-03-04",
		"2026-03-05", "2026-03-06", "2026-03-07", "2026-03-10",
	}
	for i, w := range wantDays {
		if batch[i].Day != w {
			t.Fatalf("batch[%d].Day = %q, want %q", i, batch[i].Day, w)
		}
	}
}

func TestPurgeActivity(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	e.store.purgeLeft = 7

	n, err := e.svc.PurgeActivity(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("n: %d", n)
	}
	if len(e.store.purgeCalls) != 1 || e.store.purgeCalls[0] != "2025-02-03" {
		t.Fatalf("purge before: %v", e.store.purgeCalls)
	}
}

func TestActivityPortError(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	e.ports.errFor["ActivityByDay"] = errBoom

	_, err := e.svc.ActivityTimeseries(ctx, 7)
	if err != errBoom {
		t.Fatalf("err: %v", err)
	}
}
