package panel

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWriteFileAtomicMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := WriteFileAtomic(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上文件权限语义不同")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("权限应为 0600，实际 %o", info.Mode().Perm())
	}
}

func TestPromoteStagedFinishesInterruptedReplace(t *testing.T) {
	pki := newTestPKI(t)
	serverID := newServerID()
	oldNotAfter := time.Now().Add(7 * 24 * time.Hour)
	newNotAfter := time.Now().Add(30 * 24 * time.Hour)

	oldKey, oldCSR, err := newKeyAndCSR(serverID)
	if err != nil {
		t.Fatal(err)
	}
	oldCert := pki.signCSR(t, oldCSR, serverID, oldNotAfter)

	newKey, newCSR, err := newKeyAndCSR(serverID)
	if err != nil {
		t.Fatal(err)
	}
	newCert := pki.signCSR(t, newCSR, serverID, newNotAfter)
	newLeaf := mustParseCertPEM(t, newCert)

	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// 根目录仍是旧 cert/ca/server_id，新 key 已 promote 到位。
	for name, body := range map[string][]byte{
		certFile:     oldCert,
		caFile:       pki.caPEM,
		serverIDFile: []byte(serverID + "\n"),
		keyFile:      newKey,
	} {
		if err := WriteFileAtomic(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	next := filepath.Join(dir, nextDir)
	if err := os.MkdirAll(next, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(filepath.Join(next, certFile), newCert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(filepath.Join(next, caFile), pki.caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(filepath.Join(next, readyMarker), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	id, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, nextDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("next/ 应已删除: %v", err)
	}
	for _, name := range []string{certFile, keyFile, caFile, serverIDFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("缺少 %s: %v", name, err)
		}
	}
	if id.Certificate().Leaf.SerialNumber.Cmp(newLeaf.SerialNumber) != 0 {
		t.Fatalf("应为新叶子证书")
	}
	if _, err := tls.X509KeyPair(mustRead(t, filepath.Join(dir, certFile)), newKey); err != nil {
		t.Fatalf("新 cert/key 不配对: %v", err)
	}
	if _, err := tls.X509KeyPair(oldCert, newKey); err == nil {
		t.Fatal("新 key 不应配对旧 cert")
	}
	_ = oldKey
}

func TestStagedWithoutReadyIsDiscarded(t *testing.T) {
	pki := newTestPKI(t)
	serverID := newServerID()
	notAfter := time.Now().Add(7 * 24 * time.Hour)

	keyPEM, csr, err := newKeyAndCSR(serverID)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pki.signCSR(t, csr, serverID, notAfter)

	dir := t.TempDir()
	if err := stageAndPromote(dir, map[string][]byte{
		certFile:     certPEM,
		keyFile:      keyPEM,
		caFile:       pki.caPEM,
		serverIDFile: []byte(serverID + "\n"),
	}); err != nil {
		t.Fatal(err)
	}
	idBefore, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldSerial := idBefore.Certificate().Leaf.SerialNumber.String()

	next := filepath.Join(dir, nextDir)
	if err := os.MkdirAll(next, 0o700); err != nil {
		t.Fatal(err)
	}
	junkKey, junkCSR, err := newKeyAndCSR(serverID)
	if err != nil {
		t.Fatal(err)
	}
	junkCert := pki.signCSR(t, junkCSR, serverID, time.Now().Add(time.Hour))
	if err := WriteFileAtomic(filepath.Join(next, certFile), junkCert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(filepath.Join(next, keyFile), junkKey, 0o600); err != nil {
		t.Fatal(err)
	}

	idAfter, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(next); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("无 ready 的 next/ 应被删: %v", err)
	}
	if idAfter.Certificate().Leaf.SerialNumber.String() != oldSerial {
		t.Fatal("应继续使用旧身份")
	}
}

func TestReplaceSwapsCertificate(t *testing.T) {
	pki := newTestPKI(t)
	serverID := newServerID()
	id := newTestIdentity(t, pki, serverID, time.Now().Add(7*24*time.Hour))

	newKey, newCSR, err := newKeyAndCSR(serverID)
	if err != nil {
		t.Fatal(err)
	}
	newCert := pki.signCSR(t, newCSR, serverID, time.Now().Add(30*24*time.Hour))
	newLeaf := mustParseCertPEM(t, newCert)

	if err := id.Replace(newCert, pki.caPEM, newKey); err != nil {
		t.Fatal(err)
	}
	if id.Certificate().Leaf.SerialNumber.Cmp(newLeaf.SerialNumber) != 0 {
		t.Fatal("内存证书未更新")
	}
	diskCert, err := os.ReadFile(filepath.Join(id.Dir(), certFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(diskCert) != string(newCert) {
		t.Fatal("磁盘 cert.pem 未更新")
	}
	diskKey, err := os.ReadFile(filepath.Join(id.Dir(), keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(diskKey) != string(newKey) {
		t.Fatal("磁盘 key.pem 未更新")
	}
	reloaded, err := LoadIdentity(id.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Certificate().Leaf.SerialNumber.Cmp(newLeaf.SerialNumber) != 0 {
		t.Fatal("重载后证书不一致")
	}

	badKey, _, err := newKeyAndCSR(serverID)
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Replace(newCert, pki.caPEM, badKey); err == nil {
		t.Fatal("应拒绝不配对的私钥")
	}
	if id.Certificate().Leaf.SerialNumber.Cmp(newLeaf.SerialNumber) != 0 {
		t.Fatal("失败后内存证书被改")
	}
	onDisk, err := os.ReadFile(filepath.Join(id.Dir(), certFile))
	if err != nil || string(onDisk) != string(newCert) {
		t.Fatal("失败后磁盘证书被改")
	}
}

func TestLoadIdentityMissingIsNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadIdentity(dir)
	if err == nil {
		t.Fatal("应失败")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("应为 ErrNotExist，实际 %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustParseCertPEM(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("无效 PEM")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
