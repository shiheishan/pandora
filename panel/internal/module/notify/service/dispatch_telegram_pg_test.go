package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/google/uuid"
)

func tgdReadySettings(e *pgEnv) {
	e.st.tg = settingssvc.Telegram{
		Enabled:     true,
		BotUsername: "example_bot",
		BotToken:    "123:abc",
		HasToken:    true,
	}
}

func tgdInsert(t *testing.T, e *pgEnv, vars map[string]string) {
	t.Helper()
	if vars == nil {
		vars = map[string]string{
			"plan":       "基础版",
			"days":       "3",
			"expires_at": "2026-10-20",
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(deliveryPayload{To: "123456789", Vars: vars})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := e.svc.sealer.Seal(raw, deliveryAAD(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.store.InsertDelivery(e.ctx, &model.Delivery{
		ID:            id,
		TemplateCode:  TemplateSubscriptionExpiring,
		Channel:       ChannelTelegram,
		RecipientHash: e.svc.recipientHash("123456789"),
		PayloadEnc:    blob,
		Status:        model.StatusQueued,
		MaxAttempts:   maxAttempts,
	}); err != nil {
		t.Fatal(err)
	}
}

func tgdRows(t *testing.T, e *pgEnv, channel string) []deliveryRow {
	t.Helper()
	var out []deliveryRow
	err := e.db.Conn(e.ctx).Raw(`SELECT id::text AS id, template_code, status, attempts, error_message,
	        payload_enc IS NOT NULL AS has_payload
	   FROM notification_deliveries WHERE channel = ? ORDER BY created_at, id`, channel).Scan(&out).Error
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDispatchTelegramReady(t *testing.T) {
	e := newPG(t)
	fake := &fakeTG{}
	e.svc.tg = fake
	tgdReadySettings(e)
	tgdInsert(t, e, nil)

	n, err := e.svc.Dispatch(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("sent %d", len(fake.sent))
	}
	m := fake.sent[0]
	if m.ChatID != 123456789 || m.Token != "123:abc" {
		t.Fatalf("message %+v", m)
	}
	if !strings.Contains(m.Text, "套餐即将到期") || !strings.Contains(m.Text, "基础版") {
		t.Fatalf("text %q", m.Text)
	}
	rows := tgdRows(t, e, ChannelTelegram)
	if len(rows) != 1 || rows[0].Status != model.StatusSent || rows[0].HasPayload {
		t.Fatalf("rows %+v", rows)
	}
}

func TestDispatchTelegramNotReady(t *testing.T) {
	e := newPG(t)
	fake := &fakeTG{}
	e.svc.tg = fake
	e.st.tg = settingssvc.Telegram{Enabled: false, BotUsername: "x", BotToken: "t", HasToken: true}
	tgdInsert(t, e, nil)

	if n, err := e.svc.Dispatch(e.ctx); err != nil || n != 1 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	if len(fake.sent) != 0 {
		t.Fatal("should not send")
	}
	rows := tgdRows(t, e, ChannelTelegram)
	if len(rows) != 1 || rows[0].Status != model.StatusSuppressed ||
		rows[0].ErrorMessage == nil || *rows[0].ErrorMessage != "渠道未配置" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestDispatchTelegramPermanentFailure(t *testing.T) {
	e := newPG(t)
	fake := &fakeTG{sendErr: &TelegramError{Status: 403, Detail: "HTTP 403 Forbidden", Permanent: true}}
	e.svc.tg = fake
	tgdReadySettings(e)
	tgdInsert(t, e, nil)

	if n, err := e.svc.Dispatch(e.ctx); err != nil || n != 1 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	rows := tgdRows(t, e, ChannelTelegram)
	if len(rows) != 1 || rows[0].Status != model.StatusFailed {
		t.Fatalf("rows %+v", rows)
	}
}

func TestDispatchTelegramRetryableFailure(t *testing.T) {
	e := newPG(t)
	fake := &fakeTG{sendErr: &TelegramError{Status: 500, Detail: "HTTP 500 internal", Permanent: false}}
	e.svc.tg = fake
	tgdReadySettings(e)
	tgdInsert(t, e, nil)

	if n, err := e.svc.Dispatch(e.ctx); err != nil || n != 1 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	rows := tgdRows(t, e, ChannelTelegram)
	if len(rows) != 1 || rows[0].Status != model.StatusQueued || rows[0].Attempts != 1 ||
		!rows[0].HasPayload || rows[0].ErrorMessage == nil {
		t.Fatalf("rows %+v", rows)
	}
}

func TestDispatchEmailAndTelegramSameRun(t *testing.T) {
	e := newPG(t)
	fake := &fakeTG{}
	e.svc.tg = fake
	tgdReadySettings(e)
	e.send(t, TemplateEmailVerify)
	tgdInsert(t, e, nil)

	n, err := e.svc.Dispatch(e.ctx)
	if err != nil || n != 2 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	if len(e.sender.sent) != 1 || len(fake.sent) != 1 {
		t.Fatalf("email %d tg %d", len(e.sender.sent), len(fake.sent))
	}
}
