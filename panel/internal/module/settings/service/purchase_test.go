package service

import (
	"context"
	"encoding/json"
	"testing"
)

func newPurchaseService(st *kvStore, al *auditLog) *Service {
	return newService(st, fakeTx{}, nil, nil, al)
}

func TestPurchaseLimitsDefaultsEmptyStore(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newPurchaseService(st, &auditLog{})
	lim, err := s.PurchaseLimits(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if lim.MaxUnpaidOrders != 0 || lim.MaxSubscriptions != 0 {
		t.Fatalf("defaults: unpaid=%d subs=%d", lim.MaxUnpaidOrders, lim.MaxSubscriptions)
	}
}

func TestSetPurchaseLimitsPersists(t *testing.T) {
	ctx := context.Background()
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newPurchaseService(st, &auditLog{})

	if err := s.SetPurchaseLimits(ctx, PurchaseLimits{MaxUnpaidOrders: 3, MaxSubscriptions: 2}); err != nil {
		t.Fatal(err)
	}
	lim, err := s.PurchaseLimits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lim.MaxUnpaidOrders != 3 || lim.MaxSubscriptions != 2 {
		t.Fatalf("read back: %+v", lim)
	}
}

func TestSetPurchaseLimitsValidation(t *testing.T) {
	ctx := context.Background()
	s := newPurchaseService(&kvStore{kv: map[string]json.RawMessage{}}, &auditLog{})

	err := s.SetPurchaseLimits(ctx, PurchaseLimits{MaxUnpaidOrders: -1, MaxSubscriptions: 0})
	assertFieldValidation(t, err, "max_unpaid_orders")

	err = s.SetPurchaseLimits(ctx, PurchaseLimits{MaxUnpaidOrders: 0, MaxSubscriptions: 51})
	assertFieldValidation(t, err, "max_subscriptions")
}

func TestSetPurchaseLimitsSameValuesNoExtraAudit(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newPurchaseService(st, al)
	ctx := context.Background()

	v := PurchaseLimits{MaxUnpaidOrders: 3, MaxSubscriptions: 2}
	if err := s.SetPurchaseLimits(ctx, v); err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after first: %d", len(al.entries))
	}
	if err := s.SetPurchaseLimits(ctx, v); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after noop: %d", len(al.entries))
	}
}

func TestPurchaseLimitsInvalidStoredFallsBack(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{
		keyPurchaseMaxUnpaidOrders: json.RawMessage(`99`),
	}}
	s := newPurchaseService(st, &auditLog{})
	lim, err := s.PurchaseLimits(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if lim.MaxUnpaidOrders != 0 {
		t.Fatalf("invalid stored unpaid: %d want 0", lim.MaxUnpaidOrders)
	}
}
