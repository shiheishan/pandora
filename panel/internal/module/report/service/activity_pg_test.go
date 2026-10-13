package service

import (
	"testing"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

func TestActivityRollupPG(t *testing.T) {
	e, ctx := newPGEnv(t)
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	today := midnight(time.Now(), sh)
	y := addDays(today, -1)
	yy := addDays(today, -2)

	e.ports.activityDays = []auditsvc.ActivityDay{
		{Day: dayString(y), Registered: 2, Logins: 1, UniqueIPs: 2},
		{Day: dayString(yy), Orders: 1},
	}
	e.ports.activeUsers = []subsvc.ActiveUsersDay{{Day: dayString(y), Users: 5}}

	if err := e.svc.RefreshActivity(ctx); err != nil {
		t.Fatal(err)
	}
	var reg, logins, orders, ips, active int64
	if err := e.row(ctx,
		`SELECT registered, logins, orders, unique_ips, active_users FROM activity_daily WHERE tz = 'Asia/Shanghai' AND day = ?::date`,
		dayString(y)).Scan(&reg, &logins, &orders, &ips, &active); err != nil {
		t.Fatal(err)
	}
	if reg != 2 || logins != 1 || orders != 0 || ips != 2 || active != 5 {
		t.Fatalf("y row: reg=%d log=%d ord=%d ip=%d act=%d", reg, logins, orders, ips, active)
	}
	if err := e.row(ctx,
		`SELECT orders FROM activity_daily WHERE tz = 'Asia/Shanghai' AND day = ?::date`,
		dayString(yy)).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if orders != 1 {
		t.Fatalf("yy orders: %d", orders)
	}

	for i := range e.ports.activityDays {
		if e.ports.activityDays[i].Day == dayString(y) {
			e.ports.activityDays[i].Registered = 3
		}
		if e.ports.activityDays[i].Day == dayString(yy) {
			e.ports.activityDays[i].Orders = 9
		}
	}
	if err := e.svc.RefreshActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx,
		`SELECT registered, orders FROM activity_daily WHERE tz = 'Asia/Shanghai' AND day = ?::date`,
		dayString(y)).Scan(&reg, &orders); err != nil {
		t.Fatal(err)
	}
	if reg != 3 {
		t.Fatalf("y registered after refresh: %d", reg)
	}
	if err := e.row(ctx,
		`SELECT orders FROM activity_daily WHERE tz = 'Asia/Shanghai' AND day = ?::date`,
		dayString(yy)).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if orders != 1 {
		t.Fatalf("yy orders should stay finalized: %d", orders)
	}

	series, err := e.svc.ActivityTimeseries(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 3 {
		t.Fatalf("series len: %d", len(series))
	}
	if series[0].Orders != 1 {
		t.Fatalf("first point orders: %d", series[0].Orders)
	}

	oldDay := dayString(addDays(today, -401))
	if err := e.exec(ctx,
		`INSERT INTO activity_daily (tz, day, registered, logins, orders, unique_ips, active_users, computed_at) VALUES ('Asia/Shanghai', ?::date, 1, 0, 0, 0, 0, now())`,
		oldDay); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.PurgeActivity(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("purge n: %d", n)
	}
	var cnt int64
	if err := e.row(ctx, `SELECT count(*) FROM activity_daily WHERE tz = 'Asia/Shanghai' AND day = ?::date`, oldDay).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatalf("old row count: %d", cnt)
	}
}
