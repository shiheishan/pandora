package node

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// fakeCerts 实现 CertManager，供证书编排测试用。
type fakeCerts struct {
	mu sync.Mutex

	certs      map[string]*tls.Certificate
	status     map[string]core.CertStatus
	wantCalls  [][]string
	purgeCount int
	updates    chan string
}

func newFakeCerts() *fakeCerts {
	return &fakeCerts{
		certs:   make(map[string]*tls.Certificate),
		status:  make(map[string]core.CertStatus),
		updates: make(chan string, 8),
	}
}

func (f *fakeCerts) Certificate(id string) (*tls.Certificate, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.certs[id]
	return c, ok
}

func (f *fakeCerts) Want(ids []string) {
	f.mu.Lock()
	f.wantCalls = append(f.wantCalls, append([]string(nil), ids...))
	f.mu.Unlock()
}

func (f *fakeCerts) Status(id string) core.CertStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.status[id]; ok {
		return st
	}
	return core.CertStatus{Identifier: id}
}

func (f *fakeCerts) Updates() <-chan string { return f.updates }

func (f *fakeCerts) Purge() error {
	f.mu.Lock()
	f.purgeCount++
	f.certs = make(map[string]*tls.Certificate)
	f.status = make(map[string]core.CertStatus)
	f.mu.Unlock()
	return nil
}

func (f *fakeCerts) setCert(id string, cert *tls.Certificate) {
	f.mu.Lock()
	f.certs[id] = cert
	f.mu.Unlock()
}

func (f *fakeCerts) setStatus(id string, st core.CertStatus) {
	f.mu.Lock()
	st.Identifier = id
	f.status[id] = st
	f.mu.Unlock()
}

func (f *fakeCerts) lastWant() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.wantCalls) == 0 {
		return nil
	}
	return f.wantCalls[len(f.wantCalls)-1]
}

func newCertTestServer(t *testing.T) (*testServer, *fakeCerts) {
	t.Helper()
	ts := newTestServer(t, newFakeKernel(), "")
	fake := newFakeCerts()
	ts.Server.certs = fake
	ts.welcome()
	return ts, fake
}

func acmeNodeConfig(t *testing.T, id, identifier string) *nodev1.NodeConfig {
	t.Helper()
	return nodeConfig(t, id, "trojan", map[string]any{
		"server_port":     443,
		"cert_mode":       "acme",
		"cert_identifier": identifier,
	})
}

func runtimeInHeartbeat(ts *testServer, nodeID string) *nodev1.NodeRuntime {
	hb := ts.link.lastHeartbeat()
	if hb == nil {
		return nil
	}
	for _, rt := range hb.Nodes {
		if rt.NodeId == nodeID {
			return rt
		}
	}
	return nil
}

func writeTestCertPEM(t *testing.T, dir string) (certPath, keyPath string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	notAfter = time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "node1.example.com"},
		NotBefore:    time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	must(t, err)
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	must(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	keyDER, err := x509.MarshalECPrivateKey(key)
	must(t, err)
	must(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600))
	return certPath, keyPath, notAfter
}

func TestCertWantCollectsACMEIdentifiers(t *testing.T) {
	ts, fake := newCertTestServer(t)
	n1 := acmeNodeConfig(t, "n1", "node1.example.com")
	n2 := acmeNodeConfig(t, "n2", "node1.example.com")
	n3 := portConfig(t, "n3", "trojan", 4443)
	ts.config(n1, n2, n3)
	got := fake.lastWant()
	sort.Strings(got)
	assertStrings(t, "Want", got, "node1.example.com")
}

func TestCertPendingDoesNotTouchKernel(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	fake := newFakeCerts()
	ts.Server.certs = fake
	ts.welcome()
	nc := acmeNodeConfig(t, "n1", "node1.example.com")
	ts.config(nc)
	if k.port("n1") != 0 {
		t.Fatal("证书没到不应装入站")
	}
	wantAck := "CONFIG_PHASE_FAILED:" + nc.ContentSha256[:8] + ":cert_pending"
	assertStrings(t, "回执", ts.link.acks("n1"), wantAck)
	ts.sendHeartbeat()
	rt := runtimeInHeartbeat(ts, "n1")
	if rt == nil {
		t.Fatal("没有心跳")
	}
	if rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DOWN || rt.Reason != core.ReasonCertPending {
		t.Fatalf("状态 = %v %q，期望 DOWN cert_pending", rt.Status, rt.Reason)
	}
	if rt.Cert == nil || rt.Cert.Identifier != "node1.example.com" {
		t.Fatalf("Cert = %v", rt.Cert)
	}
}

