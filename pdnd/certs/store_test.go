package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testSelfSigned(t *testing.T, id string) (certPEM, keyPEM []byte, notAfter time.Time) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	notBefore := time.Now().Add(-time.Hour).UTC()
	notAfter = notBefore.Add(90 * 24 * time.Hour)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: id},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	if ip := net.ParseIP(id); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{id}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certPEM, keyPEM, notAfter
}

func TestStoreSaveLoadAndPermissions(t *testing.T) {
	root := t.TempDir()
	st, err := newStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := "192.0.2.10"
	certPEM, keyPEM, wantAfter := testSelfSigned(t, id)
	meta := certMeta{Identifier: id, Profile: ProfileShortlived}
	sc, err := st.saveCert(id, certPEM, keyPEM, meta)
	if err != nil {
		t.Fatal(err)
	}
	// 证书里的时间只到秒
	if !sc.meta.NotAfter.Equal(wantAfter.Truncate(time.Second)) {
		t.Fatalf("NotAfter save=%v want=%v", sc.meta.NotAfter, wantAfter)
	}

	loaded, skipped := st.loadCerts()
	if len(skipped) != 0 {
		t.Fatalf("skipped: %v", skipped)
	}
	got := loaded[id]
	if got == nil {
		t.Fatal("missing cert")
	}
	if !got.meta.NotAfter.Equal(sc.meta.NotAfter) {
		t.Fatalf("load NotAfter %v != save %v", got.meta.NotAfter, sc.meta.NotAfter)
	}

	if runtime.GOOS != "windows" {
		certDir := st.certDir(id)
		info, err := os.Stat(certDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("dir perm %o", info.Mode().Perm())
		}
		for _, name := range []string{bundleFile, "meta.json"} {
			fi, err := os.Stat(filepath.Join(certDir, name))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s perm %o", name, fi.Mode().Perm())
			}
		}
	}
}

func TestStoreDNSCert(t *testing.T) {
	st, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "node1.example.com"
	certPEM, keyPEM, _ := testSelfSigned(t, id)
	_, err = st.saveCert(id, certPEM, keyPEM, certMeta{Identifier: id})
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := st.loadCerts()
	if loaded[id] == nil {
		t.Fatal("dns cert missing")
	}
}

func TestStoreSaveCertMismatch(t *testing.T) {
	st, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _, _ := testSelfSigned(t, "192.0.2.10")
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherDER, _ := x509.MarshalECPrivateKey(otherKey)
	wrongKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: otherDER})
	if _, err := st.saveCert("192.0.2.10", certPEM, wrongKey, certMeta{Identifier: "192.0.2.10"}); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestStoreLoadSkipsBad(t *testing.T) {
	root := t.TempDir()
	st, err := newStore(root)
	if err != nil {
		t.Fatal(err)
	}
	goodID := "192.0.2.11"
	certPEM, keyPEM, _ := testSelfSigned(t, goodID)
	if _, err := st.saveCert(goodID, certPEM, keyPEM, certMeta{Identifier: goodID}); err != nil {
		t.Fatal(err)
	}

	// SAN 与标识符不符
	badSAN := "192.0.2.12"
	c2, k2, _ := testSelfSigned(t, badSAN)
	wrongDir := filepath.Join(st.certsRoot(), dirName("192.0.2.99"))
	if err := os.MkdirAll(wrongDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(wrongDir, bundleFile), append(k2, c2...), 0o600)
	wrongMeta, _ := json.Marshal(certMeta{Identifier: "192.0.2.99"})
	_ = os.WriteFile(filepath.Join(wrongDir, "meta.json"), wrongMeta, 0o600)

	// meta 损坏
	badMetaDir := filepath.Join(st.certsRoot(), dirName("node2.example.com"))
	if err := os.MkdirAll(badMetaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(badMetaDir, "meta.json"), []byte("not-json"), 0o600)
	c3, k3, _ := testSelfSigned(t, "node2.example.com")
	_ = os.WriteFile(filepath.Join(badMetaDir, bundleFile), append(k3, c3...), 0o600)

	loaded, skipped := st.loadCerts()
	if len(loaded) != 1 || loaded[goodID] == nil {
		t.Fatalf("expected one good cert, got %d", len(loaded))
	}
	if len(skipped) < 2 {
		t.Fatalf("expected skipped dirs, got %v", skipped)
	}
}

func TestStoreAccount(t *testing.T) {
	st, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acc, err := st.loadAccount()
	if err != nil || acc != nil {
		t.Fatalf("missing account: acc=%v err=%v", acc, err)
	}
	want := &accountFile{DirectoryURL: "https://acme.example/directory", URI: "https://acme.example/acct/1", KeyPEM: "-----BEGIN EC PRIVATE KEY-----\n"}
	if err := st.saveAccount(want); err != nil {
		t.Fatal(err)
	}
	acc, err = st.loadAccount()
	if err != nil {
		t.Fatal(err)
	}
	if acc.DirectoryURL != want.DirectoryURL || acc.URI != want.URI {
		t.Fatalf("roundtrip: %+v", acc)
	}
}

func TestStoreRemovePurge(t *testing.T) {
	root := t.TempDir()
	st, err := newStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := "node1.example.com"
	certPEM, keyPEM, _ := testSelfSigned(t, id)
	if _, err := st.saveCert(id, certPEM, keyPEM, certMeta{Identifier: id}); err != nil {
		t.Fatal(err)
	}
	if err := st.remove(id); err != nil {
		t.Fatal(err)
	}
	loaded, _ := st.loadCerts()
	if len(loaded) != 0 {
		t.Fatal("after remove")
	}
	if err := st.saveAccount(&accountFile{DirectoryURL: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := st.purge(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("purge: %v", err)
	}
}

func TestDirName(t *testing.T) {
	if dirName("2001:db8::1") != "2001_db8__1" {
		t.Fatalf("ipv6: %q", dirName("2001:db8::1"))
	}
	h := dirName(strings.Repeat("x", 250))
	if len(h) < 3 || h[:2] != "h-" {
		t.Fatalf("long name: %q", h)
	}
	if dirName("weird/name\x00")[:2] != "h-" {
		t.Fatal("unsafe chars")
	}
	// ".." 规整后是 "."，不能当目录名
	if dirName("..")[:2] != "h-" {
		t.Fatalf("dot name: %q", dirName(".."))
	}
	if dirName("Node1.Example.com.") != "node1.example.com" {
		t.Fatalf("normalize: %q", dirName("Node1.Example.com."))
	}
}

func TestNewStoreEmptyDir(t *testing.T) {
	if _, err := newStore(""); err == nil {
		t.Fatal("empty dir")
	}
}
