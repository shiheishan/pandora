package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 目录覆盖只给开发与测试：生产设了就拒绝启动，开发环境要 https 目录、根文件可读
func TestLoadACMEOverride(t *testing.T) {
	t.Setenv(acmeDirectoryOverrideEnv, "")
	t.Setenv(acmeTrustedRootsEnv, "")
	if got, err := loadACME(true); err != nil || got.DirectoryOverride != "" || got.TrustedRoots != nil {
		t.Fatalf("unset override = %+v, %v", got, err)
	}

	t.Setenv(acmeDirectoryOverrideEnv, "https://127.0.0.1:14000/dir")
	if _, err := loadACME(true); err == nil || !strings.Contains(err.Error(), "生产环境不能设置") {
		t.Fatalf("production must refuse the override, err=%v", err)
	}
	got, err := loadACME(false)
	if err != nil || got.DirectoryOverride != "https://127.0.0.1:14000/dir" || got.TrustedRoots != nil {
		t.Fatalf("development override = %+v, %v", got, err)
	}

	t.Setenv(acmeDirectoryOverrideEnv, "http://127.0.0.1:14000/dir")
	if _, err := loadACME(false); err == nil {
		t.Fatal("plain http directory must be rejected")
	}

	t.Setenv(acmeDirectoryOverrideEnv, "")
	t.Setenv(acmeTrustedRootsEnv, "/nonexistent.pem")
	if _, err := loadACME(false); err == nil {
		t.Fatal("roots without a directory must be rejected")
	}

	t.Setenv(acmeDirectoryOverrideEnv, "https://127.0.0.1:14000/dir")
	if _, err := loadACME(false); err == nil {
		t.Fatal("unreadable roots file must be rejected")
	}
	path := filepath.Join(t.TempDir(), "roots.pem")
	if err := os.WriteFile(path, testRootPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(acmeTrustedRootsEnv, path)
	if got, err := loadACME(false); err != nil || got.TrustedRoots == nil {
		t.Fatalf("roots = %+v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadACME(false); err == nil {
		t.Fatal("garbage roots file must be rejected")
	}
}

func testRootPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// lego 自己读的开关：只回名字（不回值），LEGO_DEBUG_ 前缀与两个行为开关
func TestLookupLibraryEnv(t *testing.T) {
	got := lookupLibraryEnv([]string{"PATH=/bin", "LEGO_DEBUG_DNS_API_HTTP_CLIENT=1", "LEGO_DISABLE_CNAME_SUPPORT=true",
		"LEGO_CA_CERTIFICATES=/x", "AEGIS_ENV=development", "LEGO_DEBUG_CLIENT_VERBOSE_ERROR=secret-ish"})
	want := []string{"LEGO_DEBUG_CLIENT_VERBOSE_ERROR", "LEGO_DEBUG_DNS_API_HTTP_CLIENT", "LEGO_DISABLE_CNAME_SUPPORT"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("library env = %v, want %v", got, want)
	}
}