func TestCertReadyInstallsImmediately(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	fake := newFakeCerts()
	ts.Server.certs = fake
	ts.welcome()
	nc := acmeNodeConfig(t, "n1", "node1.example.com")
	ts.config(nc)
	fake.setCert("node1.example.com", &tls.Certificate{})
	ts.onCertReady("node1.example.com")
	if k.port("n1") != 443 {
		t.Fatalf("入站端口 = %d", k.port("n1"))
	}
	sw := "CONFIG_PHASE_SWITCHED:" + nc.ContentSha256[:8]
	assertStrings(t, "回执", ts.link.acks("n1"), "CONFIG_PHASE_FAILED:"+nc.ContentSha256[:8]+":cert_pending", sw)
}

func TestCertPendingKeepsOldConfigServing(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	fake := newFakeCerts()
	ts.Server.certs = fake
	ts.welcome()
	fileNC := portConfig(t, "n1", "trojan", 443)
	ts.config(fileNC)
	if k.port("n1") != 443 || k.protocol("n1") != "trojan" {
		t.Fatal("file 入站没装上")
	}
	acmeNC := acmeNodeConfig(t, "n1", "node1.example.com")
	ts.config(acmeNC)
	if k.port("n1") != 443 || k.protocol("n1") != "trojan" {
		t.Fatal("旧入站应仍在服务")
	}
	ts.sendHeartbeat()
	rt := runtimeInHeartbeat(ts, "n1")
	if rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DEGRADED || rt.Reason != core.ReasonCertPending {
		t.Fatalf("状态 = %v %q", rt.Status, rt.Reason)
	}
}

func TestCertExpiredReportsDegraded(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	fake := newFakeCerts()
	ts.Server.certs = fake
	ts.welcome()
	nc := acmeNodeConfig(t, "n1", "node1.example.com")
	expired := ts.clock.now().Add(-time.Hour)
	future := ts.clock.now().Add(24 * time.Hour)
	fake.setCert("node1.example.com", &tls.Certificate{})
	fake.setStatus("node1.example.com", core.CertStatus{NotAfter: expired})
	ts.config(nc)
	ts.sendHeartbeat()
	rt := runtimeInHeartbeat(ts, "n1")
	if rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DEGRADED || rt.Reason != core.ReasonCertExpired {
		t.Fatalf("过期：状态 = %v %q", rt.Status, rt.Reason)
	}
	fake.setStatus("node1.example.com", core.CertStatus{NotAfter: future})
	ts.sendHeartbeat()
	rt = runtimeInHeartbeat(ts, "n1")
	if rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING {
		t.Fatalf("未过期：状态 = %v", rt.Status)
	}
	if rt.Cert == nil || rt.Cert.NotAfterUnix != future.Unix() {
		t.Fatalf("Cert.NotAfterUnix = %d，期望 %d", rt.Cert.GetNotAfterUnix(), future.Unix())
	}
}

func TestCertStatusForFileNode(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, notAfter := writeTestCertPEM(t, dir)
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	nc := nodeConfig(t, "n1", "trojan", map[string]any{
		"server_port": 445,
		"cert_path":   certPath,
		"key_path":    keyPath,
	})
	ts.config(nc)
	ts.sendHeartbeat()
	rt := runtimeInHeartbeat(ts, "n1")
	if rt.Cert == nil || rt.Cert.Identifier != "" || rt.Cert.NotAfterUnix != notAfter.Unix() {
		t.Fatalf("Cert = %v，期望 NotAfter %d", rt.Cert, notAfter.Unix())
	}
}

func TestTombstonePurgesCerts(t *testing.T) {
	ts, fake := newCertTestServer(t)
	ts.config(acmeNodeConfig(t, "n1", "node1.example.com"))
	ts.handleEvent(panel.Event{Kind: panel.EventTombstone, Reason: "x"})
	if fake.purgeCount != 1 {
		t.Fatalf("Purge 次数 = %d", fake.purgeCount)
	}
}

func TestNilCertManagerReportsPending(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	ts.welcome()
	nc := acmeNodeConfig(t, "n1", "node1.example.com")
	ts.config(nc)
	wantAck := "CONFIG_PHASE_FAILED:" + nc.ContentSha256[:8] + ":cert_pending"
	assertStrings(t, "回执", ts.link.acks("n1"), wantAck)
	ts.sendHeartbeat()
	rt := runtimeInHeartbeat(ts, "n1")
	if rt.Cert == nil || rt.Cert.Identifier != "node1.example.com" {
		t.Fatalf("Cert = %v", rt.Cert)
	}
}
