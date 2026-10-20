package service

import (
	"context"
	"encoding/json"
	"testing"
)

func newSupportService(st *kvStore, al *auditLog) *Service {
	return newService(st, fakeTx{}, nil, nil, al)
}

func TestValidateSupportRules(t *testing.T) {
	cases := []struct {
		name   string
		rules  SupportRules
		field  string
		wantOK bool
	}{
		{name: "hours=0", rules: SupportRules{AutoCloseHours: 0, MaxOpenTickets: DefaultSupportMaxOpenTickets}, field: "auto_close_hours"},
		{name: "hours=1", rules: SupportRules{AutoCloseHours: 1, MaxOpenTickets: DefaultSupportMaxOpenTickets}, wantOK: true},
		{name: "hours=720", rules: SupportRules{AutoCloseHours: 720, MaxOpenTickets: DefaultSupportMaxOpenTickets}, wantOK: true},
		{name: "hours=721", rules: SupportRules{AutoCloseHours: 721, MaxOpenTickets: DefaultSupportMaxOpenTickets}, field: "auto_close_hours"},
		{name: "max_open=0", rules: SupportRules{AutoCloseHours: DefaultSupportAutoCloseHours, MaxOpenTickets: 0}, field: "max_open_tickets"},
		{name: "max_open=51", rules: SupportRules{AutoCloseHours: DefaultSupportAutoCloseHours, MaxOpenTickets: 51}, field: "max_open_tickets"},
		{name: "max_open=8", rules: SupportRules{AutoCloseHours: DefaultSupportAutoCloseHours, MaxOpenTickets: 8}, wantOK: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSupportRules(tc.rules)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("err: %v", err)
				}
				return
			}
			assertFieldValidation(t, err, tc.field)
		})
	}
}

func TestSupportRulesDefaultsEmptyStore(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newSupportService(st, &auditLog{})
	rules, err := s.SupportRules(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rules.MaxOpenTickets != DefaultSupportMaxOpenTickets {
		t.Fatalf("max_open_tickets: %d want %d", rules.MaxOpenTickets, DefaultSupportMaxOpenTickets)
	}
}

func TestSetSupportRulesMaxOpenTickets(t *testing.T) {
	ctx := context.Background()
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newSupportService(st, &auditLog{})

	if err := s.SetSupportRules(ctx, SupportRules{
		WaitForReply: false, AutoClose: false,
		AutoCloseHours: DefaultSupportAutoCloseHours, MaxOpenTickets: 8,
	}); err != nil {
		t.Fatal(err)
	}
	rules, err := s.SupportRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rules.MaxOpenTickets != 8 {
		t.Fatalf("read back: %d", rules.MaxOpenTickets)
	}

	err = s.SetSupportRules(ctx, SupportRules{
		AutoCloseHours: DefaultSupportAutoCloseHours, MaxOpenTickets: 0,
	})
	assertFieldValidation(t, err, "max_open_tickets")

	err = s.SetSupportRules(ctx, SupportRules{
		AutoCloseHours: DefaultSupportAutoCloseHours, MaxOpenTickets: 51,
	})
	assertFieldValidation(t, err, "max_open_tickets")
}

func TestSupportRulesInvalidStoredMaxOpenFallsBack(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{
		keySupportMaxOpenTickets: json.RawMessage(`0`),
	}}
	s := newSupportService(st, &auditLog{})
	rules, err := s.SupportRules(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rules.MaxOpenTickets != DefaultSupportMaxOpenTickets {
		t.Fatalf("invalid stored: %d want %d", rules.MaxOpenTickets, DefaultSupportMaxOpenTickets)
	}
}
