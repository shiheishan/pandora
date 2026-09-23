package node

import "testing"

func TestParseRoutingPreservesAbsentAndExplicitEmpty(t *testing.T) {
	if got, err := parseRouting(map[string]any{}); err != nil || got != nil {
		t.Fatalf("absent routing = %#v, %v", got, err)
	}
	got, err := parseRouting(map[string]any{"outbounds": []any{}, "routes": []any{}})
	if err != nil || got == nil {
		t.Fatalf("explicit empty routing = %#v, %v", got, err)
	}
}

func TestParseRoutingRejectsMalformedEntries(t *testing.T) {
	for _, cfg := range []map[string]any{
		{"outbounds": "not-array"},
		{"outbounds": []any{"not-object"}},
		{"outbounds": []any{map[string]any{"tag": "x"}}},
		{"routes": []any{map[string]any{"outbound": "x", "matcher": "not-object"}}},
		{"final": 42},
	} {
		if _, err := parseRouting(cfg); err == nil {
			t.Fatalf("malformed routing accepted: %#v", cfg)
		}
	}
}
