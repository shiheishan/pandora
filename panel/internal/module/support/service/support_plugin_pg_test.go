package service

import (
	"context"
	"encoding/json"
	"testing"
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

func TestPluginTicketCreated(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	name := "门户用户"
	user := e.mkUser(t, ctx, &name)
	uctx := asUser(ctx, user)

	body := "首行当标题\n" + createBody("")
	tk, err := e.svc.Create(uctx, CreateInput{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("事件数 %d", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Event != "ticket.created" {
		t.Fatalf("event %q", ev.Event)
	}
	wantKey := "ticket.created:" + tk.ID.String()
	if ev.Key != wantKey {
		t.Fatalf("key %q want %q", ev.Key, wantKey)
	}
	if ev.Payload["ticket_no"] != tk.TicketNo || ev.Payload["user_id"] != user.String() {
		t.Fatalf("ids: %+v", ev.Payload)
	}
	if ev.Payload["category"] != tk.Category || ev.Payload["priority"] != tk.Priority || ev.Payload["subject"] != tk.Subject {
		t.Fatalf("字段: %+v", ev.Payload)
	}
}
