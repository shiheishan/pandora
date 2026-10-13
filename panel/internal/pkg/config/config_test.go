package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 32 字节密钥的 base64
const testMasterKeyB64 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func setMinimalValidEnv(t *testing.T) {
	t.Setenv("PANDORA_ENV", "development")
	t.Setenv("PANDORA_DATABASE_URL", "postgres://u:p@127.0.0.1:5432/db")
	t.Setenv("PANDORA_REDIS_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("PANDORA_SECRETS_MASTER_KEY", testMasterKeyB64)
	t.Setenv("PANDORA_AUTH_PUBLIC_JWT_SECRET", strings.Repeat("a", 32))
	t.Setenv("PANDORA_AUTH_ADMIN_JWT_SECRET", strings.Repeat("b", 32))
}

func TestLoadFromEnvOnly(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_HTTP_PUBLIC_ADDR", "127.0.0.1:7777")
	t.Setenv("PANDORA_AUTH_ACCESS_TTL", "10m")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Public.Addr != "127.0.0.1:7777" {
		t.Fatalf("public addr: got %q", cfg.HTTP.Public.Addr)
	}
	if cfg.Auth.AccessTTL != 10*time.Minute {
		t.Fatalf("access_ttl: got %v", cfg.Auth.AccessTTL)
	}
}

func TestSecretInConfigFileRejected(t *testing.T) {
	setMinimalValidEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	content := "database:\n  url: postgres://bad@localhost/db\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for secret in config file")
	}
	if !strings.Contains(err.Error(), "database.url") || !strings.Contains(err.Error(), "不能写在配置文件里") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecretFromFileOK(t *testing.T) {
	dir := t.TempDir()
	mkPath := filepath.Join(dir, "mk")
	if err := os.WriteFile(mkPath, []byte(testMasterKeyB64), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PANDORA_SECRETS_MASTER_KEY_FILE", mkPath)
	t.Setenv("PANDORA_ENV", "development")
	t.Setenv("PANDORA_DATABASE_URL", "postgres://u:p@127.0.0.1/db")
	t.Setenv("PANDORA_REDIS_URL", "redis://127.0.0.1/0")
	t.Setenv("PANDORA_AUTH_PUBLIC_JWT_SECRET", strings.Repeat("a", 32))
	t.Setenv("PANDORA_AUTH_ADMIN_JWT_SECRET", strings.Repeat("b", 32))

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Secrets.MasterKey.Reveal() != testMasterKeyB64 {
		t.Fatalf("master key mismatch")
	}
}

func TestSecretFilePermTooOpen(t *testing.T) {
	dir := t.TempDir()
	mkPath := filepath.Join(dir, "mk")
	if err := os.WriteFile(mkPath, []byte(testMasterKeyB64), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PANDORA_ENV", "development")
	t.Setenv("PANDORA_DATABASE_URL", "postgres://u:p@127.0.0.1:5432/db")
	t.Setenv("PANDORA_REDIS_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("PANDORA_AUTH_PUBLIC_JWT_SECRET", strings.Repeat("a", 32))
	t.Setenv("PANDORA_AUTH_ADMIN_JWT_SECRET", strings.Repeat("b", 32))
	t.Setenv("PANDORA_SECRETS_MASTER_KEY_FILE", mkPath)
	if prev, had := os.LookupEnv("PANDORA_SECRETS_MASTER_KEY"); had {
		os.Unsetenv("PANDORA_SECRETS_MASTER_KEY")
		t.Cleanup(func() { _ = os.Setenv("PANDORA_SECRETS_MASTER_KEY", prev) })
	} else {
		t.Cleanup(func() { os.Unsetenv("PANDORA_SECRETS_MASTER_KEY") })
	}

	_, err := Load("")
	if err == nil {
		t.Fatal("expected permission error")
	}
	if !strings.Contains(err.Error(), "权限过宽") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateJWTSameSecret(t *testing.T) {
	setMinimalValidEnv(t)
	same := strings.Repeat("x", 32)
	t.Setenv("PANDORA_AUTH_PUBLIC_JWT_SECRET", same)
	t.Setenv("PANDORA_AUTH_ADMIN_JWT_SECRET", same)
	_, err := Load("")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "不能相同") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidateMasterKeyTooShort(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_SECRETS_MASTER_KEY", base64Short())
	_, err := Load("")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "master_key") {
		t.Fatalf("unexpected: %v", err)
	}
}

func base64Short() string {
	return "YWJj" // "abc"
}

func TestValidatePprofNotLoopback(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_HTTP_PUBLIC_PPROF_ADDR", "0.0.0.0:6060")
	_, err := Load("")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "回环") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidateProductionHTTPSBaseURL(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_ENV", "production")
	t.Setenv("PANDORA_PUBLIC_BASE_URL", "http://example.com")
	_, err := Load("")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "https://") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestSecretStringDoesNotLeak(t *testing.T) {
	setMinimalValidEnv(t)
	secretVal := strings.Repeat("z", 40)
	t.Setenv("PANDORA_AUTH_PUBLIC_JWT_SECRET", secretVal)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.PublicJWTSecret.String() != "***" {
		t.Fatalf("String(): %q", cfg.Auth.PublicJWTSecret.String())
	}
	formatted := fmt.Sprintf("%v", cfg.Auth.PublicJWTSecret)
	if strings.Contains(formatted, secretVal) || formatted != "***" {
		t.Fatalf("Sprintf leaked: %q", formatted)
	}
}

// 没设 env 时按生产处理：生产要求 https 的 public_base_url，缺省的本机地址启动不了。
func TestDefaultEnvIsProduction(t *testing.T) {
	// CI 在 job 级设了 PANDORA_ENV=development；空值在 viper 里等同没设
	t.Setenv("PANDORA_ENV", "")
	t.Setenv("PANDORA_DATABASE_URL", "postgres://u:p@127.0.0.1:5432/db")
	t.Setenv("PANDORA_REDIS_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("PANDORA_SECRETS_MASTER_KEY", testMasterKeyB64)
	t.Setenv("PANDORA_AUTH_PUBLIC_JWT_SECRET", strings.Repeat("a", 32))
	t.Setenv("PANDORA_AUTH_ADMIN_JWT_SECRET", strings.Repeat("b", 32))
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("缺省 env 应按 production 校验: %v", err)
	}
}

// 生产环境两个 HTTP 监听必须在回环上（只信 nginx 覆写的 X-Real-IP）；开发环境不限制。
func TestValidateProductionHTTPAddrLoopback(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_ENV", "production")
	t.Setenv("PANDORA_PUBLIC_BASE_URL", "https://panel.example.test")
	t.Setenv("PANDORA_HTTP_ADMIN_ADDR", "0.0.0.0:9001")
	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), "http.admin.addr") {
		t.Fatalf("应拒绝非回环的 admin 监听: %v", err)
	}

	t.Setenv("PANDORA_ENV", "development")
	if _, err := Load(""); err != nil {
		t.Fatalf("开发环境不限制: %v", err)
	}
}

func TestSingboxRuleSetPrefixFromEnv(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_SUBSCRIPTION_SINGBOX_GEOSITE_URL_PREFIX", "https://mirror.example.com/geosite/")
	t.Setenv("PANDORA_SUBSCRIPTION_SINGBOX_BLOCK_ADS", "true")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	sb := cfg.Subscription.Singbox
	if sb.GeositeURLPrefix != "https://mirror.example.com/geosite/" || sb.GeoIPURLPrefix != "" || !sb.BlockAds {
		t.Fatalf("得到 %+v", sb)
	}
}

func TestSingboxRuleSetPrefixRejected(t *testing.T) {
	for _, bad := range []string{
		"http://mirror.example.com/geosite/",
		"https://mirror.example.com/geosite",
		"https://mirror.example.com/geosite/?x=1",
		"https://user@mirror.example.com/geosite/",
		"https:///geosite/",
	} {
		t.Run(bad, func(t *testing.T) {
			setMinimalValidEnv(t)
			t.Setenv("PANDORA_SUBSCRIPTION_SINGBOX_GEOIP_URL_PREFIX", bad)
			_, err := Load("")
			if err == nil || !strings.Contains(err.Error(), "geoip_url_prefix") {
				t.Fatalf("应拒绝 %q，得到 %v", bad, err)
			}
		})
	}
}

func TestGRPCNodeDefaults(t *testing.T) {
	setMinimalValidEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GRPC.Node.Addr != ":9443" {
		t.Fatalf("addr: got %q", cfg.GRPC.Node.Addr)
	}
	if cfg.GRPC.Node.PublicHost != "" {
		t.Fatalf("public_host: got %q", cfg.GRPC.Node.PublicHost)
	}
	if cfg.GRPC.Node.EffectivePublicPort() != 9443 {
		t.Fatalf("EffectivePublicPort: got %d", cfg.GRPC.Node.EffectivePublicPort())
	}
}

func TestGRPCNodePublicPortOverride(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_GRPC_NODE_PUBLIC_HOST", "node.example.test")
	t.Setenv("PANDORA_GRPC_NODE_PUBLIC_PORT", "443")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GRPC.Node.PublicHost != "node.example.test" {
		t.Fatalf("public_host: got %q", cfg.GRPC.Node.PublicHost)
	}
	if cfg.GRPC.Node.EffectivePublicPort() != 443 {
		t.Fatalf("EffectivePublicPort: got %d", cfg.GRPC.Node.EffectivePublicPort())
	}
}

func TestGRPCNodePublicHostRequiredInProduction(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_ENV", "production")
	t.Setenv("PANDORA_PUBLIC_BASE_URL", "https://panel.example.test")
	_, err := Load("")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "grpc.node.public_host") {
		t.Fatalf("应含 grpc.node.public_host: %v", err)
	}
	t.Setenv("PANDORA_GRPC_NODE_PUBLIC_HOST", "203.0.113.7")
	// 生产环境的其它校验可能仍报错，只要求不再报这个字段
	_, err = Load("")
	if err != nil && strings.Contains(err.Error(), "grpc.node.public_host") {
		t.Fatalf("设 public_host 后不应再报该字段: %v", err)
	}
}

