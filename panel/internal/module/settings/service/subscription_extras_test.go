package service

import (
	"context"
	"encoding/json"
	"testing"
)

func newSubscriptionExtrasService(st *kvStore, al *auditLog) *Service {
	return newService(st, fakeTx{}, nil, nil, al)
}

func TestSubscriptionExtrasDefaultsEmptyStore(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newSubscriptionExtrasService(st, &auditLog{})
	e, err := s.SubscriptionExtras(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if e.InfoNodes || e.ProtocolPrefix {
		t.Fatalf("bools: info=%v prefix=%v", e.InfoNodes, e.ProtocolPrefix)
	}
}

func TestSetSubscriptionExtrasBoolsAudits(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newSubscriptionExtrasService(st, al)
	ctx := context.Background()

	info := true
	prefix := true
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{InfoNodes: &info, ProtocolPrefix: &prefix}); err != nil {
		t.Fatalf("set: %v", err)
	}
	e, err := s.SubscriptionExtras(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !e.InfoNodes || !e.ProtocolPrefix {
		t.Fatalf("read back: info=%v prefix=%v", e.InfoNodes, e.ProtocolPrefix)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit count %d", len(al.entries))
	}
	ent := al.entries[0]
	if ent.action != auditSubscriptionExtrasChanged || ent.resourceType != auditResourceSettings {
		t.Fatalf("audit: %s %s", ent.action, ent.resourceType)
	}
	before, _ := ent.data["before"].(map[string]any)
	after, _ := ent.data["after"].(map[string]any)
	if len(before) != 2 || len(after) != 2 {
		t.Fatalf("before/after keys: %v %v", before, after)
	}
	if _, ok := before[keySubscriptionInfoNodes]; !ok {
		t.Fatalf("before missing info_nodes")
	}
	if _, ok := before[keySubscriptionProtocolPrefix]; !ok {
		t.Fatalf("before missing protocol_prefix")
	}
}

func TestSetSubscriptionExtrasSameValuesNoExtraAudit(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newSubscriptionExtrasService(st, al)
	ctx := context.Background()

	info := true
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{InfoNodes: &info}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after first: %d", len(al.entries))
	}
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{InfoNodes: &info}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after noop: %d", len(al.entries))
	}
}
