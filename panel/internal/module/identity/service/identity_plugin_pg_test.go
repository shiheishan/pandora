package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
)

type pluginRec struct {
	events []pluginEvt
	err    error
}

type pluginEvt struct {
	Event, Key string
	Payload    map[string]any
}

func (r *pluginRec) Emit(_ context.Context, event, key string, payload any) error {
	if r.err != nil {
		return r.err
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	r.events = append(r.events, pluginEvt{Event: event, Key: key, Payload: m})
	return nil
}

func TestPluginUserRegistered(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	admin := e.mkUser(t, ctx, uniqEmail(t, "plugadmin"), "Passw0rdLong1")
	setRegistration(t, asAdmin(ctx, admin.ID), e.settings, settingssvc.RegistrationOpen, false)

	email := uniqEmail(t, "New")
	start, err := e.svc.StartRegistration(ctx, " "+strings.ToUpper(email)+" ", "", "")
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.svc.CompleteRegistration(ctx, start.Token, "", "abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("事件数 %d", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Event != "user.registered" {
		t.Fatalf("event %q", ev.Event)
	}
	wantKey := "user.registered:" + u.ID.String()
	if ev.Key != wantKey {
		t.Fatalf("key %q want %q", ev.Key, wantKey)
	}
	if ev.Payload["user_id"] != u.ID.String() || ev.Payload["email"] != strings.ToLower(email) {
		t.Fatalf("payload %+v", ev.Payload)
	}
}

// 事件写不进去时注册失败、错误原样带回。ctx 是回滚型事务（service 的 tx.Do 会加入它），这里看不出回滚本身；
// 「事件与业务同生共死」由 plugin 模块的 TestPGEmitRollsBackWithBusiness 在真提交下验证。
func TestPluginUserRegisteredEmitError(t *testing.T) {
	e, ctx := newEnv(t)
	emitErr := errors.New("emit failed")
	rec := &pluginRec{err: emitErr}
	e.svc.UsePluginEvents(rec)
	admin := e.mkUser(t, ctx, uniqEmail(t, "plugfail"), "Passw0rdLong1")
	setRegistration(t, asAdmin(ctx, admin.ID), e.settings, settingssvc.RegistrationOpen, false)

	start, err := e.svc.StartRegistration(ctx, uniqEmail(t, "Fail"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CompleteRegistration(ctx, start.Token, "", "abc12345"); !errors.Is(err, emitErr) {
		t.Fatalf("期望注册因事件失败而失败，得 %v", err)
	}
}
