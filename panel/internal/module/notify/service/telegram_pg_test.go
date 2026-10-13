package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func tgbReady(e *pgEnv) {
	e.st.tg = settingssvc.Telegram{
		Enabled: true, BotUsername: "mybot", BotToken: "tok-1", HasToken: true,
		WebhookSecret: "whsec",
	}
}

func tgbSave(e *pgEnv, in TelegramSettingsInput) error {
	return e.svc.SaveTelegramSettings(e.ctx, in)
}

func tgbCountUnusedCodes(t *testing.T, e *pgEnv, userID uuid.UUID) int {
	t.Helper()
	var n int64
	if err := e.scan(e.ctx, &n,
		`SELECT count(*) FROM telegram_bind_codes WHERE user_id = ? AND used_at IS NULL`, userID,
	); err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func tgbWebhookBody(code string, chatID int64, chatType string, text string) []byte {
	msg := map[string]any{
		"chat": map[string]any{"id": chatID, "type": chatType},
		"from": map[string]any{"username": "tguser"},
		"text": text,
	}
	b, _ := json.Marshal(map[string]any{"message": msg})
	return b
}

func tgbLastSentText(t *testing.T, tg *fakeTG) string {
	t.Helper()
	tg.mu.Lock()
	defer tg.mu.Unlock()
	if len(tg.sent) == 0 {
		return ""
	}
	return tg.sent[len(tg.sent)-1].Text
}

func TestSaveTelegramSettingsWebhook(t *testing.T) {
	e := newPG(t)
	tg := &fakeTG{}
	e.svc.tg = tg
	e.svc.UsePublicBaseURL("https://panel.example.com")

	if err := tgbSave(e, TelegramSettingsInput{
		Enabled: true, BotUsername: "mybot", BotToken: "new-token",
	}); err != nil {
		t.Fatal(err)
	}
	if len(tg.webhooks) != 1 {
		t.Fatalf("webhooks %d", len(tg.webhooks))
	}
	if tg.webhooks[0].URL != "https://panel.example.com/v1/webhooks/telegram" {
		t.Fatalf("url %q", tg.webhooks[0].URL)
	}
	if len(tg.webhooks[0].Secret) != 43 {
		t.Fatalf("secret len %d", len(tg.webhooks[0].Secret))
	}
	if e.st.setTg == nil || e.st.setTg.WebhookSecret == nil || *e.st.setTg.WebhookSecret != tg.webhooks[0].Secret {
		t.Fatalf("setTg secret %+v", e.st.setTg)
	}

	// SetWebhook 失败不落库
	e2 := newPG(t)
	tg2 := &fakeTG{webhookErr: &TelegramError{Status: 400, Detail: "bad hook"}}
	e2.svc.tg = tg2
	e2.svc.UsePublicBaseURL("https://panel.example.com")
	err := tgbSave(e2, TelegramSettingsInput{Enabled: true, BotUsername: "mybot", BotToken: "t"})
	if !errors.As(err, new(*errs.Error)) || !isCode(err, errs.ValidationFailed) {
		t.Fatalf("err %v", err)
	}
	if e2.st.setTg != nil {
		t.Fatalf("should not save: %+v", e2.st.setTg)
	}

	// http 不调 SetWebhook
	e3 := newPG(t)
	tg3 := &fakeTG{}
	e3.svc.tg = tg3
	e3.svc.UsePublicBaseURL("http://panel.example.com")
	if err := tgbSave(e3, TelegramSettingsInput{Enabled: true, BotUsername: "mybot", BotToken: "t"}); err != nil {
		t.Fatal(err)
	}
	if len(tg3.webhooks) != 0 {
		t.Fatal("http should skip webhook")
	}
	if e3.st.setTg == nil {
		t.Fatal("should still save settings")
	}

	// 启用无 Token
	e4 := newPG(t)
	e4.st.tg = settingssvc.Telegram{Enabled: false}
	f := fieldsOf(t, tgbSave(e4, TelegramSettingsInput{Enabled: true}))
	if f["bot_token"] == "" {
		t.Fatalf("fields %v", f)
	}

	// BotToken 空串 → update.BotToken nil（沿用 cur）
	e5 := newPG(t)
	tg5 := &fakeTG{}
	e5.svc.tg = tg5
	e5.st.tg = settingssvc.Telegram{Enabled: true, BotUsername: "mybot", BotToken: "keep", HasToken: true}
	e5.svc.UsePublicBaseURL("https://panel.example.com")
	if err := tgbSave(e5, TelegramSettingsInput{Enabled: true, BotUsername: "mybot", BotToken: ""}); err != nil {
		t.Fatal(err)
	}
	if e5.st.setTg.BotToken != nil {
		t.Fatalf("BotToken ptr %+v", e5.st.setTg.BotToken)
	}
}

func TestTestTelegram(t *testing.T) {
	e := newPG(t)
	tg := &fakeTG{}
	e.svc.tg = tg

	if f := fieldsOf(t, e.svc.TestTelegram(e.ctx, 0)); f["chat_id"] == "" {
		t.Fatalf("fields %v", f)
	}

	e.st.tg = settingssvc.Telegram{Enabled: false}
	if err := e.svc.TestTelegram(e.ctx, 123); !isCode(err, errs.ValidationFailed) {
		t.Fatalf("err %v", err)
	}

	tgbReady(e)
	if err := e.svc.TestTelegram(e.ctx, 12345); err != nil {
		t.Fatal(err)
	}
	if len(tg.sent) != 1 || tg.sent[0].ChatID != 12345 {
		t.Fatalf("sent %+v", tg.sent)
	}
	if len(e.audit.events) != 1 || e.audit.events[0].Action != "notify.test_sent" {
		t.Fatalf("audit %+v", e.audit.events)
	}

	tg.sendErr = &TelegramError{Detail: "blocked"}
	err := e.svc.TestTelegram(e.ctx, 99)
	var er *errs.Error
	if !errors.As(err, &er) || er.Code != errs.ValidationFailed || !strings.Contains(er.Message, "blocked") {
		t.Fatalf("err %v", err)
	}
}

func TestIssueBindCode(t *testing.T) {
	e := newPG(t)
	uid := e.insertTestUser(t, nil)
	fixed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	e.svc.now = func() time.Time { return fixed }

	e.st.tg = settingssvc.Telegram{Enabled: false}
	if _, err := e.svc.IssueBindCode(e.ctx, uid); !isCode(err, errs.ValidationFailed) {
		t.Fatalf("err %v", err)
	}

	tgbReady(e)
	bc, err := e.svc.IssueBindCode(e.ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(bc.Code) != 8 || !bc.ExpiresAt.Equal(fixed.Add(BindCodeTTL)) {
		t.Fatalf("code %q exp %v", bc.Code, bc.ExpiresAt)
	}
	var hash string
	if err := e.scan(e.ctx, &hash,
		`SELECT encode(code_hash, 'hex') FROM telegram_bind_codes WHERE user_id = ?`, uid,
	); err != nil {
		t.Fatal(err)
	}
	if hash == bc.Code {
		t.Fatal("hash must not equal plaintext code")
	}

	_, err = e.svc.IssueBindCode(e.ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if tgbCountUnusedCodes(t, e, uid) != 1 {
		t.Fatal("only one unused code")
	}

	if err := e.repo.InsertBinding(e.ctx, &model.TelegramBinding{UserID: uid, ChatID: 1, Username: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.IssueBindCode(e.ctx, uid); !isCode(err, errs.Conflict) {
		t.Fatalf("err %v", err)
	}
}

func TestHandleTelegramUpdateBind(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	uid := e.insertTestUser(t, dir)
	tg := &fakeTG{}
	e.svc.tg = tg
	tgbReady(e)

	if err := e.svc.HandleTelegramUpdate(e.ctx, "wrong", tgbWebhookBody("", 1, "private", "ABCD1234")); !isCode(err, errs.Unauthorized) {
		t.Fatalf("err %v", err)
	}
	// 关闭即拒绝：secret 对得上也不处理
	e.st.tg.Enabled = false
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", tgbWebhookBody("", 1, "private", "ABCD1234")); !isCode(err, errs.Unauthorized) {
		t.Fatalf("disabled telegram must reject webhook, got %v", err)
	}
	e.st.tg.Enabled = true

	bc, err := e.svc.IssueBindCode(e.ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	chatID := int64(70001)
	body := tgbWebhookBody("", chatID, "private", "/start "+bc.Code)
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", body); err != nil {
		t.Fatal(err)
	}
	b, ok, err := e.repo.Binding(e.ctx, uid)
	if err != nil || !ok || b.ChatID != chatID {
		t.Fatalf("binding %+v ok %v err %v", b, ok, err)
	}
	var usedAt *time.Time
	if err := e.scan(e.ctx, &usedAt,
		`SELECT used_at FROM telegram_bind_codes WHERE user_id = ?`, uid,
	); err != nil || usedAt == nil {
		t.Fatalf("used_at %v err %v", usedAt, err)
	}
	if len(e.audit.events) == 0 || e.audit.events[len(e.audit.events)-1].Action != "telegram.bound" {
		t.Fatalf("audit %+v", e.audit.events)
	}
	if txt := tgbLastSentText(t, tg); !strings.Contains(txt, "绑定成功") {
		t.Fatalf("reply %q", txt)
	}

	tg.sent = nil
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", body); err != nil {
		t.Fatal(err)
	}
	if txt := tgbLastSentText(t, tg); !strings.Contains(txt, "无效或已过期") {
		t.Fatalf("reply %q", txt)
	}

	// 非私聊、无 text
	tg.sent = nil
	_ = e.svc.HandleTelegramUpdate(e.ctx, "whsec", tgbWebhookBody("", 2, "group", bc.Code))
	if tgbLastSentText(t, tg) != "" {
		t.Fatal("group should not reply")
	}
	empty := tgbWebhookBody("", 2, "private", "")
	_ = e.svc.HandleTelegramUpdate(e.ctx, "whsec", empty)
	if tgbLastSentText(t, tg) != "" {
		t.Fatal("empty text")
	}

	// 乱码
	tg.sent = nil
	_ = e.svc.HandleTelegramUpdate(e.ctx, "whsec", tgbWebhookBody("", 3, "private", "!!!!!!!!"))
	if txt := tgbLastSentText(t, tg); !strings.Contains(txt, "8 位绑定码") {
		t.Fatalf("reply %q", txt)
	}

	// 同一 chat_id 绑第二个用户
	uid2 := e.insertTestUser(t, dir)
	bc2, err := e.svc.IssueBindCode(e.ctx, uid2)
	if err != nil {
		t.Fatal(err)
	}
	tg.sent = nil
	body2 := tgbWebhookBody("", chatID, "private", bc2.Code)
	if err := e.svc.HandleTelegramUpdate(e.ctx, "whsec", body2); err != nil {
		t.Fatal(err)
	}
	if txt := tgbLastSentText(t, tg); !strings.Contains(txt, "已经绑定了其它用户") {
		t.Fatalf("reply %q", txt)
	}
	var used2 bool
	if err := e.scan(e.ctx, &used2,
		`SELECT used_at IS NOT NULL FROM telegram_bind_codes WHERE user_id = ?`, uid2,
	); err != nil || used2 {
		t.Fatalf("uid2 code should stay unused: used=%v err %v", used2, err)
	}
}

func TestTelegramBindingAndUnbind(t *testing.T) {
	e := newPG(t)
	uid := e.insertTestUser(t, nil)

	info, err := e.svc.TelegramBinding(e.ctx, uid)
	if err != nil || info.Bound || info.Enabled {
		t.Fatalf("info %+v err %v", info, err)
	}

	tgbReady(e)
	info, err = e.svc.TelegramBinding(e.ctx, uid)
	if err != nil || info.Enabled != true || info.BotUsername != "mybot" {
		t.Fatalf("info %+v", info)
	}

	uidBare := e.insertTestUser(t, nil)
	if err := e.svc.UnbindTelegram(e.ctx, uidBare); !isCode(err, errs.NotFound) {
		t.Fatalf("unbind without bind %v", err)
	}

	if err := e.repo.InsertBinding(e.ctx, &model.TelegramBinding{UserID: uid, ChatID: 9, Username: "alice"}); err != nil {
		t.Fatal(err)
	}
	info, err = e.svc.TelegramBinding(e.ctx, uid)
	if err != nil || !info.Bound || info.Username != "alice" {
		t.Fatalf("info %+v", info)
	}

	if err := e.svc.UnbindTelegram(e.ctx, uid); err != nil {
		t.Fatal(err)
	}
	_, ok, _ := e.repo.Binding(e.ctx, uid)
	if ok {
		t.Fatal("binding should be gone")
	}
	found := false
	for _, ev := range e.audit.events {
		if ev.Action == "telegram.unbound" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit %+v", e.audit.events)
	}
}
