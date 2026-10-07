package certstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func managedVersion(leaf testLeaf, version uint64) ManagedVersion {
	return ManagedVersion{ServerID: testServerID, CertID: testCertID, Version: version, ChainPEM: leaf.chainPEM, PrivateKeyPKCS8: leaf.keyDER}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

// 安装后：盘上布局与权限（目录 0700、文件 0600）、current.json 指针、握手拿到新证书；
// 重启（新仓库从盘上 Load）后恢复同一张证书。
func TestManagedInstallPersistsAndReloadsAfterRestart(t *testing.T) {
	ca := newTestCA(t)
	root := filepath.Join(t.TempDir(), "panels")
	store := New()
	src := NewManagedSource(root, store)
	v1 := ca.issue(t, time.Time{})
	if err := src.Install(managedVersion(v1, 1)); err != nil {
		t.Fatal(err)
	}
	key, err := ManagedKey(testServerID, testCertID)
	if err != nil {
		t.Fatal(err)
	}
	get, err := store.Resolve(key)
	if err != nil {
		t.Fatal(err)
	}
	addr := tlsServer(t, get)
	oldConn, fp := dialLeaf(t, addr, ca, testHost)
	if fp != v1.fp {
		t.Fatalf("leaf = %s, want v1 %s", fp, v1.fp)
	}

	v2 := ca.issue(t, time.Time{})
	if err := src.Install(managedVersion(v2, 2)); err != nil {
		t.Fatal(err)
	}
	if _, fp = dialLeaf(t, addr, ca, testHost); fp != v2.fp {
		t.Fatalf("leaf after install v2 = %s, want %s", fp, v2.fp)
	}
	if _, fp = dialLeaf(t, addr, ca, ""); fp != v2.fp {
		t.Fatalf("empty-SNI leaf after install v2 = %s, want %s", fp, v2.fp)
	}
	echo(t, oldConn, "still-alive\n")

	certDir := filepath.Join(root, testServerID, "certs", testCertID)
	for _, d := range []string{filepath.Join(root, testServerID), filepath.Join(root, testServerID, "certs"), certDir, filepath.Join(certDir, "v2")} {
		assertMode(t, d, 0o700)
	}
	for _, f := range []string{"fullchain.pem", "privkey.pem", "meta.json"} {
		assertMode(t, filepath.Join(certDir, "v2", f), 0o600)
	}
	assertMode(t, filepath.Join(certDir, currentFile), 0o600)
	var ptr currentPointer
	body, err := os.ReadFile(filepath.Join(certDir, currentFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &ptr); err != nil || ptr.Version != 2 || ptr.ChainSHA256 != ChainSHA256(v2.chainPEM) {
		t.Fatalf("current.json = %s (%v)", body, err)
	}

	restarted := New()
	if err := NewManagedSource(root, restarted).Load(); err != nil {
		t.Fatal(err)
	}
	st, ok := restarted.StatusOf(key)
	if !ok || st.Version != 2 || st.FingerprintSHA256 != v2.fp || st.ErrorCode != "" || st.Source != KindManaged {
		t.Fatalf("status after restart = %+v", st)
	}
	if restarted.Digest() != store.Digest() {
		t.Fatal("digest after restart differs from the running store")
	}
}

// 自检失败（坏 PEM、私钥不配对、已过期）保留旧证书；版本回退与同版本不同内容一律拒绝。
func TestManagedInstallRejectsBadVersionsAndKeepsOld(t *testing.T) {
	ca := newTestCA(t)
	store := New()
	src := NewManagedSource(filepath.Join(t.TempDir(), "panels"), store)
	good := ca.issue(t, time.Time{})
	if err := src.Install(managedVersion(good, 5)); err != nil {
		t.Fatal(err)
	}
	key, _ := ManagedKey(testServerID, testCertID)
	other := ca.issue(t, time.Time{})
	expired := ca.issue(t, time.Now().Add(-time.Minute))
	cases := []struct {
		name string
		v    ManagedVersion
		code string
	}{
		{"bad pem", ManagedVersion{ServerID: testServerID, CertID: testCertID, Version: 6, ChainPEM: []byte("garbage"), PrivateKeyPKCS8: good.keyDER}, ErrCodeBadPEM},
		{"key mismatch", ManagedVersion{ServerID: testServerID, CertID: testCertID, Version: 6, ChainPEM: good.chainPEM, PrivateKeyPKCS8: other.keyDER}, ErrCodeKeyMismatch},
		{"expired", managedVersion(expired, 6), ErrCodeExpired},
		{"rollback", managedVersion(other, 4), ""},
		{"same version different chain", managedVersion(other, 5), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := src.Install(tc.v); err == nil {
				t.Fatal("bad version was installed")
			}
			st, _ := store.StatusOf(key)
			if st.Version != 5 || st.FingerprintSHA256 != good.fp {
				t.Fatalf("old certificate not kept: %+v", st)
			}
			if tc.code != "" && st.ErrorCode != tc.code {
				t.Fatalf("error code = %q, want %q", st.ErrorCode, tc.code)
			}
		})
	}
	// 同版本同内容重装是幂等的（修盘），错误随之清掉。
	if err := src.Install(managedVersion(good, 5)); err != nil {
		t.Fatalf("idempotent reinstall: %v", err)
	}
	if st, _ := store.StatusOf(key); st.ErrorCode != "" || st.Version != 5 {
		t.Fatalf("status after reinstall = %+v", st)
	}
}

func TestManagedRejectsUnsafeIdentifiers(t *testing.T) {
	ca := newTestCA(t)
	root := filepath.Join(t.TempDir(), "panels")
	src := NewManagedSource(root, New())
	leaf := ca.issue(t, time.Time{})
	for name, v := range map[string]ManagedVersion{
		"traversal server": {ServerID: "../../etc", CertID: testCertID, Version: 1},
		"file namespace":   {ServerID: FileNamespace, CertID: testCertID, Version: 1},
		"uppercase uuid":   {ServerID: strings.ToUpper(testServerID), CertID: testCertID, Version: 1},
		"traversal cert":   {ServerID: testServerID, CertID: "../x", Version: 1},
		"nil uuid":         {ServerID: "00000000-0000-0000-0000-000000000000", CertID: testCertID, Version: 1},
		"zero version":     {ServerID: testServerID, CertID: testCertID, Version: 0},
	} {
		v.ChainPEM, v.PrivateKeyPKCS8 = leaf.chainPEM, leaf.keyDER
		if err := src.Install(v); err == nil {
			t.Fatalf("%s: unsafe version accepted", name)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("rejected installs touched disk: %v", err)
	}
}

// 落盘失败（目录被占成文件）时内存照样替换，状态记 persist_failed。
func TestManagedInstallPersistFailureStillServes(t *testing.T) {
	ca := newTestCA(t)
	root := filepath.Join(t.TempDir(), "panels")
	if err := os.MkdirAll(filepath.Join(root, testServerID), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, testServerID, "certs"), []byte("not a directory"))
	store := New()
	src := NewManagedSource(root, store)
	leaf := ca.issue(t, time.Time{})
	if err := src.Install(managedVersion(leaf, 1)); err == nil {
		t.Fatal("persist failure not reported")
	}
	key, _ := ManagedKey(testServerID, testCertID)
	st, _ := store.StatusOf(key)
	if st.ErrorCode != ErrCodePersistFailed || st.FingerprintSHA256 != leaf.fp || store.Current(key) == nil {
		t.Fatalf("status = %+v", st)
	}
}

// Load：坏指针、指针与链不一致、私钥不配对的证书单独报错，不影响别的证书；过期证书照样加载但记 expired。
func TestManagedLoadIsolatesBrokenEntries(t *testing.T) {
	ca := newTestCA(t)
	root := filepath.Join(t.TempDir(), "panels")
	writer := NewManagedSource(root, New())
	ids := []string{
		"0193f0a0-3333-7000-8000-00000000c001",
		"0193f0a0-3333-7000-8000-00000000c002",
		"0193f0a0-3333-7000-8000-00000000c003",
	}
	leaves := make([]testLeaf, len(ids))
	for i, id := range ids {
		leaves[i] = ca.issue(t, time.Time{})
		if err := writer.Install(ManagedVersion{ServerID: testServerID, CertID: id, Version: 1, ChainPEM: leaves[i].chainPEM, PrivateKeyPKCS8: leaves[i].keyDER}); err != nil {
			t.Fatal(err)
		}
	}
	dir := func(id string) string { return filepath.Join(root, testServerID, "certs", id) }
	writeFile(t, filepath.Join(dir(ids[1]), currentFile), []byte(`{"version":1,"chain_sha256":"bm90LXRoZS1jaGFpbg=="}`))
	writeFile(t, filepath.Join(dir(ids[2]), "v1", privKeyFile), ca.issue(t, time.Time{}).keyPEM)
	// 一个过期证书直接写盘（Install 会拒绝过期证书）。
	expiredID := "0193f0a0-3333-7000-8000-00000000c004"
	old := ca.issue(t, time.Now().Add(-time.Minute))
	edir := dir(expiredID)
	if err := os.MkdirAll(filepath.Join(edir, "v3"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(edir, "v3", chainFile), old.chainPEM)
	writeFile(t, filepath.Join(edir, "v3", privKeyFile), old.keyPEM)
	ptr, _ := json.Marshal(currentPointer{Version: 3, ChainSHA256: ChainSHA256(old.chainPEM)})
	writeFile(t, filepath.Join(edir, currentFile), ptr)
	// 不认识的目录名被忽略。
	if err := os.MkdirAll(filepath.Join(root, "not-a-uuid", "certs"), 0o700); err != nil {
		t.Fatal(err)
	}

	store := New()
	err := NewManagedSource(root, store).Load()
	if err == nil {
		t.Fatal("broken entries not reported")
	}
	want := map[string]string{ids[0]: "", ids[1]: ErrCodeReadFailed, ids[2]: ErrCodeKeyMismatch, expiredID: ErrCodeExpired}
	for id, code := range want {
		key, _ := ManagedKey(testServerID, id)
		st, ok := store.StatusOf(key)
		if !ok || st.ErrorCode != code {
			t.Fatalf("%s: status = %+v, want code %q", id, st, code)
		}
		served := store.Current(key) != nil
		if wantServed := code == "" || code == ErrCodeExpired; served != wantServed {
			t.Fatalf("%s: served = %v, want %v", id, served, wantServed)
		}
	}
}

// Cleanup：删非当前版本与临时遗留；不在最新包里的证书满 7 天才删，删后握手失败；重新出现则撤销计时。
func TestManagedCleanupRetentionAndNamespaceRemoval(t *testing.T) {
	ca := newTestCA(t)
	root := filepath.Join(t.TempDir(), "panels")
	store := New()
	src := NewManagedSource(root, store)
	clock := time.Now()
	src.now = func() time.Time { return clock }
	for v := uint64(1); v <= 3; v++ {
		if err := src.Install(managedVersion(ca.issue(t, clock.Add(30*24*time.Hour)), v)); err != nil {
			t.Fatal(err)
		}
	}
	certDir := filepath.Join(root, testServerID, "certs", testCertID)
	if err := os.Mkdir(filepath.Join(certDir, tempPrefix+"vcrash"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := src.Cleanup(testServerID, map[string]bool{testCertID: true}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(certDir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "current.json,v3" {
		t.Fatalf("after cleanup: %v", names)
	}

	key, _ := ManagedKey(testServerID, testCertID)
	get, err := store.Resolve(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Cleanup(testServerID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(certDir, retiredFile)); err != nil {
		t.Fatalf("retired marker missing: %v", err)
	}
	// 回到包里：撤销计时。
	if err := src.Cleanup(testServerID, map[string]bool{testCertID: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(certDir, retiredFile)); !os.IsNotExist(err) {
		t.Fatalf("retired marker not cleared: %v", err)
	}
	if err := src.Cleanup(testServerID, nil); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(RetiredRetention - time.Second)
	if err := src.Cleanup(testServerID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := get(&tls13Hello); err != nil {
		t.Fatalf("certificate removed before retention elapsed: %v", err)
	}
	clock = clock.Add(2 * time.Second)
	if err := src.Cleanup(testServerID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(certDir); !os.IsNotExist(err) {
		t.Fatalf("retired certificate dir still present: %v", err)
	}
	if _, err := get(&tls13Hello); err == nil {
		t.Fatal("GetCertificate still serves a removed certificate")
	}
	if len(store.Snapshot()) != 0 {
		t.Fatalf("snapshot still lists removed certificate: %+v", store.Snapshot())
	}

	// 整个命名空间解除绑定。
	if err := src.Install(managedVersion(ca.issue(t, clock.Add(30*24*time.Hour)), 9)); err != nil {
		t.Fatal(err)
	}
	if err := src.RemoveNamespace(testServerID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, testServerID)); !os.IsNotExist(err) {
		t.Fatalf("namespace dir still present: %v", err)
	}
	if store.Current(key) != nil {
		t.Fatal("namespace removal left the certificate serving")
	}
}
