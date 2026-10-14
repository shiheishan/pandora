package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func ptrInt64(v int64) *int64 {
	return &v
}

func TestAdminOrderPaidScan_EnabledAndDedupe(t *testing.T) {
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

	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000031")
	subID := uuid.MustParse("018f0000-0000-7000-8000-000000000032")
	orderID := uuid.MustParse("018f0000-0000-7000-8000-000000000033")

	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "企业套餐"}}
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
		{
			OrderID:        orderID,
			UserID:         uid,
			OrderNo:        "PD20260601008",
			SubscriptionID: subID,
			FulfilledAt:    paidAt,
		},
	}
	e.svc.now = scanFixedNow

	n, err := e.svc.Scan(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("scan %d %v", n, err)
	}

	var adminRows []model.Delivery
	for _, row := range tgdRows(t, e, ChannelTelegram) {
		if row.TemplateCode == TemplateAdminOrderPaid {
			adminRows = append(adminRows, model.Delivery{
				ID:           uuid.MustParse(row.ID),
				TemplateCode: row.TemplateCode,
				Status:       row.Status,
			})
		}
	}
	if len(adminRows) != 1 {
		t.Fatalf("应有 1 条管理员收款投递，实际有 %d 条", len(adminRows))
	}
	if adminRows[0].Status != model.StatusQueued {
		t.Fatalf("投递状态应为 queued，实际为 %s", adminRows[0].Status)
	}

	var row struct {
		UserID     *uuid.UUID
		PayloadEnc []byte
	}
	if err := e.scan(e.ctx, &row, `SELECT user_id, payload_enc FROM notification_deliveries WHERE id = ?`, adminRows[0].ID); err != nil {
		t.Fatal(err)
	}
	if row.UserID != nil {
		t.Fatalf("管理员投递 user_id 应为空，实际为 %v", row.UserID)
	}
	d := model.Delivery{ID: adminRows[0].ID, PayloadEnc: row.PayloadEnc}

	p := noticeOpenPayload(t, e, d)
	if p.Vars["order_no"] != "PD20260601008" {
		t.Fatalf("order_no = %q, want PD20260601008", p.Vars["order_no"])
	}
	if p.Vars["amount"] != "—" {
		t.Fatalf("amount = %q, want —", p.Vars["amount"])
	}
	if p.Vars["plan"] != "企业套餐" {
		t.Fatalf("plan = %q, want 企业套餐", p.Vars["plan"])
	}
	if !strings.Contains(p.Vars["email"], "***") {
		t.Fatalf("email 应打码，实际为 %q", p.Vars["email"])
	}

	// 再扫一轮不重复写
	n2, err := e.svc.Scan(e.ctx)
	if err != nil || n2 != 0 {
		t.Fatalf("second scan %d %v", n2, err)
	}
	var count int64
	if err := e.scan(e.ctx, &count, `SELECT count(*) FROM notification_deliveries WHERE template_code = ?`, TemplateAdminOrderPaid); err != nil || count != 1 {
		t.Fatalf("管理员投递条数应保持为 1，count=%d err=%v", count, err)
	}
}

func TestAdminOrderPaidScan_Disabled(t *testing.T) {
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

	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000041")
	subID := uuid.MustParse("018f0000-0000-7000-8000-000000000042")
	orderID := uuid.MustParse("018f0000-0000-7000-8000-000000000043")

	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "入门套餐"}}
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
		{
			OrderID:        orderID,
			UserID:         uid,
			OrderNo:        "PD20260601009",
			SubscriptionID: subID,
			FulfilledAt:    paidAt,
		},
	}
	e.svc.now = scanFixedNow

	n, err := e.svc.Scan(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("scan %d %v", n, err)
	}

	var count int64
	if err := e.scan(e.ctx, &count, `SELECT count(*) FROM notification_deliveries WHERE template_code = ?`, TemplateAdminOrderPaid); err != nil || count != 0 {
		t.Fatalf("没开收款提醒时不应有管理员投递，count=%d err=%v", count, err)
	}
	if noticeCountInbox(t, e, uid) != 1 {
		t.Fatal("用户应收到通知")
	}
}

