package service

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

type countingListener struct {
	site   int
	extras int
}

func (c *countingListener) SiteSettingsChanged()       { c.site++ }
func (c *countingListener) SubscriptionExtrasChanged() { c.extras++ }

func TestSetSiteNotifiesListenerOnChange(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newService(st, fakeTx{}, nil, nil, al)
	ln := &countingListener{}
	s.UseSiteChangeListener(ln)
	ctx := context.Background()

	name := "测试站"
	if err := s.SetSite(ctx, SiteUpdate{Name: &name}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if ln.site != 1 {
		t.Fatalf("site count after change = %d, want 1", ln.site)
	}
	if err := s.SetSite(ctx, SiteUpdate{Name: &name}); err != nil {
		t.Fatalf("set same: %v", err)
	}
	if ln.site != 1 {
		t.Fatalf("site count after noop = %d, want 1", ln.site)
	}
}

func TestSetSubscriptionExtrasNotifiesListenerOnChange(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newService(st, fakeTx{}, nil, nil, al)
	ln := &countingListener{}
	s.UseSubscriptionExtrasListener(ln)
	ctx := context.Background()

	info := true
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{InfoNodes: &info}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if ln.extras != 1 {
		t.Fatalf("extras count after change = %d, want 1", ln.extras)
	}
	if err := s.SetSubscriptionExtras(ctx, SubscriptionExtrasUpdate{InfoNodes: &info}); err != nil {
		t.Fatalf("set same: %v", err)
	}
	if ln.extras != 1 {
		t.Fatalf("extras count after noop = %d, want 1", ln.extras)
	}
}

func TestMissingPortsListsListeners(t *testing.T) {
	s := newService(&kvStore{kv: map[string]json.RawMessage{}}, fakeTx{}, nil, nil, &auditLog{})
	ports := s.MissingPorts()
	if !slices.Contains(ports, "SiteChangeListener") {
		t.Fatalf("MissingPorts %v should contain SiteChangeListener", ports)
	}
	if !slices.Contains(ports, "SubscriptionExtrasListener") {
		t.Fatalf("MissingPorts %v should contain SubscriptionExtrasListener", ports)
	}

	ln := &countingListener{}
	s.UseSiteChangeListener(ln)
	s.UseSubscriptionExtrasListener(ln)
	ports = s.MissingPorts()
	if slices.Contains(ports, "SiteChangeListener") || slices.Contains(ports, "SubscriptionExtrasListener") {
		t.Fatalf("MissingPorts after inject %v should not list listener ports", ports)
	}
}
