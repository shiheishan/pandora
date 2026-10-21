package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestNotifyLinkRotatedWritesInboxAndEmail(t *testing.T) {
	e := newPG(t)
	dir, plans, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	planID := uuid.New()
	plans.plans = append(plans.plans, catalogsvc.Plan{ID: planID, Name: "旗舰套餐"})
	e.st.siteURL = "https://panel.example.test/"

	subID := uuid.New()
	rotated := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	if err := e.svc.NotifyLinkRotated(e.ctx, LinkRotatedNotice{
		UserID:         uid,
		SubscriptionID: subID,
		PlanID:         planID,
		Label:          "家里",
		RotatedAt:      rotated,
	}); err != nil {
		t.Fatal(err)
	}
	if noticeCountInbox(t, e, uid) != 1 {
		t.Fatal("应有 1 条站内信")
	}
	var inbox struct {
		Subject string
		Body    string
	}
	if err := e.scan(e.ctx, &inbox, `SELECT subject, body FROM notification_inbox WHERE user_id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	if inbox.Subject != "链接已更换" || !strings.Contains(inbox.Body, "「家里 · 旗舰套餐」") {
		t.Fatalf("inbox sub=%q body=%q", inbox.Subject, inbox.Body)
	}

	d := noticeClaimEmail(t, e)
	if d.TemplateCode != TemplateLinkRotated {
		t.Fatalf("template %q", d.TemplateCode)
	}
	p := noticeOpenPayload(t, e, d)
	if p.Vars["subs_url"] != "https://panel.example.test/#/subs" || p.Vars["plan"] != "家里 · 旗舰套餐" || p.Vars["site"] != "测试站" {
		t.Fatalf("email vars %+v", p.Vars)
	}

	delBefore := len(e.rows(t))
	if err := e.svc.NotifyLinkRotated(e.ctx, LinkRotatedNotice{
		UserID:         uid,
		SubscriptionID: subID,
		PlanID:         planID,
		Label:          "家里",
		RotatedAt:      rotated,
	}); err != nil {
		t.Fatal(err)
	}
	if noticeCountInbox(t, e, uid) != 1 {
		t.Fatal("同 RotatedAt 去重后不应再写站内信")
	}
	if len(e.rows(t)) != delBefore {
		t.Fatal("同 RotatedAt 去重后投递总数不变")
	}

	if err := e.svc.NotifyLinkRotated(e.ctx, LinkRotatedNotice{
		UserID:         uid,
		SubscriptionID: subID,
		PlanID:         planID,
		Label:          "家里",
		RotatedAt:      rotated.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if noticeCountInbox(t, e, uid) != 2 {
		t.Fatal("RotatedAt 加 1 秒应再写一条站内信")
	}
}

func TestNotifyLinkRotatedTelegram(t *testing.T) {
	e := newPG(t)
	dir, plans, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	planID := uuid.New()
	plans.plans = append(plans.plans, catalogsvc.Plan{ID: planID, Name: "旗舰套餐"})
	e.st.tg = settingssvc.Telegram{Enabled: true, BotUsername: "bot", BotToken: "tok"}
	if err := e.repo.InsertBinding(e.ctx, &model.TelegramBinding{UserID: uid, ChatID: 12345, Username: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.NotifyLinkRotated(e.ctx, LinkRotatedNotice{
		UserID:         uid,
		SubscriptionID: uuid.New(),
		PlanID:         planID,
		RotatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	d := noticeClaimTelegram(t, e)
	if d.TemplateCode != TemplateLinkRotated {
		t.Fatalf("template %q", d.TemplateCode)
	}
	p := noticeOpenPayload(t, e, d)
	if p.To != "12345" {
		t.Fatalf("to %q", p.To)
	}
}

func TestNotifyOrderPending(t *testing.T) {
	e := newPG(t)
	dir, plans, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	planID := uuid.New()
	plans.plans = append(plans.plans, catalogsvc.Plan{ID: planID, Name: "旗舰套餐"})
	e.st.siteURL = "https://panel.example.test/"
	e.st.site.Timezone = "Asia/Shanghai"

	orderID := uuid.New()
	if err := e.svc.NotifyOrderPending(e.ctx, PendingOrderNotice{
		UserID:    uid,
		OrderID:   orderID,
		PlanID:    planID,
		OrderNo:   "AO20260804-ABCDEF",
		Amount:    decimal.RequireFromString("29.9"),
		ExpiresAt: time.Date(2026, 8, 31, 15, 59, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if noticeCountInbox(t, e, uid) != 1 {
		t.Fatal("应有 1 条站内信")
	}
	var body string
	if err := e.scan(e.ctx, &body, `SELECT body FROM notification_inbox WHERE user_id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "应付 29.90 元") {
		t.Fatalf("body %q", body)
	}

	d := noticeClaimEmail(t, e)
	p := noticeOpenPayload(t, e, d)
	if p.Vars["amount"] != "29.90 元" || p.Vars["expires_at"] != "2026-08-31 23:59" ||
		p.Vars["order_url"] != "https://panel.example.test/#/orders/AO20260804-ABCDEF" ||
		p.Vars["order_no"] != "AO20260804-ABCDEF" || p.Vars["plan"] != "旗舰套餐" {
		t.Fatalf("email vars %+v", p.Vars)
	}

	if err := e.svc.NotifyOrderPending(e.ctx, PendingOrderNotice{
		UserID:    uid,
		OrderID:   orderID,
		PlanID:    planID,
		OrderNo:   "AO20260804-ABCDEF",
		Amount:    decimal.RequireFromString("29.9"),
		ExpiresAt: time.Date(2026, 8, 31, 15, 59, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if noticeCountInbox(t, e, uid) != 1 {
		t.Fatal("同 OrderID 去重后不应再写")
	}
}

func TestNotifyAdminOpsNeedsPlans(t *testing.T) {
	e := newPG(t)
	e.svc.UseDirectory(newFakeDir())

	var ie *errs.Error
	if err := e.svc.NotifyLinkRotated(e.ctx, LinkRotatedNotice{
		UserID:         uuid.New(),
		SubscriptionID: uuid.New(),
		PlanID:         uuid.New(),
		RotatedAt:      time.Now().UTC(),
	}); !errors.As(err, &ie) || ie.Code != errs.Internal {
		t.Fatalf("NotifyLinkRotated: %v", err)
	}
	if err := e.svc.NotifyOrderPending(e.ctx, PendingOrderNotice{
		UserID:  uuid.New(),
		OrderID: uuid.New(),
		PlanID:  uuid.New(),
	}); !errors.As(err, &ie) || ie.Code != errs.Internal {
		t.Fatalf("NotifyOrderPending: %v", err)
	}
}
