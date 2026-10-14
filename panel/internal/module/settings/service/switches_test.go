package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/settings/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

type fakeStore struct {
	mu     sync.Mutex
	rows   map[string]model.FeatureSwitch
	gets   int
	getErr error
}

func (f *fakeStore) Values(context.Context, []string) (map[string]json.RawMessage, error) {
	return map[string]json.RawMessage{}, nil
}

func (f *fakeStore) LockValues(context.Context, []string) (map[string]json.RawMessage, error) {
	return map[string]json.RawMessage{}, nil
}

func (f *fakeStore) Upsert(context.Context, string, json.RawMessage, *uuid.UUID) error { return nil }
func (f *fakeStore) InsertIfAbsent(context.Context, string, json.RawMessage) (bool, error) {
	return true, nil
}

func (f *fakeStore) Delete(context.Context, ...string) error { return nil }

func (f *fakeStore) ListSwitches(ctx context.Context) ([]model.FeatureSwitch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.FeatureSwitch, 0, len(f.rows))
	for _, sw := range f.rows {
		out = append(out, sw)
	}
	return out, nil
}

func (f *fakeStore) GetSwitch(ctx context.Context, code string) (model.FeatureSwitch, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.getErr != nil {
		return model.FeatureSwitch{}, false, f.getErr
	}
	sw, ok := f.rows[code]
	return sw, ok, nil
}

func (f *fakeStore) LockSwitch(ctx context.Context, code string) (model.FeatureSwitch, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return model.FeatureSwitch{}, false, f.getErr
	}
	sw, ok := f.rows[code]
	return sw, ok, nil
}

func (f *fakeStore) UpdateSwitch(ctx context.Context, code string, enabled bool, reason *string, by *uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	sw := f.rows[code]
	sw.Enabled = enabled
	sw.Reason = reason
	sw.ChangedBy = by
	f.rows[code] = sw
	return nil
}

type fakeTx struct{}

func (fakeTx) Do(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type auditLog struct {
	entries []struct {
		action, resourceType string
		data                 map[string]any
	}
}

func (a *auditLog) Write(ctx context.Context, e auditsvc.Event) error {
	a.entries = append(a.entries, struct {
		action, resourceType string
		data                 map[string]any
	}{e.Action, e.ResourceType, e.Data})
	return nil
}

func TestSwitchEnabledMissingRowIsOpen(t *testing.T) {
	st := &fakeStore{rows: map[string]model.FeatureSwitch{}}
	s := newService(st, fakeTx{}, nil, nil, nil)
	enabled, err := s.SwitchEnabled(context.Background(), "any.code")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !enabled {
		t.Fatal("expected true for missing row")
	}
}

func TestSwitchEnabledCachesForTTL(t *testing.T) {
	st := &fakeStore{rows: map[string]model.FeatureSwitch{
		"x": {Code: "x", Enabled: false},
	}}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := newService(st, fakeTx{}, nil, nil, nil)
	s.now = func() time.Time { return now }

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		enabled, err := s.SwitchEnabled(ctx, "x")
		if err != nil || enabled {
			t.Fatalf("call %d: enabled=%v err=%v", i, enabled, err)
		}
	}
	st.mu.Lock()
	gets := st.gets
	st.mu.Unlock()
	if gets != 1 {
		t.Fatalf("gets=%d want 1", gets)
	}

	st.mu.Lock()
	st.rows["x"] = model.FeatureSwitch{Code: "x", Enabled: true}
	st.mu.Unlock()
	enabled, _ := s.SwitchEnabled(ctx, "x")
	if enabled {
		t.Fatal("cache should still be false")
	}

	now = now.Add(switchTTL + time.Millisecond)
	enabled, err := s.SwitchEnabled(ctx, "x")
	if err != nil || !enabled {
		t.Fatalf("after ttl: enabled=%v err=%v", enabled, err)
	}
	st.mu.Lock()
	gets = st.gets
	st.mu.Unlock()
	if gets != 2 {
		t.Fatalf("gets=%d want 2", gets)
	}
}

