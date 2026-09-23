package public

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPDNDInstallerUsesSameIdentityPathForBootstrapAndRuntime(t *testing.T) {
	bootstrap := `--identity "${CONFIG_DIR}/identity.json"`
	runtime := `"identity_path": "${CONFIG_DIR}/identity.json"`
	if !strings.Contains(pdndInstallTemplate, bootstrap) {
		t.Fatalf("installer bootstrap identity contract missing %q", bootstrap)
	}
	if !strings.Contains(pdndInstallTemplate, runtime) {
		t.Fatalf("runtime identity contract missing %q", runtime)
	}
	if strings.Index(pdndInstallTemplate, bootstrap) > strings.Index(pdndInstallTemplate, runtime) {
		t.Fatal("identity must be bootstrapped before runtime config is written")
	}
}

func TestPDNDInstallerSeparatesBootstrapAndRuntimeTokens(t *testing.T) {
	required := []string{
		`"runtime_token": *"\([^"]*\)"`,
		`"token": "${RUNTIME_TOKEN}"`,
		`--node-id) NODE_ID="$2"`,
		`"signed_required": ${SIGNED_REQUIRED}`,
		`if [ "$HAD_IDENTITY" = 1 ]; then`,
		`reusing the existing Pandora NativeCore identity`,
	}
	for _, fragment := range required {
		if !strings.Contains(pdndInstallTemplate, fragment) {
			t.Fatalf("installer runtime-token contract missing %q", fragment)
		}
	}
}

func TestPDNDPanelBaseURLRejectsUnsafeOrInsecureOrigins(t *testing.T) {
	tests := []struct {
		name       string
		origin     string
		production bool
	}{
		{name: "production http", origin: "http://panel.example.com", production: true},
		{name: "shell substitution", origin: "https://panel.example.com/$(id)", production: true},
		{name: "embedded quote", origin: "https://panel.example.com/\"oops", production: true},
		{name: "credentials", origin: "https://user@panel.example.com", production: true},
		{name: "query", origin: "https://panel.example.com?x=1", production: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := pdndPanelBaseURL(tt.origin, tt.production); err == nil {
				t.Fatalf("unsafe origin accepted: %q", got)
			}
		})
	}
	if got, err := pdndPanelBaseURL("https://panel.example.com/", true); err != nil || got != "https://panel.example.com" {
		t.Fatalf("canonical HTTPS origin rejected: got=%q err=%v", got, err)
	}
}

func TestPDNDInstallerRunsAsHardenedUnprivilegedUser(t *testing.T) {
	for _, fragment := range []string{
		"User=pandora",
		"Group=pandora",
		"NoNewPrivileges=true",
		"ProtectSystem=strict",
		"PrivateDevices=true",
		"RestrictNamespaces=true",
		"KillSignal=SIGTERM",
		"TimeoutStopSec=20s",
		"RestartSec=5s",
		"Description=Pandora NativeCore node agent",
		"LimitNOFILE=1048576",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE",
		"StateDirectory=pandora-native",
		"LogsDirectory=pandora-native",
		"CapabilityBoundingSet=CAP_NET_BIND_SERVICE",
	} {
		if !strings.Contains(pdndInstallTemplate, fragment) {
			t.Fatalf("installer service hardening missing %q", fragment)
		}
	}
	if strings.Contains(pdndInstallTemplate, "Documentation=${PANEL}") {
		t.Fatal("installer unit must use the canonical packaged unit contract")
	}
}

func TestPDNDInstallerUnitMatchesPackagedContract(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	packagedPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "pdnd", "release", "pandora-native.service")
	packaged, err := os.ReadFile(packagedPath)
	if err != nil {
		t.Fatalf("read packaged service unit: %v", err)
	}

	dynamic := installerUnitLines(pdndInstallTemplate)
	canonical := installerUnitLines(string(packaged))
	for _, key := range []string{
		"Description",
		"RestartSec",
		"NoNewPrivileges",
		"LimitNOFILE",
		"AmbientCapabilities",
	} {
		dynamicValue, dynamicCount := unitValue(dynamic, key)
		canonicalValue, canonicalCount := unitValue(canonical, key)
		if dynamicCount != 1 || canonicalCount != 1 {
			t.Fatalf("unit key %s must occur exactly once: dynamic=%d canonical=%d", key, dynamicCount, canonicalCount)
		}
		if dynamicValue != canonicalValue {
			t.Fatalf("unit key %s differs: dynamic=%q canonical=%q", key, dynamicValue, canonicalValue)
		}
	}
}

func installerUnitLines(content string) []string {
	start := strings.Index(content, "[Unit]\n")
	if start < 0 {
		return nil
	}
	end := strings.Index(content[start:], "\nUNIT\n")
	if end < 0 {
		end = len(content) - start
	}
	unit := content[start : start+end]
	var lines []string
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func unitValue(lines []string, key string) (string, int) {
	prefix := key + "="
	var value string
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			value = strings.TrimPrefix(line, prefix)
			count++
		}
	}
	return value, count
}
