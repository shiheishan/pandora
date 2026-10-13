package service

import (
	"encoding/json"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func fieldErr(err error, key string) string {
	e := errs.As(err)
	if e.Fields == nil {
		return ""
	}
	return e.Fields[key]
}

func TestValidateRoutingInputDuplicateTag(t *testing.T) {
	in := &RoutingInput{
		Outbounds: []RoutingOutbound{
			{Tag: "A", Type: "socks", Settings: json.RawMessage(`{}`)},
			{Tag: "a", Type: "http", Settings: json.RawMessage(`{}`)},
		},
	}
	_, err := validateRoutingInput(in)
	if err == nil || fieldErr(err, "outbounds") == "" {
		t.Fatalf("expected duplicate tag error, got %v", err)
	}
}

func TestValidateRoutingInputBuiltinTag(t *testing.T) {
	in := &RoutingInput{
		Outbounds: []RoutingOutbound{
			{Tag: "Direct", Type: "socks", Settings: json.RawMessage(`{}`)},
		},
	}
	_, err := validateRoutingInput(in)
	if err == nil {
		t.Fatal("expected error for Direct tag")
	}
}

func TestValidateRoutingInputUnsupportedType(t *testing.T) {
	in := &RoutingInput{
		Outbounds: []RoutingOutbound{
			{Tag: "x", Type: "NOTREAL", Settings: json.RawMessage(`{}`)},
		},
	}
	_, err := validateRoutingInput(in)
	msg := fieldErr(err, "outbounds")
	if err == nil || msg == "" {
		t.Fatalf("got %v", err)
	}
}

func TestValidateRoutingInputTypeLowercase(t *testing.T) {
	in := &RoutingInput{
		Outbounds: []RoutingOutbound{
			{Tag: "p", Type: "SOCKS", Settings: json.RawMessage(`{}`)},
		},
	}
	_, err := validateRoutingInput(in)
	if err != nil {
		t.Fatal(err)
	}
	if in.Outbounds[0].Type != "socks" {
		t.Fatalf("type %q", in.Outbounds[0].Type)
	}
}

func TestValidateRoutingInputSettingsNotObject(t *testing.T) {
	in := &RoutingInput{
		Outbounds: []RoutingOutbound{
			{Tag: "p", Type: "socks", Settings: json.RawMessage(`[]`)},
		},
	}
	_, err := validateRoutingInput(in)
	if err == nil {
		t.Fatal("expected settings error")
	}
}

func TestValidateRoutingInputEmptySettings(t *testing.T) {
	in := &RoutingInput{
		Outbounds: []RoutingOutbound{
			{Tag: "p", Type: "socks", Settings: nil},
		},
	}
	_, err := validateRoutingInput(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(in.Outbounds[0].Settings) != "{}" {
		t.Fatalf("settings %q", in.Outbounds[0].Settings)
	}
}

func TestValidateRoutingInputPriorityZero(t *testing.T) {
	in := &RoutingInput{
		Routes: []RoutingRule{
			{Priority: 0, Matcher: json.RawMessage(`{"domain":["a.com"]}`), OutboundTag: "direct", Enabled: true},
			{Priority: 0, Matcher: json.RawMessage(`{"domain":["b.com"]}`), OutboundTag: "block", Enabled: true},
		},
	}
	_, err := validateRoutingInput(in)
	if err != nil {
		t.Fatal(err)
	}
	if in.Routes[0].Priority != 10 || in.Routes[1].Priority != 20 {
		t.Fatalf("priorities %d %d", in.Routes[0].Priority, in.Routes[1].Priority)
	}
}

func TestValidateRoutingInputStableSortByPriority(t *testing.T) {
	in := &RoutingInput{
		Routes: []RoutingRule{
			{Priority: 30, Matcher: json.RawMessage(`{"domain":["c.com"]}`), OutboundTag: "direct", Enabled: true},
			{Priority: 10, Matcher: json.RawMessage(`{"domain":["a.com"]}`), OutboundTag: "direct", Enabled: true},
			{Priority: 20, Matcher: json.RawMessage(`{"domain":["b.com"]}`), OutboundTag: "direct", Enabled: true},
		},
	}
	_, err := validateRoutingInput(in)
	if err != nil {
		t.Fatal(err)
	}
	if in.Routes[0].Priority != 10 || in.Routes[2].Priority != 30 {
		t.Fatalf("order wrong: %d %d %d", in.Routes[0].Priority, in.Routes[1].Priority, in.Routes[2].Priority)
	}
}

func TestValidateRoutingInputCatchAllNotLast(t *testing.T) {
	in := &RoutingInput{
		Routes: []RoutingRule{
			{Priority: 10, Matcher: json.RawMessage(`{}`), OutboundTag: "direct", Enabled: true},
			{Priority: 20, Matcher: json.RawMessage(`{"domain":["x.com"]}`), OutboundTag: "direct", Enabled: true},
		},
	}
	_, err := validateRoutingInput(in)
	if err == nil {
		t.Fatal("expected catch-all not last error")
	}
}

func TestValidateRoutingInputCatchAllWithDisabledAfter(t *testing.T) {
	in := &RoutingInput{
		Routes: []RoutingRule{
			{Priority: 10, Matcher: json.RawMessage(`{"domain":["x.com"]}`), OutboundTag: "direct", Enabled: true},
			{Priority: 20, Matcher: json.RawMessage(`{}`), OutboundTag: "block", Enabled: true},
			{Priority: 30, Matcher: json.RawMessage(`{"domain":["y.com"]}`), OutboundTag: "direct", Enabled: false},
		},
	}
	_, err := validateRoutingInput(in)
	if err != nil {
		t.Fatalf("expected pass: %v", err)
	}
}

func TestValidateRoutingInputBuiltinOutboundCanonical(t *testing.T) {
	in := &RoutingInput{
		Routes: []RoutingRule{
			{Priority: 10, Matcher: json.RawMessage(`{"domain":["x.com"]}`), OutboundTag: " Direct ", Enabled: true},
		},
	}
	_, err := validateRoutingInput(in)
	if err != nil {
		t.Fatal(err)
	}
	if in.Routes[0].OutboundTag != "direct" {
		t.Fatalf("tag %q", in.Routes[0].OutboundTag)
	}
}

func TestValidateRoutingInputNULRejected(t *testing.T) {
	in := &RoutingInput{
		Outbounds: []RoutingOutbound{
			{Tag: "a\x00b", Type: "socks", Settings: json.RawMessage(`{}`)},
		},
	}
	_, err := validateRoutingInput(in)
	if err == nil {
		t.Fatal("expected NUL rejection")
	}
}

func TestValidateMatcherAllowedKeys(t *testing.T) {
	_, err := validateMatcher(json.RawMessage(`{"domain":["a.com"],"port":[443]}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateMatcherGeoipRejected(t *testing.T) {
	_, err := validateMatcher(json.RawMessage(`{"geoip":["CN"]}`))
	if err == nil {
		t.Fatal("expected geoip rejected")
	}
}

func TestValidateMatcherEmptyArray(t *testing.T) {
	_, err := validateMatcher(json.RawMessage(`{"domain":[]}`))
	if err == nil {
		t.Fatal("expected empty array error")
	}
}

func TestValidateMatcherBadCIDR(t *testing.T) {
	_, err := validateMatcher(json.RawMessage(`{"ip":["not-a-cidr"]}`))
	if err == nil {
		t.Fatal("expected bad cidr")
	}
}

func TestValidateMatcherBadPortRange(t *testing.T) {
	_, err := validateMatcher(json.RawMessage(`{"port":["9-1"]}`))
	if err == nil {
		t.Fatal("expected bad port range")
	}
}

func TestValidateMatcherPortRangeAndInt(t *testing.T) {
	_, err := validateMatcher(json.RawMessage(`{"port":["8000-9000",443]}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateMatcherNetworkTCP(t *testing.T) {
	_, err := validateMatcher(json.RawMessage(`{"network":["TCP"]}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateMatcherNetworkIcmpRejected(t *testing.T) {
	_, err := validateMatcher(json.RawMessage(`{"network":["icmp"]}`))
	if err == nil {
		t.Fatal("expected icmp rejected")
	}
}

func TestValidateMatcherEmptyObject(t *testing.T) {
	empty, err := validateMatcher(json.RawMessage(`{}`))
	if err != nil || !empty {
		t.Fatalf("empty=%v err=%v", empty, err)
	}
}

func TestCheckRouteRefs(t *testing.T) {
	routes := []RoutingRule{
		{OutboundTag: "direct"},
		{OutboundTag: "my"},
	}
	self := map[string]bool{"my": true}
	vis := map[string]bool{"other": true}
	if err := checkRouteRefs(routes, self, vis); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRouteRefsCaseSensitive(t *testing.T) {
	routes := []RoutingRule{{OutboundTag: "My"}}
	vis := map[string]bool{"my": true}
	err := checkRouteRefs(routes, vis)
	if err == nil {
		t.Fatal("expected case mismatch error")
	}
	if errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("code %v", err)
	}
}

func TestMergeRoutingLayerOrder(t *testing.T) {
	gid := uuid.MustParse("018f0000-0000-7000-8000-000000000001")
	layers := []routingLayer{
		{
			source: RoutingSource{Scope: routingScopeNode},
			routes: []RoutingRule{{Matcher: json.RawMessage(`{"domain":["n"]}`), OutboundTag: "n1", Enabled: true}},
		},
		{
			source: RoutingSource{Scope: routingScopeGroup, GroupID: &gid, GroupName: "g"},
			routes: []RoutingRule{{Matcher: json.RawMessage(`{"domain":["g"]}`), OutboundTag: "g1", Enabled: true}},
		},
		{
			source: RoutingSource{Scope: routingScopeGlobal},
			routes: []RoutingRule{{Matcher: json.RawMessage(`{"domain":["w"]}`), OutboundTag: "w1", Enabled: true}},
		},
	}
	_, routes := mergeRouting(layers)
	if len(routes) != 3 || routes[0].Outbound != "n1" || routes[1].Outbound != "g1" || routes[2].Outbound != "w1" {
		t.Fatalf("routes %v", routes)
	}
}

func TestMergeRoutingOutboundOverwriteKeepsPosition(t *testing.T) {
	layers := []routingLayer{
		{
			source:    RoutingSource{Scope: routingScopeNode},
			outbounds: []RoutingOutbound{{Tag: "x", Type: "socks", Settings: json.RawMessage(`{"a":1}`)}},
		},
		{
			source: RoutingSource{Scope: routingScopeGlobal},
			outbounds: []RoutingOutbound{
				{Tag: "x", Type: "http", Settings: json.RawMessage(`{"b":2}`)},
				{Tag: "y", Type: "http", Settings: json.RawMessage(`{}`)},
			},
		},
	}
	outs, _ := mergeRouting(layers)
	if len(outs) != 2 {
		t.Fatalf("len %d", len(outs))
	}
	if outs[0].Tag != "x" || outs[0].Type != "socks" || outs[0].Source.Scope != routingScopeNode {
		t.Fatalf("first %v", outs[0])
	}
	if outs[1].Tag != "y" {
		t.Fatalf("second %v", outs[1])
	}
}

func TestMergeRoutingSkipsDisabled(t *testing.T) {
	layers := []routingLayer{
		{
			source: RoutingSource{Scope: routingScopeGlobal},
			routes: []RoutingRule{
				{Matcher: json.RawMessage(`{"domain":["a"]}`), OutboundTag: "direct", Enabled: false},
			},
		},
	}
	_, routes := mergeRouting(layers)
	if routes == nil || len(routes) != 0 {
		t.Fatalf("routes %v", routes)
	}
}

func TestMergeRoutingEmptyLayers(t *testing.T) {
	outs, routes := mergeRouting(nil)
	if outs == nil || routes == nil {
		t.Fatal("expected non-nil empty slices")
	}
	if len(outs) != 0 || len(routes) != 0 {
		t.Fatal("expected empty")
	}
}
