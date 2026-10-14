package certstore

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 轮换文件后：新握手拿到新叶子证书，轮换前建立的连接继续收发；SNI 为空的握手也拿到同一张证书。
func TestFileSourceRotationHotSwapsWithoutDroppingConnections(t *testing.T) {
	ca := newTestCA(t)
	root, _ := newFileRoot(t)
	certPath, keyPath := filepath.Join(root, "fullchain.pem"), filepath.Join(root, "privkey.pem")
	first := ca.issue(t, time.Time{})
	writeFile(t, certPath, first.chainPEM)
	writeFile(t, keyPath, first.keyPEM)

	store := New()
	src := NewFileSource(root, store)
	key, err := src.Register(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if key != FileKey(certPath, keyPath) || key.Namespace != FileNamespace {
		t.Fatalf("unexpected key %v", key)
	}
	get, err := store.Resolve(key)
	if err != nil {
		t.Fatal(err)
	}
	addr := tlsServer(t, get)

	oldConn, fp := dialLeaf(t, addr, ca, testHost)
	if fp != first.fp {
		t.Fatalf("first handshake leaf = %s, want %s", fp, first.fp)
	}
	echo(t, oldConn, "before-rotation\n")

	second := ca.issue(t, time.Time{})
	writeFile(t, certPath, second.chainPEM)
	writeFile(t, keyPath, second.keyPEM)
	later := time.Now().Add(time.Minute)
	bumpMTime(t, certPath, later)
	bumpMTime(t, keyPath, later)
	src.Poll()

	_, fp = dialLeaf(t, addr, ca, testHost)
	if fp != second.fp {
		t.Fatalf("handshake after rotation leaf = %s, want %s", fp, second.fp)
	}
	_, fp = dialLeaf(t, addr, ca, "")
	if fp != second.fp {
		t.Fatalf("empty-SNI handshake leaf = %s, want %s", fp, second.fp)
	}
	echo(t, oldConn, "after-rotation\n")

	st, ok := store.StatusOf(key)
	if !ok || st.FingerprintSHA256 != second.fp || st.ErrorCode != "" || st.Source != KindFile ||
		st.CertPath != certPath || st.NotAfter.IsZero() || st.ChainSHA256 != ChainSHA256(second.chainPEM) {
		t.Fatalf("status after rotation = %+v", st)
	}
}

// 一轮 Poll 里文件没变（mtime/size 相同）就不重读；Run 按间隔调 Poll，ctx 结束即返回。
func TestFileSourcePollSkipsUnchangedAndRunStops(t *testing.T) {
	ca := newTestCA(t)
	root, _ := newFileRoot(t)
	certPath, keyPath := filepath.Join(root, "a.pem"), filepath.Join(root, "a.key")
	leaf := ca.issue(t, time.Time{})
	writeFile(t, certPath, leaf.chainPEM)
	writeFile(t, keyPath, leaf.keyPEM)
	store := New()
	src := NewFileSource(root, store)
	key, err := src.Register(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := store.StatusOf(key)
	src.Poll()
	after, _ := store.StatusOf(key)
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("unchanged files were reloaded")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { src.Run(ctx, 5*time.Millisecond); close(done) }()
	next := ca.issue(t, time.Time{})
	writeFile(t, certPath, next.chainPEM)
	writeFile(t, keyPath, next.keyPEM)
	later := time.Now().Add(time.Minute)
	bumpMTime(t, certPath, later)
	bumpMTime(t, keyPath, later)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, _ := store.StatusOf(key); st.FingerprintSHA256 == next.fp {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not pick up the rotated certificate")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestFileSourceRejectsDirectoryEscape(t *testing.T) {
	ca := newTestCA(t)
	root, outside := newFileRoot(t)
	leaf := ca.issue(t, time.Time{})
	writeFile(t, filepath.Join(outside, "c.pem"), leaf.chainPEM)
	writeFile(t, filepath.Join(outside, "k.pem"), leaf.keyPEM)
	writeFile(t, filepath.Join(root, "k.pem"), leaf.keyPEM)

	store := New()
	src := NewFileSource(root, store)
	cases := map[string][2]string{
		"dotdot":         {filepath.Join(root, "..", "outside", "c.pem"), filepath.Join(root, "k.pem")},
		"absolute":       {filepath.Join(outside, "c.pem"), filepath.Join(outside, "k.pem")},
		"root itself":    {root, filepath.Join(root, "k.pem")},
		"prefix sibling": {root + "-evil/c.pem", filepath.Join(root, "k.pem")},
	}
	for name, paths := range cases {
		t.Run(name, func(t *testing.T) {
			key, err := src.Register(paths[0], paths[1])
			if err == nil {
				t.Fatal("escaping path was accepted")
			}
			if errorCode(err) != ErrCodePathEscape {
				t.Fatalf("error code = %q (%v), want %q", errorCode(err), err, ErrCodePathEscape)
			}
			if _, err := store.Resolve(key); err == nil {
				t.Fatal("Resolve succeeded for a rejected path")
			}
		})
	}
	if _, err := src.Register("relative/c.pem", "relative/k.pem"); err == nil {
		t.Fatal("relative paths were accepted")
	}
}

func TestFileSourceRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	ca := newTestCA(t)
	root, outside := newFileRoot(t)
	leaf := ca.issue(t, time.Time{})
	writeFile(t, filepath.Join(outside, "c.pem"), leaf.chainPEM)
	writeFile(t, filepath.Join(root, "k.pem"), leaf.keyPEM)
	link := filepath.Join(root, "c.pem")
	if err := os.Symlink(filepath.Join(outside, "c.pem"), link); err != nil {
		t.Fatal(err)
	}
	store := New()
	src := NewFileSource(root, store)
	if _, err := src.Register(link, filepath.Join(root, "k.pem")); err == nil || errorCode(err) != ErrCodePathEscape {
		t.Fatalf("symlink escaping root: err = %v", err)
	}

	// 目录里的符号链接指向目录内（certbot live → archive 的做法）是允许的；
	// 运行中被改指到目录外时保留旧证书并记 path_escape。
	inner := filepath.Join(root, "archive")
	if err := os.Mkdir(inner, 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(inner, "c1.pem"), leaf.chainPEM)
	live := filepath.Join(root, "live.pem")
	if err := os.Symlink(filepath.Join(inner, "c1.pem"), live); err != nil {
		t.Fatal(err)
	}
	key, err := src.Register(live, filepath.Join(root, "k.pem"))
	if err != nil {
		t.Fatalf("in-root symlink rejected: %v", err)
	}
	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "c.pem"), live); err != nil {
		t.Fatal(err)
	}
	src.Poll()
	st, _ := store.StatusOf(key)
	if st.ErrorCode != ErrCodePathEscape || st.FingerprintSHA256 != leaf.fp {
		t.Fatalf("status after escaping relink = %+v", st)
	}
	if store.Current(key) == nil {
		t.Fatal("old certificate dropped after escaping relink")
	}
}

