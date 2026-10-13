package panel

import (
	"bytes"
	"crypto/x509"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

// bareSession 建一个不跑 Run 的会话，时钟与日志由测试给。
func bareSession(t *testing.T, now func() time.Time, logger *slog.Logger, notAfter time.Time) *Session {
	t.Helper()
	pki := newTestPKI(t)
	s, err := NewSession(SessionOptions{
		Address:  "127.0.0.1:9443",
		Identity: newTestIdentity(t, pki, newServerID(), notAfter),
		Applied:  func() ([]*nodev1.AppliedConfig, []*nodev1.AppliedUsers) { return nil, nil },
		Logger:   logger,
		Now:      now,
		Rand:     func(n int64) int64 { return n - 1 },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// 收到 Welcome 后很快又断的流不复位退避；活够 stableStreamAfter 才复位；面板给的 reconnect 不动退避。
func TestReconnectDelayResetsOnlyAfterStableStream(t *testing.T) {
	now := time.Now()
	s := bareSession(t, func() time.Time { return now }, slog.New(slog.DiscardHandler), now.Add(30*24*time.Hour))
	bo := Backoff{Base: backoffBase, Max: backoffMax, Rand: s.o.Rand}
	short := streamResult{welcomed: true, welcomedAt: now.Add(-stableStreamAfter + time.Second)}
	stable := streamResult{welcomed: true, welcomedAt: now.Add(-stableStreamAfter)}
	ceil := func(d time.Duration) time.Duration { return d - time.Nanosecond }

	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if got := s.reconnectDelay(&bo, short); got != ceil(want) {
			t.Fatalf("第 %d 条短命流后等待 %s，期望 %s（不该复位）", i+1, got, ceil(want))
		}
	}
	if got := s.reconnectDelay(&bo, stable); got != ceil(time.Second) {
		t.Fatalf("稳定流断开后等待 %s，期望复位到 %s", got, ceil(time.Second))
	}
	if got := s.reconnectDelay(&bo, streamResult{}); got != ceil(2*time.Second) {
		t.Fatalf("没收到 Welcome 的流后等待 %s", got)
	}
	cmd := streamResult{welcomed: true, welcomedAt: now.Add(-time.Hour), reconnect: &nodev1.Reconnect{InMs: 500, JitterMs: 100}}
	if got := s.reconnectDelay(&bo, cmd); got != 599*time.Millisecond {
		t.Fatalf("面板 reconnect 的等待 %s", got)
	}
	if got := s.reconnectDelay(&bo, streamResult{}); got != ceil(4*time.Second) {
		t.Fatalf("面板 reconnect 不该动退避，得到 %s", got)
	}
}

// lockedBuffer 是并发安全的日志缓冲。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// 证书已过期：建流前记 Error（至多每小时一次），没过期不记。
func TestExpiredCertificateLoggedAsError(t *testing.T) {
	var logs lockedBuffer
	now := time.Now()
	notAfter := now.Add(time.Hour)
	s := bareSession(t, func() time.Time { return now }, slog.New(slog.NewTextHandler(&logs, nil)), notAfter)
	count := func() int { return strings.Count(logs.String(), "level=ERROR") }

	s.warnIfExpired()
	if count() != 0 {
		t.Fatal("没过期也记了 Error")
	}
	now = s.o.Identity.NotAfter().Add(time.Second)
	s.warnIfExpired()
	s.warnIfExpired()
	if count() != 1 || !strings.Contains(logs.String(), "重新接入") {
		t.Fatalf("过期应记一条 Error：%s", logs.String())
	}
	now = now.Add(expiredLogEvery)
	s.warnIfExpired()
	if count() != 2 {
		t.Fatalf("一小时后应再记一条：%d", count())
	}
}

// 续期申请迟迟没回证书：丢掉待配对私钥重新申请；面板对第二次申请签回的证书能装上。
func TestRenewRetriesAfterTimeout(t *testing.T) {
	pki := newTestPKI(t)
	sid := newServerID()
	id := newTestIdentity(t, pki, sid, time.Now().Add(5*24*time.Hour))
	fc := newHandoffControl()
	s, _, _ := newSessionFixture(t, pki, id, fc, func(o *SessionOptions) { o.RenewCheck = 100 * time.Millisecond })
	st := fc.takeStream(t, 5*time.Second)
	_ = st.SendWelcome(60, 30)
	nextEvent(t, s, EventWelcome)

	first := st.Expect(t, "Renew").(*nodev1.RenewRequest)
	second := st.Expect(t, "Renew").(*nodev1.RenewRequest)
	csr1, err := x509.ParseCertificateRequest(first.GetCsrDer())
	if err != nil {
		t.Fatal(err)
	}
	csr2, err := x509.ParseCertificateRequest(second.GetCsrDer())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(csr1.RawSubjectPublicKeyInfo, csr2.RawSubjectPublicKeyInfo) {
		t.Fatal("超时后重新申请应换新私钥")
	}
	_ = st.SendCertificate(pki.signCSR(t, second.GetCsrDer(), sid, time.Now().Add(30*24*time.Hour)), pki.caPEM)

	deadline := time.Now().Add(5 * time.Second)
	for id.NotAfter().Before(time.Now().Add(20*24*time.Hour)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if id.NotAfter().Before(time.Now().Add(20 * 24 * time.Hour)) {
		t.Fatal("第二次申请签回的证书没装上")
	}
	st.end()
}
