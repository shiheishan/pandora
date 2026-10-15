package service

import (
	"context"
	"errors"
	"testing"
	"time"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/google/uuid"
)

// failEventStore 让指定去重键的写入失败：返回普通 Go 错误（不是 SQL 错误），不会打断整例回滚的外层事务。
type failEventStore struct {
	store
	failKey string
}

func (f failEventStore) InsertEvent(ctx context.Context, e *model.Event) (bool, error) {
	if e.DedupeKey == f.failKey {
		return false, errors.New("注入的写入失败")
	}
	return f.store.InsertEvent(ctx, e)
}

// TestScanPaidResetAdminOnlyAndCursor：流量重置单夹在两张普通单中间、第二张普通单的用户通知写失败时，
// 游标停在重置单；修好后推进到第二张普通单，管理员收款提醒（重置单套餐一栏带「流量重置 · 」前缀）不重复写。
func TestScanPaidResetAdminOnlyAndCursor(t *testing.T) {
	e := newPG(t)
	dir, plans, subs := e.usePorts()
	uid := e.insertTestUser(t, dir)
	adminChatID := int64(-1001987654321)
	e.st.tg = settingssvc.Telegram{
		Enabled:     true,
		BotUsername: "admin_bot",
		BotToken:    "123:abc",
		HasToken:    true,
		AdminChatID: &adminChatID,
	}
	e.st.tgx = settingssvc.TelegramExtras{
		AdminPaidNotify: true,
	}

	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000501")
	subID := uuid.MustParse("018f0000-0000-7000-8000-000000000502")
	orderA := uuid.MustParse("018f0000-0000-7000-8000-000000000503")
	orderR := uuid.MustParse("018f0000-0000-7000-8000-000000000504")
	orderB := uuid.MustParse("018f0000-0000-7000-8000-000000000505")

	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "重置测试套餐"}}
	subs.byID = map[uuid.UUID]subsvc.Subscription{
		subID: {
			ID:        subID,
			UserID:    uid,
			PlanID:    planID,
			PeriodEnd: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		},
	}
	paidAt := scanFixedNow().Add(-10 * time.Minute)
	subs.paid = []ordersvc.PaidNotice{
		{OrderID: orderA, UserID: uid, OrderNo: "PD20261014101", SubscriptionID: subID, FulfilledAt: paidAt},
		{OrderID: orderR, UserID: uid, OrderNo: "PD20261014102", Kind: ordersvc.KindTrafficReset,
			SubscriptionID: subID, FulfilledAt: paidAt.Add(time.Minute)},
		{OrderID: orderB, UserID: uid, OrderNo: "PD20261014103", SubscriptionID: subID,
			FulfilledAt: paidAt.Add(2 * time.Minute)},
	}
	e.svc.now = scanFixedNow
	// 注入 B 的用户通知写失败
	e.svc.store = failEventStore{store: e.svc.store, failKey: "order-paid:" + orderB.String()}

	if n, err := e.svc.Scan(e.ctx); err != nil {
		t.Fatalf("scan %d %v", n, err)
	}

	// 游标停在 R（B 写失败，R 本身算处理完）
	var at time.Time
	if err := e.scan(e.ctx, &at, `SELECT last_at FROM notify_scan_cursors WHERE name = 'order_paid'`); err != nil ||
		!at.Equal(paidAt.Add(time.Minute)) {
		t.Fatalf("游标应停在 R 的履约时间 %v，实际 %v err %v", paidAt.Add(time.Minute), at, err)
	}
	// 只有 A 给用户写了站内信；R 没有用户通知
	if got := noticeCountInbox(t, e, uid); got != 1 {
		t.Fatalf("用户站内信应为 1 条（只有 A），实际 %d", got)
	}
	var evCount int64
	if err := e.scan(e.ctx, &evCount, `SELECT count(*) FROM notification_events WHERE dedupe_key = ?`,
		"order-paid:"+orderR.String()); err != nil || evCount != 0 {
		t.Fatalf("重置单不应写用户通知去重键，count=%d err=%v", evCount, err)
	}
	// 管理员投递 2 条（A 与 R）；R 那条的套餐一栏带「流量重置 · 」前缀
	adminRows := tgdRows(t, e, ChannelTelegram)
	var adminPaid []deliveryRow
	for _, row := range adminRows {
		if row.TemplateCode == TemplateAdminOrderPaid {
			adminPaid = append(adminPaid, row)
		}
	}
	if len(adminPaid) != 2 {
		t.Fatalf("应有 2 条管理员收款投递（A 与 R），实际 %d", len(adminPaid))
	}
	resetPlan := ""
	for _, row := range adminPaid {
		var r struct {
			PayloadEnc []byte
		}
		id := uuid.MustParse(row.ID)
		if err := e.scan(e.ctx, &r, `SELECT payload_enc FROM notification_deliveries WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		p := noticeOpenPayload(t, e, model.Delivery{ID: id, PayloadEnc: r.PayloadEnc})
		if p.Vars["order_no"] == "PD20261014102" {
			resetPlan = p.Vars["plan"]
		}
	}
	if resetPlan != "流量重置 · 重置测试套餐" {
		t.Fatalf("重置单收款提醒 plan = %q, want 流量重置 · 重置测试套餐", resetPlan)
	}

	// 换回原 store（修好），再扫：游标推进到 B，B 的用户通知补上，管理员投递不重复
	e.svc.store = e.repo
	if n, err := e.svc.Scan(e.ctx); err != nil {
		t.Fatalf("second scan %d %v", n, err)
	}
	if err := e.scan(e.ctx, &at, `SELECT last_at FROM notify_scan_cursors WHERE name = 'order_paid'`); err != nil ||
		!at.Equal(paidAt.Add(2*time.Minute)) {
		t.Fatalf("游标应推进到 B 的履约时间 %v，实际 %v err %v", paidAt.Add(2*time.Minute), at, err)
	}
	if got := noticeCountInbox(t, e, uid); got != 2 {
		t.Fatalf("用户站内信应共 2 条（A 与 B），实际 %d", got)
	}
	var total int64
	if err := e.scan(e.ctx, &total, `SELECT count(*) FROM notification_deliveries WHERE template_code = ?`,
		TemplateAdminOrderPaid); err != nil || total != 3 {
		t.Fatalf("管理员收款投递应共 3 条（R 不重复），count=%d err=%v", total, err)
	}
}

// TestScanPaidResetSkippedWhenAdminOff：收款提醒关着时重置单整单跳过，但游标照样推进（算处理完）。
func TestScanPaidResetSkippedWhenAdminOff(t *testing.T) {
	e := newPG(t)
	dir, plans, subs := e.usePorts()
	uid := e.insertTestUser(t, dir)
	adminChatID := int64(-1001987654321)
	e.st.tg = settingssvc.Telegram{
		Enabled:     true,
		BotUsername: "admin_bot",
		BotToken:    "123:abc",
		HasToken:    true,
		AdminChatID: &adminChatID,
	}
	e.st.tgx = settingssvc.TelegramExtras{
		AdminPaidNotify: false, // 关着
	}

	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000506")
	subID := uuid.MustParse("018f0000-0000-7000-8000-000000000507")
	orderR := uuid.MustParse("018f0000-0000-7000-8000-000000000508")

	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "重置测试套餐"}}
	subs.byID = map[uuid.UUID]subsvc.Subscription{
		subID: {
			ID:        subID,
			UserID:    uid,
			PlanID:    planID,
			PeriodEnd: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		},
	}
	paidAt := scanFixedNow().Add(-10 * time.Minute)
	subs.paid = []ordersvc.PaidNotice{
		{OrderID: orderR, UserID: uid, OrderNo: "PD20261014104", Kind: ordersvc.KindTrafficReset,
			SubscriptionID: subID, FulfilledAt: paidAt},
	}
	e.svc.now = scanFixedNow

	if n, err := e.svc.Scan(e.ctx); err != nil || n != 0 {
		t.Fatalf("scan %d %v", n, err)
	}
	if got := noticeCountInbox(t, e, uid); got != 0 {
		t.Fatalf("重置单不应给用户写站内信，实际 %d", got)
	}
	var total int64
	if err := e.scan(e.ctx, &total, `SELECT count(*) FROM notification_deliveries WHERE template_code = ?`,
		TemplateAdminOrderPaid); err != nil || total != 0 {
		t.Fatalf("关着收款提醒时不应有管理员投递，count=%d err=%v", total, err)
	}
	var at time.Time
	if err := e.scan(e.ctx, &at, `SELECT last_at FROM notify_scan_cursors WHERE name = 'order_paid'`); err != nil ||
		!at.Equal(paidAt) {
		t.Fatalf("游标应推进到 R 的履约时间 %v，实际 %v err %v", paidAt, at, err)
	}
}
