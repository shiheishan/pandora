package certs

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func runUntilIdle(t *testing.T, m *Manager, ctx context.Context, clock *fakeClock) {
	t.Helper()
	now := clock.Now()
	for i := 0; i < 10; i++ {
		next := m.step(ctx)
		// 零值：没有任何待办（例如证书都回收了）
		if next.IsZero() || next.After(now) {
			return
		}
	}
	t.Fatal("runUntilIdle: 10 次 step 仍未空闲")
}

func leafFromManager(t *testing.T, m *Manager, id string) *x509.Certificate {
	t.Helper()
	cert, ok := m.Certificate(id)
	if !ok {
		t.Fatal("没有证书")
	}
	if cert.Leaf != nil {
		return cert.Leaf
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func TestManagerIssuesIPCertificate(t *testing.T) {
	useIdentityJitter(t)
	ca := startTestCA(t, true, "")
	clock := newFakeClock()
	dir := t.TempDir()
	m := newTestManager(t, ca, dir, clock)
	ctx := context.Background()

	m.Want([]string{"127.0.0.1"})
	m.step(ctx)

	leaf := leafFromManager(t, m, "127.0.0.1")
	if !slices.ContainsFunc(leaf.IPAddresses, func(ip net.IP) bool { return ip.String() == "127.0.0.1" }) {
		t.Fatalf("叶子 IP = %v", leaf.IPAddresses)
	}

	orders, replaces, profiles := ca.stats()
	if orders != 1 || len(replaces) != 1 || replaces[0] != "" || len(profiles) != 1 || profiles[0] != ProfileShortlived {
		t.Fatalf("stats: orders=%d replaces=%v profiles=%v", orders, replaces, profiles)
	}

	select {
	case id := <-m.Updates():
		if id != "127.0.0.1" {
			t.Fatalf("Updates: %q", id)
		}
	default:
		t.Fatal("Updates 应能非阻塞读到 id")
	}

	st := m.Status("127.0.0.1")
	if st.ErrorCode != "" || st.NotAfter.IsZero() || st.NextAttempt.IsZero() {
		t.Fatalf("Status: %+v", st)
	}

	keyPath := filepath.Join(dir, "certs", "127.0.0.1", "key.pem")
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("key.pem perm = %o", fi.Mode().Perm())
	}
	certDir := filepath.Join(dir, "certs", "127.0.0.1")
	fi, err = os.Stat(certDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("cert dir perm = %o", fi.Mode().Perm())
	}
	acctPath := filepath.Join(dir, "account.json")
	fi, err = os.Stat(acctPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("account.json perm = %o", fi.Mode().Perm())
	}
}

func TestManagerIssuesDomainCertificate(t *testing.T) {
	useIdentityJitter(t)
	dns := startFakeDNS(t)
	dns.setA("node1.example.com", "127.0.0.1")
	ca := startTestCA(t, true, dns.addr())
	clock := newFakeClock()
	dir := t.TempDir()
	m := newTestManager(t, ca, dir, clock)
	ctx := context.Background()

	m.Want([]string{"node1.example.com"})
	m.step(ctx)

	leaf := leafFromManager(t, m, "node1.example.com")
	if !slices.Contains(leaf.DNSNames, "node1.example.com") {
		t.Fatalf("DNSNames = %v", leaf.DNSNames)
	}
	_, _, profiles := ca.stats()
	if len(profiles) != 1 || profiles[0] != "" {
		t.Fatalf("profile = %v", profiles)
	}
}

func TestManagerReusesCertificateAfterRestart(t *testing.T) {
	useIdentityJitter(t)
	ca := startTestCA(t, true, "")
	clock := newFakeClock()
	dir := t.TempDir()
	m1 := newTestManager(t, ca, dir, clock)
	ctx := context.Background()
	m1.Want([]string{"127.0.0.1"})
	m1.step(ctx)
	if _, ok := m1.Certificate("127.0.0.1"); !ok {
		t.Fatal("首签失败")
	}
	acctBefore, err := os.ReadFile(filepath.Join(dir, "account.json"))
	if err != nil {
		t.Fatal(err)
	}

	m2 := newTestManager(t, ca, dir, clock)
	m2.Want([]string{"127.0.0.1"})
	if _, ok := m2.Certificate("127.0.0.1"); !ok {
		t.Fatal("重启后应立刻有证书")
	}

	clock.Add(time.Minute)
	runUntilIdle(t, m2, ctx, clock)
	orders, _, _ := ca.stats()
	if orders != 1 {
		t.Fatalf("新订单数 = %d, want 1", orders)
	}
	acctAfter, err := os.ReadFile(filepath.Join(dir, "account.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(acctBefore, acctAfter) {
		t.Fatal("account.json 不应因第二个 Manager 而变")
	}
}

func TestManagerRenewsWithReplaces(t *testing.T) {
	useIdentityJitter(t)
	ca := startTestCA(t, true, "")
	clock := newFakeClock()
	dir := t.TempDir()
	m := newTestManager(t, ca, dir, clock)
	ctx := context.Background()

	m.Want([]string{"127.0.0.1"})
	m.step(ctx)
	firstLeaf := leafFromManager(t, m, "127.0.0.1")
	firstSerial := firstLeaf.SerialNumber
	select {
	case <-m.Updates():
	default:
	}
	ca.settle(t, serialHex(firstLeaf), "")

	renewAt := m.Status("127.0.0.1").NextAttempt
	clock.Add(renewAt.Sub(clock.Now()) + time.Minute)
	runUntilIdle(t, m, ctx, clock)

	orders, replaces, _ := ca.stats()
	if orders != 2 || len(replaces) < 2 || replaces[1] == "" {
		t.Fatalf("续期: orders=%d replaces=%v", orders, replaces)
	}
	secondLeaf := leafFromManager(t, m, "127.0.0.1")
	if secondLeaf.SerialNumber.Cmp(firstSerial) == 0 {
		t.Fatal("续期后序列号应变化")
	}
	select {
	case id := <-m.Updates():
		if id != "127.0.0.1" {
			t.Fatalf("Updates: %q", id)
		}
	default:
		t.Fatal("续期后 Updates 应再收到一次")
	}
}

func TestManagerChallengePortBusy(t *testing.T) {
	useIdentityJitter(t)
	ca := startTestCA(t, true, "")
	clock := newFakeClock()
	dir := t.TempDir()
	m := newTestManager(t, ca, dir, clock)
	ctx := context.Background()

	ln, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(ca.httpPort)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	before := clock.Now()
	m.Want([]string{"127.0.0.1"})
	m.step(ctx)

	st := m.Status("127.0.0.1")
	if st.ErrorCode != ReasonChallengePortBusy {
		t.Fatalf("ErrorCode = %q", st.ErrorCode)
	}
	orders, _, _ := ca.stats()
	if orders != 0 {
		t.Fatalf("新订单数 = %d", orders)
	}
	wantNext := before.Add(portBusyRetry)
	if st.NextAttempt.Sub(wantNext).Abs() > 2*time.Second {
		t.Fatalf("NextAttempt = %v want ~%v", st.NextAttempt, wantNext)
	}
}

func TestManagerValidationFailedBacksOff(t *testing.T) {
	useIdentityJitter(t)
	// VA 连 managerHTTPPort，Manager 在另一端口监听，HTTP-01 验不过。
	ca := startTestCA(t, true, "")
	ca.managerHTTPPort = freeTCPPort(t)

	clock := newFakeClock()
	dir := t.TempDir()
	m := newTestManager(t, ca, dir, clock)
	ctx := context.Background()

	m.Want([]string{"127.0.0.1"})
	m.step(ctx)
	st := m.Status("127.0.0.1")
	if st.ErrorCode != ReasonValidationFailed {
		t.Fatalf("ErrorCode = %q", st.ErrorCode)
	}
	want15 := clock.Now().Add(firstRetry)
	if st.NextAttempt.Sub(want15).Abs() > 2*time.Second {
		t.Fatalf("NextAttempt = %v want ~%v", st.NextAttempt, want15)
	}

	clock.Add(st.NextAttempt.Sub(clock.Now()))
	m.step(ctx)
	st = m.Status("127.0.0.1")
	want30 := clock.Now().Add(2 * firstRetry)
	if st.NextAttempt.Sub(want30).Abs() > 2*time.Second {
		t.Fatalf("第二次退避 NextAttempt = %v want ~%v", st.NextAttempt, want30)
	}
}

func TestManagerRateLimited(t *testing.T) {
	useIdentityJitter(t)
	ca := startTestCA(t, true, "")
	ca.set429("3600")
	clock := newFakeClock()
	dir := t.TempDir()
	m := newTestManager(t, ca, dir, clock)
	ctx := context.Background()

	before := clock.Now()
	m.Want([]string{"127.0.0.1"})
	m.step(ctx)

	st := m.Status("127.0.0.1")
	if st.ErrorCode != ReasonRateLimited {
		t.Fatalf("ErrorCode = %q", st.ErrorCode)
	}
	want := before.Add(time.Hour)
	if st.NextAttempt.Sub(want).Abs() > time.Second {
		t.Fatalf("NextAttempt = %v want ~%v", st.NextAttempt, want)
	}
}

func TestManagerProfileUnsupported(t *testing.T) {
	useIdentityJitter(t)
	ca := startTestCA(t, false, "")
	clock := newFakeClock()
	dir := t.TempDir()
	m := newTestManager(t, ca, dir, clock)
	ctx := context.Background()

	m.Want([]string{"127.0.0.1"})
	m.step(ctx)
	if m.Status("127.0.0.1").ErrorCode != ReasonProfileUnsupported {
		t.Fatalf("ErrorCode = %q", m.Status("127.0.0.1").ErrorCode)
	}
}

func TestManagerSkipsCorruptStoredCertificate(t *testing.T) {
	useIdentityJitter(t)
	ca := startTestCA(t, true, "")
	clock := newFakeClock()
	dir := t.TempDir()
	m1 := newTestManager(t, ca, dir, clock)
	ctx := context.Background()
	m1.Want([]string{"127.0.0.1"})
	m1.step(ctx)

	certPath := filepath.Join(dir, "certs", "127.0.0.1", "cert.pem")
	badDER, err := makeWrongSANCertPEM()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, badDER, 0o600); err != nil {
		t.Fatal(err)
	}

	m2 := newTestManager(t, ca, dir, clock)
	if _, ok := m2.Certificate("127.0.0.1"); ok {
		t.Fatal("损坏证书不应加载")
	}
	m2.Want([]string{"127.0.0.1"})
	m2.step(ctx)
	if _, ok := m2.Certificate("127.0.0.1"); !ok {
		t.Fatal("应重新申请成功")
	}
}

// makeWrongSANCertPEM 生成与盘上 key.pem 不配的 ECDSA 自签证书，SAN 为 127.0.0.1。
func makeWrongSANCertPEM() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "wrong"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func TestManagerCollectsUnusedAfterSevenDays(t *testing.T) {
	useIdentityJitter(t)
	ca := startTestCA(t, true, "")
	clock := newFakeClock()
	dir := t.TempDir()
	m := newTestManager(t, ca, dir, clock)
	ctx := context.Background()

	m.Want([]string{"127.0.0.1"})
	m.step(ctx)
	certDir := filepath.Join(dir, "certs", "127.0.0.1")

	m.Want(nil)
	clock.Add(unusedKeep - time.Minute)
	runUntilIdle(t, m, ctx, clock)
	if _, err := os.Stat(certDir); err != nil {
		t.Fatalf("未满 7 天不应删目录: %v", err)
	}

	clock.Add(2 * time.Minute)
	runUntilIdle(t, m, ctx, clock)
	if _, err := os.Stat(certDir); !os.IsNotExist(err) {
		t.Fatalf("满 7 天应删目录: err=%v", err)
	}
	if _, ok := m.Certificate("127.0.0.1"); ok {
		t.Fatal("Certificate 应不存在")
	}
}

func TestManagerWantDoesNotResetBackoff(t *testing.T) {
	useIdentityJitter(t)
	ca := startTestCA(t, true, "")
	ca.managerHTTPPort = freeTCPPort(t)

	clock := newFakeClock()
	dir := t.TempDir()
	m := newTestManager(t, ca, dir, clock)
	ctx := context.Background()

	m.Want([]string{"127.0.0.1"})
	m.step(ctx)
	if m.Status("127.0.0.1").ErrorCode != ReasonValidationFailed {
		t.Fatal("应先 validation_failed")
	}
	nextBefore := m.Status("127.0.0.1").NextAttempt
	ordersBefore, _, _ := ca.stats()

	m.Want([]string{"127.0.0.1"})
	if !m.Status("127.0.0.1").NextAttempt.Equal(nextBefore) {
		t.Fatal("Want 不应重置 NextAttempt")
	}
	m.step(ctx)
	ordersAfter, _, _ := ca.stats()
	if ordersAfter != ordersBefore {
		t.Fatalf("退避中 step 不应下新单: %d -> %d", ordersBefore, ordersAfter)
	}
}

func TestManagerPurge(t *testing.T) {
	useIdentityJitter(t)
	ca := startTestCA(t, true, "")
	clock := newFakeClock()
	dir := t.TempDir()
	m := newTestManager(t, ca, dir, clock)
	ctx := context.Background()

	m.Want([]string{"127.0.0.1"})
	m.step(ctx)
	if err := m.Purge(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "account.json")); !os.IsNotExist(err) {
		t.Fatalf("account.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "certs", "127.0.0.1")); !os.IsNotExist(err) {
		t.Fatalf("cert dir: %v", err)
	}
	if _, ok := m.Certificate("127.0.0.1"); ok {
		t.Fatal("Purge 后不应有证书")
	}

	m.Want([]string{"127.0.0.1"})
	m.step(ctx)
	if _, ok := m.Certificate("127.0.0.1"); !ok {
		t.Fatal("Purge 后应能重新注册并申请")
	}
}

// freeTCPPort 取一个空闲端口后立即释放：Manager 下单前要自己绑它（端口预检），测试不能占着。
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
