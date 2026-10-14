package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// kvStore 用内存 map 模拟设置键读写。
type kvStore struct {
	fakeStore
	kv map[string]json.RawMessage
}

func (k *kvStore) Values(_ context.Context, keys []string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(keys))
	for _, key := range keys {
		if raw, ok := k.kv[key]; ok {
			out[key] = raw
		}
	}
	return out, nil
}

func (k *kvStore) LockValues(ctx context.Context, keys []string) (map[string]json.RawMessage, error) {
	return k.Values(ctx, keys)
}

func (k *kvStore) InsertIfAbsent(_ context.Context, key string, value json.RawMessage) (bool, error) {
	if _, ok := k.kv[key]; ok {
		return false, nil
	}
	k.kv[key] = value
	return true, nil
}

func (k *kvStore) Upsert(_ context.Context, key string, value json.RawMessage, _ *uuid.UUID) error {
	if k.kv == nil {
		k.kv = map[string]json.RawMessage{}
	}
	k.kv[key] = value
	return nil
}

func newCommissionService(st *kvStore, al *auditLog) *Service {
	return newService(st, fakeTx{}, nil, nil, al)
}

func TestCommissionDefaultsEmptyStore(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newCommissionService(st, &auditLog{})
	c, err := s.Commission(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if c.RatePercent != 0 || c.FreezeDays != 3 {
		t.Fatalf("rate/freeze: %d %d", c.RatePercent, c.FreezeDays)
	}
	if !c.MinWithdraw.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("min: %s", c.MinWithdraw)
	}
	if c.Scope != CommissionScopeEveryOrder {
		t.Fatalf("scope: %q", c.Scope)
	}
}

func TestCommissionWeirdScopeFallsBack(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{
		keyCommissionScope: json.RawMessage(`"weird"`),
	}}
	s := newCommissionService(st, &auditLog{})
	c, err := s.Commission(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if c.Scope != CommissionScopeEveryOrder {
		t.Fatalf("scope: %q", c.Scope)
	}
}

func TestCommissionCorruptStoreValues(t *testing.T) {
	ctx := context.Background()

	stRate := &kvStore{kv: map[string]json.RawMessage{
		keyCommissionRatePercent: json.RawMessage(`80`),
	}}
	if _, err := newCommissionService(stRate, &auditLog{}).Commission(ctx); err == nil {
		t.Fatal("rate 80: expected error")
	}

	stMin := &kvStore{kv: map[string]json.RawMessage{
		keyCommissionMinWithdraw: json.RawMessage(`"1.234"`),
	}}
	if _, err := newCommissionService(stMin, &auditLog{}).Commission(ctx); err == nil {
		t.Fatal("min 1.234: expected error")
	}
}

func TestSetCommissionValidationFailed(t *testing.T) {
	s := newCommissionService(&kvStore{kv: map[string]json.RawMessage{}}, &auditLog{})
	ctx := context.Background()

	badRate := -1
	err := s.SetCommission(ctx, CommissionUpdate{RatePercent: &badRate})
	assertFieldValidation(t, err, "rate_percent")

	badFreeze := 91
	err = s.SetCommission(ctx, CommissionUpdate{FreezeDays: &badFreeze})
	assertFieldValidation(t, err, "freeze_days")

	neg := decimal.RequireFromString("-0.01")
	err = s.SetCommission(ctx, CommissionUpdate{MinWithdraw: &neg})
	assertFieldValidation(t, err, "min_withdraw")

	three := decimal.RequireFromString("1.234")
	err = s.SetCommission(ctx, CommissionUpdate{MinWithdraw: &three})
	assertFieldValidation(t, err, "min_withdraw")

	scope := "bad_scope"
	err = s.SetCommission(ctx, CommissionUpdate{Scope: &scope})
	assertFieldValidation(t, err, "scope")
}

func assertFieldValidation(t *testing.T, err error, field string) {
	t.Helper()
	e := errs.As(err)
	if e.Code != errs.ValidationFailed {
		t.Fatalf("code=%s want validation_failed", e.Code)
	}
	if e.Fields == nil || e.Fields[field] == "" {
		t.Fatalf("fields missing %q: %v", field, e.Fields)
	}
}

func TestSetCommissionPersistsAndAudits(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newCommissionService(st, al)
	ctx := context.Background()

	rate := 10
	minW := decimal.RequireFromString("50.5")
	if err := s.SetCommission(ctx, CommissionUpdate{RatePercent: &rate, MinWithdraw: &minW}); err != nil {
		t.Fatalf("set: %v", err)
	}

	c, err := s.Commission(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c.RatePercent != 10 || !c.MinWithdraw.Equal(decimal.RequireFromString("50.50")) {
		t.Fatalf("read back: rate=%d min=%s", c.RatePercent, c.MinWithdraw)
	}
	if string(st.kv[keyCommissionMinWithdraw]) != `"50.50"` {
		t.Fatalf("stored min: %s", st.kv[keyCommissionMinWithdraw])
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit count %d", len(al.entries))
	}
	e := al.entries[0]
	if e.action != auditCommissionChanged || e.resourceType != auditResourceSettings {
		t.Fatalf("audit: %s %s", e.action, e.resourceType)
	}
}

func TestSetCommissionSameValuesNoExtraAudit(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newCommissionService(st, al)
	ctx := context.Background()

	rate := 10
	minW := decimal.RequireFromString("50.5")
	if err := s.SetCommission(ctx, CommissionUpdate{RatePercent: &rate, MinWithdraw: &minW}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after first: %d", len(al.entries))
	}

	if err := s.SetCommission(ctx, CommissionUpdate{RatePercent: &rate, MinWithdraw: &minW}); err != nil {
		t.Fatalf("second set: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after noop set: %d", len(al.entries))
	}
}
