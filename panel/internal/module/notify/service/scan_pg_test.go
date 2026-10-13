package service

import (
	"strings"
	"testing"
	"time"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/google/uuid"
)

func scanFixedNow() time.Time {
	return time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
}

func TestScanExpiringWritesInboxAndEmail(t *testing.T) {
	e := newPG(t)
	dir, plans, subs := e.usePorts()
	uid := e.insertTestUser(t, dir)
	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000001")
	subID := uuid.MustParse("018f0000-0000-7000-8000-000000000002")
	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "基础套餐"}}
	periodEnd := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	subs.expiring = []subsvc.NotifyCandidate{{
		SubscriptionID: subID,
		UserID:         uid,
		PlanID:         planID,
		PeriodEnd:      periodEnd,
	}}
	e.svc.now = scanFixedNow

	n, err := e.svc.Scan(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("scan %d %v", n, err)
	}
	if noticeCountInbox(t, e, uid) != 1 {
		t.Fatal("应有 1 条站内信")
	}
	items, err := e.repo.ListInbox(e.ctx, uid, false, 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox %v", err)
	}
	if items[0].TemplateCode != TemplateSubscriptionExpiring || !strings.Contains(items[0].Body, "基础套餐") {
		t.Fatalf("inbox %+v", items[0])
	}
	em := noticeClaimEmail(t, e)
	pe := noticeOpenPayload(t, e, em)
	if pe.Vars["plan"] != "基础套餐" || pe.Vars["days"] != "3" {
		t.Fatalf("email vars %+v", pe.Vars)
	}

	n2, err := e.svc.Scan(e.ctx)
	if err != nil || n2 != 0 {
		t.Fatalf("second scan %d %v", n2, err)
	}
	if noticeCountInbox(t, e, uid) != 1 {
		t.Fatal("不应重复写")
	}
}

func TestScanOrderPaid(t *testing.T) {
	e := newPG(t)
	dir, plans, subs := e.usePorts()
	uid := e.insertTestUser(t, dir)
	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000011")
	subID := uuid.MustParse("018f0000-0000-7000-8000-000000000012")
	orderID := uuid.MustParse("018f0000-0000-7000-8000-000000000013")
	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "进阶套餐"}}
	subs.byID = map[uuid.UUID]subsvc.Subscription{subID: {ID: subID, UserID: uid, PlanID: planID,
		PeriodEnd: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)}}
	paidAt := scanFixedNow().Add(-10 * time.Minute)
	subs.paid = []ordersvc.PaidNotice{
		// 早于首次回看窗口的老订单不补发
		{OrderID: uuid.MustParse("018f0000-0000-7000-8000-000000000010"), UserID: uid, OrderNo: "PD-OLD",
			SubscriptionID: subID, FulfilledAt: scanFixedNow().Add(-3 * time.Hour)},
		{OrderID: orderID, UserID: uid, OrderNo: "PD20260601001", SubscriptionID: subID, FulfilledAt: paidAt},
	}
	e.svc.now = scanFixedNow

	if n, err := e.svc.Scan(e.ctx); err != nil || n != 1 {
		t.Fatalf("scan %d %v", n, err)
	}
	items, err := e.repo.ListInbox(e.ctx, uid, false, 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox %d %v", len(items), err)
	}
	if items[0].TemplateCode != TemplateOrderPaid || !strings.Contains(items[0].Body, "PD20260601001") ||
		!strings.Contains(items[0].Body, "进阶套餐") {
		t.Fatalf("inbox %+v", items[0])
	}
	if n, err := e.svc.Scan(e.ctx); err != nil || n != 0 {
		t.Fatalf("second scan must dedupe per order: %d %v", n, err)
	}
	// 游标已推进：之后履约的单照写，前面的不再重读
	subs.paid = append(subs.paid, ordersvc.PaidNotice{OrderID: uuid.MustParse("018f0000-0000-7000-8000-000000000014"),
		UserID: uid, OrderNo: "PD20260601002", SubscriptionID: subID, FulfilledAt: paidAt.Add(time.Minute)})
	if n, err := e.svc.Scan(e.ctx); err != nil || n != 1 {
		t.Fatalf("scan after cursor %d %v", n, err)
	}
	var at time.Time
	if err := e.scan(e.ctx, &at, `SELECT last_at FROM notify_scan_cursors WHERE name = 'order_paid'`); err != nil ||
		!at.Equal(paidAt.Add(time.Minute)) {
		t.Fatalf("cursor at %v err %v", at, err)
	}
	if noticeCountInbox(t, e, uid) != 2 {
		t.Fatal("old order must not be notified, the two new ones once each")
	}

	// 流量包订单用流量包的文案
	pack := int64(100) << 30
	subs.paid = append(subs.paid, ordersvc.PaidNotice{OrderID: uuid.MustParse("018f0000-0000-7000-8000-000000000015"),
		UserID: uid, OrderNo: "PD20260601003", Kind: ordersvc.KindAddon, TrafficBytes: &pack,
		SubscriptionID: subID, FulfilledAt: paidAt.Add(2 * time.Minute)})
	if n, err := e.svc.Scan(e.ctx); err != nil || n != 1 {
		t.Fatalf("pack scan %d %v", n, err)
	}
	latest, err := e.repo.ListInbox(e.ctx, uid, false, 1)
	if err != nil || len(latest) != 1 || latest[0].TemplateCode != TemplatePackPaid ||
		!strings.Contains(latest[0].Body, "100.0 GB 流量包已加到「进阶套餐」") {
		t.Fatalf("pack inbox %+v %v", latest, err)
	}
}