func TestSwitchEnabledErrorNotCached(t *testing.T) {
	boom := errors.New("db down")
	st := &fakeStore{rows: map[string]model.FeatureSwitch{}, getErr: boom}
	s := newService(st, fakeTx{}, nil, nil, nil)
	ctx := context.Background()
	_, err := s.SwitchEnabled(ctx, "x")
	if err == nil {
		t.Fatal("expected error")
	}
	st.mu.Lock()
	gets1 := st.gets
	st.getErr = nil
	st.rows["x"] = model.FeatureSwitch{Code: "x", Enabled: true}
	st.mu.Unlock()

	enabled, err := s.SwitchEnabled(ctx, "x")
	if err != nil || !enabled {
		t.Fatalf("second: enabled=%v err=%v", enabled, err)
	}
	st.mu.Lock()
	if st.gets <= gets1 {
		t.Fatal("expected additional get after error")
	}
	st.mu.Unlock()
}

func TestSetSwitchRules(t *testing.T) {
	st := &fakeStore{rows: map[string]model.FeatureSwitch{
		SwitchAuthLogin:    {Code: SwitchAuthLogin, Enabled: true, Essential: true},
		"billing.checkout": {Code: "billing.checkout", Enabled: true, Essential: false},
	}}
	al := &auditLog{}
	s := newService(st, fakeTx{}, nil, nil, al)
	ctx := context.Background()

	err := s.SetSwitch(ctx, SwitchAuthLogin, false, "维护")
	if e := errs.As(err); e.Code != errs.Conflict {
		t.Fatalf("essential: code=%s", e.Code)
	}

	err = s.SetSwitch(ctx, "billing.checkout", false, "  ")
	if e := errs.As(err); e.Code != errs.Conflict {
		t.Fatalf("blank reason: code=%s", e.Code)
	}

	err = s.SetSwitch(ctx, "unknown", true, "")
	if e := errs.As(err); e.Code != errs.NotFound {
		t.Fatalf("unknown: code=%s", e.Code)
	}

	long := strings.Repeat("字", 501)
	err = s.SetSwitch(ctx, "billing.checkout", false, long)
	if e := errs.As(err); e.Code != errs.ValidationFailed {
		t.Fatalf("long reason: code=%s", e.Code)
	}

	err = s.SetSwitch(ctx, "billing.checkout", false, "维护")
	if err != nil {
		t.Fatalf("ok close: %v", err)
	}
	st.mu.Lock()
	sw := st.rows["billing.checkout"]
	st.mu.Unlock()
	if sw.Enabled || sw.Reason == nil || *sw.Reason != "维护" {
		t.Fatalf("row not updated: %+v", sw)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit count %d", len(al.entries))
	}
	e := al.entries[0]
	if e.action != auditSwitchToggle || e.resourceType != auditResourceSwitch {
		t.Fatalf("audit meta: %s %s", e.action, e.resourceType)
	}
	if e.data["code"] != "billing.checkout" || e.data["enabled"] != false {
		t.Fatalf("audit data: %v", e.data)
	}
}

func TestSetSwitchInvalidatesOtherProcess(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	st := &fakeStore{rows: map[string]model.FeatureSwitch{
		"billing.checkout": {Code: "billing.checkout", Enabled: false, Essential: false, Reason: strPtr("x")},
	}}
	a := newService(st, fakeTx{}, nil, rdb, &auditLog{})
	b := newService(st, fakeTx{}, nil, rdb, &auditLog{})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.WatchSwitches(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mr.PubSubNumSub(SwitchChannel)[SwitchChannel] == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if mr.PubSubNumSub(SwitchChannel)[SwitchChannel] != 1 {
		t.Fatal("subscriber not ready")
	}

	ctxOp := context.Background()
	falseVal, err := b.SwitchEnabled(ctxOp, "billing.checkout")
	if err != nil || falseVal {
		t.Fatalf("b cache false: %v %v", falseVal, err)
	}

	if err := a.SetSwitch(ctxOp, "billing.checkout", true, ""); err != nil {
		t.Fatalf("set: %v", err)
	}
	trueA, _ := a.SwitchEnabled(ctxOp, "billing.checkout")
	if !trueA {
		t.Fatal("a should see true immediately after set")
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v, err := b.SwitchEnabled(ctxOp, "billing.checkout")
		if err == nil && v {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("b did not observe true via pub/sub")
}

func strPtr(s string) *string { return &s }
