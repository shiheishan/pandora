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
	"github.com/aegispanel/aegis/internal/pkg/money"
)

func TestRevenueTimeseriesFillsAndPrevious(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	e.ports.revenueDays = []ledgersvc.RevenueDay{
		{Currency: money.CNY, Day: "2026-03-10", Credit: dec("100"), Debit: dec("10")},
		{Currency: money.CNY, Day: "2026-03-04", Credit: dec("50")},
		{Currency: money.USD, Day: "2026-03-10", Credit: dec("7")},
		{Currency: money.CNY, Day: "2026-03-02", Credit: dec("30")},
	}
	e.store.adjustments = append(e.store.adjustments, model.Adjustment{
		Currency:    "CNY",
		Amount:      dec("-5"),
		EffectiveOn: "2026-03-09",
	})

	got, err := e.svc.RevenueTimeseries(ctx, "CNY", 7)
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
	wantFrom := time.Date(2026, 2, 26, 0, 0, 0, 0, sh)
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

	_, err := e.svc.RevenueTimeseries(ctx, "EUR", 7)
	if err == nil {
		t.Fatal("expected error for EUR")
	}
	var ve *errs.Error
	if !errors.As(err, &ve) || ve.Code != errs.ValidationFailed {
		t.Fatalf("EUR: %v", err)
	}

	_, err = e.svc.RevenueTimeseries(ctx, "CNY", 14)
	if err == nil {
		t.Fatal("expected error for days 14")
	}
	if !errors.As(err, &ve) || ve.Code != errs.ValidationFailed {
		t.Fatalf("days 14: %v", err)
	}

	got, err := e.svc.RevenueTimeseries(ctx, "CNY", 0)
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
		{Currency: money.CNY, Day: "2026-03-10", Credit: dec("100"), Debit: dec("10")},
		{Currency: money.CNY, Day: "2026-03-09", Credit: dec("20")},
		{Currency: money.CNY, Day: "2026-03-04", Credit: dec("50")},
		{Currency: money.USD, Day: "2026-03-10", Credit: dec("7")},
	}
	e.ports.revenueTotals = []ledgersvc.RevenueTotal{
		{Currency: money.CNY, Credit: dec("1000"), Debit: dec("100")},
	}
	e.store.adjustments = []model.Adjustment{
		{Currency: "CNY", Amount: dec("3"), EffectiveOn: "2026-03-10"},
		{Currency: "CNY", Amount: dec("1000"), EffectiveOn: "2026-01-01"},
	}

	ov, err := e.svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Revenue) != 2 {
		t.Fatalf("revenue rows: %d", len(ov.Revenue))
	}
	if ov.Revenue[0].Currency != money.CNY || ov.Revenue[1].Currency != money.USD {
		t.Fatalf("currency order: %v %v", ov.Revenue[0].Currency, ov.Revenue[1].Currency)
	}
	cny := ov.Revenue[0]
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"ActualToday", cny.ActualToday.String(), "90"},
		{"ActualYesterday", cny.ActualYesterday.String(), "20"},
		{"Actual7Days", cny.Actual7Days.String(), "160"},
		{"ActualTotal", cny.ActualTotal.String(), "900"},
		{"AdjustmentToday", cny.AdjustmentToday.String(), "3"},
		{"AdjustmentTotal", cny.AdjustmentTotal.String(), "1003"},
		{"Today", cny.Today.String(), "93"},
		{"Total", cny.Total.String(), "1903"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Fatalf("CNY %s: got %s want %s", c.name, c.got, c.want)
		}
	}
	usd := ov.Revenue[1]
	if !usd.ActualToday.Equal(dec("7")) || !usd.Today.Equal(dec("7")) {
		t.Fatalf("USD today: actual=%s display=%s", usd.ActualToday, usd.Today)
	}
	if !usd.ActualTotal.Equal(dec("0")) || !usd.Total.Equal(dec("0")) {
		t.Fatalf("USD totals: actual=%s total=%s", usd.ActualTotal, usd.Total)
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
	if ov.Subscriptions.Active != 10 || ov.Subscriptions.Trialing != 0 ||
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
	e.ports.errFor["DriftAccounts"] = errBoom

	_, err := e.svc.Overview(ctx)
	if !errors.Is(err, errBoom) {
		t.Fatalf("got %v", err)
	}
}
