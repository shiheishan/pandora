package panel

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeEnroll 记录接入请求，按 request_id 幂等签发。
type fakeEnroll struct {
	nodev1.UnimplementedEnrollmentServiceServer

	t        *testing.T
	pki      *testPKI
	serverID string

	mu       sync.Mutex
	requests []*nodev1.EnrollRequest
	byReqID  map[string][]byte // cert chain PEM

	failFirstUnavailable bool
	firstCallDone        bool
}

func (f *fakeEnroll) Enroll(_ context.Context, req *nodev1.EnrollRequest) (*nodev1.EnrollResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, cloneEnrollRequest(req))
	if f.failFirstUnavailable && !f.firstCallDone {
		f.firstCallDone = true
		// 模拟回包丢失：已签发并缓存，再报错。
		if f.byReqID[req.RequestId] == nil {
			cert := f.pki.signCSR(f.t, req.CsrDer, f.serverID, time.Now().Add(30*24*time.Hour))
			f.byReqID[req.RequestId] = cert
		}
		f.mu.Unlock()
		return nil, status.Error(codes.Unavailable, "模拟不可用")
	}
	if cert, ok := f.byReqID[req.RequestId]; ok && cert != nil {
		f.mu.Unlock()
		return &nodev1.EnrollResponse{
			ServerId:     f.serverID,
			CertChainPem: cert,
			CaBundlePem:  f.pki.caPEM,
		}, nil
	}
	cert := f.pki.signCSR(f.t, req.CsrDer, f.serverID, time.Now().Add(30*24*time.Hour))
	f.byReqID[req.RequestId] = cert
	f.mu.Unlock()
	return &nodev1.EnrollResponse{
		ServerId:     f.serverID,
		CertChainPem: cert,
		CaBundlePem:  f.pki.caPEM,
	}, nil
}

func (f *fakeEnroll) requestCount() int {
	f.mu.Lock()
	n := len(f.requests)
	f.mu.Unlock()
	return n
}

func (f *fakeEnroll) snapshotRequests() []*nodev1.EnrollRequest {
	f.mu.Lock()
	out := make([]*nodev1.EnrollRequest, len(f.requests))
	for i, r := range f.requests {
		out[i] = cloneEnrollRequest(r)
	}
	f.mu.Unlock()
	return out
}

func (f *fakeEnroll) cachedCert(requestID string) []byte {
	f.mu.Lock()
	cert := append([]byte(nil), f.byReqID[requestID]...)
	f.mu.Unlock()
	return cert
}

func cloneEnrollRequest(r *nodev1.EnrollRequest) *nodev1.EnrollRequest {
	if r == nil {
		return nil
	}
	return &nodev1.EnrollRequest{
		Token:        r.Token,
		CsrDer:       append([]byte(nil), r.CsrDer...),
		RequestId:    r.RequestId,
		AgentVersion: r.AgentVersion,
		Host:         r.Host,
	}
}

func enrollOpts(t *testing.T, pki *testPKI, dialer Dialer, dir string, token string) EnrollOptions {
	t.Helper()
	return EnrollOptions{
		Panel:         "127.0.0.1:9443",
		CAFingerprint: pki.fingerprint,
		Token:         token,
		Dir:           dir,
		AgentVersion:  "test",
		Dialer:        dialer,
		Timeout:       5 * time.Second,
	}
}

func TestEnrollWritesIdentity(t *testing.T) {
	pki := newTestPKI(t)
	serverID := uuid.NewString()
	fake := &fakeEnroll{t: t, pki: pki, serverID: serverID, byReqID: make(map[string][]byte)}
	dialer := startFakeGRPC(t, pki.serverTLS(t, "127.0.0.1"), fake, nil)
	dir := t.TempDir()
	token := "tok-enroll-ok"

	id, err := Enroll(context.Background(), enrollOpts(t, pki, dialer, dir, token))
	if err != nil {
		t.Fatal(err)
	}
	if id.ServerID() != serverID {
		t.Fatalf("ServerID: want %s got %s", serverID, id.ServerID())
	}
	for _, name := range []string{certFile, keyFile, caFile, serverIDFile} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("缺少 %s: %v", name, err)
		}
		if runtime.GOOS != "windows" && name != serverIDFile {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s 权限应为 0600", name)
			}
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("目录权限应为 0700，实际 %o", info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, journalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("enroll.json 应已删: %v", err)
	}
	reqs := fake.snapshotRequests()
	if len(reqs) != 1 {
		t.Fatalf("应有 1 次请求，实际 %d", len(reqs))
	}
	r := reqs[0]
	if r.Token != token || r.RequestId == "" || len(r.CsrDer) == 0 {
		t.Fatal("请求字段不完整")
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if !csrMatchesKeyPEM(r.CsrDer, keyPEM) {
		t.Fatal("CSR 公钥与 key.pem 不配对")
	}
}

