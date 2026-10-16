package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/google/uuid"
)

type pluginRec struct {
	events []pluginEvt
	err    error
}

type pluginEvt struct {
	Event, Key string
	Payload    map[string]any
}

// Emit 把 payload 经 json.Marshal / Unmarshal 成 map[string]any 存下；r.err 非 nil 时返回它且不记录。
func (r *pluginRec) Emit(_ context.Context, event, key string, payload any) error {
	if r.err != nil {
		return r.err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var pl map[string]any
	if err := json.Unmarshal(raw, &pl); err != nil {
		return err
	}
	r.events = append(r.events, pluginEvt{Event: event, Key: key, Payload: pl})
	return nil
}

func payloadStr(pl map[string]any, key string) string {
	v, ok := pl[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(v)
}

func payloadInt(pl map[string]any, key string) (int, bool) {
	v, ok := pl[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

func TestPluginScanExpiring(t *testing.T) {
	e := newPG(t)
	dir, plans, subs := e.usePorts()
	uid := e.insertTestUser(t, dir)
	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000001")
	subID := uuid.MustParse("018f0000-0000-7000-8000-000000000002")
	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "基础套餐"}}
	periodEnd := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	subs.expiring = []subsvc.NotifyCandidate{{
		SubscriptionID: subID,
		UserID:         uid,
		PlanID:         planID,
		PeriodEnd:      periodEnd,
	}}
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	e.svc.now = scanFixedNow

	n, err := e.svc.Scan(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("scan %d %v", n, err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("plugin events %d", len(rec.events))
	}
	ev := rec.events[0]
	wantKey := fmt.Sprintf("expiring:%s:%dd:%d", subID, 3, periodEnd.Unix())
	if ev.Event != "subscription.expiring" || ev.Key != wantKey {
		t.Fatalf("event %+v want key %q", ev, wantKey)
	}
	if payloadStr(ev.Payload, "plan") != "基础套餐" {
		t.Fatalf("plan %v", ev.Payload["plan"])
	}
	dl, ok := payloadInt(ev.Payload, "days_left")
	if !ok || dl != 3 {
		t.Fatalf("days_left %v", ev.Payload["days_left"])
	}
	if payloadStr(ev.Payload, "subscription_id") != subID.String() ||
		payloadStr(ev.Payload, "user_id") != uid.String() {
		t.Fatalf("ids %+v", ev.Payload)
	}
	if payloadStr(ev.Payload, "expires_at") == "" {
		t.Fatal("expires_at empty")
	}

	n2, err := e.svc.Scan(e.ctx)
	if err != nil || n2 != 0 {
		t.Fatalf("second scan %d %v", n2, err)
	}
	if len(rec.events) != 1 {
		t.Fatal("第二次扫描不应再发插件事件")
	}
}

func TestPluginScanTraffic(t *testing.T) {
	e := newPG(t)
	dir, plans, subs := e.usePorts()
	uid := e.insertTestUser(t, dir)
	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000020")
	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "按量套餐"}}
	subs.quota = []subsvc.QuotaNotice{{
		SubscriptionID: uuid.New(),
		UserID:         uid,
		PlanID:         planID,
		Consumed:       96,
		Total:          100,
		PeriodStart:    time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}}
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	e.svc.now = scanFixedNow

	n, err := e.svc.Scan(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("scan %d %v", n, err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("plugin events %d", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Event != "traffic.exhausted" || !strings.HasPrefix(ev.Key, "quota:") {
		t.Fatalf("event %+v", ev)
	}
	pct, ok := payloadInt(ev.Payload, "percent")
	if !ok || pct != 95 {
		t.Fatalf("percent %v", ev.Payload["percent"])
	}
	if payloadStr(ev.Payload, "plan") != "按量套餐" {
		t.Fatalf("plan %v", ev.Payload["plan"])
	}
	cb, _ := payloadInt(ev.Payload, "consumed_bytes")
	tb, _ := payloadInt(ev.Payload, "total_bytes")
	if cb != 96 || tb != 100 {
		t.Fatalf("bytes consumed=%d total=%d", cb, tb)
	}
}

// 事件写不进去时这一条按写入失败处理（不计入 written，记日志、下轮重试）。e.ctx 是回滚型事务（service 的 tx.Do
// 会加入它），这里看不出站内信随之回滚；「事件与业务同生共死」由 plugin 模块的 TestPGEmitRollsBackWithBusiness 在真提交下验证。
func TestPluginScanEmitErrorSkipsNotice(t *testing.T) {
	e := newPG(t)
	dir, plans, subs := e.usePorts()
	uid := e.insertTestUser(t, dir)
	planID := uuid.MustParse("018f0000-0000-7000-8000-000000000001")
	subID := uuid.MustParse("018f0000-0000-7000-8000-000000000002")
	plans.plans = []catalogsvc.Plan{{ID: planID, Name: "基础套餐"}}
	periodEnd := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	subs.expiring = []subsvc.NotifyCandidate{{
		SubscriptionID: subID,
		UserID:         uid,
		PlanID:         planID,
		PeriodEnd:      periodEnd,
	}}
	rec := &pluginRec{err: errors.New("boom")}
	e.svc.UsePluginEvents(rec)
	e.svc.now = scanFixedNow

	n, err := e.svc.Scan(e.ctx)
	if err != nil || n != 0 {
		t.Fatalf("scan %d %v", n, err)
	}
	if len(rec.events) != 0 {
		t.Fatal("err 时不应记录事件")
	}
}
