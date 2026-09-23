package kernel

import (
	"strings"
	"testing"
	"time"
)

func TestParseRealityServerConfig(t *testing.T) {
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	cfg, err := ParseRealityServerConfig(map[string]any{
		"dest":          "example.com:443",
		"server_names":  []any{"Example.com."},
		"private_key":   key,
		"short_ids":     []any{"01", "aabbccdd"},
		"max_time_diff": 10.0,
		"xver":          2.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PrivateKey) != 32 || !cfg.ServerNames["example.com"] || cfg.MaxTimeDiff != 10*time.Second || cfg.Xver != 2 {
		t.Fatalf("parsed reality config = %+v", cfg)
	}
	if len(cfg.ShortIDs) != 2 {
		t.Fatalf("short ids = %d, want 2", len(cfg.ShortIDs))
	}
}

func TestParseRealityRejectsUnsafeOrMalformedConfig(t *testing.T) {
	base := map[string]any{
		"dest":         "example.com:443",
		"server_names": "example.com",
		"private_key":  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing dest":  func(m map[string]any) { delete(m, "dest") },
		"missing names": func(m map[string]any) { delete(m, "server_names") },
		"bad key":       func(m map[string]any) { m["private_key"] = "bad" },
		"bad short id":  func(m map[string]any) { m["short_ids"] = []any{"abc"} },
		"large skew":    func(m map[string]any) { m["max_time_diff"] = 601.0 },
		"bad xver":      func(m map[string]any) { m["xver"] = 3.0 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := make(map[string]any, len(base))
			for k, v := range base {
				copy[k] = v
			}
			mutate(copy)
			if _, err := ParseRealityServerConfig(copy); err == nil || strings.TrimSpace(err.Error()) == "" {
				t.Fatalf("malformed reality config unexpectedly accepted: %v", err)
			}
		})
	}
}