func TestEnrollRejectsWrongFingerprint(t *testing.T) {
	pki := newTestPKI(t)
	other := newTestPKI(t)
	fake := &fakeEnroll{t: t, pki: pki, serverID: uuid.NewString(), byReqID: make(map[string][]byte)}
	dialer := startFakeGRPC(t, pki.serverTLS(t, "127.0.0.1"), fake, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Enroll(ctx, EnrollOptions{
		Panel:         "127.0.0.1:9443",
		CAFingerprint: other.fingerprint,
		Token:         "tok-1",
		Dir:           t.TempDir(),
		AgentVersion:  "test",
		Dialer:        dialer,
		Timeout:       5 * time.Second,
	})
	if err == nil {
		t.Fatal("应失败")
	}
	if fake.requestCount() != 0 {
		t.Fatal("假服务端不应收到请求")
	}
}

func TestEnrollRejectsWrongHost(t *testing.T) {
	pki := newTestPKI(t)
	fake := &fakeEnroll{t: t, pki: pki, serverID: uuid.NewString(), byReqID: make(map[string][]byte)}
	dialer := startFakeGRPC(t, pki.serverTLS(t, "panel.example.test"), fake, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Enroll(ctx, enrollOpts(t, pki, dialer, t.TempDir(), "tok-1"))
	if err == nil {
		t.Fatal("应失败")
	}
	if fake.requestCount() != 0 {
		t.Fatal("假服务端不应收到请求")
	}
}

func TestEnrollRetryReusesRequestID(t *testing.T) {
	pki := newTestPKI(t)
	serverID := uuid.NewString()
	fake := &fakeEnroll{
		t:                    t,
		pki:                  pki,
		serverID:             serverID,
		byReqID:              make(map[string][]byte),
		failFirstUnavailable: true,
	}
	dialer := startFakeGRPC(t, pki.serverTLS(t, "127.0.0.1"), fake, nil)
	dir := t.TempDir()
	token := "tok-retry-secret"
	ctx := context.Background()

	_, err := Enroll(ctx, enrollOpts(t, pki, dialer, dir, token))
	if err == nil {
		t.Fatal("第一次应失败")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("错误信息不得含令牌")
	}
	if _, err2 := os.Stat(filepath.Join(dir, journalFile)); err2 != nil {
		t.Fatalf("enroll.json 应仍在: %v", err2)
	}

	id, err := Enroll(ctx, enrollOpts(t, pki, dialer, dir, token))
	if err != nil {
		t.Fatal(err)
	}
	if id.ServerID() != serverID {
		t.Fatal("server id 不对")
	}
	reqs := fake.snapshotRequests()
	if len(reqs) != 2 {
		t.Fatalf("应有 2 次请求，实际 %d", len(reqs))
	}
	if reqs[0].RequestId != reqs[1].RequestId {
		t.Fatal("request_id 应相同")
	}
	cert1 := fake.cachedCert(reqs[0].RequestId)
	if len(cert1) == 0 {
		t.Fatal("缓存证书缺失")
	}
	onDisk, _ := os.ReadFile(filepath.Join(dir, certFile))
	if string(onDisk) != string(cert1) {
		t.Fatal("应拿到同一张证书")
	}

	// 换令牌应生成新 request_id。
	dir2 := t.TempDir()
	fake2 := &fakeEnroll{t: t, pki: pki, serverID: uuid.NewString(), byReqID: make(map[string][]byte)}
	dialer2 := startFakeGRPC(t, pki.serverTLS(t, "127.0.0.1"), fake2, nil)
	if _, err := Enroll(ctx, enrollOpts(t, pki, dialer2, dir2, "other-token")); err != nil {
		t.Fatal(err)
	}
	reqs2 := fake2.snapshotRequests()
	if len(reqs2) != 1 || reqs2[0].RequestId == reqs[0].RequestId {
		t.Fatal("新令牌应有不同 request_id")
	}
}

func TestParseCAFingerprint(t *testing.T) {
	validHex := "a" + strings.Repeat("b", 63) // 64 hex
	cases := []struct {
		in   string
		ok   bool
		want string
	}{
		{in: "SHA256:" + validHex, ok: true, want: "sha256:" + validHex},
		{in: "sha256:" + validHex, ok: true, want: "sha256:" + validHex},
		{in: "md5:" + validHex, ok: false},
		{in: "sha256:" + strings.Repeat("a", 63), ok: false},
		{in: "sha256:" + strings.Repeat("g", 64), ok: false},
	}
	for _, tc := range cases {
		got, err := ParseCAFingerprint(tc.in)
		if tc.ok {
			if err != nil {
				t.Fatalf("%q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("%q: want %q got %q", tc.in, tc.want, got)
			}
		} else if err == nil {
			t.Fatalf("%q: 应失败", tc.in)
		}
	}
}

func TestPanelHost(t *testing.T) {
	hp, host, err := PanelHost("10.0.0.1")
	if err != nil || hp != "10.0.0.1:9443" || host != "10.0.0.1" {
		t.Fatalf("IPv4: %q %q %v", hp, host, err)
	}
	hp, host, err = PanelHost("[2001:db8::1]:9443")
	if err != nil || hp != "[2001:db8::1]:9443" || host != "2001:db8::1" {
		t.Fatalf("IPv6: %q %q %v", hp, host, err)
	}
	hp, host, err = PanelHost("panel.example.test:443")
	if err != nil || hp != "panel.example.test:443" || host != "panel.example.test" {
		t.Fatalf("host:port: %q %q %v", hp, host, err)
	}
}

func csrMatchesKeyPEM(csrDER, keyPEM []byte) bool {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return false
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return false
	}
	return key.PublicKey.Equal(csr.PublicKey)
}
