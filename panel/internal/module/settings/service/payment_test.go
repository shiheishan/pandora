package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func newPaymentService(st *kvStore, al *auditLog) *Service {
	return newCommissionService(st, al)
}

func TestPaymentCallbackDefaultsEmptyStore(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newPaymentService(st, &auditLog{})
	p, err := s.PaymentCallback(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.NotifyBaseURL != "" {
		t.Fatalf("notify base: %q", p.NotifyBaseURL)
	}
}

func TestSetPaymentCallbackPersistsAndAudits(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newPaymentService(st, al)
	ctx := context.Background()

	if err := s.SetPaymentCallback(ctx, "https://pay-cb.example.test/"); err != nil {
		t.Fatalf("set: %v", err)
	}
	p, err := s.PaymentCallback(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.NotifyBaseURL != "https://pay-cb.example.test" {
		t.Fatalf("read back: %q", p.NotifyBaseURL)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit count %d", len(al.entries))
	}
	e := al.entries[0]
	if e.action != auditPaymentChanged || e.resourceType != auditResourceSettings {
		t.Fatalf("audit: %s %s", e.action, e.resourceType)
	}
}

func TestSetPaymentCallbackValidShapes(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newPaymentService(st, &auditLog{})
	ctx := context.Background()

	cases := []string{
		"https://example.test/pay",
		"http://127.0.0.1:8080",
	}
	for _, u := range cases {
		if err := s.SetPaymentCallback(ctx, u); err != nil {
			t.Fatalf("set %q: %v", u, err)
		}
		p, err := s.PaymentCallback(ctx)
		if err != nil {
			t.Fatalf("get %q: %v", u, err)
		}
		if p.NotifyBaseURL != u {
			t.Fatalf("read back %q: %q", u, p.NotifyBaseURL)
		}
	}
}

func TestSetPaymentCallbackRejectsInvalid(t *testing.T) {
	s := newPaymentService(&kvStore{kv: map[string]json.RawMessage{}}, &auditLog{})
	ctx := context.Background()

	bad := []string{
		"ftp://x.example.test",
		"https://",
		"https://a.example.test?x=1",
		"https://a.example.test#f",
		"https://u:p@a.example.test",
		"https://a.example.test/x y",
		"https://" + strings.Repeat("a", 201-len("https://")) + ".example.test",
	}
	for _, u := range bad {
		err := s.SetPaymentCallback(ctx, u)
		assertFieldValidation(t, err, "notify_base_url")
	}
}

func TestSetPaymentCallbackClearAndNoopAudit(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newPaymentService(st, al)
	ctx := context.Background()

	if err := s.SetPaymentCallback(ctx, "https://pay-cb.example.test"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after set: %d", len(al.entries))
	}

	if err := s.SetPaymentCallback(ctx, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	p, err := s.PaymentCallback(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.NotifyBaseURL != "" {
		t.Fatalf("cleared: %q", p.NotifyBaseURL)
	}
	if len(al.entries) != 2 {
		t.Fatalf("audit after clear: %d", len(al.entries))
	}

	if err := s.SetPaymentCallback(ctx, ""); err != nil {
		t.Fatalf("noop: %v", err)
	}
	if len(al.entries) != 2 {
		t.Fatalf("audit after noop: %d", len(al.entries))
	}
}

func TestPaymentCallbackCorruptStoreFallsBack(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{
		keyPaymentNotifyBaseURL: json.RawMessage(`"javascript:x"`),
	}}
	s := newPaymentService(st, &auditLog{})
	p, err := s.PaymentCallback(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.NotifyBaseURL != "" {
		t.Fatalf("notify base: %q", p.NotifyBaseURL)
	}
}

func TestNormalizeNotifyBaseURL(t *testing.T) {
	got, ok := NormalizeNotifyBaseURL("  https://pay-cb.example.test/  ")
	if !ok || got != "https://pay-cb.example.test" {
		t.Fatalf("trim: ok=%v got=%q", ok, got)
	}
	got, ok = NormalizeNotifyBaseURL("")
	if !ok || got != "" {
		t.Fatalf("empty: ok=%v got=%q", ok, got)
	}
	got, ok = NormalizeNotifyBaseURL("HTTPS://Pay-CB.example.test/cb")
	if !ok || got != "https://Pay-CB.example.test/cb" {
		t.Fatalf("scheme lower: ok=%v got=%q", ok, got)
	}
}
