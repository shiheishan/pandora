package route

import (
	"net/netip"
	"strings"
	"testing"
)

func TestSourceCIDRAndSourcePortAliases(t *testing.T) {
	engine, err := CompileStrict([]RawRule{{
		Matcher: map[string]any{
			"source_cidrs": []any{"10.0.0.0/8"},
			"source_ports": []any{"1000-2000", "3000-4000"},
			"networks":     "tcp",
		},
		OutboundTag: "relay",
	}}, map[string]bool{"direct": true, "relay": true}, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}

	base := Meta{
		SourceIP:   netip.MustParseAddr("::ffff:10.1.2.3"),
		SourcePort: 3500,
		Network:    "tcp",
	}
	if got := engine.Match(base); got != "relay" {
		t.Fatalf("source matcher = %q, want relay", got)
	}
	base.SourcePort = 2500
	if got := engine.Match(base); got != "direct" {
		t.Fatalf("gap between source port ranges matched %q", got)
	}
	base.SourcePort = 1500
	base.SourceIP = netip.MustParseAddr("192.0.2.10")
	if got := engine.Match(base); got != "direct" {
		t.Fatalf("outside source CIDR matched %q", got)
	}
}

func TestMultipleDestinationPortRangesAreAllPreserved(t *testing.T) {
	engine, err := CompileStrict([]RawRule{{
		Matcher:     map[string]any{"ports": []any{"80-90", "8000-9000"}},
		OutboundTag: "relay",
	}}, map[string]bool{"direct": true, "relay": true}, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []uint16{80, 85, 90, 8000, 8443, 9000} {
		if got := engine.Match(Meta{Port: port}); got != "relay" {
			t.Errorf("port %d = %q, want relay", port, got)
		}
	}
	for _, port := range []uint16{79, 91, 7999, 9001} {
		if got := engine.Match(Meta{Port: port}); got != "direct" {
			t.Errorf("port %d unexpectedly matched %q", port, got)
		}
	}
}

func TestCompileStrictRejectsSilentFallbacks(t *testing.T) {
	tests := []struct {
		name      string
		rules     []RawRule
		outbounds map[string]bool
		final     string
		want      string
	}{
		{
			name:      "missing final",
			outbounds: map[string]bool{"direct": true},
			want:      "缺少最终出站",
		},
		{
			name:      "unknown final",
			outbounds: map[string]bool{"direct": true},
			final:     "missing",
			want:      "不存在",
		},
		{
			name: "unknown rule target",
			rules: []RawRule{{
				Matcher:     map[string]any{"domain": "example.com"},
				OutboundTag: "missing",
			}},
			outbounds: map[string]bool{"direct": true},
			final:     "direct",
			want:      "不存在的出站",
		},
		{
			name: "missing geo database",
			rules: []RawRule{{
				Matcher:     map[string]any{"geoips": "cn"},
				OutboundTag: "direct",
			}},
			outbounds: map[string]bool{"direct": true},
			final:     "direct",
			want:      "GeoIP",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompileStrict(tc.rules, tc.outbounds, tc.final, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestCompileStrictRejectsPresentButEmptyOrInvalidMatchers(t *testing.T) {
	for _, value := range []any{nil, true, map[string]any{"bad": true}, "", []any{}} {
		_, err := CompileStrict([]RawRule{{
			Matcher:     map[string]any{"domains": value},
			OutboundTag: "relay",
		}}, map[string]bool{"direct": true, "relay": true}, "direct", nil)
		if err == nil {
			t.Fatalf("matcher value %#v became an unconditional rule", value)
		}
	}
}

func TestMatcherAliasesMergeDeterministically(t *testing.T) {
	engine, err := CompileStrict([]RawRule{{
		Matcher: map[string]any{
			"domain":   "one.example",
			"domains":  "two.example",
			"network":  "tcp",
			"networks": "udp",
		},
		OutboundTag: "relay",
	}}, map[string]bool{"direct": true, "relay": true}, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []Meta{
		{Domain: "one.example", Network: "tcp"},
		{Domain: "one.example", Network: "udp"},
		{Domain: "two.example", Network: "tcp"},
		{Domain: "two.example", Network: "udp"},
	} {
		if got := engine.Match(tc); got != "relay" {
			t.Fatalf("aliases did not merge for %+v: %q", tc, got)
		}
	}
}
