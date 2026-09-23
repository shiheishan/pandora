package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDefaultsToNativeOnly(t *testing.T) {
	cfg := loadTestConfig(t, `{"panel":{"url":"https://panel.invalid"},"nodes":[{"node_id":"n1","node_type":"vless","token":"token"}]}`)
	if cfg.NativeOnly != nil || !cfg.nativeOnly() {
		t.Fatalf("omitted native_only must default true: raw=%v effective=%v", cfg.NativeOnly, cfg.nativeOnly())
	}
}

func TestDefaultConfigPathMatchesNativeReleaseService(t *testing.T) {
	if defaultConfigPath != "/etc/pandora-native/config.json" {
		t.Fatalf("default config path = %q", defaultConfigPath)
	}
}

func TestConfigIdentityPathDefaultsToBootstrapDefault(t *testing.T) {
	cfg := loadTestConfig(t, `{"panel":{"url":"https://panel.invalid"},"nodes":[{"node_id":"n1","node_type":"vless","token":"token"}]}`)
	if got := cfg.identityPath(""); got != "/var/lib/pandora-native/identity.json" {
		t.Fatalf("identity path = %q", got)
	}
}

func TestConfigIdentityPathCanMatchPackagedInstaller(t *testing.T) {
	cfg := loadTestConfig(t, `{"panel":{"url":"https://panel.invalid","identity_path":"/etc/pandora-native/identity.json"},"nodes":[{"node_id":"n1","node_type":"vless","token":"token"}]}`)
	if got := cfg.identityPath(""); got != "/etc/pandora-native/identity.json" {
		t.Fatalf("identity path = %q", got)
	}
}

func TestNodeIdentityPathOverridesPanelDefault(t *testing.T) {
	cfg := loadTestConfig(t, `{"panel":{"url":"https://panel.invalid","identity_path":"/shared/identity.json"},"nodes":[{"node_id":"n1","node_type":"vless","token":"token","identity_path":"/nodes/n1.json"}]}`)
	if got := cfg.identityPath(cfg.Nodes[0].IdentityPath); got != "/nodes/n1.json" {
		t.Fatalf("identity path = %q", got)
	}
}

func TestVerifyIdentityRequiresNodeIDBeforeNetworkAccess(t *testing.T) {
	if err := verifyIdentityCommand([]string{"--identity", filepath.Join(t.TempDir(), "missing.json")}); err == nil || err.Error() != "node-id is required" {
		t.Fatalf("verifyIdentityCommand error = %v", err)
	}
}

func TestConfigExplicitCompatibilityOverride(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
		want bool
	}{
		{name: "false", json: "false", want: false},
		{name: "true", json: "true", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadTestConfig(t, `{"native_only":`+tc.json+`,"panel":{"url":"https://panel.invalid"},"nodes":[{"node_id":"n1","node_type":"vless","token":"token"}]}`)
			if cfg.NativeOnly == nil || cfg.nativeOnly() != tc.want {
				t.Fatalf("native_only=%s: raw=%v effective=%v want=%v", tc.json, cfg.NativeOnly, cfg.nativeOnly(), tc.want)
			}
		})
	}
}

func loadTestConfig(t *testing.T, body string) *config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
