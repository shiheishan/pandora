package kernel

import (
	"encoding/base64"
	"testing"
)

func TestParseRealityClientConfig(t *testing.T) {
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	cfg, err := ParseRealityClientConfig(map[string]any{
		"server_name": "example.com",
		"public_key":  key,
		"short_id":    "01020304",
		"fingerprint": "Firefox",
		"spider_x":    "/cdn-cgi/trace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != "example.com" || len(cfg.PublicKey) != 32 || cfg.ShortID[0] != 1 || cfg.ShortID[3] != 4 || cfg.Fingerprint != "firefox" || cfg.SpiderX != "/cdn-cgi/trace" {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestParseRealityClientConfigRejectsInvalidKeyAndShortID(t *testing.T) {
	for name, raw := range map[string]map[string]any{
		"missing key":  {"server_name": "example.com"},
		"bad key":      {"server_name": "example.com", "public_key": "not-a-key"},
		"bad short id": {"server_name": "example.com", "public_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "short_id": "xyz"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRealityClientConfig(raw); err == nil {
				t.Fatal("invalid reality client config unexpectedly accepted")
			}
		})
	}
}