func TestGRPCNodePublicHostInvalid(t *testing.T) {
	for _, bad := range []string{
		"https://x.example.test",
		"x.example.test:9443",
		"bad host",
	} {
		t.Run(bad, func(t *testing.T) {
			setMinimalValidEnv(t)
			t.Setenv("PANDORA_GRPC_NODE_PUBLIC_HOST", bad)
			_, err := Load("")
			if err == nil || !strings.Contains(err.Error(), "grpc.node.public_host") {
				t.Fatalf("应拒绝 %q: %v", bad, err)
			}
		})
	}
	for _, ok := range []string{"::1", "x.example.test"} {
		t.Run(ok, func(t *testing.T) {
			setMinimalValidEnv(t)
			t.Setenv("PANDORA_GRPC_NODE_PUBLIC_HOST", ok)
			_, err := Load("")
			if err != nil {
				t.Fatalf("应接受 %q: %v", ok, err)
			}
		})
	}
}

func TestGRPCNodeAddrInvalid(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_GRPC_NODE_ADDR", "9443")
	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), "grpc.node.addr") {
		t.Fatalf("应拒绝无冒号的 addr: %v", err)
	}
}

// 节点端分发目录：缺省是发布包装到的位置，部署脚本经 PANDORA_PDND_DIST_DIR 改；只收绝对路径。
func TestPdndDistDir(t *testing.T) {
	setMinimalValidEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pdnd.DistDir != "/opt/pandora/pdnd-dist" {
		t.Fatalf("缺省 %q", cfg.Pdnd.DistDir)
	}
	t.Setenv("PANDORA_PDND_DIST_DIR", "/srv/pandora/pdnd-dist")
	if cfg, err = Load(""); err != nil || cfg.Pdnd.DistDir != "/srv/pandora/pdnd-dist" {
		t.Fatalf("环境变量覆盖：%+v %v", cfg, err)
	}
	t.Setenv("PANDORA_PDND_DIST_DIR", "pdnd-dist")
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "pdnd.dist_dir") {
		t.Fatalf("应拒绝相对路径: %v", err)
	}
}
