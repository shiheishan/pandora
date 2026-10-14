package certstore

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestKeyValidation(t *testing.T) {
	if _, err := ManagedKey(testServerID, testCertID); err != nil {
		t.Fatal(err)
	}
	fk := FileKey("/etc/pandora-native/certs/a.pem", "/etc/pandora-native/certs/a.key")
	if kind, err := fk.Kind(); err != nil || kind != KindFile {
		t.Fatalf("file key kind = %v, %v", kind, err)
	}
	if _, err := NewFileSource(t.TempDir(), New()).Register("/a|b", "/c"); err == nil {
		t.Fatal("path containing '|' accepted: FileKey would be ambiguous")
	}
	for _, k := range []Key{
		{},
		{Namespace: FileNamespace, ID: "short"},
		{Namespace: "tenant", ID: testCertID},
		{Namespace: testServerID, ID: "not-a-uuid"},
	} {
		if _, err := k.Kind(); err == nil {
			t.Fatalf("invalid key %v accepted", k)
		}
	}
	if _, err := New().Resolve(Key{Namespace: testServerID, ID: testCertID}); err == nil {
		t.Fatal("Resolve of unknown key succeeded")
	}
}

// 并发握手（GetCertificate）、Resolve、Snapshot/Digest 与安装新版本、file 轮换同时进行，-race 下干净；
// 每次拿到的证书都是某个完整版本，不会是 nil。
func TestConcurrentResolveAndReplaceIsRaceFree(t *testing.T) {
	ca := newTestCA(t)
	store := New()
	managed := NewManagedSource(filepath.Join(t.TempDir(), "panels"), store)
	root, _ := newFileRoot(t)
	certPath, keyPath := filepath.Join(root, "c.pem"), filepath.Join(root, "k.pem")
	first := ca.issue(t, time.Time{})
	writeFile(t, certPath, first.chainPEM)
	writeFile(t, keyPath, first.keyPEM)
	files := NewFileSource(root, store)
	fkey, err := files.Register(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := managed.Install(managedVersion(first, 1)); err != nil {
		t.Fatal(err)
	}
	mkey, _ := ManagedKey(testServerID, testCertID)

	const rounds = 20
	leaves := make([]testLeaf, rounds)
	for i := range leaves {
		leaves[i] = ca.issue(t, time.Time{})
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, k := range []Key{mkey, fkey} {
					get, err := store.Resolve(k)
					if err != nil {
						t.Errorf("Resolve(%v): %v", k, err)
						return
					}
					c, err := get(&tls13Hello)
					if err != nil || c == nil || c.Leaf == nil {
						t.Errorf("GetCertificate(%v) = %v, %v", k, c, err)
						return
					}
				}
				_ = store.Snapshot()
				_ = store.Digest()
			}
		}()
	}
	for i, leaf := range leaves {
		if err := managed.Install(managedVersion(leaf, uint64(i+2))); err != nil {
			t.Fatal(err)
		}
		writeFile(t, certPath, leaf.chainPEM)
		writeFile(t, keyPath, leaf.keyPEM)
		at := time.Now().Add(time.Duration(i+1) * time.Minute)
		bumpMTime(t, certPath, at)
		bumpMTime(t, keyPath, at)
		files.Poll()
	}
	close(stop)
	readers.Wait()

	last := leaves[rounds-1].fp
	for _, k := range []Key{mkey, fkey} {
		if st, _ := store.StatusOf(k); st.FingerprintSHA256 != last {
			t.Fatalf("%v final fingerprint = %s, want %s", k, st.FingerprintSHA256, last)
		}
	}
}
