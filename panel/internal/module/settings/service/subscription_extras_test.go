package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
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
	if e.LinkBaseURLs == nil || len(e.LinkBaseURLs) != 0 {
		t.Fatalf("urls: %v", e.LinkBaseURLs)
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

func TestSetSubscriptionExtrasLinkBaseURLsNormalize(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newSubscriptionExtrasService(st, al)
	ctx := context.Background()

	urls := []string{" https://Sub.Example.com/ ", "https://sub.example.com", "https://b.example.test:8443"}
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{LinkBaseURLs: &urls}); err != nil {
		t.Fatalf("set: %v", err)
	}
	e, err := s.SubscriptionExtras(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := []string{"https://sub.example.com", "https://b.example.test:8443"}
	if len(e.LinkBaseURLs) != len(want) {
		t.Fatalf("urls %v want %v", e.LinkBaseURLs, want)
	}
	for i := range want {
		if e.LinkBaseURLs[i] != want[i] {
			t.Fatalf("urls %v want %v", e.LinkBaseURLs, want)
		}
	}
	var stored []string
	if err := json.Unmarshal(st.kv[keySubscriptionLinkBaseURLs], &stored); err != nil {
		t.Fatalf("stored json: %v", err)
	}
	if len(stored) != 2 || stored[0] != want[0] || stored[1] != want[1] {
		t.Fatalf("stored %v", stored)
	}
}

func TestSetSubscriptionExtrasSameValuesNoExtraAudit(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newSubscriptionExtrasService(st, al)
	ctx := context.Background()

	urls := []string{"https://a.example.com"}
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{LinkBaseURLs: &urls}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after first: %d", len(al.entries))
	}
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{LinkBaseURLs: &urls}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after noop: %d", len(al.entries))
	}
}

func TestSetSubscriptionExtrasClearLinkBaseURLs(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newSubscriptionExtrasService(st, &auditLog{})
	ctx := context.Background()

	urls := []string{"https://a.example.com"}
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{LinkBaseURLs: &urls}); err != nil {
		t.Fatalf("set: %v", err)
	}
	empty := []string{}
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{LinkBaseURLs: &empty}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	e, err := s.SubscriptionExtras(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if e.LinkBaseURLs == nil || len(e.LinkBaseURLs) != 0 {
		t.Fatalf("urls %v", e.LinkBaseURLs)
	}
}

func TestSetSubscriptionExtrasInvalidLinkBaseURLs(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newSubscriptionExtrasService(st, &auditLog{})
	ctx := context.Background()

	cases := []struct {
		name string
		urls []string
	}{
		{"http", []string{"http://a.example.com"}},
		{"path", []string{"https://a.example.com/sub"}},
		{"query", []string{"https://a.example.com?x=1"}},
		{"userinfo", []string{"https://u:p@a.example.com"}},
		{"ftp", []string{"ftp://a.example.com"}},
	}
	for _, tc := range cases {
		err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{LinkBaseURLs: &tc.urls})
		if err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		assertFieldValidation(t, err, "link_base_urls")
	}

	many := make([]string, 11)
	for i := range many {
		many[i] = "https://n" + string(rune('0'+i)) + ".example.com"
	}
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{LinkBaseURLs: &many}); err == nil {
		t.Fatal("11 urls: expected error")
	}
	assertFieldValidation(t, err, "link_base_urls")

	long := []string{"https://" + strings.Repeat("x", 250)}
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{LinkBaseURLs: &long}); err == nil {
		t.Fatal("long url: expected error")
	}
	assertFieldValidation(t, err, "link_base_urls")

	if len(st.kv) != 0 {
		t.Fatalf("store changed: %v", st.kv)
	}
}

func TestSubscriptionExtrasCorruptLinkBaseURLs(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{
		keySubscriptionLinkBaseURLs: json.RawMessage(`"oops"`),
	}}
	s := newSubscriptionExtrasService(st, &auditLog{})
	if _, err := s.SubscriptionExtras(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if errs.As(err).Code != errs.Internal {
		t.Fatalf("code %s", errs.As(err).Code)
	}
}
