package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func TestBotPrivateCommands(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	tg := &fakeTG{}
	e.svc.tg = tg
	tgbReady(e)

	chatID := int64(88801)

	// 1. 未绑定发 /traffic 回 tgNotBound
	body := tgbWebhookBody("", chatID, "private", "/traffic")
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", body); err != nil {
		t.Fatal(err)
	}
	if txt := tgbLastSentText(t, tg); txt != tgNotBound {
		t.Fatalf("unbound /traffic want %q, got %q", tgNotBound, txt)
	}

	// 2. 绑定账号
	bc, err := e.svc.IssueBindCode(e.ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	bindBody := tgbWebhookBody("", chatID, "private", bc.Code)
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", bindBody); err != nil {
		t.Fatal(err)
	}

	// 3. 绑定后发 /traffic，装配 fakeSummaries
	summaries := &fakeSummaries{
		byUser: map[uuid.UUID]subsvc.UserSubscriptions{
			uid: {
				Subscriptions: []subsvc.Summary{
					{
						Subscription: subsvc.Subscription{
							ID:        uuid.New(),
							UserID:    uid,
							PeriodEnd: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
						},
						State:    subsvc.StateLive,
						PlanName: "黄金套餐",
					},
				},
			},
		},
	}
	e.svc.UseSubscriptionSummaries(summaries)

	tg.sent = nil
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", body); err != nil {
		t.Fatal(err)
	}
	if txt := tgbLastSentText(t, tg); !strings.Contains(txt, "「黄金套餐」") {
		t.Fatalf("bound /traffic want plan name, got %q", txt)
	}

	// 4. /sub 关着回 tgSubDisabled
	e.st.tgx = settingssvc.TelegramExtras{SubCommand: false}
	subBody := tgbWebhookBody("", chatID, "private", "/sub")
	tg.sent = nil
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", subBody); err != nil {
		t.Fatal(err)
	}
	if txt := tgbLastSentText(t, tg); txt != tgSubDisabled {
		t.Fatalf("/sub disabled want %q, got %q", tgSubDisabled, txt)
	}

	// 5. /sub 开着回链接
	e.st.tgx = settingssvc.TelegramExtras{SubCommand: true}
	links := &fakeLinks{
		byUser: map[uuid.UUID][]string{
			uid: {"https://example.com/sub/token123456"},
		},
	}
	e.svc.UseSubscribeLinks(links)
	tg.sent = nil
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", subBody); err != nil {
		t.Fatal(err)
	}
	txt := tgbLastSentText(t, tg)
	if !strings.Contains(txt, tgSubHeader) || !strings.Contains(txt, "https://example.com/sub/token123456") {
		t.Fatalf("/sub enabled want links, got %q", txt)
	}

	// 5b. 账号停用：/traffic、/sub 都不给内容
	dir.mu.Lock()
	dir.inactive[uid] = true
	dir.mu.Unlock()
	for _, b := range [][]byte{body, subBody} {
		tg.sent = nil
		if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", b); err != nil {
			t.Fatal(err)
		}
		if txt := tgbLastSentText(t, tg); txt != tgInactive {
			t.Fatalf("inactive account want %q, got %q", tgInactive, txt)
		}
	}
	dir.mu.Lock()
	dir.inactive[uid] = false
	dir.mu.Unlock()

	// 6. /unbind 解绑
	unbindBody := tgbWebhookBody("", chatID, "private", "/unbind")
	tg.sent = nil
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", unbindBody); err != nil {
		t.Fatal(err)
	}
	if txt := tgbLastSentText(t, tg); txt != tgUnbound {
		t.Fatalf("/unbind want %q, got %q", tgUnbound, txt)
	}
	// 绑定已删除
	_, ok, err := e.repo.Binding(e.ctx, uid)
	if err != nil || ok {
		t.Fatalf("binding should be gone: ok=%v, err=%v", ok, err)
	}
	// 审计 ActorID 是本人、Audience 是 public、data.via = telegram
	var foundUnbound bool
	for _, ev := range e.audit.events {
		if ev.Action == "telegram.unbound" {
			foundUnbound = true
			if ev.ActorID == nil || *ev.ActorID != uid {
				t.Fatalf("audit ActorID want %v, got %v", uid, ev.ActorID)
			}
			if ev.Audience != "public" {
				t.Fatalf("audit Audience want public, got %q", ev.Audience)
			}
			if ev.Data == nil || ev.Data["via"] != "telegram" {
				t.Fatalf("audit Data.via want telegram, got %+v", ev.Data)
			}
		}
	}
	if !foundUnbound {
		t.Fatal("missing telegram.unbound audit event")
	}
}

