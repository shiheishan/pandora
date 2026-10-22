package service

import (
	"context"
	"errors"
	"testing"
	"time"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/aegispanel/aegis/internal/module/report/model"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestRevenueTimeseriesFillsAndPrevious(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	e.ports.revenueDays = []ledgersvc.RevenueDay{
		{Day: "2026-03-10", Credit: dec("100"), Debit: dec("10")},
		{Day: "2026-03-04", Credit: dec("50")},
		{Day: "2026-03-02", Credit: dec("30")},
	}
	e.store.adjustments = append(e.store.adjustments, model.Adjustment{
		Amount:      dec("-5"),
		EffectiveOn: "2026-03-09",
	})

	got, err := e.svc.RevenueTimeseries(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 7 {
		t.Fatalf("points: got %d want 7", len(got.Points))
	}
	if got.Points[0].Date != "2026-03-04" {
		t.Fatalf("first date: %s", got.Points[0].Date)
	}
	if got.Points[6].Date != "2026-03-10" {
		t.Fatalf("last date: %s", got.Points[6].Date)
	}
	last := got.Points[6]
	if !last.DisplayedNet.Equal(dec("90")) {
		t.Fatalf("last DisplayedNet: %s", last.DisplayedNet)
	}
	var p09 *RevenuePoint
	for i := range got.Points {
		if got.Points[i].Date == "2026-03-09" {
			p09 = &got.Points[i]
			break
		}
	}
	if p09 == nil {
		t.Fatal("missing 2026-03-09 point")
	}
	if !p09.Adjustment.Equal(dec("-5")) || !p09.DisplayedNet.Equal(dec("-5")) {
		t.Fatalf("2026-03-09: adj=%s net=%s", p09.Adjustment, p09.DisplayedNet)
	}
	if !got.PreviousTotal.Equal(dec("30")) {
		t.Fatalf("PreviousTotal: %s", got.PreviousTotal)
	}

	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := time.Date(2026, 2, 25, 0, 0, 0, 0, sh) // 上一窗口 7 天：02-25 … 03-03（2026 年 2 月 28 天）
	wantTo := time.Date(2026, 3, 11, 0, 0, 0, 0, sh)
	if len(e.ports.revenueCalls) < 1 {
		t.Fatal("no RevenueByDay call")
	}
	call := e.ports.revenueCalls[0]
	if !call[0].Equal(wantFrom) || !call[1].Equal(wantTo) {
		t.Fatalf("RevenueByDay window: from=%v to=%v want %v %v", call[0], call[1], wantFrom, wantTo)
	}
}

func TestRevenueTimeseriesValidation(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()

	_, err := e.svc.RevenueTimeseries(ctx, 14)
	if err == nil {
		t.Fatal("expected error for days 14")
	}
	var ve *errs.Error
	if !errors.As(err, &ve) || ve.Code != errs.ValidationFailed {
		t.Fatalf("days 14: %v", err)
	}

	got, err := e.svc.RevenueTimeseries(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 30 {
		t.Fatalf("days 0: got %d points want 30", len(got.Points))
	}
}

func TestOverviewRevenueRows(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	e.ports.revenueDays = []ledgersvc.RevenueDay{
		{Day: "2026-03-10", Credit: dec("100"), Debit: dec("10")},
		{Day: "2026-03-09", Credit: dec("20")},
		{Day: "2026-03-04", Credit: dec("50")},
	}
	e.ports.revenueTotal = ledgersvc.RevenueTotal{
		Credit: dec("1000"), Debit: dec("100"),
	}
	e.store.adjustments = []model.Adjustment{
		{Amount: dec("3"), EffectiveOn: "2026-03-10"},
		{Amount: dec("1000"), EffectiveOn: "2026-01-01"},
	}

	ov, err := e.svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rev := ov.Revenue
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"ActualToday", rev.ActualToday.String(), "90"},
		{"ActualYesterday", rev.ActualYesterday.String(), "20"},
		{"Actual7Days", rev.Actual7Days.String(), "160"},
		{"ActualTotal", rev.ActualTotal.String(), "900"},
		{"AdjustmentToday", rev.AdjustmentToday.String(), "3"},
		{"AdjustmentTotal", rev.AdjustmentTotal.String(), "1003"},
		{"Today", rev.Today.String(), "93"},
		{"Total", rev.Total.String(), "1903"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Fatalf("%s: got %s want %s", c.name, c.got, c.want)
		}
	}
}

func TestOverviewCountsMapping(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	e.ports.userCounts = identitysvc.ReportUserCounts{Total: 1, Active: 2, CreatedSince: 3, CreatedRecent: 4}
	e.ports.subCounts = subsvc.ReportSubscriptionCounts{Live: 10, Expiring: 11, NotLive: 12, NewLive: 13}
	e.ports.nodeCounts = nodesvc.ReportNodeCounts{Total: 20, Online: 21}
	e.ports.orderCounts = ordersvc.ReportOrderCounts{PaidSince: 30, Pending: 31, ClosedSince: 32}
	e.ports.drift = 99

	ov, err := e.svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Users.Total != 1 || ov.Users.Active != 2 || ov.Users.Today != 3 || ov.Users.Last7Days != 4 {
		t.Fatalf("users: %+v", ov.Users)
	}
	if ov.Subscriptions.Active != 10 ||
		ov.Subscriptions.Expiring7Days != 11 || ov.Subscriptions.Expired != 12 || ov.Subscriptions.New7Days != 13 {
		t.Fatalf("subs: %+v", ov.Subscriptions)
	}
	if ov.Nodes.Total != 20 || ov.Nodes.Online != 21 {
		t.Fatalf("nodes: %+v", ov.Nodes)
	}
	if ov.Orders.PaidToday != 30 || ov.Orders.Pending != 31 || ov.Orders.FailedToday != 32 {
		t.Fatalf("orders: %+v", ov.Orders)
	}
	if ov.LedgerDriftAccounts != 99 {
		t.Fatalf("drift: %d", ov.LedgerDriftAccounts)
	}
}

func TestOverviewPortError(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	e.ports.errFor["CheckDrift"] = errBoom

	_, err := e.svc.Overview(ctx)
	if !errors.Is(err, errBoom) {
		t.Fatalf("got %v", err)
	}
}
