package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"reflect"
	"testing"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func mustProfile(t *testing.T, users, delta int) *profile.Profile {
	t.Helper()
	dir := t.TempDir()
	protos, err := profile.ParseProtocols("all")
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.Init(profile.InitOptions{
		Dir: dir, Seed: "test", Users: users, DeltaSize: delta, PortBase: 20000,
		Protocols: protos, RealityDest: "127.0.0.1:19443",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUsersFullMessages12000(t *testing.T) {
	p := mustProfile(t, 12000, 500)
	roster := p.Roster(0)
	if len(roster) != 12000 {
		t.Fatalf("名单人数 %d，期望 12000", len(roster))
	}
	msgs := usersFullMessages("node-1", "ng-g0", roster)
	if len(msgs) != 3 {
		t.Fatalf("块数 %d，期望 3", len(msgs))
	}
	var total int
	for i, m := range msgs {
		if m.ChunkTotal != 3 || m.ChunkIndex != uint32(i) {
			t.Fatalf("块 %d: index=%d total=%d", i, m.ChunkIndex, m.ChunkTotal)
		}
		for j, u := range m.Users {
			wantUID := int64(i*usersFullChunkSize + j + 1)
			if u.Uid != wantUID {
				t.Fatalf("块 %d 用户 %d: uid=%d 期望 %d", i, j, u.Uid, wantUID)
			}
		}
		total += len(m.Users)
	}
	if total != 12000 {
		t.Fatalf("总用户数 %d", total)
	}
}

func TestHelloHasAllConfigs(t *testing.T) {
	p := mustProfile(t, 20, 5)
	pan, err := newPanel(p, "tok", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	helloEmpty := &nodev1.Hello{}
	if helloHasAllConfigs(helloEmpty, pan.nodes) {
		t.Fatal("空 Hello 不应视为已带全配置")
	}
	applied := make([]*nodev1.AppliedConfig, len(pan.nodes))
	for i, n := range pan.nodes {
		applied[i] = &nodev1.AppliedConfig{NodeId: n.nodeID, ContentSha256: n.configSHA}
	}
	if !helloHasAllConfigs(&nodev1.Hello{AppliedConfigs: applied}, pan.nodes) {
		t.Fatal("应识别全部配置已应用")
	}
	applied[0].ContentSha256 = "wrong"
	if helloHasAllConfigs(&nodev1.Hello{AppliedConfigs: applied}, pan.nodes) {
		t.Fatal("摘要错误时不应通过")
	}
}

func TestHelloNeedsUsers(t *testing.T) {
	if helloNeedsUsers(&nodev1.Hello{}, "n1", "ng-g0") {
		// 无 applied → 需要下发
	} else {
		t.Fatal("无 applied 时应需要下发")
	}
	h := &nodev1.Hello{AppliedUsers: []*nodev1.AppliedUsers{{NodeId: "n1", Version: "ng-g0"}}}
	if helloNeedsUsers(h, "n1", "ng-g0") {
		t.Fatal("版本一致时不应再下发")
	}
	if !helloNeedsUsers(h, "n1", "ng-g1") {
		t.Fatal("版本不一致时应下发")
	}
}

func TestBuildUsersDelta(t *testing.T) {
	p := mustProfile(t, 20, 5)
	nodeID := profile.NodeID(p.Seed, profile.VLESSVision)
	d1 := buildUsersDelta(p, nodeID, 1)
	if d1.FromVersion != "ng-g0" || d1.ToVersion != "ng-g1" {
		t.Fatalf("版本 %s -> %s", d1.FromVersion, d1.ToVersion)
	}
	if len(d1.Upserted) != p.DeltaSize {
		t.Fatalf("upserted %d 期望 %d", len(d1.Upserted), p.DeltaSize)
	}
	if len(d1.RemovedUids) != p.DeltaSize {
		t.Fatalf("removed %d 期望 %d", len(d1.RemovedUids), p.DeltaSize)
	}
	d2 := buildUsersDelta(p, nodeID, 2)
	if d2.FromVersion != "ng-g1" || d2.ToVersion != "ng-g2" {
		t.Fatalf("第二次 swap 版本 %s -> %s", d2.FromVersion, d2.ToVersion)
	}
}

func TestStatusJSONTopLevelKeys(t *testing.T) {
	p := mustProfile(t, 20, 5)
	pan, err := newPanel(p, "tok", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	st := pan.status()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"now_unix_ms", "down", "enrolls", "enroll_rejections", "sessions_total", "active",
		"first_hello_unix_ms", "last_hello_unix_ms", "configs_sent", "config_sent_unix_ms",
		"config_acks", "roster", "users_acks_ok", "users_acks_failed", "heartbeats",
		"last_heartbeat_unix_ms", "runtime", "traffic_reports", "traffic_entries",
		"online_reports", "online_entries", "renewals",
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Fatalf("缺少顶层键 %q", k)
		}
	}
	var roster map[string]json.RawMessage
	if err := json.Unmarshal(m["roster"], &roster); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"gen", "version", "full_sent_unix_ms", "delta_sent_unix_ms", "users_full_sent", "users_delta_sent", "acks"} {
		if _, ok := roster[k]; !ok {
			t.Fatalf("roster 缺少键 %q", k)
		}
	}
}

func TestEnrollWrongTokenAndReplay(t *testing.T) {
	p := mustProfile(t, 20, 5)
	pan, err := newPanel(p, "good-token", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pan.Enroll(nil, &nodev1.EnrollRequest{Token: "bad", RequestId: "r1", CsrDer: mustCSR(t)})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("错令牌: %v", err)
	}
	cert1, err := pan.Enroll(nil, &nodev1.EnrollRequest{Token: "good-token", RequestId: "r1", CsrDer: mustCSR(t)})
	if err != nil {
		t.Fatal(err)
	}
	cert2, err := pan.Enroll(nil, &nodev1.EnrollRequest{Token: "good-token", RequestId: "r1", CsrDer: mustCSR(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cert1.CertChainPem, cert2.CertChainPem) {
		t.Fatal("同 request_id 应返回同一张证书")
	}
}

func mustCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "test"},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	_ = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	return der
}