func TestBotJoinReview(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	tg := &fakeTG{}
	e.svc.tg = tg
	tgbReady(e)

	userGroupChatID := int64(-100123456789)
	e.st.tgx = settingssvc.TelegramExtras{
		JoinReview:      true,
		UserGroupChatID: &userGroupChatID,
	}

	subs := newFakeSubs()
	e.svc.UseSubscriptions(subs)

	applicantTGID := int64(99901)
	joinReqBody := func(chatID, fromID int64) []byte {
		b, _ := json.Marshal(map[string]any{
			"chat_join_request": map[string]any{
				"chat": map[string]any{"id": chatID},
				"from": map[string]any{"id": fromID, "username": "applicant"},
			},
		})
		return b
	}

	// 1. 群不对 → 不调
	wrongGroupBody := joinReqBody(-100999999999, applicantTGID)
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", wrongGroupBody); err != nil {
		t.Fatal(err)
	}
	if len(tg.joins) != 0 {
		t.Fatalf("wrong group should not call joins, got %+v", tg.joins)
	}

	// 2. 群对，但未绑定 → Decline
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", joinReqBody(userGroupChatID, applicantTGID)); err != nil {
		t.Fatal(err)
	}
	if len(tg.joins) != 1 || !tg.joins[0].Decline || tg.joins[0].UserID != applicantTGID {
		t.Fatalf("expected decline for unbound user, got %+v", tg.joins)
	}

	// 3. 绑定了，但无生效订阅 → Decline
	if err := e.repo.InsertBinding(e.ctx, &model.TelegramBinding{UserID: uid, ChatID: applicantTGID, Username: "applicant"}); err != nil {
		t.Fatal(err)
	}
	tg.joins = nil
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", joinReqBody(userGroupChatID, applicantTGID)); err != nil {
		t.Fatal(err)
	}
	if len(tg.joins) != 1 || !tg.joins[0].Decline {
		t.Fatalf("expected decline for user without live subscription, got %+v", tg.joins)
	}

	// 4. 有生效订阅 → Approve
	subs.live[uid] = []uuid.UUID{uuid.New()}
	tg.joins = nil
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", joinReqBody(userGroupChatID, applicantTGID)); err != nil {
		t.Fatal(err)
	}
	if len(tg.joins) != 1 || !tg.joins[0].Approve {
		t.Fatalf("expected approve for user with live subscription, got %+v", tg.joins)
	}

	// 5. 有生效订阅但账号停用 → Decline
	dir.mu.Lock()
	dir.inactive[uid] = true
	dir.mu.Unlock()
	tg.joins = nil
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", joinReqBody(userGroupChatID, applicantTGID)); err != nil {
		t.Fatal(err)
	}
	if len(tg.joins) != 1 || !tg.joins[0].Decline {
		t.Fatalf("expected decline for inactive account, got %+v", tg.joins)
	}
}