// 坏 PEM、私钥与证书不配对、新证书已过期：都保留旧证书，状态记对应错误码；修好后自动恢复。
func TestFileSourceKeepsOldCertificateOnBadReplacement(t *testing.T) {
	ca := newTestCA(t)
	other := ca.issue(t, time.Time{})
	expired := ca.issue(t, time.Now().Add(-time.Minute))
	cases := []struct {
		name     string
		chain    []byte
		key      []byte
		wantCode string
	}{
		{"bad pem", []byte("-----BEGIN CERTIFICATE-----\nnot base64\n-----END CERTIFICATE-----\n"), nil, ErrCodeBadPEM},
		{"key mismatch", nil, other.keyPEM, ErrCodeKeyMismatch},
		{"expired", expired.chainPEM, expired.keyPEM, ErrCodeExpired},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := newFileRoot(t)
			certPath, keyPath := filepath.Join(root, "c.pem"), filepath.Join(root, "k.pem")
			good := ca.issue(t, time.Time{})
			writeFile(t, certPath, good.chainPEM)
			writeFile(t, keyPath, good.keyPEM)
			store := New()
			src := NewFileSource(root, store)
			key, err := src.Register(certPath, keyPath)
			if err != nil {
				t.Fatal(err)
			}
			get, err := store.Resolve(key)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(time.Duration(i+1) * time.Minute)
			if tc.chain != nil {
				writeFile(t, certPath, tc.chain)
				bumpMTime(t, certPath, at)
			}
			if tc.key != nil {
				writeFile(t, keyPath, tc.key)
				bumpMTime(t, keyPath, at)
			}
			src.Poll()
			st, _ := store.StatusOf(key)
			if st.ErrorCode != tc.wantCode || st.FingerprintSHA256 != good.fp || st.Error == "" {
				t.Fatalf("status = %+v, want code %s and old fingerprint", st, tc.wantCode)
			}
			c, err := get(&tls13Hello)
			if err != nil || c != store.Current(key) || c == nil {
				t.Fatalf("GetCertificate after bad replacement: %v", err)
			}

			fixed := ca.issue(t, time.Time{})
			writeFile(t, certPath, fixed.chainPEM)
			writeFile(t, keyPath, fixed.keyPEM)
			bumpMTime(t, certPath, at.Add(time.Hour))
			bumpMTime(t, keyPath, at.Add(time.Hour))
			src.Poll()
			st, _ = store.StatusOf(key)
			if st.ErrorCode != "" || st.FingerprintSHA256 != fixed.fp {
				t.Fatalf("status after fix = %+v", st)
			}
		})
	}
}

// 文件被删再放回（mtime/size 与之前相同）也会重读并清掉错误；Unregister 后握手失败。
func TestFileSourceRecoversAfterMissingFileAndUnregister(t *testing.T) {
	ca := newTestCA(t)
	root, _ := newFileRoot(t)
	certPath, keyPath := filepath.Join(root, "c.pem"), filepath.Join(root, "k.pem")
	leaf := ca.issue(t, time.Time{})
	writeFile(t, certPath, leaf.chainPEM)
	writeFile(t, keyPath, leaf.keyPEM)
	info, err := os.Stat(certPath)
	if err != nil {
		t.Fatal(err)
	}
	store := New()
	src := NewFileSource(root, store)
	key, err := src.Register(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(certPath); err != nil {
		t.Fatal(err)
	}
	src.Poll()
	if st, _ := store.StatusOf(key); st.ErrorCode != ErrCodeReadFailed || store.Current(key) == nil {
		t.Fatalf("missing file: status = %+v", st)
	}
	writeFile(t, certPath, leaf.chainPEM)
	bumpMTime(t, certPath, info.ModTime())
	src.Poll()
	if st, _ := store.StatusOf(key); st.ErrorCode != "" {
		t.Fatalf("restored file: status = %+v", st)
	}

	get, err := store.Resolve(key)
	if err != nil {
		t.Fatal(err)
	}
	src.Unregister(key)
	if _, err := get(&tls13Hello); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("GetCertificate after Unregister: %v", err)
	}
	if _, err := store.Resolve(key); err == nil {
		t.Fatal("Resolve succeeded after Unregister")
	}
}
