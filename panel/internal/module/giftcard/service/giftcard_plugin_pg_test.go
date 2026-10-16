package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/money"
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

func TestPluginGiftcardRedeemedBalance(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	user := e.mkUser(t, ctx)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("10.50")}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	if _, err := e.redeem(ctx, user, code, nil); err != nil {
		t.Fatalf("兑换: %v", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("事件数 %d", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Event != "giftcard.redeemed" {
		t.Fatalf("event %q", ev.Event)
	}
	wantKey := "giftcard.redeemed:" + e.codeID(t, ctx, code).String()
	if ev.Key != wantKey {
		t.Fatalf("key %q want %q", ev.Key, wantKey)
	}
	if ev.Payload["code_id"] != e.codeID(t, ctx, code).String() || ev.Payload["user_id"] != user.String() {
		t.Fatalf("ids: %+v", ev.Payload)
	}
	if ev.Payload["template"] != tpl.Name || ev.Payload["type"] != TypeGeneral {
		t.Fatalf("模板: %+v", ev.Payload)
	}
	gr, ok := ev.Payload["granted"].(map[string]any)
	if !ok {
		t.Fatalf("granted 类型 %T", ev.Payload["granted"])
	}
	bal, ok := gr["balance"].(float64)
	if !ok {
		t.Fatalf("granted.balance 类型 %T", gr["balance"])
	}
	wantCents := float64(money.MustCents(dec("10.50")))
	if bal != wantCents {
		t.Fatalf("granted.balance %v want %v", bal, wantCents)
	}
}

func TestPluginGiftcardRedeemedPlan(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &planID, PriceID: &priceID}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	if _, err := e.redeem(ctx, user, code, nil); err != nil {
		t.Fatalf("兑换: %v", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("事件数 %d", len(rec.events))
	}
	gr, ok := rec.events[0].Payload["granted"].(map[string]any)
	if !ok {
		t.Fatalf("granted 类型 %T", rec.events[0].Payload["granted"])
	}
	if gr["plan_mode"] != PlanGrantNew {
		t.Fatalf("plan_mode %v want %q", gr["plan_mode"], PlanGrantNew)
	}
	if gr["subscription_id"] == nil || gr["subscription_id"] == "" {
		t.Fatalf("缺 subscription_id: %+v", gr)
	}
}

func TestPluginGiftcardEmitError(t *testing.T) {
	e, ctx := newEnv(t)
	emitErr := errors.New("emit failed")
	rec := &pluginRec{err: emitErr}
	e.svc.UsePluginEvents(rec)
	user := e.mkUser(t, ctx)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("1")}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	_, err := e.redeem(ctx, user, code, nil)
	if !errors.Is(err, emitErr) {
		t.Fatalf("期望兑换因事件失败而失败，得 %v", err)
	}
}