func TestBotAdminReply(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	adminUID := e.insertTestUser(t, dir)
	tg := &fakeTG{}
	e.svc.tg = tg
	tgbReady(e)

	adminChatID := int64(-100888888888)
	e.st.tg.AdminChatID = &adminChatID

	adminTGID := int64(77701)
	if err := e.repo.InsertBinding(e.ctx, &model.TelegramBinding{UserID: adminUID, ChatID: adminTGID, Username: "admin_user"}); err != nil {
		t.Fatal(err)
	}

	fakeA := &fakeAdmins{active: map[uuid.UUID]bool{adminUID: true}}
	e.svc.UseAdmins(fakeA)

	fakeR := &fakeReplier{}
	e.svc.UseTicketReplier(fakeR)

	ticketID := e.insertTestTicket(t, e.insertTestUser(t, dir))
	targetMsgID := int64(555)
	if err := e.repo.InsertTicketMessage(e.ctx, &model.TelegramTicketMessage{
		ChatID:    adminChatID,
		MessageID: targetMsgID,
		TicketID:  ticketID,
	}); err != nil {
		t.Fatal(err)
	}

	// 每次调用用新的 message_id（同一条消息重推的去重见第 6 步）
	nextMsgID := int64(9000)
	adminReplyBody := func(chatID, fromID, replyToMsgID int64, text string, isBot bool) []byte {
		nextMsgID++
		msg := map[string]any{
			"message_id": nextMsgID,
			"chat":       map[string]any{"id": chatID, "type": "supergroup"},
			"from":       map[string]any{"id": fromID, "is_bot": isBot, "username": "sender"},
			"text":       text,
		}
		if replyToMsgID > 0 {
			msg["reply_to_message"] = map[string]any{"message_id": replyToMsgID}
		}
		b, _ := json.Marshal(map[string]any{"message": msg})
		return b
	}

	// 1. 群不对 → 不回复、不发消息
	wrongChatBody := adminReplyBody(-100999999999, adminTGID, targetMsgID, "回复一下", false)
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", wrongChatBody); err != nil {
		t.Fatal(err)
	}
	if len(fakeR.replies) != 0 || len(tg.sent) != 0 {
		t.Fatal("wrong chat should be ignored")
	}

	// 2. 不是管理员 → 不回复、不发消息
	fakeA.active[adminUID] = false
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", adminReplyBody(adminChatID, adminTGID, targetMsgID, "回复一下", false)); err != nil {
		t.Fatal(err)
	}
	if len(fakeR.replies) != 0 || len(tg.sent) != 0 {
		t.Fatal("non-admin should be ignored")
	}
	fakeA.active[adminUID] = true

	// 3. 没有映射（回复了非工单提醒的消息） → 不回复、不发消息
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", adminReplyBody(adminChatID, adminTGID, 99999, "回复一下", false)); err != nil {
		t.Fatal(err)
	}
	if len(fakeR.replies) != 0 || len(tg.sent) != 0 {
		t.Fatal("non-mapped message should be ignored")
	}

	// 4. 一切正确 → 成功回复工单，群里收到 tgTicketReplied
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", adminReplyBody(adminChatID, adminTGID, targetMsgID, "这个问题已经修复了", false)); err != nil {
		t.Fatal(err)
	}
	if len(fakeR.replies) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(fakeR.replies))
	}
	r := fakeR.replies[0]
	if r.AdminID != adminUID || r.TicketID != ticketID || r.Body != "这个问题已经修复了" {
		t.Fatalf("reply mismatch: %+v", r)
	}
	if txt := tgbLastSentText(t, tg); txt != tgTicketReplied {
		t.Fatalf("expected confirmation message %q, got %q", tgTicketReplied, txt)
	}

	// 5. replier 返回业务错误（非 internal） → 群里回「回复没有成功：...」
	fakeR.err = errs.Newf(errs.Conflict, "工单已关闭")
	tg.sent = nil
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", adminReplyBody(adminChatID, adminTGID, targetMsgID, "继续回复", false)); err != nil {
		t.Fatal(err)
	}
	if txt := tgbLastSentText(t, tg); txt != "回复没有成功：工单已关闭" {
		t.Fatalf("expected error message with reason, got %q", txt)
	}

	// 6. Telegram 重推同一条消息（同 chat_id、message_id）：只回复一次，第二次不再调 replier、不再回确认
	fakeR.err = nil
	fakeR.replies = nil
	tg.sent = nil
	dupBody := adminReplyBody(adminChatID, adminTGID, targetMsgID, "重推的回复", false)
	for i := 0; i < 2; i++ {
		if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", dupBody); err != nil {
			t.Fatal(err)
		}
	}
	if len(fakeR.replies) != 1 {
		t.Fatalf("duplicate push should reply once, got %d", len(fakeR.replies))
	}
	if len(tg.sent) != 1 || tg.sent[0].Text != tgTicketReplied {
		t.Fatalf("duplicate push should confirm once, got %+v", tg.sent)
	}
	var n int64
	if err := e.scan(e.ctx, &n,
		`SELECT count(*) FROM notification_events WHERE dedupe_key = ?`,
		fmt.Sprintf("tg-admin-reply:%d:%d", adminChatID, nextMsgID),
	); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dedupe events %d", n)
	}

	// 7. 管理员在私聊里 /unbind：审计记 admin
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", tgbWebhookBody("", adminTGID, "private", "/unbind")); err != nil {
		t.Fatal(err)
	}
	var adminUnbound bool
	for _, ev := range e.audit.events {
		if ev.Action == "telegram.unbound" && ev.ActorID != nil && *ev.ActorID == adminUID {
			adminUnbound = true
			if ev.Audience != "admin" {
				t.Fatalf("admin unbind audit Audience want admin, got %q", ev.Audience)
			}
		}
	}
	if !adminUnbound {
		t.Fatal("missing admin telegram.unbound audit event")
	}
}