func TestScanExpiredAndRecall(t *testing.T) {
	e := newPG(t)
	dir, plans, subs := e.usePorts()
	uidExp := e.insertTestUser(t, dir)
	uidRecall := e.insertTestUser(t, dir)
	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000010")
	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "月付"}}
	now := scanFixedNow()
	subs.expired[false] = []subsvc.NotifyCandidate{{
		SubscriptionID: uuid.New(),
		UserID:         uidExp,
		PlanID:         planID,
		PeriodEnd:      now.Add(-2 * time.Hour),
	}}
	subs.expired[true] = []subsvc.NotifyCandidate{{
		SubscriptionID: uuid.New(),
		UserID:         uidRecall,
		PlanID:         planID,
		PeriodEnd:      now.Add(-48 * time.Hour),
	}}
	e.svc.now = scanFixedNow

	n, err := e.svc.Scan(e.ctx)
	if err != nil || n != 2 {
		t.Fatalf("scan %d %v", n, err)
	}
	if noticeCountInbox(t, e, uidExp) != 1 {
		t.Fatal("过期应有站内信")
	}
	if noticeCountInbox(t, e, uidRecall) != 1 {
		t.Fatal("召回应有站内信")
	}
	items, _ := e.repo.ListInbox(e.ctx, uidRecall, false, 5)
	if len(items) != 1 || items[0].TemplateCode != TemplateSubscriptionRecall {
		t.Fatalf("recall %+v", items)
	}
}

func TestScanQuota96Only95Tier(t *testing.T) {
	e := newPG(t)
	dir, plans, subs := e.usePorts()
	uid := e.insertTestUser(t, dir)
	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000020")
	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "流量包"}}
	subs.quota = []subsvc.QuotaNotice{{
		SubscriptionID: uuid.New(),
		UserID:         uid,
		PlanID:         planID,
		Consumed:       96,
		Total:          100,
		PeriodStart:    time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}}
	e.svc.now = scanFixedNow

	n, err := e.svc.Scan(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("scan %d %v", n, err)
	}
	items, _ := e.repo.ListInbox(e.ctx, uid, false, 5)
	if len(items) != 1 || !strings.Contains(items[0].Body, "95%") {
		t.Fatalf("应只发 95 档: %+v", items[0])
	}
}

func TestScanExpiringWindowArgs(t *testing.T) {
	e := newPG(t)
	_, _, subs := e.usePorts()
	now := scanFixedNow()
	e.svc.now = func() time.Time { return now }

	_, err := e.svc.Scan(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.expiringCalls) < 3 {
		t.Fatalf("expiring 应调 3 次窗口，得 %d", len(subs.expiringCalls))
	}
	wantAfter := now.AddDate(0, 0, 1)
	wantUntil := now.AddDate(0, 0, 3)
	for _, call := range subs.expiringCalls {
		if call[0].Equal(wantAfter) && call[1].Equal(wantUntil) {
			return
		}
	}
	t.Fatalf("缺 3 天窗口 after=%v until=%v，calls=%v", wantAfter, wantUntil, subs.expiringCalls)
}
