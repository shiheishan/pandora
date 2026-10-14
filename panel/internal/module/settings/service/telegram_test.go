package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// kvStore 在 Telegram 测试里需要 Delete（清除管理员群组键）。
func (k *kvStore) Delete(_ context.Context, keys ...string) error {
	for _, key := range keys {
		delete(k.kv, key)
	}
	return nil
}

func newTelegramService(st *kvStore, al *auditLog) *Service {
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32), 1)
	if err != nil {
		panic(err)
	}
	return newService(st, fakeTx{}, sealer, nil, al)
}

func TestTelegramDefaults(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newTelegramService(st, &auditLog{})
	g, err := s.Telegram(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if g.Enabled || g.BotUsername != "" || g.HasToken || g.BotToken != "" || g.AdminChatID != nil || g.WebhookSecret != "" {
		t.Fatalf("defaults: %+v", g)
	}
}

func TestSetTelegramValidation(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newTelegramService(st, al)
	ctx := context.Background()

	err := s.SetTelegram(ctx, TelegramUpdate{Enabled: true})
	assertFieldValidation(t, err, "bot_username")

	err = s.SetTelegram(ctx, TelegramUpdate{BotUsername: "ab"})
	assertFieldValidation(t, err, "bot_username")

	empty := ""
	err = s.SetTelegram(ctx, TelegramUpdate{BotToken: &empty})
	assertFieldValidation(t, err, "bot_token")

	chat0 := int64(0)
	err = s.SetTelegram(ctx, TelegramUpdate{
		BotUsername:  "mybot",
		AdminChatSet: true,
		AdminChatID:  &chat0,
	})
	assertFieldValidation(t, err, "admin_chat_id")

	// 正数是私聊，不能当管理员群组
	chatPos := int64(42)
	err = s.SetTelegram(ctx, TelegramUpdate{
		BotUsername:  "mybot",
		AdminChatSet: true,
		AdminChatID:  &chatPos,
	})
	assertFieldValidation(t, err, "admin_chat_id")

	if len(st.kv) != 0 || len(al.entries) != 0 {
		t.Fatalf("validation should not persist: kv=%d audit=%d", len(st.kv), len(al.entries))
	}
}

func TestSetTelegramUsernameNormalized(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newTelegramService(st, &auditLog{})
	ctx := context.Background()

	if err := s.SetTelegram(ctx, TelegramUpdate{BotUsername: "@MyBot_12345"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	g, err := s.Telegram(ctx)
	if err != nil || g.BotUsername != "MyBot_12345" {
		t.Fatalf("read: %+v err=%v", g, err)
	}
}

func TestTelegramTokenSealAndKeep(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newTelegramService(st, al)
	ctx := context.Background()
	const secret = "1234567890:ABCdefGHIjklMNOpqrs"

	tok := secret
	if err := s.SetTelegram(ctx, TelegramUpdate{BotUsername: "MyBot_12345", BotToken: &tok}); err != nil {
		t.Fatalf("set token: %v", err)
	}
	if strings.Contains(string(st.kv[keyTelegramBotToken]), secret) {
		t.Fatal("stored token must not be plaintext")
	}
	g, err := s.Telegram(ctx)
	if err != nil || !g.HasToken || g.BotToken != secret {
		t.Fatalf("read token: %+v err=%v", g, err)
	}
	for _, e := range al.entries {
		if strings.Contains(fmt.Sprint(e.data), secret) {
			t.Fatalf("audit leaks token: %v", e.data)
		}
	}

	if err := s.SetTelegram(ctx, TelegramUpdate{BotUsername: "MyBot_12345", BotToken: nil}); err != nil {
		t.Fatalf("nil token: %v", err)
	}
	g2, err := s.Telegram(ctx)
	if err != nil || !g2.HasToken || g2.BotToken != secret {
		t.Fatalf("keep token: %+v err=%v", g2, err)
	}
}

func TestSetTelegramAdminChat(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newTelegramService(st, &auditLog{})
	ctx := context.Background()

	chat := int64(-1001234567890)
	if err := s.SetTelegram(ctx, TelegramUpdate{
		BotUsername:  "MyBot_12345",
		AdminChatSet: true,
		AdminChatID:  &chat,
	}); err != nil {
		t.Fatalf("set chat: %v", err)
	}
	g, err := s.Telegram(ctx)
	if err != nil || g.AdminChatID == nil || *g.AdminChatID != chat {
		t.Fatalf("read chat: %+v err=%v", g, err)
	}

	// 不动管理员群组
	if err := s.SetTelegram(ctx, TelegramUpdate{BotUsername: "MyBot_12345"}); err != nil {
		t.Fatalf("no admin touch: %v", err)
	}
	g, err = s.Telegram(ctx)
	if err != nil || g.AdminChatID == nil || *g.AdminChatID != chat {
		t.Fatalf("chat unchanged: %+v err=%v", g, err)
	}

	// 清除
	if err := s.SetTelegram(ctx, TelegramUpdate{
		BotUsername:  "MyBot_12345",
		AdminChatSet: true,
		AdminChatID:  nil,
	}); err != nil {
		t.Fatalf("clear chat: %v", err)
	}
	g, err = s.Telegram(ctx)
	if err != nil || g.AdminChatID != nil {
		t.Fatalf("after clear: %+v err=%v", g, err)
	}
	if _, ok := st.kv[keyTelegramAdminChatID]; ok {
		t.Fatal("admin chat key should be deleted")
	}
}

func TestSetTelegramNoopNoAudit(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newTelegramService(st, al)
	ctx := context.Background()

	if err := s.SetTelegram(ctx, TelegramUpdate{BotUsername: "MyBot_12345"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("first audit: %d", len(al.entries))
	}

	if err := s.SetTelegram(ctx, TelegramUpdate{BotUsername: "MyBot_12345"}); err != nil {
		t.Fatalf("noop: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("noop audit: %d", len(al.entries))
	}
}

func TestSetTelegramAuditShape(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newTelegramService(st, al)
	ctx := context.Background()

	enabled := true
	if err := s.SetTelegram(ctx, TelegramUpdate{Enabled: enabled, BotUsername: "MyBot_12345"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit count %d", len(al.entries))
	}
	e := al.entries[0]
	if e.action != auditTelegramChanged || e.resourceType != auditResourceSettings {
		t.Fatalf("audit meta: %s %s", e.action, e.resourceType)
	}
	grp, _ := e.data["group"].(string)
	if grp != "telegram" {
		t.Fatalf("group %q", grp)
	}
	before, _ := e.data["before"].(map[string]any)
	after, _ := e.data["after"].(map[string]any)
	if before == nil || after == nil {
		t.Fatalf("before/after missing: %v", e.data)
	}
	if _, ok := before[keyTelegramBotToken]; ok {
		t.Fatal("before must not contain token key")
	}
	if _, ok := after[keyTelegramBotToken]; ok {
		t.Fatal("after must not contain token key")
	}
	if e.data["bot_token_changed"] == true {
		t.Fatal("bot_token_changed should be false")
	}
}

func TestTelegramCorruptStore(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{
		keyTelegramEnabled: json.RawMessage(`"yes"`),
	}}
	s := newTelegramService(st, &auditLog{})
	_, err := s.Telegram(context.Background())
	if errs.As(err).Code != errs.Internal {
		t.Fatalf("corrupt enabled: %v", err)
	}
}