func TestNotifyAdminsTicket_EnabledAndDisabled(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	ticketID := e.insertTestTicket(t, uid)

	adminChatID := int64(-1001987654321)
	e.st.tg = settingssvc.Telegram{
		Enabled:     true,
		BotUsername: "admin_bot",
		BotToken:    "123:abc",
		HasToken:    true,
		AdminChatID: &adminChatID,
	}
	e.st.tgx = settingssvc.TelegramExtras{
		AdminTicketNotify: true,
	}

	fakeSum := &fakeSummaries{
		byUser: map[uuid.UUID]subsvc.UserSubscriptions{
			uid: {
				Current: &subsvc.Summary{
					Subscription: subsvc.Subscription{
						PeriodEnd: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
					},
					State:    subsvc.StateLive,
					PlanName: "尊贵套餐",
					Quotas: []subsvc.Quota{
						{
							Metric:    subsvc.MetricTraffic,
							Limit:     ptrInt64(100 << 30),
							Remaining: ptrInt64(60 << 30),
						},
					},
					PackRemainingBytes: 10 << 30,
				},
			},
		},
	}
	fakeBal := &fakeBalances{
		byUser: map[uuid.UUID]decimal.Decimal{
			uid: decimal.NewFromFloat(123.45),
		},
		currency: "CNY",
	}
	e.svc.UseSubscriptionSummaries(fakeSum)
	e.svc.UseBalances(fakeBal)

	msgID := uuid.New()
	in := AdminTicketInput{
		Kind:      "opened",
		UserID:    uid,
		TicketID:  ticketID,
		MessageID: msgID,
		TicketNo:  "TK2026101401",
		Subject:   "节点无法连接",
		Body:      "今天突然无法翻墙了，请帮忙检查。",
	}

	if err := e.svc.NotifyAdminsTicket(e.ctx, in); err != nil {
		t.Fatalf("NotifyAdminsTicket failed: %v", err)
	}

	var row struct {
		ID         uuid.UUID
		UserID     *uuid.UUID
		PayloadEnc []byte
	}
	if err := e.scan(e.ctx, &row, `SELECT id, user_id, payload_enc FROM notification_deliveries WHERE template_code = ?`, TemplateAdminTicketOpened); err != nil {
		t.Fatalf("找不到工单提醒投递: %v", err)
	}
	if row.UserID != nil {
		t.Fatalf("工单提醒投递 user_id 应为空，实际为 %v", row.UserID)
	}

	p := noticeOpenPayload(t, e, model.Delivery{ID: row.ID, PayloadEnc: row.PayloadEnc})
	if p.TicketID != ticketID.String() {
		t.Fatalf("payload ticket_id = %q, want %q", p.TicketID, ticketID.String())
	}
	if p.Vars["ticket_no"] != "TK2026101401" || p.Vars["subject"] != "节点无法连接" {
		t.Fatalf("ticket_no / subject 不匹配: %+v", p.Vars)
	}
	if !strings.Contains(p.Vars["email"], "***") {
		t.Fatalf("email 应打码: %q", p.Vars["email"])
	}
	if p.Vars["plan"] != "尊贵套餐" {
		t.Fatalf("plan = %q, want 尊贵套餐", p.Vars["plan"])
	}
	if p.Vars["traffic"] != "70.0 GB" {
		t.Fatalf("traffic = %q, want 70.0 GB", p.Vars["traffic"])
	}
	if p.Vars["balance"] != "123.45 CNY" {
		t.Fatalf("balance = %q, want 123.45 CNY", p.Vars["balance"])
	}

	// 同一 MessageID 再调不重复
	if err := e.svc.NotifyAdminsTicket(e.ctx, in); err != nil {
		t.Fatalf("repeat NotifyAdminsTicket failed: %v", err)
	}
	var count int64
	if err := e.scan(e.ctx, &count, `SELECT count(*) FROM notification_deliveries WHERE template_code = ?`, TemplateAdminTicketOpened); err != nil || count != 1 {
		t.Fatalf("工单提醒投递应去重，count=%d", count)
	}

	// 订阅摘要、余额读失败：只记 Warn，变量写「—」，照样写提醒、不返回错误（不拖垮建单 / 回复）
	fakeSum.err = errors.New("subscription down")
	fakeBal.err = errors.New("ledger down")
	inSoft := in
	inSoft.MessageID = uuid.New()
	if err := e.svc.NotifyAdminsTicket(e.ctx, inSoft); err != nil {
		t.Fatalf("读失败时提醒不应返回错误: %v", err)
	}
	fakeSum.err, fakeBal.err = nil, nil
	var rows []struct {
		ID         uuid.UUID
		PayloadEnc []byte
	}
	if err := e.scan(e.ctx, &rows, `SELECT id, payload_enc FROM notification_deliveries WHERE template_code = ?`, TemplateAdminTicketOpened); err != nil || len(rows) != 2 {
		t.Fatalf("读失败时也应写提醒，rows=%d err=%v", len(rows), err)
	}
	softFound := false
	for _, r := range rows {
		sp := noticeOpenPayload(t, e, model.Delivery{ID: r.ID, PayloadEnc: r.PayloadEnc})
		if sp.Vars["plan"] == "—" {
			softFound = true
			if sp.Vars["traffic"] != "—" || sp.Vars["expires_at"] != "—" || sp.Vars["balance"] != "—" || sp.Vars["ticket_no"] != "TK2026101401" {
				t.Fatalf("读失败时的变量 %+v", sp.Vars)
			}
		}
	}
	if !softFound {
		t.Fatal("没找到读失败时写的提醒")
	}

	// 关着时什么都不写
	e.st.tgx.AdminTicketNotify = false
	in2 := in
	in2.MessageID = uuid.New()
	if err := e.svc.NotifyAdminsTicket(e.ctx, in2); err != nil {
		t.Fatalf("disabled NotifyAdminsTicket failed: %v", err)
	}
	if err := e.scan(e.ctx, &count, `SELECT count(*) FROM notification_deliveries WHERE template_code = ?`, TemplateAdminTicketOpened); err != nil || count != 2 {
		t.Fatalf("关着时不应写投递，count=%d", count)
	}
}

