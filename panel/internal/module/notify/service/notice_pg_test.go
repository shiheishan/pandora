package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func noticeClaimEmail(t *testing.T, e *pgEnv) model.Delivery {
	t.Helper()
	ds, err := e.repo.ClaimDeliveries(e.ctx, ChannelEmail, 10, time.Minute)
	if err != nil || len(ds) != 1 {
		t.Fatalf("claim email %d %v", len(ds), err)
	}
	return ds[0]
}

func noticeClaimTelegram(t *testing.T, e *pgEnv) model.Delivery {
	t.Helper()
	ds, err := e.repo.ClaimDeliveries(e.ctx, ChannelTelegram, 10, time.Minute)
	if err != nil || len(ds) != 1 {
		t.Fatalf("claim telegram %d %v", len(ds), err)
	}
	return ds[0]
}

func noticeOpenPayload(t *testing.T, e *pgEnv, d model.Delivery) deliveryPayload {
	t.Helper()
	raw, err := e.svc.sealer.Open(d.PayloadEnc, deliveryAAD(d.ID))
	if err != nil {
		t.Fatal(err)
	}
	var p deliveryPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func noticeCountInbox(t *testing.T, e *pgEnv, userID uuid.UUID) int {
	t.Helper()
	var n int64
	if err := e.db.Conn(e.ctx).Raw(`SELECT count(*) FROM notification_inbox WHERE user_id = ?`, userID).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func noticeCountDeliveries(t *testing.T, e *pgEnv) int {
	t.Helper()
	return len(e.rows(t))
}

func TestNotifyUserWritesInboxAndEmailDelivery(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	vars := map[string]string{
		"order_no":   "AO2026-001",
		"plan":       "旗舰套餐",
		"expires_at": "2026-12-31",
		"extra":      "丢掉",
	}
	if err := e.svc.NotifyUser(e.ctx, uid, TemplateOrderPaid, vars, "order:paid:1"); err != nil {
		t.Fatal(err)
	}
	if noticeCountInbox(t, e, uid) != 1 {
		t.Fatal("应有 1 条站内信")
	}
	items, err := e.repo.ListInbox(e.ctx, uid, false, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox %v %v", items, err)
	}
	if items[0].TemplateCode != TemplateOrderPaid {
		t.Fatalf("code %q", items[0].TemplateCode)
	}
	if !strings.Contains(items[0].Subject, "支付成功") || !strings.Contains(items[0].Body, "AO2026-001") {
		t.Fatalf("未渲染: sub=%q body=%q", items[0].Subject, items[0].Body)
	}
	if rows := e.rows(t); len(rows) != 0 {
		t.Fatalf("order.paid 无邮件渠道，不应有投递: %+v", rows)
	}

	// 工单回复：站内信 + Telegram（无邮件登记）
	uid2 := e.insertTestUser(t, dir)
	chatID := int64(99887766)
	if err := e.repo.InsertBinding(e.ctx, &model.TelegramBinding{UserID: uid2, ChatID: chatID, Username: "u"}); err != nil {
		t.Fatal(err)
	}
	e.st.tg = settingssvc.Telegram{Enabled: true, BotUsername: "bot", BotToken: "tok"}
	if err := e.svc.NotifyUser(e.ctx, uid2, TemplateTicketReplied, map[string]string{"subject": "连不上"}, "ticket:1"); err != nil {
		t.Fatal(err)
	}
	if noticeCountInbox(t, e, uid2) != 1 {
		t.Fatal("站内信")
	}
	d := noticeClaimTelegram(t, e)
	p := noticeOpenPayload(t, e, d)
	if p.To != "99887766" || p.Vars["subject"] != "连不上" || p.Vars["extra"] != "" {
		t.Fatalf("telegram payload %+v", p)
	}

	// 到期提醒带邮件：测 To、Vars 白名单与 site
	uid3 := e.insertTestUser(t, dir)
	if err := e.svc.NotifyUser(e.ctx, uid3, TemplateSubscriptionExpiring,
		map[string]string{"plan": "基础", "days": "3", "expires_at": "2026-08-01"}, "sub:exp:1"); err != nil {
		t.Fatal(err)
	}
	em := noticeClaimEmail(t, e)
	pe := noticeOpenPayload(t, e, em)
	email := dir.emails[uid3]
	if pe.To != email || pe.Vars["site"] != "测试站" || pe.Vars["plan"] != "基础" || pe.Vars["extra"] != "" {
		t.Fatalf("email payload %+v", pe)
	}
	if !bytes.Equal(em.RecipientHash, e.keys.HMAC(crypto.InfoHMACNotify, []byte(strings.ToLower(email)))) {
		t.Fatal("recipient_hash")
	}

	delBefore := noticeCountDeliveries(t, e)
	if err := e.svc.NotifyUser(e.ctx, uid3, TemplateSubscriptionExpiring,
		map[string]string{"plan": "基础", "days": "3", "expires_at": "2026-08-01"}, "sub:exp:1"); err != nil {
		t.Fatal(err)
	}
	if noticeCountInbox(t, e, uid3) != 1 {
		t.Fatal("去重后不应再写站内信")
	}
	if noticeCountDeliveries(t, e) != delBefore {
		t.Fatal("去重后不应再写投递")
	}
}

func TestNotifyUserRespectsServiceEmailPreference(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	if err := e.svc.SetPreference(e.ctx, uid, categoryService, ChannelEmail, false); err != nil {
		t.Fatal(err)
	}
	key := "sub:exp:pref"
	if err := e.svc.NotifyUser(e.ctx, uid, TemplateSubscriptionExpiring,
		map[string]string{"plan": "p", "days": "1", "expires_at": "x"}, key); err != nil {
		t.Fatal(err)
	}
	if noticeCountInbox(t, e, uid) != 1 {
		t.Fatal("站内信应照写")
	}
	rows := e.rows(t)
	for _, r := range rows {
		if r.TemplateCode == TemplateSubscriptionExpiring {
			t.Fatal("不应写邮件投递")
		}
	}

	uid2 := e.insertTestUser(t, dir)
	if err := e.svc.NotifyUser(e.ctx, uid2, TemplateOrderPaid,
		map[string]string{"order_no": "o", "plan": "p", "expires_at": "x"}, "order:paid:pref"); err != nil {
		t.Fatal(err)
	}
	if noticeCountInbox(t, e, uid2) != 1 {
		t.Fatal("交易类站内信")
	}
	// order.paid 无 email 渠道，仅确认偏好关闭不影响站内信
}

func TestNotifyUserTelegramDelivery(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	vars := map[string]string{"subject": "s"}
	key := "ticket:tg"

	e.st.tg = settingssvc.Telegram{Enabled: false, BotUsername: "bot", BotToken: "tok"}
	if err := e.svc.NotifyUser(e.ctx, uid, TemplateTicketReplied, vars, key); err != nil {
		t.Fatal(err)
	}
	if len(e.rows(t)) != 0 {
		t.Fatal("Telegram 未就绪不应投递")
	}

	uid2 := e.insertTestUser(t, dir)
	e.st.tg = settingssvc.Telegram{Enabled: true, BotUsername: "bot", BotToken: "tok"}
	if err := e.svc.NotifyUser(e.ctx, uid2, TemplateTicketReplied, vars, key+"2"); err != nil {
		t.Fatal(err)
	}
	if len(e.rows(t)) != 0 {
		t.Fatal("未绑定不应投递")
	}

	if err := e.repo.InsertBinding(e.ctx, &model.TelegramBinding{UserID: uid2, ChatID: 12345, Username: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.NotifyUser(e.ctx, uid2, TemplateTicketReplied, vars, key+"3"); err != nil {
		t.Fatal(err)
	}
	d := noticeClaimTelegram(t, e)
	p := noticeOpenPayload(t, e, d)
	if p.To != "12345" {
		t.Fatalf("payload %+v", p)
	}
}

func TestInboxReadAndList(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	other := e.insertTestUser(t, dir)
	if err := e.svc.NotifyUser(e.ctx, uid, TemplateTicketReplied, map[string]string{"subject": "a"}, "inbox:1"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.NotifyUser(e.ctx, uid, TemplateOrderPaid,
		map[string]string{"order_no": "o", "plan": "p", "expires_at": "e"}, "inbox:2"); err != nil {
		t.Fatal(err)
	}
	list, unread, err := e.svc.Inbox(e.ctx, uid, false, 10)
	if err != nil || unread != 2 || len(list) != 2 {
		t.Fatalf("list %d unread %d err %v", len(list), unread, err)
	}
	if list[0].Code != TemplateOrderPaid || list[1].Code != TemplateTicketReplied {
		t.Fatalf("倒序 %+v", list)
	}
	if list[0].SentAt.IsZero() {
		t.Fatal("SentAt")
	}

	only, unread2, err := e.svc.Inbox(e.ctx, uid, true, 10)
	if err != nil || unread2 != 2 || len(only) != 2 {
		t.Fatalf("onlyUnread %d %d", len(only), unread2)
	}

	if err := e.svc.MarkInboxRead(e.ctx, uid, list[0].ID); err != nil {
		t.Fatal(err)
	}
	_, unread3, err := e.svc.Inbox(e.ctx, uid, false, 10)
	if err != nil || unread3 != 1 {
		t.Fatalf("unread after mark %d", unread3)
	}
	if err := e.svc.NotifyUser(e.ctx, other, TemplateTicketReplied, map[string]string{"subject": "other"}, "inbox:other"); err != nil {
		t.Fatal(err)
	}
	otherList, _ := e.repo.ListInbox(e.ctx, other, false, 10)
	if len(otherList) == 0 {
		t.Fatal("need other inbox id")
	}
	if err := e.svc.MarkInboxRead(e.ctx, uid, otherList[0].ID); err != nil {
		t.Fatal(err)
	}
	_, unread4, _ := e.svc.Inbox(e.ctx, uid, false, 10)
	if unread4 != 1 {
		t.Fatal("不能标别人的已读")
	}

	if err := e.svc.MarkAllInboxRead(e.ctx, uid); err != nil {
		t.Fatal(err)
	}
	_, unread5, _ := e.svc.Inbox(e.ctx, uid, false, 10)
	if unread5 != 0 {
		t.Fatalf("all read %d", unread5)
	}
}

func TestPreferencesCatalog(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	prefs, err := e.svc.Preferences(e.ctx, uid)
	if err != nil || len(prefs) != 6 {
		t.Fatalf("prefs %d %v", len(prefs), err)
	}
	if !prefs[0].Locked || !prefs[0].Enabled || prefs[0].Category != "transactional" {
		t.Fatalf("catalog[0] %+v", prefs[0])
	}

	if err := e.svc.SetPreference(e.ctx, uid, categoryService, ChannelEmail, false); err != nil {
		t.Fatal(err)
	}
	prefs2, err := e.svc.Preferences(e.ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prefs2 {
		if p.Category == categoryService && p.Channel == ChannelEmail && p.Enabled {
			t.Fatal("应关闭 service/email")
		}
	}

	err = e.svc.SetPreference(e.ctx, uid, categoryTransactional, ChannelEmail, false)
	if !isCode(err, errs.ValidationFailed) {
		t.Fatalf("transactional off: %v", err)
	}
	var ve *errs.Error
	if errors.As(err, &ve) && ve.Message != "交易类通知无法关闭" {
		t.Fatalf("msg %q", ve.Message)
	}

	err = e.svc.SetPreference(e.ctx, uid, "nope", ChannelEmail, false)
	if !isCode(err, errs.ValidationFailed) {
		t.Fatalf("invalid: %v", err)
	}
	if f := fieldsOf(t, err); f["preference"] != "不支持的通知类别或渠道" {
		t.Fatalf("fields %v", f)
	}
}
