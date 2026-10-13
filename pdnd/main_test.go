package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegispanel/nodeagent/panel"
)

const testConfig = `{"panel":{"address":"panel.example.test:9443"}}`

func TestConfigDefaultsToNativeOnly(t *testing.T) {
	cfg := loadTestConfig(t, testConfig)
	if cfg.NativeOnly != nil || !cfg.nativeOnly() {
		t.Fatalf("omitted native_only must default true: raw=%v effective=%v", cfg.NativeOnly, cfg.nativeOnly())
	}
}

func TestDefaultConfigPathMatchesNativeReleaseService(t *testing.T) {
	if defaultConfigPath != "/etc/pandora-native/config.json" {
		t.Fatalf("default config path = %q", defaultConfigPath)
	}
}

// 身份目录缺省落在状态目录下：会话里续期要原地换证书，必须是服务可写的路径。
func TestConfigIdentityDirDefaultsToStateDirectory(t *testing.T) {
	cfg := loadTestConfig(t, testConfig)
	if got := cfg.identityDir(); got != "/var/lib/pandora-native/identity" || got != panel.DefaultIdentityDir {
		t.Fatalf("identity dir = %q", got)
	}
}

func TestConfigIdentityDirOverride(t *testing.T) {
	cfg := loadTestConfig(t, `{"panel":{"address":"panel.example.test","identity_dir":"/srv/pandora/identity"}}`)
	if got := cfg.identityDir(); got != "/srv/pandora/identity" {
		t.Fatalf("identity dir = %q", got)
	}
}

func TestConfigRequiresPanelAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"panel":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("panel.address 为空时应拒绝")
	}
}

// 旧通道的字段（url、token、signed_required、nodes[]）已删除，留在配置里要报错而不是被静默忽略。
func TestConfigRejectsRemovedFields(t *testing.T) {
	for _, body := range []string{
		`{"panel":{"address":"p.example.test","url":"https://p.example.test"}}`,
		`{"panel":{"address":"p.example.test","signed_required":true}}`,
		`{"panel":{"address":"p.example.test"},"nodes":[{"node_id":"n1","node_type":"vless","token":"t"}]}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil {
			t.Fatalf("应拒绝已删除的字段: %s", body)
		}
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
			cfg := loadTestConfig(t, `{"native_only":`+tc.json+`,"panel":{"address":"panel.example.test"}}`)
			if cfg.NativeOnly == nil || cfg.nativeOnly() != tc.want {
				t.Fatalf("native_only=%s: raw=%v effective=%v want=%v", tc.json, cfg.NativeOnly, cfg.nativeOnly(), tc.want)
			}
		})
	}
}

// 令牌不进 argv：没有 --token 这个参数。
func TestEnrollRejectsTokenOnCommandLine(t *testing.T) {
	err := enrollCommand([]string{"--panel", "127.0.0.1:9443", "--ca-sha256", "sha256:" + strings.Repeat("0", 64), "--token", "secret"}, strings.NewReader(""))
	if err == nil {
		t.Fatal("--token 应被拒绝")
	}
}

func TestEnrollRequiresExactlyOneTokenSource(t *testing.T) {
	base := []string{"--panel", "127.0.0.1:9443", "--ca-sha256", "sha256:" + strings.Repeat("0", 64)}
	if err := enrollCommand(base, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "--token-file") {
		t.Fatalf("没有令牌来源时应报错，得 %v", err)
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	both := append(append([]string{}, base...), "--token-file", file, "--token-stdin")
	if err := enrollCommand(both, strings.NewReader("t\n")); err == nil || !strings.Contains(err.Error(), "只能给一个") {
		t.Fatalf("两个来源同时给时应报错，得 %v", err)
	}
}

func TestReadToken(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("  abc123 \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readToken(file, false, nil); err != nil || got != "abc123" {
		t.Fatalf("token-file = %q, %v", got, err)
	}
	if got, err := readToken("", true, strings.NewReader("xyz\nignored\n")); err != nil || got != "xyz" {
		t.Fatalf("token-stdin = %q, %v", got, err)
	}
	if _, err := readToken("", true, strings.NewReader("\n")); err == nil {
		t.Fatal("空令牌应报错")
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