func TestDispatchAdminTicket_MapsMessage(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	ticketID := e.insertTestTicket(t, uid)

	adminChatID := int64(-1001987654321)
	e.st.tg = settingssvc.Telegram{
		Enabled:     true,
		BotUsername: "admin_bot",
		BotToken:    "123:abc",
		HasToken:    true,
		AdminChatID: &adminChatID,
	}
	e.st.tgx = settingssvc.TelegramExtras{
		AdminTicketNotify: true,
	}

	fake := &fakeTG{}
	e.svc.tg = fake

	fakeSum := &fakeSummaries{
		byUser: map[uuid.UUID]subsvc.UserSubscriptions{
			uid: {
				Current: &subsvc.Summary{
					Subscription: subsvc.Subscription{
						PeriodEnd: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
					},
					State:    subsvc.StateLive,
					PlanName: "基础套餐",
				},
			},
		},
	}
	fakeBal := &fakeBalances{
		byUser: map[uuid.UUID]decimal.Decimal{
			uid: decimal.NewFromFloat(0),
		},
		currency: "CNY",
	}
	e.svc.UseSubscriptionSummaries(fakeSum)
	e.svc.UseBalances(fakeBal)

	in := AdminTicketInput{
		Kind:      "opened",
		UserID:    uid,
		TicketID:  ticketID,
		MessageID: uuid.New(),
		TicketNo:  "TK2026101402",
		Subject:   "工单标题",
		Body:      "工单正文",
	}
	if err := e.svc.NotifyAdminsTicket(e.ctx, in); err != nil {
		t.Fatal(err)
	}

	n, err := e.svc.Dispatch(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("Dispatch %d %v", n, err)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("sent %d", len(fake.sent))
	}

	// fakeTG.SendMessage 第一次发送返回 message_id 1001
	gotTicketID, ok, err := e.repo.TicketForMessage(e.ctx, adminChatID, 1001)
	if err != nil {
		t.Fatalf("TicketForMessage err: %v", err)
	}
	if !ok {
		t.Fatal("TicketForMessage 未找到工单")
	}
	if gotTicketID != ticketID {
		t.Fatalf("gotTicketID = %s, want %s", gotTicketID, ticketID)
	}
}
